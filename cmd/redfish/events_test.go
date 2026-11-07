package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-redfish/redfish/mockbmc"
)

const hdrSecret = "Bearer Qm4-receiver-secret-77"

func TestEventsStatusSubscriptionsAndTest(t *testing.T) {
	e := newEnv(t)
	out, _, err := e.run("events", "status")
	st := jsonOut(t, out)
	if err != nil || st["server_sent_event_uri"] != "/redfish/v1/EventService/SSE" || st["submit_test_event"] != true || st["service_enabled"] != true {
		t.Fatalf("status: %v\n%s", err, out)
	}
	out, _, err = e.run("events", "status", "-o", "table")
	if err != nil || !strings.Contains(out, "server_sent_event_uri") {
		t.Fatalf("status table: %v\n%s", err, out)
	}
	out, _, err = e.run("events", "subscriptions")
	var subs []map[string]any
	if err != nil || json.Unmarshal([]byte(out), &subs) != nil || len(subs) != 4 {
		t.Fatalf("subscriptions: %v\n%s", err, out)
	}
	out, _, err = e.run("events", "subscriptions", "1")
	if err != nil || jsonOut(t, out)["destination"] != "http://www.dnsname.com/Destination1" {
		t.Fatalf("subscriptions 1: %v\n%s", err, out)
	}
	out, _, err = e.run("events", "test", "--message-id", "Contoso.1.0.TempAssert", "--message", "hot", "--arg", "a", "--severity", "Warning",
		"--origin", "/redfish/v1/Systems/437XR1138R2", "--group-id", "0")
	if err != nil || jsonOut(t, out)["changed"] != true {
		t.Fatalf("test: %v\n%s", err, out)
	}
	sent := e.bmc.TestEvents()
	if len(sent) != 1 || sent[0]["MessageId"] != "Contoso.1.0.TempAssert" || sent[0]["EventGroupId"] != float64(0) {
		t.Fatalf("sent = %v", sent)
	}
	for name, args := range map[string][]string{
		"no message id":   {"events", "test"},
		"bad message id":  {"events", "test", "--message-id", "nope"},
		"foreign origin":  {"events", "test", "--message-id", "A.1.0.B", "--origin", "http://evil/x"},
		"subscribe no -d": {"events", "subscribe"},
		"unsubscribe two": {"events", "unsubscribe", "1", "2"},
		"foreign unsub":   {"events", "unsubscribe", "/redfish/v1/AccountService/Accounts/1"},
		"sub foreign":     {"events", "subscriptions", "../x"},
	} {
		if _, _, err := e.run(args...); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// The emulator tree has no SubmitTestEvent and no SSE.
	e2 := newEnvTree(t, mockbmc.TreeEmulator)
	if _, _, err := e2.run("events", "test", "--message-id", "A.1.0.B"); err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Errorf("emulator test: %v", err)
	}
	if _, _, err := e2.run("events", "listen", "--count", "1"); err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Errorf("emulator listen: %v", err)
	}
}

