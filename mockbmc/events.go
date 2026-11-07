package mockbmc

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// The EventService of the mock (DSP0266 1.23.0 12.1 and 12.5, EventService 1.12,
// EventDestination 1.17, Event 1.14). The rackmount mockup carries the service
// with a SubmitTestEvent action, its ActionInfo and four static subscriptions;
// augmentEvents adds what the Events commands need and the mockup does not carry:
//
//   - ServerSentEventUri, the SSE endpoint, with SSEFilterPropertiesSupported, and
//     the lists of EventFormatTypes, RegistryPrefixes and ResourceTypes a
//     subscription may use;
//   - POST to the Subscriptions collection: 201 Created with the Location and the
//     representation of the new EventDestination, or 400 when the body conflicts
//     with what the service supports (12.1.2); DELETE of a member (the generic
//     handler). HttpHeaders is stored by the mock for the tests and never
//     returned (the schema: null or an empty array in responses);
//   - SubmitTestEvent adds an event to the stream (history of 256 events);
//   - the SSE stream: 200 text/event-stream, an EventDestination of type SSE with
//     a service-generated Context for as long as the connection is open (12.5.2),
//     events with an id field and a pretty-printed JSON data field (12.5.2.1), a
//     comment line first, a retry field, Last-Event-ID honoured for the events
//     the history still holds, only events that happen after the connection when
//     there is none, and the $filter subset documented on parseFilter.
//
// The mock does not push events to the destinations of the subscriptions (it
// makes no outgoing connection). The emulator tree has the old EventService 1.0:
// its subscriptions collection exists (empty) but there is no ServerSentEventUri
// and no SubmitTestEvent.

const (
	subsRoot = root + "/EventService/Subscriptions"
	sseURI   = root + "/EventService/SSE"
	maxHist  = 256
)

// eventHub holds the events of the stream.
type eventHub struct {
	next    int
	hist    []histEvent
	wake    chan struct{}
	dropped bool
	sseConn int
}

type histEvent struct {
	id  int
	rec map[string]any
}

// Subscribed returns the bodies of the subscriptions accepted by POST so far,
// as sent (HttpHeaders included: for the tests that check what was sent).
func (b *BMC) Subscribed() []map[string]any {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(b.subscribed)
}

// TestEvents returns the bodies of the SubmitTestEvent actions accepted so far.
func (b *BMC) TestEvents() []map[string]any {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(b.testEvents)
}

var (
	registrySet = []any{"Base", "Resource", "Task", "Contoso"}
	typeSet     = []any{"ComputerSystem", "Chassis", "Manager", "Task"}
)

func (b *BMC) augmentEvents(tree string) {
	b.ev = &eventHub{wake: make(chan struct{})}
	es := b.res[root+"/EventService"]
	if es == nil {
		return
	}
	if b.res[subsRoot] == nil {
		b.res[subsRoot] = map[string]any{"@odata.type": "#EventDestinationCollection.EventDestinationCollection", "@odata.id": subsRoot,
			"Name": "Event Subscriptions Collection", "Members": []any{}, "Members@odata.count": 0}
	}
	if b.opts.EventsDisabled {
		es["ServiceEnabled"] = false
	}
	if tree != TreeRackmount {
		return
	}
	es["ServerSentEventUri"] = sseURI
	es["EventFormatTypes"] = []any{"Event", "MetricReport"}
	es["RegistryPrefixes"] = registrySet
	es["ResourceTypes"] = typeSet
	es["SubordinateResourcesSupported"] = true
	es["OriginResourcesSupported"] = true
	es["ExcludeMessageId"] = true
	es["ExcludeRegistryPrefix"] = true
	es["SSEFilterPropertiesSupported"] = map[string]any{"EventFormatType": true, "MessageId": true, "OriginResource": true,
		"RegistryPrefix": true, "ResourceType": true, "SubordinateResources": true, "EventType": false, "MetricReportDefinition": false}
}

