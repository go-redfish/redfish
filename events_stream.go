package redfish

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

// The SSE client (DSP0266 1.23.0 12.5 Server-sent events, 12.5.2 Event service,
// 12.1.3 Open an SSE connection; the wire format is WHATWG HTML 9.2, see sse.go).
//
// A GET on the ServerSentEventUri of the EventService opens the stream: the
// service answers 200 with Content-Type text/event-stream (12.5.1), makes an
// EventDestination of the connection and deletes it when the connection closes
// (12.5.2), and "shall not send events to clients that do not have sufficient
// privilege" (13.7): the client authenticates like any other request, with the
// session token or basic authentication, over the https URL of the client.
//
// Reconnection. 12.5.2 says a service "should accept the Last-Event-ID header
// from the client to allow a client to restart the event stream in case the
// connection is interrupted", and HTML 9.2.3/9.2.4 have the client reconnect with
// that header after a network error. So the client may reconnect, and does it
// only after a broken connection (a read error), only when the stream has given
// an id (without one there is nothing to resume from) and at most
// EventsOptions.MaxReconnects times. It does not reconnect when the service ends
// the stream cleanly: 12.5.2 has the service close the connection when its
// EventDestination is deleted, and opening a new one would undo that.

// EventsOptions tune an event stream.
type EventsOptions struct {
	// Filter is the $filter expression of the SSE URI (DSP0266 12.5.2), for
	// instance `MessageId eq 'Contoso.1.0.TempAssert'` or
	// `(RegistryPrefix eq 'Resource') or (RegistryPrefix eq 'Task')`. The service
	// applies it; the client only checks that it is a reasonable text and that
	// the properties it names are ones the service says it supports.
	Filter string
	// MaxLine and MaxEvent bound a line and an event (default MaxSSELine and
	// MaxEventBytes): a larger one ends the stream with ErrEventTooLarge.
	MaxLine, MaxEvent int
	// MaxReconnects is how many times a broken connection is reopened with
	// Last-Event-ID (default 0: never).
	MaxReconnects int
	// Buffer is the capacity of the channel (default 16): a reader that does not
	// keep up holds the connection, nothing grows.
	Buffer int
}

// EventStream is an open stream of events.
type EventStream struct {
	// C delivers the events; it is closed when the stream ends. Then Err says why.
	C <-chan Event

	cancel context.CancelFunc
	done   chan struct{}
	mu     sync.Mutex
	err    error
}

// Err is why the stream ended: nil for a cancelled context, a Close or a clean
// end by the service. Valid once C is closed.
func (s *EventStream) Err() error {
	<-s.done
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

// Close ends the stream and waits for it.
func (s *EventStream) Close() {
	s.cancel()
	<-s.done
}

// MaxFilter bounds a $filter expression.
const MaxFilter = 1024

var filterProp = regexp.MustCompile(`([A-Za-z]+)\s+eq\s`)

// checkFilter refuses a filter that is not text a service could read, and one
// that names a property the service says it does not support.
func checkFilter(filter string, supported map[string]bool) error {
	if len(filter) > MaxFilter {
		return fmt.Errorf("redfish: the filter is longer than %d bytes", MaxFilter)
	}
	for _, r := range filter {
		if r < 0x20 || r == 0x7f {
			return errors.New("redfish: the filter holds a control character")
		}
	}
	if len(supported) > 0 {
		for _, m := range filterProp.FindAllStringSubmatch(filter, -1) {
			if !supported[m[1]] {
				return fmt.Errorf("%w: the service does not support $filter on %s", ErrUnsupported, m[1])
			}
		}
	}
	return nil
}

// Events opens the SSE stream of the service with a filter ("" for none), and
// reconnects up to 3 times after a broken connection (EventsWith for the
// options). The stream runs until ctx is cancelled, the service ends it, or an
// event is larger than the limit.
func (c *Client) Events(ctx context.Context, filter string) (*EventStream, error) {
	return c.EventsWith(ctx, EventsOptions{Filter: filter, MaxReconnects: 3})
}

// EventsWith is Events with options. The first connection is opened before it
// returns: a refusal (401, 403, 400 for a filter the service does not know,
// anything but 200 text/event-stream) is its error; ErrUnsupported when the
// EventService has no ServerSentEventUri or is disabled.
func (c *Client) EventsWith(ctx context.Context, o EventsOptions) (*EventStream, error) {
	if err := c.login(ctx); err != nil {
		return nil, err
	}
	es, err := c.EventService(ctx)
	if err != nil {
		return nil, err
	}
	switch {
	case es.ServerSentEventURI == "":
		return nil, fmt.Errorf("%w: the EventService has no ServerSentEventUri", ErrUnsupported)
	case es.ServiceEnabled != nil && !*es.ServiceEnabled:
		return nil, fmt.Errorf("%w: the EventService is disabled (ServiceEnabled is false)", ErrUnsupported)
	}
	if err := checkFilter(o.Filter, es.SSEFilterProperties); err != nil {
		return nil, err
	}
	target := es.ServerSentEventURI
	if o.Filter != "" {
		target += "?$filter=" + strings.ReplaceAll(url.QueryEscape(o.Filter), "+", "%20")
	}
	buffer := o.Buffer
	if buffer <= 0 {
		buffer = 16
	}
	cctx, cancel := context.WithCancel(ctx)
	resp, err := c.openStream(cctx, target, "")
	if err != nil {
		cancel()
		return nil, err
	}
	ch := make(chan Event, buffer)
	s := &EventStream{C: ch, cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(s.done)
		defer close(ch)
		err := c.pump(cctx, resp, target, o, ch)
		cancel()
		s.mu.Lock()
		s.err = err
		s.mu.Unlock()
	}()
	return s, nil
}

// pump reads the stream, reconnecting after a broken connection.
func (c *Client) pump(ctx context.Context, resp *http.Response, target string, o EventsOptions, ch chan<- Event) error {
	reconnects := 0
	for {
		rd := NewSSEReader(resp.Body, o.MaxLine, o.MaxEvent)
		var err error
		for {
			var msg SSEMessage
			if msg, err = rd.Next(); err != nil {
				break
			}
			ev, derr := ParseEvent(msg.Data)
			if derr != nil {
				ev = Event{Data: msg.Data, Err: derr}
			}
			ev.ID = msg.ID
			select {
			case ch <- ev:
			case <-ctx.Done():
				_ = resp.Body.Close()
				return nil
			}
		}
		_ = resp.Body.Close()
		switch {
		case ctx.Err() != nil, errors.Is(err, io.EOF):
			return nil // cancelled, or ended by the service
		case errors.Is(err, ErrEventTooLarge):
			return err
		case reconnects >= o.MaxReconnects || rd.LastID() == "":
			return scrub(err)
		}
		reconnects++
		wait := rd.Retry()
		if wait <= 0 {
			wait = time.Second
		}
		wait = min(wait, 30*time.Second)
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return nil
		}
		var cerr error
		if resp, cerr = c.openStream(ctx, target, rd.LastID()); cerr != nil {
			if ctx.Err() != nil {
				return nil
			}
			return cerr
		}
	}
}

