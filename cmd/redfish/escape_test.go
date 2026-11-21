package main

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/go-redfish/redfish"
	"github.com/spf13/cobra"
)

// The BMC is hostile: nothing it sends may put a control sequence on the
// operator's terminal. These tests drive the CLI against a server that does.

// leak names the first thing in s that a terminal could act on: a C0 control
// other than tab and newline, DEL, a C1 control, a bidirectional control, or a
// byte that is not UTF-8 (0x9b alone is CSI on some terminals). "" when none.
func leak(s string) string {
	if !utf8.ValidString(s) {
		return "invalid UTF-8"
	}
	// Spelled out here, not taken from forbidden(): the judge must not share
	// the subject's mistakes.
	for _, r := range s {
		c0 := r < 0x20 && r != '\t' && r != '\n'
		c1 := r >= 0x7f && r <= 0x9f
		bidi := (r >= 0x202a && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069) || r == 0x200e || r == 0x200f
		if c0 || c1 || bidi {
			return "U+" + strings.ToUpper(hex4(r))
		}
	}
	return ""
}

func hex4(r rune) string {
	const d = "0123456789abcdef"
	b := []byte{d[r>>12&15], d[r>>8&15], d[r>>4&15], d[r&15]}
	return string(b)
}

type hostile struct {
	srv  *httptest.Server
	args []string
	ca   string
}

func newHostile(t *testing.T, h http.HandlerFunc) *hostile {
	t.Helper()
	srv := httptest.NewTLSServer(h)
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	ca := filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o644); err != nil {
		t.Fatal(err)
	}
	pw := filepath.Join(dir, "pw")
	if err := os.WriteFile(pw, []byte("hunter2-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := userConfigDir
	userConfigDir = func() (string, error) { return filepath.Join(dir, "config"), nil }
	t.Cleanup(func() { userConfigDir = old })
	return &hostile{srv: srv, ca: ca, args: []string{"--endpoint", srv.URL, "--user", "admin", "--password-file", pw, "--ca-file", ca}}
}

// exec runs the CLI as main does: the error goes through execute to stderr.
func (h *hostile) exec(args ...string) (stdout, stderr string, code int) {
	return h.execWith(h.args, args...)
}

func (h *hostile) execWith(base []string, args ...string) (stdout, stderr string, code int) {
	var o, e bytes.Buffer
	cmd := RootCmd()
	cmd.SetOut(&o)
	cmd.SetErr(&e)
	cmd.SetArgs(append(append([]string{}, base...), args...))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	code = execute(ctx, cmd, &e)
	return o.String(), e.String(), code
}

func (h *hostile) sessionArgs() []string {
	return []string{"--endpoint", h.srv.URL, "--ca-file", h.ca, "--session"}
}

func TestHostileErrorTextIsCleaned(t *testing.T) {
	h := newHostile(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(400)
		_, _ = w.Write([]byte(`{"error":{"code":"Base.1.0.GeneralError","message":"\u001b]0;pwned\u0007\u001b[2Jx\u009b2J‮gnp.exe"}}`))
	})
	_, errOut, code := h.exec("raw", "get", "/redfish/v1/Systems")
	if code != 1 {
		t.Fatalf("code %d, want 1", code)
	}
	if l := leak(errOut); l != "" {
		t.Errorf("%s reached stderr: %q", l, errOut)
	}
	if !strings.Contains(errOut, "pwned") {
		t.Errorf("the message is gone, only its controls should be: %q", errOut)
	}
}

func TestHostileNonJSONBodyIsCleaned(t *testing.T) {
	h := newHostile(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("\x1b]0;pwned\x07\x1b[2Jhello\n\tworld\x9b\xe2\x80\xaegnp\r"))
	})
	for _, o := range []string{"json", "table"} {
		out, errOut, code := h.exec("-o", o, "raw", "get", "/redfish/v1/Systems")
		if code != 0 {
			t.Fatalf("-o %s: code %d: %s", o, code, errOut)
		}
		if l := leak(out); l != "" {
			t.Errorf("-o %s: %s reached stdout: %q", o, l, out)
		}
		if !strings.Contains(out, "hello\n\tworld") {
			t.Errorf("-o %s: the text lost its layout: %q", o, out)
		}
	}
}

