package redfish

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// The generic API: any Redfish operation on any resource, with the parts of
// DSP0266 1.23.0 (the section numbers below are its; 1.19.0 numbered some of
// them differently) every operation shares.
//
//   - Entity tags (DSP0266 6.5 ETags, 7.6 PATCH; RFC 9110 sections 8.8.3 and
//     13.1.1): a response carries an ETag header; a PATCH or PUT may send it back
//     in If-Match, and the service answers 412 when the resource changed since.
//   - Asynchronous operations (12.2): a 202 answer names a task monitor in
//     Location, and may carry Retry-After; the monitor answers 202 while the task
//     runs and the final response of the operation when it is done. WaitTask
//     follows it.
//   - Errors (8.6 Error responses; the Message object, 9.5.11; extended
//     information, 9.9.5): a non-2xx answer carries an error object with code,
//     message and @Message.ExtendedInfo, decoded into StatusError.

// Limits on what a service may make us hold.
const (
	maxInfo        = 64
	maxMessageText = 2048
	maxArgs        = 16
)

// Message is one Message object (Message.v1_x, DSP8010): an entry of
// @Message.ExtendedInfo.
type Message struct {
	MessageID         string   `json:"MessageId"`
	Message           string   `json:"Message"`
	Severity          string   `json:"Severity"`
	Resolution        string   `json:"Resolution"`
	MessageArgs       []string `json:"MessageArgs"`
	RelatedProperties []string `json:"RelatedProperties"`
}

func clipText(s string) string {
	if len(s) > maxMessageText {
		return s[:maxMessageText]
	}
	return s
}

// ParseError decodes the error object of an answer (and the top-level
// @Message.ExtendedInfo of a success): the code, the message and the extended
// information, bounded. A body that is not that JSON gives empty values.
func ParseError(data []byte) (code, message string, info []Message) {
	var d struct {
		Error *struct {
			Code    string    `json:"code"`
			Message string    `json:"message"`
			Info    []Message `json:"@Message.ExtendedInfo"`
		} `json:"error"`
		Info []Message `json:"@Message.ExtendedInfo"`
	}
	if json.Unmarshal(data, &d) != nil {
		return "", "", nil
	}
	if d.Error != nil {
		code, message, info = clipText(d.Error.Code), clipText(d.Error.Message), d.Error.Info
	} else {
		info = d.Info
	}
	if len(info) > maxInfo {
		info = info[:maxInfo]
	}
	for i := range info {
		m := &info[i]
		m.MessageID, m.Message, m.Severity, m.Resolution = clipText(m.MessageID), clipText(m.Message), clipText(m.Severity), clipText(m.Resolution)
		if len(m.MessageArgs) > maxArgs {
			m.MessageArgs = m.MessageArgs[:maxArgs]
		}
		if len(m.RelatedProperties) > maxArgs {
			m.RelatedProperties = m.RelatedProperties[:maxArgs]
		}
	}
	return code, message, info
}

// Request is one operation.
type Request struct {
	Method string // GET, PATCH, PUT, POST, DELETE, HEAD
	// Path is a path of the service, /redfish/... (a next link or a Location
	// header's path); never a URL.
	Path string
	Body []byte // a JSON document, sent as is
	// IfMatch is the ETag the resource must still have (PATCH, PUT, DELETE).
	IfMatch string
	// Header adds headers. Authorization, X-Auth-Token, Host and
	// Content-Length are refused: the client alone authenticates.
	Header map[string]string
}

// Response is an answer with a 2xx status.
type Response struct {
	Status int
	Header http.Header
	Body   []byte
}

// ETag is the entity tag of the resource the answer describes ("" if none).
func (r *Response) ETag() string { return r.Header.Get("ETag") }

// Location is the Location header: the new resource of a 201, or the task
// monitor of a 202 (a path of the service, "" if it is anything else).
func (r *Response) Location() string { return servicePath(r.Header.Get("Location")) }

// IsTask reports whether the operation continues as a task (202).
func (r *Response) IsTask() bool { return r.Status == http.StatusAccepted }

// RetryAfter is the delay the service asked for (seconds or an HTTP date), 0
// when it asked for none; bounded to an hour.
func (r *Response) RetryAfter() time.Duration {
	v := strings.TrimSpace(r.Header.Get("Retry-After"))
	if v == "" {
		return 0
	}
	if n, err := strconv.Atoi(v); err == nil && n >= 0 {
		return min(time.Duration(n)*time.Second, time.Hour)
	}
	if t, err := http.ParseTime(v); err == nil {
		return max(0, min(time.Until(t), time.Hour))
	}
	return 0
}

// JSON decodes the body.
func (r *Response) JSON(v any) error {
	if err := json.Unmarshal(r.Body, v); err != nil {
		return fmt.Errorf("%w: %v", ErrMalformed, err)
	}
	return nil
}

// Info is the @Message.ExtendedInfo of the answer.
func (r *Response) Info() []Message {
	_, _, info := ParseError(r.Body)
	return info
}

// servicePath keeps the path of a Location or link when it belongs to the
// service (/redfish/...), "" otherwise: a service cannot send us elsewhere.
func servicePath(loc string) string {
	if strings.HasPrefix(loc, "/redfish/") || loc == "/redfish" {
		if strings.Contains(loc, "..") || strings.ContainsAny(loc, "\\\r\n") {
			return ""
		}
		return loc
	}
	if i := strings.Index(loc, "://"); i > 0 {
		// An absolute URL is accepted only as its path, on this client's own host
		// (the caller checks it): the host is dropped, never followed.
		rest := loc[i+3:]
		if j := strings.IndexByte(rest, '/'); j >= 0 {
			return servicePath(rest[j:])
		}
	}
	return ""
}