func TestEventsSubscribeAndUnsubscribe(t *testing.T) {
	e := newEnv(t)
	hf := e.writePW("hdrs", "# for the receiver\nAuthorization: "+hdrSecret+"\nX-Tenant: blue\n", 0o600)
	out, errOut, err := e.run("events", "subscribe", "--destination", "https://receiver.example:8443/events", "--context", "mine",
		"--registry-prefix", "Base", "--resource-type", "ComputerSystem", "--origin", "/redfish/v1/Systems/437XR1138R2", "--subordinate", "--headers-file", hf)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out+errOut, hdrSecret) {
		t.Fatal("the header was printed")
	}
	doc := jsonOut(t, out)
	sub, _ := doc["subscription"].(map[string]any)
	id, _ := sub["id"].(string)
	if doc["changed"] != true || id == "" || sub["context"] != "mine" || sub["destination"] != "https://receiver.example:8443/events" {
		t.Fatalf("subscribe: %s", out)
	}
	got := e.bmc.Subscribed()
	if len(got) != 1 {
		t.Fatalf("subscribed = %v", got)
	}
	hdrs, _ := got[0]["HttpHeaders"].([]any)
	if len(hdrs) != 2 || hdrs[0].(map[string]any)["Authorization"] != hdrSecret || hdrs[1].(map[string]any)["X-Tenant"] != "blue" || got[0]["SubordinateResources"] != true {
		t.Errorf("the service received %v", got[0])
	}
	// The headers on the standard input.
	out, errOut, err = e.runIn("Authorization: "+hdrSecret+"\n", "events", "subscribe", "--destination", "https://receiver.example/x", "--headers-stdin")
	if err != nil || strings.Contains(out+errOut, hdrSecret) {
		t.Fatalf("stdin: %v\n%s%s", err, out, errOut)
	}
	out, _, err = e.run("events", "unsubscribe", id)
	if err != nil || jsonOut(t, out)["changed"] != true {
		t.Fatalf("unsubscribe: %v\n%s", err, out)
	}
	out, _, err = e.run("events", "unsubscribe", id)
	if err != nil || jsonOut(t, out)["changed"] != false {
		t.Fatalf("a second unsubscribe: %v\n%s", err, out)
	}
	// Refusals: nothing reaches the service, nothing prints the secret.
	n := len(e.bmc.Subscribed())
	loose := e.writePW("loose", "Authorization: "+hdrSecret+"\n", 0o644)
	for name, args := range map[string][]string{
		"http":              {"events", "subscribe", "--destination", "http://receiver.example/x"},
		"credentials":       {"events", "subscribe", "--destination", "https://u:" + hdrSecret + "@receiver.example/x"},
		"query":             {"events", "subscribe", "--destination", "https://receiver.example/x?token=" + hdrSecret},
		"loose headers":     {"events", "subscribe", "--destination", "https://receiver.example/x", "--headers-file", loose},
		"both sources":      {"events", "subscribe", "--destination", "https://receiver.example/x", "--headers-file", hf, "--headers-stdin"},
		"unsupported":       {"events", "subscribe", "--destination", "https://receiver.example/x", "--registry-prefix", "Nope"},
		"bad format":        {"events", "subscribe", "--destination", "https://receiver.example/x", "--format", "Bogus"},
		"no such headers":   {"events", "subscribe", "--destination", "https://receiver.example/x", "--headers-file", "/no/such/file"},
		"headers on a flag": {"events", "subscribe", "--destination", "https://receiver.example/x", "--header", "Authorization: " + hdrSecret},
	} {
		_, errOut, err := e.run(args...)
		if err == nil {
			t.Errorf("%s: accepted", name)
		} else if strings.Contains(err.Error()+errOut, hdrSecret) || strings.Contains(err.Error()+errOut, "u:") && strings.Contains(err.Error(), "@receiver") {
			t.Errorf("%s: the secret is in the error: %v", name, err)
		}
	}
	// A malformed headers file: the error names the line, not its text.
	for name, content := range map[string]string{"no colon": "Authorization " + hdrSecret + "\n", "empty": "\n# c\n", "dup": "A: 1\nA: 2\n", "control": "A: x\x01y\n"} {
		p := e.writePW("bad-"+strings.ReplaceAll(name, " ", "-"), content, 0o600)
		_, errOut, err := e.run("events", "subscribe", "--destination", "https://receiver.example/x", "--headers-file", p)
		if err == nil || strings.Contains(err.Error()+errOut, hdrSecret) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if len(e.bmc.Subscribed()) != n {
		t.Error("a refused subscription reached the service")
	}
}