// C1 controls, DEL and bidi overrides are valid inside a JSON string, so
// neither the table nor the indented JSON would have stopped them.
func TestHostileC1AndBidiAreCleaned(t *testing.T) {
	h := newHostile(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("{\"@odata.id\":\"/redfish/v1/Systems/1\",\"Name\":\"a\u009b2Jb‮gnp.exe⁦‏\",\"Del\":\"x\u007fy\",\"k\u009b\":1}"))
	})
	for _, o := range []string{"table", "json"} {
		out, _, code := h.exec("-o", o, "raw", "get", "/redfish/v1/Systems/1")
		if code != 0 {
			t.Fatalf("-o %s: code %d", o, code)
		}
		if l := leak(out); l != "" {
			t.Errorf("-o %s: %s reached stdout: %q", o, l, out)
		}
		if !strings.Contains(out, "gnp.exe") {
			t.Errorf("-o %s: the value is gone: %q", o, out)
		}
	}
}

func TestCleanDropsC1AndBidi(t *testing.T) {
	for _, r := range []rune{0x80, 0x9b, 0x9f, 0x202a, 0x202e, 0x2066, 0x2069, 0x200e, 0x200f, 0x7f, 0x1b} {
		if got := clean("a" + string(r) + "b"); got != "a?b" {
			t.Errorf("clean(U+%04X) = %q", r, got)
		}
	}
	for _, r := range []rune{0xa0, 0x2028, 0x200d, 0x202f, 0x2065, 0x206a, 'é', '日'} {
		if got := clean("a" + string(r) + "b"); got != "a"+string(r)+"b" {
			t.Errorf("clean(U+%04X) = %q: a printable rune was dropped", r, got)
		}
	}
}

// A Location with a control character or a ".." segment is refused by login:
// nothing is printed, nothing is saved, and logout can never be sent anywhere
// else.
func TestHostileSessionLocationRefused(t *testing.T) {
	for name, loc := range map[string]string{
		"escape":       "/redfish/v1/SessionService/Sessions/1%1b%5b2J%1b%5d0;pwned%07",
		"c1":           "/redfish/v1/SessionService/Sessions/1%c2%9b2J",
		"dotdot":       "/redfish/v1/SessionService/Sessions/../../AccountService/Accounts/1",
		"encodeddots":  "/redfish/v1/SessionService/Sessions/%2e%2e/%2e%2e/AccountService/Accounts/1",
		"encodedquery": "/redfish/v1/SessionService/Sessions/1%3Fx=1",
	} {
		t.Run(name, func(t *testing.T) {
			var deleted atomic.Int64
			h := newHostile(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.Method {
				case http.MethodPost:
					w.Header().Set("X-Auth-Token", "tok-123456")
					w.Header().Set("Location", loc)
					w.WriteHeader(201)
					_, _ = w.Write([]byte(`{}`))
				case http.MethodDelete:
					deleted.Add(1)
					w.WriteHeader(204)
				default:
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`{"@odata.id":"/redfish/v1/","RedfishVersion":"1.20.0"}`))
				}
			})
			out, errOut, code := h.exec("session", "login")
			if code == 0 {
				t.Fatalf("login accepted %q: %q", loc, out)
			}
			if l := leak(out + errOut); l != "" {
				t.Errorf("%s reached the terminal: %q %q", l, out, errOut)
			}
			if _, err := os.Stat(mustSessionFileOf(t, h)); err == nil {
				t.Errorf("a session file was saved for %q", loc)
			}
			if n := deleted.Load(); n != 0 {
				t.Errorf("%d DELETE sent", n)
			}
		})
	}
}