var refusedHeaders = map[string]bool{"authorization": true, "x-auth-token": true, "host": true, "content-length": true, "cookie": true}

// Do sends a request and returns the answer. A non-2xx answer is an error:
// ErrAuth (401, 403), ErrNotFound (404) or a *StatusError carrying what the
// service said (412 when If-Match no longer matches, 400 with its
// ExtendedInfo...).
func (c *Client) Do(ctx context.Context, req Request) (*Response, error) {
	switch req.Method {
	case http.MethodGet, http.MethodHead, http.MethodPatch, http.MethodPut, http.MethodPost, http.MethodDelete:
	default:
		return nil, fmt.Errorf("redfish: method %q is not a Redfish method", req.Method)
	}
	p := servicePath(req.Path)
	if p == "" || strings.HasPrefix(req.Path, "http") {
		return nil, fmt.Errorf("%w: %q is not a path of this service (/redfish/...)", ErrMalformed, req.Path)
	}
	hdr := map[string]string{}
	for k, v := range req.Header {
		if refusedHeaders[strings.ToLower(k)] || strings.ContainsAny(k+v, "\r\n") {
			return nil, fmt.Errorf("redfish: the header %q is not allowed", k)
		}
		hdr[k] = v
	}
	if req.IfMatch != "" {
		if strings.ContainsAny(req.IfMatch, "\r\n") {
			return nil, errors.New("redfish: invalid If-Match")
		}
		hdr["If-Match"] = req.IfMatch
	}
	if err := c.login(ctx); err != nil {
		return nil, err
	}
	var status int
	data, h, err := c.requestStatus(ctx, req.Method, p, req.Body, hdr, &status)
	if err != nil {
		return nil, err
	}
	return &Response{Status: status, Header: h, Body: data}, nil
}

// Get reads a resource.
func (c *Client) Get(ctx context.Context, p string) (*Response, error) {
	return c.Do(ctx, Request{Method: http.MethodGet, Path: p})
}

func (c *Client) send(ctx context.Context, method, p string, v any, ifMatch string) (*Response, error) {
	var body []byte
	if v != nil {
		var err error
		if body, err = json.Marshal(v); err != nil {
			return nil, err
		}
	}
	return c.Do(ctx, Request{Method: method, Path: p, Body: body, IfMatch: ifMatch})
}

// Patch changes properties of a resource. ifMatch, when not empty, is the
// ETag the resource must still have.
func (c *Client) Patch(ctx context.Context, p string, v any, ifMatch string) (*Response, error) {
	return c.send(ctx, http.MethodPatch, p, v, ifMatch)
}

// Post creates a member of a collection or invokes an action (v is its
// parameters).
func (c *Client) Post(ctx context.Context, p string, v any) (*Response, error) {
	return c.send(ctx, http.MethodPost, p, v, "")
}

// Delete removes a resource.
func (c *Client) Delete(ctx context.Context, p, ifMatch string) (*Response, error) {
	return c.send(ctx, http.MethodDelete, p, nil, ifMatch)
}

// Login opens the session now (Config.Session); a client does it on first use.
func (c *Client) Login(ctx context.Context) error { return c.login(ctx) }

// Session is the token and the Session resource of the open session, to resume
// it later (Config.Token and SessionURI), or empty. The token is a credential:
// keep it as one.
func (c *Client) Session() (token, uri string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.token, c.location
}

// Task is what a Task resource says (Task.v1_x, DSP8010).
type Task struct {
	ID, Name        string
	State, Status   string // TaskState, TaskStatus
	PercentComplete *float64
	StartTime       string
	EndTime         string
	Messages        []Message
}

// ParseTask decodes a Task resource.
func ParseTask(data []byte) (Task, error) {
	var d struct {
		ID, Name        string
		TaskState       string
		TaskStatus      string
		PercentComplete *float64
		StartTime       string
		EndTime         string
		Messages        []Message
	}
	if err := json.Unmarshal(data, &d); err != nil {
		return Task{}, fmt.Errorf("%w: task: %v", ErrMalformed, err)
	}
	if len(d.Messages) > maxInfo {
		d.Messages = d.Messages[:maxInfo]
	}
	t := Task{ID: clipText(d.ID), Name: clipText(d.Name), State: clipText(d.TaskState), Status: clipText(d.TaskStatus),
		PercentComplete: d.PercentComplete, StartTime: clipText(d.StartTime), EndTime: clipText(d.EndTime), Messages: d.Messages}
	if t.PercentComplete != nil && (*t.PercentComplete < 0 || *t.PercentComplete > 100) {
		t.PercentComplete = nil
	}
	return t, nil
}

// WaitTask follows a task monitor until the task ends: it polls the monitor
// (Retry-After, else every poll, never faster than 100 ms) while it answers 202,
// calls onProgress with the Task each time, and returns the final response. The
// context bounds the wait.
func (c *Client) WaitTask(ctx context.Context, monitor string, poll time.Duration, onProgress func(Task)) (*Response, error) {
	if poll < 100*time.Millisecond {
		poll = 100 * time.Millisecond
	}
	for {
		resp, err := c.Get(ctx, monitor)
		if err != nil {
			return nil, err
		}
		if resp.Status != http.StatusAccepted {
			return resp, nil
		}
		if onProgress != nil {
			if t, err := ParseTask(resp.Body); err == nil {
				onProgress(t)
			}
		}
		wait := poll
		if ra := resp.RetryAfter(); ra > 0 {
			wait = max(ra, 100*time.Millisecond)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(wait):
		}
	}
}