// checkSubscription validates the body of a POST to the Subscriptions
// collection and completes it as the service would; false: it answered.
func (b *BMC) checkSubscription(w http.ResponseWriter, in map[string]any) bool {
	bad := func(msg, id string) bool {
		b.fail(w, http.StatusBadRequest, "", msg, id)
		return false
	}
	dest, _ := in["Destination"].(string)
	if dest == "" {
		return bad("The property Destination is a required property and must be included in the request.", "Base.1.19.PropertyMissing")
	}
	if !strings.HasPrefix(dest, "https://") && !strings.HasPrefix(dest, "http://") {
		return bad("The value "+dest+" for the property Destination is not in the correct format.", "Base.1.19.PropertyValueFormatError")
	}
	if p, _ := in["Protocol"].(string); p != "Redfish" {
		return bad(fmt.Sprintf("The value %v for the property Protocol is not in the list of acceptable values.", in["Protocol"]), "Base.1.19.PropertyValueNotInList")
	}
	es := b.res[root+"/EventService"]
	for _, c := range []struct {
		prop string
		have any
	}{{"RegistryPrefixes", es["RegistryPrefixes"]}, {"ExcludeRegistryPrefixes", es["RegistryPrefixes"]}, {"ResourceTypes", es["ResourceTypes"]}, {"EventFormatType", es["EventFormatTypes"]}} {
		got, present := in[c.prop]
		if !present {
			continue
		}
		have, _ := c.have.([]any)
		vals, isList := got.([]any)
		if !isList {
			vals = []any{got}
		}
		for _, v := range vals {
			if !slices.Contains(have, v) {
				return bad(fmt.Sprintf("The value %v for the property %s is not in the list of acceptable values.", v, c.prop), "Base.1.19.PropertyValueNotInList")
			}
		}
	}
	sent := map[string]any{}
	for k, v := range in {
		sent[k] = v
	}
	b.subscribed = append(b.subscribed, sent)
	delete(in, "HttpHeaders")
	in["HttpHeaders"] = []any{}
	if _, ok := in["Context"]; !ok {
		in["Context"] = ""
	}
	in["SubscriptionType"] = "RedfishEvent"
	if _, ok := in["EventFormatType"]; !ok {
		in["EventFormatType"] = "Event"
	}
	in["@odata.type"] = "#EventDestination.v1_17_1.EventDestination"
	in["Name"] = "EventSubscription"
	in["Status"] = map[string]any{"State": "Enabled", "Health": "OK"}
	return true
}

var messageID = regexp.MustCompile(`^\w+\.\d+\.\d+\.\w+$`)

// submitTestEvent serves the SubmitTestEvent action: the event is added to the
// stream. The padding of Options.EventPad makes the event as large as a test
// needs.
func (b *BMC) submitTestEvent(w http.ResponseWriter, in map[string]any) {
	mid, _ := in["MessageId"].(string)
	if mid == "" {
		b.fail(w, http.StatusBadRequest, "", "The action parameter MessageId is required.", "Base.1.19.ActionParameterMissing")
		return
	}
	if !messageID.MatchString(mid) {
		b.fail(w, http.StatusBadRequest, "", "The value "+mid+" for the parameter MessageId is not in the correct format.", "Base.1.19.ActionParameterValueFormatError")
		return
	}
	b.testEvents = append(b.testEvents, in)
	n := b.ev.next + 1
	rec := map[string]any{"MemberId": "1", "EventId": strconv.Itoa(n), "MessageId": mid}
	for _, k := range []string{"EventId", "EventGroupId", "EventTimestamp", "EventType", "Message", "MessageArgs", "MessageSeverity", "Severity"} {
		if v, ok := in[k]; ok {
			rec[k] = v
		}
	}
	if o, _ := in["OriginOfCondition"].(string); o != "" {
		rec["OriginOfCondition"] = map[string]any{"@odata.id": o}
	}
	if b.opts.EventPad > 0 {
		msg, _ := rec["Message"].(string)
		rec["Message"] = msg + strings.Repeat("x", b.opts.EventPad)
	}
	b.ev.next = n
	b.ev.hist = append(b.ev.hist, histEvent{id: n, rec: rec})
	if len(b.ev.hist) > maxHist {
		b.ev.hist = b.ev.hist[1:]
	}
	close(b.ev.wake)
	b.ev.wake = make(chan struct{})
	w.WriteHeader(http.StatusNoContent)
}

// ---- The $filter of the stream ----