// A session file written before the fix can hold a hostile URI: status prints
// it cleaned, and --session refuses to use it.
func TestSessionStatusCleansTheSavedURI(t *testing.T) {
	h := newHostile(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
	p := mustSessionFileOf(t, h)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	doc := `{"endpoint":"` + h.srv.URL + `","token":"t","uri":"/redfish/v1/S/1\u001b[2J\u009b‮"}`
	if err := os.WriteFile(p, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	out, errOut, code := h.execWith([]string{"--endpoint", h.srv.URL}, "session", "status")
	if code != 0 {
		t.Fatalf("code %d: %s", code, errOut)
	}
	if l := leak(out); l != "" {
		t.Errorf("%s reached stdout: %q", l, out)
	}
	if _, errOut, code = h.execWith(h.sessionArgs(), "raw", "get", "/redfish/v1/"); code == 0 || leak(errOut) != "" {
		t.Errorf("--session used or echoed the hostile URI (code %d): %q", code, errOut)
	}
}

func mustSessionFileOf(t *testing.T, h *hostile) string {
	t.Helper()
	g := &globals{endpoint: h.srv.URL}
	p, err := g.sessionFile()
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// --- fuzz targets: one per output path, none skipping an input that already
// holds an escape. ---

func mustBeClean(t *testing.T, path, out string) {
	t.Helper()
	if l := leak(out); l != "" {
		t.Fatalf("%s: %s reached the writer: %q", path, l, out)
	}
}

// The error print of execute.
func FuzzExecuteError(f *testing.F) {
	for _, s := range []string{
		"plain", "\x1b]0;pwned\x07\x1b[2Jx", "\x9b2J", "a‮gnp.exe", "\x7f", "line1\nline2\ttab\r", "\xff\xfe",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, msg string) {
		root := &cobra.Command{Use: "x", SilenceUsage: true, SilenceErrors: true,
			RunE: func(*cobra.Command, []string) error { return errors.New(msg) }}
		var e bytes.Buffer
		if code := execute(context.Background(), root, &e); code != 1 {
			t.Fatalf("code %d", code)
		}
		mustBeClean(t, "execute", e.String())
	})
}

// printDoc, on JSON and on whatever is not, in both formats.
func FuzzPrintDoc(f *testing.F) {
	for _, s := range []string{
		"\x1b]0;pwned\x07\x1b[2Jhello", "\x9b2J", "not json ‮", `{"a":"\u001b[31m","b":"x` + "\u009b" + `y"}`,
		"{\"Name\":\"a\u009b2Jb‮gnp.exe\"}", "[\"\x7f\"]", "\"\x1b\"", "{\"\x1b\":1}", " ", "{}",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, doc string) {
		for _, o := range []string{"json", "table"} {
			var b bytes.Buffer
			g := &globals{output: o}
			_ = g.printDoc(&b, []byte(doc))
			mustBeClean(t, "printDoc -o "+o, b.String())
		}
	})
}

// printEvent, for a payload it decodes and for one it does not.
func FuzzPrintEvent(f *testing.F) {
	for _, s := range []string{
		`{"@odata.type":"#Event.v1_6_0.Event","Events":[{"Message":"\u001b[2J","MessageId":"a\u009b"}]}`,
		"\x1b]0;pwned\x07", "\x9b2J‮", `{"Events":[{"Message":"x\u007fy"}]}`, "{", "",
	} {
		f.Add(s, s)
	}
	f.Fuzz(func(t *testing.T, id, data string) {
		evs := []redfish.Event{{ID: id, Data: []byte(data)}, {ID: id, Data: []byte(data), Err: errors.New(data)}}
		if ev, err := redfish.ParseEvent([]byte(data)); err == nil {
			ev.ID = id
			evs = append(evs, ev)
		}
		for _, o := range []string{"json", "table"} {
			for _, ev := range evs {
				var b bytes.Buffer
				g := &globals{output: o}
				_ = g.printEvent(&b, ev)
				mustBeClean(t, "printEvent -o "+o, b.String())
			}
		}
	})
}

// The session status line, from a saved file whatever it holds.
func FuzzSessionStatus(f *testing.F) {
	for _, s := range []string{"/redfish/v1/S/1", "\x1b[2J", "\x9b", "‮", "\x7f", "a\\u001bb"} {
		f.Add(s)
	}
	dir := f.TempDir()
	old := userConfigDir
	userConfigDir = func() (string, error) { return dir, nil }
	f.Cleanup(func() { userConfigDir = old })
	g := &globals{endpoint: "https://bmc.example"}
	p, err := g.sessionFile()
	if err != nil {
		f.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, uri string) {
		b, _ := json.Marshal(savedSession{Endpoint: g.endpoint, Token: "t", URI: uri})
		if err := writeSecret(p, b); err != nil {
			t.Fatal(err)
		}
		var o, e bytes.Buffer
		cmd := RootCmd()
		cmd.SetOut(&o)
		cmd.SetErr(&e)
		cmd.SetArgs([]string{"--endpoint", g.endpoint, "session", "status"})
		code := execute(context.Background(), cmd, &e)
		mustBeClean(t, "session status", o.String())
		mustBeClean(t, "session status error", e.String())
		_ = code
	})
}
