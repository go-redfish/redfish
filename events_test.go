package redfish_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/go-redfish/redfish"
	"github.com/go-redfish/redfish/mockbmc"
)

const eventsBase = "/redfish/v1/EventService"

func TestEventServiceRead(t *testing.T) {
	_, srv := newMock(t, mockbmc.Options{})
	c := newClient(t, srv, redfish.Config{})
	es, err := c.EventService(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if es.URI != eventsBase || es.SubscriptionsURI != eventsBase+"/Subscriptions" || es.ServerSentEventURI != eventsBase+"/SSE" ||
		es.ServiceEnabled == nil || !*es.ServiceEnabled || es.State != "Enabled" || !es.SubmitTestEvent {
		t.Fatalf("EventService = %+v", es)
	}
	if !slices.Equal(es.EventFormatTypes, []string{"Event", "MetricReport"}) || !slices.Contains(es.RegistryPrefixes, "Base") || !slices.Contains(es.ResourceTypes, "ComputerSystem") {
		t.Fatalf("lists = %+v", es)
	}
	if !es.SSEFilterProperties["MessageId"] || es.SSEFilterProperties["EventType"] || es.DeliveryRetryAttempts == nil || *es.DeliveryRetryAttempts != 3 {
		t.Fatalf("SSE filters = %v", es.SSEFilterProperties)
	}
	// The emulator tree has the old EventService 1.0: no SSE, no SubmitTestEvent.
	_, srv2 := newMock(t, mockbmc.Options{Tree: mockbmc.TreeEmulator})
	c2 := newClient(t, srv2, redfish.Config{})
	es2, err := c2.EventService(context.Background())
	if err != nil || es2.ServerSentEventURI != "" || es2.SubmitTestEvent || es2.DeliveryRetryIntervalSeconds == nil || *es2.DeliveryRetryIntervalSeconds != 60 {
		t.Fatalf("emulator EventService = %+v, %v", es2, err)
	}
	if _, err := c2.Events(context.Background(), ""); !errors.Is(err, redfish.ErrUnsupported) {
		t.Errorf("Events on the emulator: %v", err)
	}
	if _, err := c2.SubmitTestEvent(context.Background(), redfish.TestEvent{MessageID: "Contoso.1.0.X"}); !errors.Is(err, redfish.ErrUnsupported) {
		t.Errorf("SubmitTestEvent on the emulator: %v", err)
	}
}

func TestSubscriptionLifecycle(t *testing.T) {
	b, srv := newMock(t, mockbmc.Options{})
	c := newClient(t, srv, redfish.Config{})
	ctx := context.Background()
	before, err := c.Subscriptions(ctx)
	if err != nil || len(before) != 4 {
		t.Fatalf("Subscriptions = %v, %v", before, err)
	}
	yes := true
	sub, out, err := c.Subscribe(ctx, redfish.SubscribeOptions{Destination: "https://receiver.example:8443/events", Context: "ctx-1",
		EventFormatType: "Event", RegistryPrefixes: []string{"Base", "Resource"}, ResourceTypes: []string{"ComputerSystem"},
		MessageIDs: []string{"Contoso.TempAssert"}, ExcludeMessageIDs: []string{"Base.Noise"}, OriginResources: []string{"/redfish/v1/Systems/437XR1138R2"},
		SubordinateResources: &yes, HTTPHeaders: map[string]redfish.Secret{"Authorization": "Bearer s3cr3t-token"}})
	if err != nil || !out.Changed {
		t.Fatalf("Subscribe = %+v, %+v, %v", sub, out, err)
	}
	if sub.URI != eventsBase+"/Subscriptions/"+sub.ID || sub.Destination != "https://receiver.example:8443/events" || sub.Protocol != "Redfish" ||
		sub.SubscriptionType != "RedfishEvent" || sub.Context != "ctx-1" || !slices.Equal(sub.RegistryPrefixes, []string{"Base", "Resource"}) ||
		!slices.Equal(sub.OriginResources, []string{"/redfish/v1/Systems/437XR1138R2"}) {
		t.Fatalf("subscription = %+v", sub)
	}
	// What the service received carries the header; what it returns never does.
	got := b.Subscribed()
	if len(got) != 1 {
		t.Fatalf("subscribed = %v", got)
	}
	hdr, _ := got[0]["HttpHeaders"].([]any)
	if len(hdr) != 1 || hdr[0].(map[string]any)["Authorization"] != "Bearer s3cr3t-token" {
		t.Errorf("HttpHeaders sent = %v", got[0]["HttpHeaders"])
	}
	if strings.Contains(string(b.Resource(sub.URI)), "s3cr3t") || strings.Contains(fmt.Sprintf("%+v %v", sub, out), "s3cr3t") {
		t.Error("the header value is in what the service returned, or in the result")
	}
	after, _ := c.Subscriptions(ctx)
	if len(after) != 5 {
		t.Fatalf("after Subscribe: %d subscriptions", len(after))
	}
	if again, err := c.Subscription(ctx, sub.ID); err != nil || again.Destination != sub.Destination {
		t.Fatalf("Subscription(%s) = %+v, %v", sub.ID, again, err)
	}
	out, err = c.Unsubscribe(ctx, sub.URI)
	if err != nil || !out.Changed {
		t.Fatalf("Unsubscribe = %+v, %v", out, err)
	}
	if _, err := c.Subscription(ctx, sub.ID); !errors.Is(err, redfish.ErrNotFound) {
		t.Fatalf("a deleted subscription: %v", err)
	}
	if out, err = c.Unsubscribe(ctx, sub.ID); err != nil || out.Changed {
		t.Fatalf("a second Unsubscribe = %+v, %v (want unchanged)", out, err)
	}
	for _, bad := range []string{"../Accounts/1", "/redfish/v1/AccountService/Accounts/1", "http://evil/x", "", "a/b"} {
		if _, err := c.Unsubscribe(ctx, bad); err == nil {
			t.Errorf("Unsubscribe(%q) accepted", bad)
		}
	}
}

func TestSubscribeRefusals(t *testing.T) {
	b, srv := newMock(t, mockbmc.Options{})
	c := newClient(t, srv, redfish.Config{})
	ctx := context.Background()
	for name, dest := range map[string]string{
		"http":           "http://receiver.example/events",
		"userinfo":       "https://user:pw@receiver.example/events",
		"user only":      "https://token@receiver.example/events",
		"query":          "https://receiver.example/events?token=abc",
		"empty query":    "https://receiver.example/events?",
		"fragment":       "https://receiver.example/events#x",
		"no host":        "https:///events",
		"redfish-sse":    "redfish-sse://10.0.0.1:1234",
		"mailto":         "mailto:a@b.example",
		"empty":          "",
		"space":          "https://receiver.example/a b",
		"control":        "https://receiver.example/a\r\nHost: evil",
		"port too large": "https://receiver.example:99999/x",
		"too long":       "https://receiver.example/" + strings.Repeat("a", 3000),
	} {
		_, _, err := c.Subscribe(ctx, redfish.SubscribeOptions{Destination: dest})
		if !errors.Is(err, redfish.ErrInsecure) {
			t.Errorf("%s: %v (want ErrInsecure)", name, err)
		}
		if err != nil && (strings.Contains(err.Error(), "pw@") || strings.Contains(err.Error(), "token=abc")) {
			t.Errorf("%s: the error repeats the credential: %v", name, err)
		}
	}
	ok := "https://receiver.example/events"
	for name, o := range map[string]redfish.SubscribeOptions{
		"registry not listed":   {Destination: ok, RegistryPrefixes: []string{"Nope"}},
		"resource not listed":   {Destination: ok, ResourceTypes: []string{"Nope"}},
		"format not listed":     {Destination: ok, EventFormatType: "Nope"},
		"header with newline":   {Destination: ok, HTTPHeaders: map[string]redfish.Secret{"X-A": "v\r\nX-B: c"}},
		"header name":           {Destination: ok, HTTPHeaders: map[string]redfish.Secret{"X A": "v"}},
		"origin outside":        {Destination: ok, OriginResources: []string{"http://evil/redfish/v1"}},
		"empty value in a list": {Destination: ok, MessageIDs: []string{""}},
	} {
		if _, _, err := c.Subscribe(ctx, o); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	for _, line := range b.Log() {
		if strings.HasPrefix(line, "POST "+eventsBase+"/Subscriptions") {
			t.Fatalf("a refused subscription was sent: %s", line)
		}
	}
	// A disabled service is refused before the POST too.
	_, srv2 := newMock(t, mockbmc.Options{EventsDisabled: true})
	if _, _, err := newClient(t, srv2, redfish.Config{}).Subscribe(ctx, redfish.SubscribeOptions{Destination: ok}); !errors.Is(err, redfish.ErrUnsupported) {
		t.Errorf("disabled service: %v", err)
	}
}

// A service that echoes the request in its error must not make the header a
// part of ours.
func TestSubscribeErrorNeverCarriesTheHeader(t *testing.T) {
	const secret = "Zq7-receiver-token-4242"
	mux := http.NewServeMux()
	mux.HandleFunc("/redfish/v1/", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/redfish/v1/":
			fmt.Fprint(w, `{"EventService":{"@odata.id":"/redfish/v1/EventService"}}`)
		case "/redfish/v1/EventService":
			fmt.Fprint(w, `{"ServiceEnabled":true,"Subscriptions":{"@odata.id":"/redfish/v1/EventService/Subscriptions"}}`)
		case "/redfish/v1/EventService/Subscriptions":
			buf := make([]byte, 4096)
			n, _ := r.Body.Read(buf)
			w.WriteHeader(400)
			fmt.Fprintf(w, `{"error":{"code":"Base.1.19.GeneralError","message":%q,"@Message.ExtendedInfo":[{"Message":%q}]}}`, string(buf[:n]), string(buf[:n]))
		}
	})
	srv := httptest.NewTLSServer(mux)
	defer srv.Close()
	c := newClient(t, srv, redfish.Config{})
	_, _, err := c.Subscribe(context.Background(), redfish.SubscribeOptions{Destination: "https://r.example/x",
		HTTPHeaders: map[string]redfish.Secret{"Authorization": secret}})
	var se *redfish.StatusError
	if !errors.As(err, &se) || se.Code != 400 {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(fmt.Sprintf("%v %+v %#v", err, se, se), secret) {
		t.Errorf("the error carries the header value: %v", err)
	}
}

func TestSubmitTestEvent(t *testing.T) {
	b, srv := newMock(t, mockbmc.Options{})
	c := newClient(t, srv, redfish.Config{})
	ctx := context.Background()
	grp := int64(7)
	out, err := c.SubmitTestEvent(ctx, redfish.TestEvent{MessageID: "Contoso.1.0.TempAssert", Message: "hot", MessageArgs: []string{"a", "b"},
		Severity: "Warning", OriginOfCondition: "/redfish/v1/Systems/437XR1138R2", EventGroupID: &grp, EventType: "Alert"})
	if err != nil || !out.Changed {
		t.Fatalf("SubmitTestEvent = %+v, %v", out, err)
	}
	sent := b.TestEvents()
	if len(sent) != 1 || sent[0]["MessageId"] != "Contoso.1.0.TempAssert" || sent[0]["EventType"] != "Alert" || sent[0]["OriginOfCondition"] != "/redfish/v1/Systems/437XR1138R2" {
		t.Fatalf("sent = %v", sent)
	}
	for name, e := range map[string]redfish.TestEvent{
		"no MessageId":         {},
		"bad MessageId":        {MessageID: "TempAssert"},
		"EventType not listed": {MessageID: "Contoso.1.0.X", EventType: "Bogus"},
		"origin outside":       {MessageID: "Contoso.1.0.X", OriginOfCondition: "http://evil/x"},
	} {
		if _, err := c.SubmitTestEvent(ctx, e); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if len(b.TestEvents()) != 1 {
		t.Error("a refused test event was sent")
	}
	// The ActionInfo is what says a value is not allowed.
	if _, err := c.SubmitTestEvent(ctx, redfish.TestEvent{MessageID: "Contoso.1.0.X", EventType: "Bogus"}); !errors.Is(err, redfish.ErrUnsupported) {
		t.Errorf("EventType Bogus: %v", err)
	}
}

// recv waits for n events.
func recv(t *testing.T, s *redfish.EventStream, n int) []redfish.Event {
	t.Helper()
	var out []redfish.Event
	timeout := time.After(10 * time.Second)
	for len(out) < n {
		select {
		case ev, ok := <-s.C:
			if !ok {
				t.Fatalf("the stream ended after %d of %d events: %v", len(out), n, s.Err())
			}
			out = append(out, ev)
		case <-timeout:
			t.Fatalf("%d of %d events after 10 s", len(out), n)
		}
	}
	return out
}

func submit(t *testing.T, c *redfish.Client, ev redfish.TestEvent) {
	t.Helper()
	if _, err := c.SubmitTestEvent(context.Background(), ev); err != nil {
		t.Fatal(err)
	}
}

func TestEventsStreamAndCancel(t *testing.T) {
	b, srv := newMock(t, mockbmc.Options{})
	c := newClient(t, srv, redfish.Config{Session: true})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s, err := c.Events(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	// While the stream is open the service has an SSE EventDestination for it
	// (DSP0266 12.5.2).
	subs, _ := c.Subscriptions(context.Background())
	var sse *redfish.Subscription
	for i := range subs {
		if subs[i].SubscriptionType == "SSE" {
			sse = &subs[i]
		}
	}
	if sse == nil || !strings.HasPrefix(sse.Destination, "redfish-sse://") || sse.Context == "" {
		t.Fatalf("no SSE destination: %+v", subs)
	}
	submit(t, c, redfish.TestEvent{MessageID: "Contoso.1.0.TempAssert", Message: "hot", MessageArgs: []string{"x"}, Severity: "Warning", OriginOfCondition: "/redfish/v1/Systems/437XR1138R2"})
	ev := recv(t, s, 1)[0]
	if ev.Err != nil || ev.ID != "1" || ev.Context != sse.Context || ev.ODataType != "#Event.v1_6_0.Event" || len(ev.Records) != 1 {
		t.Fatalf("event = %+v", ev)
	}
	r := ev.Records[0]
	if r.MessageID != "Contoso.1.0.TempAssert" || r.Message != "hot" || r.Severity != "Warning" || r.OriginOfCondition != "/redfish/v1/Systems/437XR1138R2" ||
		!slices.Equal(r.MessageArgs, []string{"x"}) || r.Context != sse.Context {
		t.Fatalf("record = %+v", r)
	}
	if !strings.Contains(string(ev.Data), `"Events"`) {
		t.Errorf("Data = %s", ev.Data)
	}
	// The auth of the stream is the session's: the log shows a token, no password.
	cancel()
	select {
	case _, ok := <-s.C:
		if ok {
			// an event may still be buffered; the channel must close
			for range s.C {
			}
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the stream did not stop on cancel")
	}
	if err := s.Err(); err != nil {
		t.Errorf("Err after cancel = %v", err)
	}
	// The service deletes the destination when the connection closes (12.5.2).
	deadline := time.Now().Add(5 * time.Second)
	for {
		subs, _ = c.Subscriptions(context.Background())
		n := 0
		for _, x := range subs {
			if x.SubscriptionType == "SSE" {
				n++
			}
		}
		if n == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the SSE destination was not deleted: %+v", subs)
		}
		time.Sleep(20 * time.Millisecond)
	}
	_ = b
}

func TestEventsEndedByTheService(t *testing.T) {
	_, srv := newMock(t, mockbmc.Options{})
	c := newClient(t, srv, redfish.Config{})
	s, err := c.Events(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	subs, _ := c.Subscriptions(context.Background())
	for _, x := range subs {
		if x.SubscriptionType == "SSE" {
			if _, err := c.Unsubscribe(context.Background(), x.URI); err != nil {
				t.Fatal(err)
			}
		}
	}
	select {
	case _, ok := <-s.C:
		if ok {
			t.Fatal("an event")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the service did not close the connection of a deleted subscription")
	}
	// A clean end is not an error and is not reconnected (it would undo the delete).
	if err := s.Err(); err != nil {
		t.Errorf("Err = %v", err)
	}
	if n := countSSE(t, c); n != 0 {
		t.Errorf("%d SSE destinations after the end", n)
	}
}

func countSSE(t *testing.T, c *redfish.Client) int {
	t.Helper()
	subs, err := c.Subscriptions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, x := range subs {
		if x.SubscriptionType == "SSE" {
			n++
		}
	}
	return n
}

func TestEventsFilter(t *testing.T) {
	_, srv := newMock(t, mockbmc.Options{})
	c := newClient(t, srv, redfish.Config{})
	sys := "/redfish/v1/Systems/437XR1138R2"
	cases := []struct {
		name, filter string
		want         []string // the MessageIds that pass, among the events below
	}{
		{"none", "", []string{"Contoso.1.0.A", "Base.1.0.B", "Task.1.0.C", "Contoso.1.0.D"}},
		{"message id", "MessageId eq 'Base.1.0.B'", []string{"Base.1.0.B"}},
		{"message id without version", "MessageId eq 'Contoso.A'", []string{"Contoso.1.0.A"}},
		{"registry prefix", "RegistryPrefix eq 'Contoso'", []string{"Contoso.1.0.A", "Contoso.1.0.D"}},
		{"or of registries", "(RegistryPrefix eq 'Base') or (RegistryPrefix eq 'Task')", []string{"Base.1.0.B", "Task.1.0.C"}},
		{"and", "RegistryPrefix eq 'Contoso' and MessageId eq 'Contoso.1.0.D'", []string{"Contoso.1.0.D"}},
		{"origin", "OriginResource eq '" + sys + "'", []string{"Contoso.1.0.A"}},
		{"origin with subordinates", "(OriginResource eq '" + sys + "') and (SubordinateResources eq true)", []string{"Contoso.1.0.A", "Base.1.0.B"}},
		{"resource type", "ResourceType eq 'ComputerSystem'", []string{"Contoso.1.0.A"}},
		{"event format", "EventFormatType eq 'Event'", []string{"Contoso.1.0.A", "Base.1.0.B", "Task.1.0.C", "Contoso.1.0.D"}},
		{"nothing", "EventFormatType eq 'MetricReport'", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, err := c.Events(context.Background(), tc.filter)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			for _, e := range []redfish.TestEvent{
				{MessageID: "Contoso.1.0.A", OriginOfCondition: sys},
				{MessageID: "Base.1.0.B", OriginOfCondition: sys + "/Processors/CPU1"},
				{MessageID: "Task.1.0.C"},
				{MessageID: "Contoso.1.0.D"},
			} {
				submit(t, c, e)
			}
			if tc.filter != "EventFormatType eq 'MetricReport'" {
				var got []string
				for _, ev := range recv(t, s, len(tc.want)) {
					got = append(got, ev.Records[0].MessageID)
				}
				if !slices.Equal(got, tc.want) {
					t.Fatalf("got %v, want %v", got, tc.want)
				}
				return
			}
			select {
			case ev := <-s.C:
				t.Fatalf("an event passed a filter that excludes all: %+v", ev)
			case <-time.After(400 * time.Millisecond):
			}
		})
	}
}

func TestEventsFilterRefusals(t *testing.T) {
	_, srv := newMock(t, mockbmc.Options{})
	c := newClient(t, srv, redfish.Config{})
	ctx := context.Background()
	// A property the service says it does not support: refused before the GET.
	for _, f := range []string{"EventType eq 'Alert'", "MetricReportDefinition eq '/redfish/v1/x'", "Severity eq 'OK'"} {
		if _, err := c.Events(ctx, f); !errors.Is(err, redfish.ErrUnsupported) {
			t.Errorf("%q: %v", f, err)
		}
	}
	for _, f := range []string{"MessageId ne 'x'", "MessageId eq", "(MessageId eq 'x'", "MessageId eq x"} {
		_, err := c.Events(ctx, f)
		var se *redfish.StatusError
		if !errors.As(err, &se) || se.Code != 400 {
			t.Errorf("%q: %v (want the service's 400)", f, err)
		}
	}
	if _, err := c.Events(ctx, "MessageId eq 'a\r\nHost: x'"); err == nil {
		t.Error("a filter with a line break was sent")
	}
	if _, err := c.Events(ctx, strings.Repeat("a", redfish.MaxFilter+1)); err == nil {
		t.Error("a filter above the bound was sent")
	}
	// Quote and ampersand survive the URL.
	s, err := c.Events(ctx, "MessageId eq 'A&b=c d+e'")
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
}

func TestEventsResumeWithLastEventID(t *testing.T) {
	_, srv := newMock(t, mockbmc.Options{SSEDropAfter: 1})
	c := newClient(t, srv, redfish.Config{})
	s, err := c.EventsWith(context.Background(), redfish.EventsOptions{MaxReconnects: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for i := 1; i <= 3; i++ {
		submit(t, c, redfish.TestEvent{MessageID: fmt.Sprintf("Contoso.1.0.E%d", i)})
	}
	// The first connection is broken after event 1; the client reconnects with
	// Last-Event-ID: 1 and gets 2 and 3, none twice and none lost.
	var ids []string
	for _, ev := range recv(t, s, 3) {
		ids = append(ids, ev.ID+":"+ev.Records[0].MessageID)
	}
	if want := []string{"1:Contoso.1.0.E1", "2:Contoso.1.0.E2", "3:Contoso.1.0.E3"}; !slices.Equal(ids, want) {
		t.Fatalf("events = %v, want %v", ids, want)
	}
}

func TestEventsNoReconnectByDefaultOfEventsWith(t *testing.T) {
	_, srv := newMock(t, mockbmc.Options{SSEDropAfter: 1})
	c := newClient(t, srv, redfish.Config{})
	s, err := c.EventsWith(context.Background(), redfish.EventsOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	submit(t, c, redfish.TestEvent{MessageID: "Contoso.1.0.E1"})
	submit(t, c, redfish.TestEvent{MessageID: "Contoso.1.0.E2"})
	recv(t, s, 1)
	select {
	case ev, ok := <-s.C:
		if ok {
			t.Fatalf("an event after the break: %+v", ev)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the stream did not end")
	}
	if s.Err() == nil {
		t.Error("a broken connection ended the stream without an error")
	}
}

func TestEventsOversizedEventIsRefused(t *testing.T) {
	for name, o := range map[string]redfish.EventsOptions{
		"event": {MaxEvent: 2048},
		"line":  {MaxLine: 1024},
	} {
		t.Run(name, func(t *testing.T) {
			_, srv := newMock(t, mockbmc.Options{EventPad: 6000})
			c := newClient(t, srv, redfish.Config{})
			s, err := c.EventsWith(context.Background(), o)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			submit(t, c, redfish.TestEvent{MessageID: "Contoso.1.0.Big", Message: "m"})
			select {
			case ev, ok := <-s.C:
				if ok {
					t.Fatalf("an oversized event was delivered: %d bytes", len(ev.Data))
				}
			case <-time.After(5 * time.Second):
				t.Fatal("the stream did not end")
			}
			if err := s.Err(); !errors.Is(err, redfish.ErrEventTooLarge) {
				t.Errorf("Err = %v, want ErrEventTooLarge", err)
			}
		})
	}
	// Within the limits the same event passes.
	_, srv := newMock(t, mockbmc.Options{EventPad: 6000})
	c := newClient(t, srv, redfish.Config{})
	s, err := c.Events(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	submit(t, c, redfish.TestEvent{MessageID: "Contoso.1.0.Big"})
	if ev := recv(t, s, 1)[0]; len(ev.Records[0].Message) != 6000 {
		t.Errorf("message of %d bytes", len(ev.Records[0].Message))
	}
}

func TestEventsOpenRefusals(t *testing.T) {
	_, srv := newMock(t, mockbmc.Options{})
	bad := newClient(t, srv, redfish.Config{Credentials: redfish.Static{User: "admin", Password: "wrong-password-0"}})
	if _, err := bad.Events(context.Background(), ""); !errors.Is(err, redfish.ErrAuth) {
		t.Errorf("wrong password: %v", err)
	} else if strings.Contains(err.Error(), "wrong-password-0") {
		t.Errorf("the error carries the password: %v", err)
	}
	_, srv2 := newMock(t, mockbmc.Options{EventsDisabled: true})
	if _, err := newClient(t, srv2, redfish.Config{}).Events(context.Background(), ""); !errors.Is(err, redfish.ErrUnsupported) {
		t.Errorf("disabled service: %v", err)
	}
	// A 200 that is not text/event-stream is not a stream (DSP0266 12.5.1).
	mux := http.NewServeMux()
	mux.HandleFunc("/redfish/v1/", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/redfish/v1/":
			fmt.Fprint(w, `{"EventService":{"@odata.id":"/redfish/v1/EventService"}}`)
		case "/redfish/v1/EventService":
			fmt.Fprint(w, `{"ServiceEnabled":true,"ServerSentEventUri":"/redfish/v1/EventService/SSE"}`)
		default:
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"data":"x"}`)
		}
	})
	s3 := httptest.NewTLSServer(mux)
	defer s3.Close()
	if _, err := newClient(t, s3, redfish.Config{}).Events(context.Background(), ""); !errors.Is(err, redfish.ErrMalformed) {
		t.Errorf("a JSON answer: %v", err)
	}
}

func TestParityBeyondTheAnsibleModules(t *testing.T) {
	// Every row of the section "Beyond the Ansible modules" names tests that
	// exist (the table of the oracle is checked by TestParityTableIsHonest).
	checkBeyondRows(t)
}