// parseFilter reads the $filter of the SSE URI (DSP0266 12.5.2). Supported:
// comparisons `Property eq 'value'` of EventFormatType, MessageId (the full
// MessageId, or without its version: Contoso.TempAssert), RegistryPrefix,
// OriginResource and ResourceType, `SubordinateResources eq true` (it widens the
// OriginResource terms to the resources below, and is neutral as a term),
// joined by `and` and `or` (and binds tighter) and grouped by parentheses. Not
// supported, and refused with 400: EventType, MetricReportDefinition and any
// other property, any other operator.
func parseFilter(s string) (func(rec map[string]any, res func(string) string) bool, error) {
	p := &fparser{toks: tokens(s)}
	if len(p.toks) == 0 {
		return func(map[string]any, func(string) string) bool { return true }, nil
	}
	n, err := p.or()
	if err != nil {
		return nil, err
	}
	if p.i != len(p.toks) {
		return nil, fmt.Errorf("unexpected %q in the filter", p.toks[p.i])
	}
	sub := false
	for _, t := range p.terms {
		sub = sub || (t.prop == "SubordinateResources" && t.val == "true")
	}
	return func(rec map[string]any, res func(string) string) bool { return n(rec, res, sub) }, nil
}

type fterm struct{ prop, val string }

type fparser struct {
	toks  []string
	i     int
	terms []fterm
}

type fnode = func(rec map[string]any, res func(string) string, sub bool) bool

func tokens(s string) []string {
	var out []string
	for i := 0; i < len(s); {
		switch c := s[i]; {
		case c == ' ' || c == '\t':
			i++
		case c == '(' || c == ')':
			out = append(out, string(c))
			i++
		case c == '\'':
			j := i + 1
			var sb strings.Builder
			for j < len(s) {
				if s[j] == '\'' {
					if j+1 < len(s) && s[j+1] == '\'' {
						sb.WriteByte('\'')
						j += 2
						continue
					}
					break
				}
				sb.WriteByte(s[j])
				j++
			}
			out = append(out, "'"+sb.String()+"'")
			i = j + 1
		default:
			j := i
			for j < len(s) && s[j] != ' ' && s[j] != '(' && s[j] != ')' && s[j] != '\'' {
				j++
			}
			out = append(out, s[i:j])
			i = j
		}
	}
	return out
}

func (p *fparser) peek() string {
	if p.i < len(p.toks) {
		return p.toks[p.i]
	}
	return ""
}

func (p *fparser) or() (fnode, error) {
	l, err := p.and()
	if err != nil {
		return nil, err
	}
	for p.peek() == "or" {
		p.i++
		r, err := p.and()
		if err != nil {
			return nil, err
		}
		a, b := l, r
		l = func(rec map[string]any, res func(string) string, sub bool) bool {
			return a(rec, res, sub) || b(rec, res, sub)
		}
	}
	return l, nil
}

func (p *fparser) and() (fnode, error) {
	l, err := p.unary()
	if err != nil {
		return nil, err
	}
	for p.peek() == "and" {
		p.i++
		r, err := p.unary()
		if err != nil {
			return nil, err
		}
		a, b := l, r
		l = func(rec map[string]any, res func(string) string, sub bool) bool {
			return a(rec, res, sub) && b(rec, res, sub)
		}
	}
	return l, nil
}

func (p *fparser) unary() (fnode, error) {
	if p.peek() == "(" {
		p.i++
		n, err := p.or()
		if err != nil {
			return nil, err
		}
		if p.peek() != ")" {
			return nil, fmt.Errorf("a parenthesis is not closed")
		}
		p.i++
		return n, nil
	}
	if p.i+3 > len(p.toks) {
		return nil, fmt.Errorf("the filter ends in the middle of a comparison")
	}
	prop, op, val := p.toks[p.i], p.toks[p.i+1], p.toks[p.i+2]
	p.i += 3
	if op != "eq" {
		return nil, fmt.Errorf("the operator %q is not supported (only eq)", op)
	}
	if strings.HasPrefix(val, "'") {
		val = val[1 : len(val)-1]
	} else if val != "true" && val != "false" {
		return nil, fmt.Errorf("the value %q is not a string or a boolean", val)
	}
	switch prop {
	case "EventFormatType", "MessageId", "RegistryPrefix", "OriginResource", "ResourceType", "SubordinateResources":
	default:
		return nil, fmt.Errorf("the property %s is not supported in $filter", prop)
	}
	p.terms = append(p.terms, fterm{prop, val})
	return func(rec map[string]any, res func(string) string, sub bool) bool {
		mid, _ := rec["MessageId"].(string)
		origin := ""
		if o, _ := rec["OriginOfCondition"].(map[string]any); o != nil {
			origin, _ = o["@odata.id"].(string)
		}
		switch prop {
		case "EventFormatType":
			return val == "Event"
		case "MessageId":
			parts := strings.Split(mid, ".")
			return mid == val || (len(parts) == 4 && parts[0]+"."+parts[3] == val)
		case "RegistryPrefix":
			return strings.Split(mid, ".")[0] == val
		case "OriginResource":
			return origin != "" && (origin == val || (sub && strings.HasPrefix(origin, strings.TrimRight(val, "/")+"/")))
		case "ResourceType":
			if origin == "" {
				return false
			}
			t := strings.TrimPrefix(res(origin), "#")
			return strings.Split(t, ".")[0] == val
		}
		return true // SubordinateResources
	}, nil
}

