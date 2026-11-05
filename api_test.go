package redfish_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-redfish/redfish"
	"github.com/go-redfish/redfish/mockbmc"
)

const sysPath = "/redfish/v1/Systems/437XR1138R2"

func newMock(t *testing.T, o mockbmc.Options) (*mockbmc.BMC, *httptest.Server) {
	t.Helper()
	b, err := mockbmc.New(o)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewTLSServer(b.Handler())
	t.Cleanup(srv.Close)
	return b, srv
}

func newClient(t *testing.T, srv *httptest.Server, cfg redfish.Config) *redfish.Client {
	t.Helper()
	cfg.BaseURL, cfg.HTTP = srv.URL, srv.Client()
	if cfg.Credentials == nil && cfg.Token == "" {
		cfg.Credentials = redfish.Static{User: "admin", Password: "password"}
	}
	c, err := redfish.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestETagAndIfMatch(t *testing.T) {
	_, srv := newMock(t, mockbmc.Options{})
	c := newClient(t, srv, redfish.Config{})
	ctx := context.Background()
	r, err := c.Get(ctx, sysPath)
	if err != nil || r.ETag() == "" {
		t.Fatalf("get: %v %q", err, r.ETag())
	}
	tag := r.ETag()
	// A matching If-Match is applied and the entity tag changes.
	p, err := c.Patch(ctx, sysPath, map[string]any{"AssetTag": "rack-7"}, tag)
	if err != nil || p.ETag() == "" || p.ETag() == tag {
		t.Fatalf("patch: %v %q", err, p.ETag())
	}
	// The old one is stale: 412, with what the service said.
	_, err = c.Patch(ctx, sysPath, map[string]any{"AssetTag": "rack-8"}, tag)
	var se *redfish.StatusError
	if !errors.As(err, &se) || se.Code != http.StatusPreconditionFailed || len(se.Info) != 1 || se.Info[0].MessageID != "Base.1.19.PreconditionFailed" {
		t.Fatalf("stale If-Match: %v (%+v)", err, se)
	}
	if !strings.Contains(err.Error(), "ETag") {
		t.Errorf("the error does not carry the service's message: %v", err)
	}
	var d struct{ AssetTag string }
	g, _ := c.Get(ctx, sysPath)
	if err := g.JSON(&d); err != nil || d.AssetTag != "rack-7" {
		t.Errorf("the stale patch changed the resource: %+v %v", d, err)
	}
}

func TestTypedErrors(t *testing.T) {
	_, srv := newMock(t, mockbmc.Options{})
	c := newClient(t, srv, redfish.Config{Credentials: redfish.Static{User: "admin", Password: "wrong"}})
	if _, err := c.Get(context.Background(), sysPath); !errors.Is(err, redfish.ErrAuth) {
		t.Errorf("wrong password: %v", err)
	}
	c = newClient(t, srv, redfish.Config{})
	if _, err := c.Get(context.Background(), "/redfish/v1/Nope"); !errors.Is(err, redfish.ErrNotFound) {
		t.Errorf("missing resource: %v", err)
	}
}

func TestSessionLoginResumeAndLogout(t *testing.T) {
	b, srv := newMock(t, mockbmc.Options{})
	c := newClient(t, srv, redfish.Config{Session: true})
	ctx := context.Background()
	if err := c.Login(ctx); err != nil {
		t.Fatal(err)
	}
	tok, uri := c.Session()
	if tok == "" || !strings.HasPrefix(uri, "/redfish/v1/SessionService/Sessions/") {
		t.Fatalf("session: %q %q", tok, uri)
	}
	// Resumed from the token alone: no credentials.
	r := newClient(t, srv, redfish.Config{Token: tok, SessionURI: uri})
	if _, err := r.Get(ctx, sysPath); err != nil {
		t.Fatalf("resumed: %v", err)
	}
	if err := r.Close(ctx); err != nil { // logout
		t.Fatal(err)
	}
	if _, err := newClient(t, srv, redfish.Config{Token: tok}).Get(ctx, sysPath); !errors.Is(err, redfish.ErrAuth) {
		t.Errorf("after logout: %v", err)
	}
	if b.Resource(uri) != nil {
		t.Error("the session resource remains")
	}
}

func TestActionAllowableValues(t *testing.T) {
	b, srv := newMock(t, mockbmc.Options{})
	c := newClient(t, srv, redfish.Config{})
	ctx := context.Background()
	target := sysPath + "/Actions/ComputerSystem.Reset"
	_, err := c.Post(ctx, target, map[string]any{"ResetType": "Explode"})
	var se *redfish.StatusError
	if !errors.As(err, &se) || se.Code != 400 || len(se.Info) == 0 || se.Info[0].MessageID != "Base.1.19.ActionParameterValueNotInList" {
		t.Fatalf("a value outside AllowableValues: %v", err)
	}
	r, err := c.Post(ctx, target, map[string]any{"ResetType": "ForceOff"})
	if err != nil || r.Status != http.StatusNoContent {
		t.Fatalf("reset: %v", err)
	}
	if !strings.Contains(string(b.Resource(sysPath)), `"PowerState":"Off"`) {
		t.Error("the power state did not change")
	}
}

func TestTaskMonitor(t *testing.T) {
	_, srv := newMock(t, mockbmc.Options{TaskPolls: 2})
	c := newClient(t, srv, redfish.Config{})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	r, err := c.Post(ctx, "/redfish/v1/UpdateService/Actions/SimpleUpdate", map[string]any{"ImageURI": "https://fw.example/bmc.bin"})
	if err != nil || !r.IsTask() || r.Location() == "" || r.RetryAfter() != time.Second {
		t.Fatalf("simple update: %v %+v", err, r)
	}
	var seen []float64
	final, err := c.WaitTask(ctx, r.Location(), 100*time.Millisecond, func(tk redfish.Task) {
		if tk.PercentComplete != nil {
			seen = append(seen, *tk.PercentComplete)
		}
	})
	if err != nil || final.Status != 200 {
		t.Fatalf("wait: %v", err)
	}
	tk, err := redfish.ParseTask(final.Body)
	if err != nil || tk.State != "Completed" || len(seen) != 2 {
		t.Fatalf("task: %+v %v progress %v", tk, err, seen)
	}
}

func TestDoRefusesWhatItShould(t *testing.T) {
	_, srv := newMock(t, mockbmc.Options{})
	c := newClient(t, srv, redfish.Config{})
	ctx := context.Background()
	for _, tc := range []redfish.Request{
		{Method: "GET", Path: "https://evil.example/redfish/v1/"},
		{Method: "GET", Path: "/etc/passwd"},
		{Method: "GET", Path: "/redfish/v1/../../etc"},
		{Method: "TRACE", Path: "/redfish/v1/"},
		{Method: "GET", Path: "/redfish/v1/", Header: map[string]string{"X-Auth-Token": "stolen"}},
		{Method: "GET", Path: "/redfish/v1/", Header: map[string]string{"Authorization": "Basic eA=="}},
		{Method: "GET", Path: "/redfish/v1/", Header: map[string]string{"X-A": "b\r\nX-B: c"}},
		{Method: "PATCH", Path: "/redfish/v1/", IfMatch: "a\r\nb"},
	} {
		if _, err := c.Do(ctx, tc); err == nil {
			t.Errorf("accepted %+v", tc)
		}
	}
}

func TestPostCreatesAndDeleteRemovesAMember(t *testing.T) {
	_, srv := newMock(t, mockbmc.Options{})
	c := newClient(t, srv, redfish.Config{})
	ctx := context.Background()
	r, err := c.Post(ctx, "/redfish/v1/AccountService/Accounts", map[string]any{"UserName": "bob", "Password": "pw-bob", "RoleId": "Operator"})
	if err != nil || r.Status != 201 || !strings.HasPrefix(r.Location(), "/redfish/v1/AccountService/Accounts/") {
		t.Fatalf("create: %v %+v", err, r)
	}
	// The new account logs in; its password is not in the resource.
	bob := newClient(t, srv, redfish.Config{Credentials: redfish.Static{User: "bob", Password: "pw-bob"}})
	g, err := bob.Get(ctx, r.Location())
	if err != nil || strings.Contains(string(g.Body), "pw-bob") {
		t.Fatalf("bob: %v %s", err, g.Body)
	}
	if _, err := c.Delete(ctx, r.Location(), ""); err != nil {
		t.Fatal(err)
	}
	if _, err := bob.Get(ctx, sysPath); !errors.Is(err, redfish.ErrAuth) {
		t.Errorf("a deleted account still logs in: %v", err)
	}
	if _, err := c.Get(ctx, r.Location()); !errors.Is(err, redfish.ErrNotFound) {
		t.Errorf("the deleted resource: %v", err)
	}
}

func FuzzParseError(f *testing.F) {
	f.Add([]byte(`{"error":{"code":"Base.1.19.GeneralError","message":"m","@Message.ExtendedInfo":[{"MessageId":"x","Message":"y","MessageArgs":["a"]}]}}`))
	f.Add([]byte(`{"@Message.ExtendedInfo":[{}]}`))
	f.Add([]byte(`[]`))
	f.Fuzz(func(t *testing.T, data []byte) {
		code, msg, info := redfish.ParseError(data)
		if len(code) > 2048 || len(msg) > 2048 || len(info) > 64 {
			t.Fatalf("unbounded: %d %d %d", len(code), len(msg), len(info))
		}
		for _, m := range info {
			if len(m.Message) > 2048 || len(m.MessageArgs) > 16 {
				t.Fatal("unbounded message")
			}
		}
	})
}

func FuzzParseTask(f *testing.F) {
	f.Add([]byte(`{"Id":"1","TaskState":"Running","PercentComplete":10,"Messages":[{"MessageId":"a"}]}`))
	f.Add([]byte(`{"PercentComplete":1e999}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		tk, err := redfish.ParseTask(data)
		if err != nil {
			return
		}
		if tk.PercentComplete != nil && (*tk.PercentComplete < 0 || *tk.PercentComplete > 100) {
			t.Fatalf("percent %v", *tk.PercentComplete)
		}
		if len(tk.Messages) > 64 {
			t.Fatal("unbounded")
		}
	})
}

// BenchmarkGet reads one resource of the mock over TLS keep-alive: the cost of
// the client's own work (request building, bounded read, decoding left to the
// caller). The comparison with redfishtool on the same mock is a separate run.
func BenchmarkGet(b *testing.B) {
	bmc, err := mockbmc.New(mockbmc.Options{})
	if err != nil {
		b.Fatal(err)
	}
	srv := httptest.NewTLSServer(bmc.Handler())
	defer srv.Close()
	c, err := redfish.New(redfish.Config{BaseURL: srv.URL, HTTP: srv.Client(), Credentials: redfish.Static{User: "admin", Password: "password"}, Session: true})
	if err != nil {
		b.Fatal(err)
	}
	ctx := context.Background()
	if _, err := c.Get(ctx, sysPath); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := c.Get(ctx, sysPath); err != nil {
			b.Fatal(err)
		}
	}
}