// openStream sends the GET of the stream and checks the answer (12.5.1: 200 and
// text/event-stream; an unsuccessful answer is a JSON error with a status of 400
// or more).
func (c *Client) openStream(ctx context.Context, target, lastID string) (*http.Response, error) {
	if strings.ContainsAny(lastID, "\r\n\x00") {
		return nil, errors.New("redfish: invalid Last-Event-ID")
	}
	u := *c.base
	u.Path, u.RawQuery = target, ""
	if i := strings.IndexByte(target, '?'); i >= 0 {
		u.Path, u.RawQuery = target[:i], target[i+1:]
	}
	// Only the wait for the answer is bounded by the timeout: the stream is not.
	hctx, stop := context.WithCancel(ctx)
	timer := time.AfterFunc(c.cfg.Timeout, stop)
	defer timer.Stop()
	req, err := http.NewRequestWithContext(hctx, http.MethodGet, u.String(), nil)
	if err != nil {
		stop()
		return nil, err
	}
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("OData-Version", "4.0")
	req.Header.Set("Cache-Control", "no-store")
	if lastID != "" {
		req.Header.Set("Last-Event-ID", lastID)
	}
	user, pw, err := c.cfg.Credentials.Get(ctx)
	if err != nil {
		stop()
		return nil, fmt.Errorf("redfish: the credentials are unavailable: %w", err)
	}
	c.mu.Lock()
	token := c.token
	c.mu.Unlock()
	switch {
	case token != "":
		req.Header.Set("X-Auth-Token", token)
	case !c.cfg.Session:
		req.SetBasicAuth(user, pw)
	}
	hc := *c.http
	hc.Timeout = 0
	resp, err := hc.Do(req)
	if err != nil {
		stop()
		return nil, scrub(err, pw, user)
	}
	if resp.StatusCode == http.StatusOK {
		mt, _, merr := mime.ParseMediaType(resp.Header.Get("Content-Type"))
		if merr != nil || mt != "text/event-stream" {
			_ = resp.Body.Close()
			stop()
			return nil, fmt.Errorf("%w: the stream is not text/event-stream", ErrMalformed)
		}
		// The request context is the caller's from here: the header timer is over.
		timer.Stop()
		go func() { // a cancel of ctx ends hctx; keep hctx alive until then
			<-ctx.Done()
			stop()
		}()
		return resp, nil
	}
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	_ = resp.Body.Close()
	stop()
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		if token != "" {
			c.mu.Lock()
			c.token, c.location = "", ""
			c.mu.Unlock()
		}
		ae := &authError{method: http.MethodGet, path: target, code: resp.StatusCode}
		_, _, ae.Info = ParseError(data)
		return nil, ae
	case resp.StatusCode == http.StatusNotFound:
		return nil, fmt.Errorf("%w (%s)", ErrNotFound, target)
	}
	se := &StatusError{Method: http.MethodGet, Path: target, Code: resp.StatusCode}
	se.ErrorCode, se.Message, se.Info = ParseError(data)
	return nil, se
}