// ---- The stream ----

// sse serves the SSE endpoint. It is called with the lock held and returns with
// it held (serve unlocks): the stream itself runs without it.
func (b *BMC) sse(w http.ResponseWriter, r *http.Request) {
	match, err := parseFilter(r.URL.Query().Get("$filter"))
	if err != nil {
		b.fail(w, http.StatusBadRequest, "", "The query parameter $filter is not valid: "+err.Error(), "Base.1.19.QueryParameterValueFormatError")
		return
	}
	fl, ok := w.(http.Flusher)
	if !ok {
		b.fail(w, http.StatusInternalServerError, "", "Streaming is not supported.", "Base.1.19.InternalError")
		return
	}
	last := b.ev.next // no retroactive events (12.1.2)
	if v := r.Header.Get("Last-Event-ID"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 && n <= b.ev.next {
			last = n
		}
	}
	b.ev.sseConn++
	ctx := "sse-" + hex.EncodeToString(sumOf("sse"+strconv.Itoa(b.ev.sseConn)))
	n := 1
	for b.res[subsRoot+"/"+strconv.Itoa(n)] != nil {
		n++
	}
	sub := subsRoot + "/" + strconv.Itoa(n)
	b.addMember(subsRoot, sub, map[string]any{"@odata.type": "#EventDestination.v1_17_1.EventDestination", "Id": strconv.Itoa(n),
		"Name": "EventSubscription SSE", "Destination": "redfish-sse://" + r.RemoteAddr, "Protocol": "Redfish", "SubscriptionType": "SSE",
		"Context": ctx, "EventFormatType": "Event", "HttpHeaders": []any{}, "Status": map[string]any{"State": "Enabled", "Health": "OK"}})
	drop := b.opts.SSEDropAfter > 0 && !b.ev.dropped
	if drop {
		b.ev.dropped = true
	}
	resLocked := func(p string) string {
		b.mu.Lock()
		defer b.mu.Unlock()
		t, _ := b.res[p]["@odata.type"].(string)
		return t
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprint(w, ": connected\nretry: 20\n\n")
	fl.Flush()

	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		if b.res[sub] != nil { // closed by the client: the service deletes the destination
			var keep []any
			for _, x := range b.res[subsRoot]["Members"].([]any) {
				if x.(map[string]any)["@odata.id"] != sub {
					keep = append(keep, x)
				}
			}
			b.res[subsRoot]["Members"], b.res[subsRoot]["Members@odata.count"] = keep, len(keep)
			delete(b.res, sub)
		}
	}()
	sent := 0
	for {
		b.mu.Lock()
		if b.res[sub] == nil { // deleted: the service closes the connection (12.5.2)
			b.mu.Unlock()
			return
		}
		var out []histEvent
		for _, e := range b.ev.hist {
			if e.id > last {
				out = append(out, e)
			}
		}
		wake := b.ev.wake
		b.mu.Unlock()
		for _, e := range out {
			last = e.id
			rec := map[string]any{}
			for k, v := range e.rec {
				rec[k] = v
			}
			if !match(rec, resLocked) {
				continue
			}
			rec["Context"] = ctx
			payload := map[string]any{"@odata.type": "#Event.v1_6_0.Event", "Id": strconv.Itoa(e.id), "Name": "Event Array", "Context": ctx, "Events": []any{rec}}
			data, _ := json.MarshalIndent(payload, "", "    ")
			var sb strings.Builder
			sb.WriteString("id: " + strconv.Itoa(e.id) + "\n")
			for _, ln := range strings.Split(string(data), "\n") {
				sb.WriteString("data:" + ln + "\n")
			}
			sb.WriteString("\n")
			if _, err := fmt.Fprint(w, sb.String()); err != nil {
				return
			}
			fl.Flush()
			sent++
			if drop && sent >= b.opts.SSEDropAfter {
				panic(http.ErrAbortHandler) // the connection breaks: no end of stream
			}
		}
		select {
		case <-wake:
		case <-r.Context().Done():
			return
		case <-time.After(100 * time.Millisecond): // to notice a deleted subscription
		}
	}
}