// syncBuffer is a bytes.Buffer safe to read while a command writes it.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestEventsListen(t *testing.T) {
	e := newEnv(t)
	// --count ends the command cleanly after that many events.
	var out syncBuffer
	cmd := RootCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(append(append([]string{}, e.baseArgs...), "events", "listen", "--count", "2", "--filter", "RegistryPrefix eq 'Contoso'"))
	done := make(chan error, 1)
	go func() { done <- cmd.Execute() }()
	waitFor(t, func() bool { return sseUp(e) })
	for _, id := range []string{"Base.1.0.Skipped", "Contoso.1.0.One", "Contoso.1.0.Two"} {
		if _, _, err := e.run("events", "test", "--message-id", id, "--message", "m-"+id); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("listen did not end after --count events")
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("output:\n%s", out.String())
	}
	for i, want := range []string{"Contoso.1.0.One", "Contoso.1.0.Two"} {
		var l struct {
			ID    string `json:"id"`
			Event struct {
				Events []struct{ MessageId, Message string }
			} `json:"event"`
		}
		if err := json.Unmarshal([]byte(lines[i]), &l); err != nil || l.Event.Events[0].MessageId != want || l.Event.Events[0].Message != "m-"+want {
			t.Fatalf("line %d = %s (%v)", i, lines[i], err)
		}
	}
}

// Ctrl-C is the cancellation of the command's context: the stream ends, cleanly.
func TestEventsListenInterrupted(t *testing.T) {
	e := newEnv(t)
	var out syncBuffer
	cmd := RootCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(append(append([]string{}, e.baseArgs...), "events", "listen", "-o", "table"))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- cmd.ExecuteContext(ctx) }()
	waitFor(t, func() bool { return sseUp(e) })
	if _, _, err := e.run("events", "test", "--message-id", "Contoso.1.0.Hot", "--message", "it is hot", "--severity", "Warning", "--timestamp", "2026-11-07T21:00:00Z"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return strings.Contains(out.String(), "Contoso.1.0.Hot") })
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("an interrupt is not an error: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("listen did not stop on cancel")
	}
	if got := strings.TrimSpace(out.String()); got != "1\t2026-11-07T21:00:00Z\tWarning\tContoso.1.0.Hot\tit is hot" {
		t.Errorf("table line = %q", got)
	}
}

func TestEventsListenOversizedEvent(t *testing.T) {
	e := newEnvOpts(t, mockbmc.Options{EventPad: 6000})
	var out syncBuffer
	cmd := RootCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(append(append([]string{}, e.baseArgs...), "events", "listen", "--max-event-bytes", "2048"))
	done := make(chan error, 1)
	go func() { done <- cmd.Execute() }()
	waitFor(t, func() bool { return sseUp(e) })
	if _, _, err := e.run("events", "test", "--message-id", "Contoso.1.0.Big"); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "larger than the limit") {
			t.Fatalf("err = %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("listen did not refuse the oversized event")
	}
	if strings.TrimSpace(out.String()) != "" {
		t.Errorf("an oversized event was printed: %.100s", out.String())
	}
}

// sseUp: the service has made the SSE EventDestination of the stream the command
// opened (the mockup has four subscriptions; the first free Id is 5). The log of
// the mock holds a request only when it ends, which a stream does not.
func sseUp(e *env) bool {
	return strings.Contains(string(e.bmc.Resource("/redfish/v1/EventService/Subscriptions/5")), `"SSE"`)
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Whatever a headers file holds, readHeaders neither panics nor returns more than
// 16 headers, a name or value with a control character, or an error that has the
// input.
func FuzzReadHeaders(f *testing.F) {
	for _, s := range []string{"Authorization: Bearer x\n", "A: 1\r\nB: 2\r\n", "", "# c\n\nA:b", "A\n", ": v\n", "A: \x00\n", strings.Repeat("A: 1\n", 40), strings.Repeat("x", maxHeadersFile+1)} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		h, err := readHeaders(bytes.NewReader(data))
		if err != nil {
			if len(data) > 8 && strings.Contains(err.Error(), string(data)) {
				t.Fatalf("the error has the input: %v", err)
			}
			return
		}
		if len(h) == 0 || len(h) > 16 {
			t.Fatalf("%d headers", len(h))
		}
		for k, v := range h {
			for _, c := range []byte(k + v.Reveal()) {
				if (c < 0x20 && c != '\t') || c == 0x7f {
					t.Fatalf("control character in %q: %q", k, v.Reveal())
				}
			}
			if k == "" || v.Reveal() == "" {
				t.Fatal("an empty name or value")
			}
		}
	})
}
