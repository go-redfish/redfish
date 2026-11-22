package main

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"strings"
	"testing"

	"github.com/go-authn/secretsource"
)

// fakeManager replaces the three secret-manager calls with a map.
type fakeManager struct {
	entries                   map[string]string
	storeErr, openErr, delErr error
	stores, opens, deletes    int
	lastReplace               bool
	openBytes                 []byte // when set, Open returns it whatever the map holds
	closed                    int
}

type fakeSecret struct {
	b []byte
	m *fakeManager
}

func (f fakeSecret) Bytes() []byte { return f.b }
func (f fakeSecret) Close() error  { f.m.closed++; clear(f.b); return nil }

func useFakeManager(t *testing.T) *fakeManager {
	t.Helper()
	m := &fakeManager{entries: map[string]string{}}
	oo, os_, od := openSecretRef, storeSecretRef, deleteSecretRef
	t.Cleanup(func() { openSecretRef, storeSecretRef, deleteSecretRef = oo, os_, od })
	storeSecretRef = func(_ context.Context, ref string, secret []byte, o secretsource.StoreOptions) error {
		m.stores++
		m.lastReplace = o.Replace
		if m.storeErr != nil {
			return m.storeErr
		}
		m.entries[ref] = string(secret)
		return nil
	}
	openSecretRef = func(_ context.Context, ref string) (opened, error) {
		m.opens++
		if m.openErr != nil {
			return nil, m.openErr
		}
		if m.openBytes != nil {
			return fakeSecret{append([]byte(nil), m.openBytes...), m}, nil
		}
		v, ok := m.entries[ref]
		if !ok {
			return nil, secretsource.ErrNotFound
		}
		return fakeSecret{[]byte(v), m}, nil
	}
	deleteSecretRef = func(_ context.Context, ref string) error {
		m.deletes++
		if m.delErr != nil {
			return m.delErr
		}
		if _, ok := m.entries[ref]; !ok {
			return secretsource.ErrNotFound
		}
		delete(m.entries, ref)
		return nil
	}
	return m
}

const testRef = "keychain:redfish/bmc1"

func sessionCount(e *env) int {
	e.t.Helper()
	var l []any
	if err := json.Unmarshal([]byte(e.ok("session", "list")), &l); err != nil {
		e.t.Fatal(err)
	}
	return len(l)
}

func fileBytes(t *testing.T, e *env) string {
	t.Helper()
	b, err := os.ReadFile(mustSessionFile(t, e))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestSessionStoreRoundTrip(t *testing.T) {
	m := useFakeManager(t)
	e := newEnv(t)
	out := e.ok("session", "login", "--session-store", testRef)
	if !strings.Contains(out, "token in "+testRef) {
		t.Errorf("login: %q", out)
	}
	tok := m.entries[testRef]
	if tok == "" || !m.lastReplace {
		t.Fatalf("stored %q replace=%v", tok, m.lastReplace)
	}
	f := fileBytes(t, e)
	if strings.Contains(f, tok) || strings.Contains(f, `"token"`) || !strings.Contains(f, testRef) {
		t.Errorf("the file must hold the reference and no token: %s", f)
	}
	if strings.Contains(out, tok) {
		t.Error("the token was printed")
	}
	// A command with --session reads the token from the manager.
	args := []string{"--endpoint", e.srv.URL, "--ca-file", e.ca, "--session", "raw", "get", "/redfish/v1/"}
	if o, _, err := run(t, args...); err != nil || !strings.Contains(o, "RedfishVersion") {
		t.Fatalf("with --session: %v %q", err, o)
	}
	if m.opens == 0 || m.closed != m.opens {
		t.Errorf("opens=%d closes=%d: every secret is wiped", m.opens, m.closed)
	}
	if o, _, err := run(t, "--endpoint", e.srv.URL, "session", "status"); err != nil || !strings.Contains(o, "token in "+testRef) {
		t.Errorf("status: %v %q", err, o)
	}
	// A re-login replaces.
	e.ok("session", "login", "--session-store", testRef)
	if m.entries[testRef] == tok || m.stores != 2 {
		t.Errorf("re-login: stores=%d same token=%v", m.stores, m.entries[testRef] == tok)
	}
	// Logout ends the session, deletes the entry, removes the file.
	out, _, err := run(t, "--endpoint", e.srv.URL, "--ca-file", e.ca, "--session", "session", "logout")
	if err != nil || !strings.Contains(out, "session closed") {
		t.Fatalf("logout: %v %q", err, out)
	}
	if len(m.entries) != 0 || m.deletes != 1 {
		t.Errorf("entries %v deletes %d", m.entries, m.deletes)
	}
	if _, err := os.Stat(mustSessionFile(t, e)); !os.IsNotExist(err) {
		t.Errorf("the file stays: %v", err)
	}
}

func TestSessionStoreRefusedBeforeTheBMC(t *testing.T) {
	m := useFakeManager(t)
	e := newEnv(t)
	base := sessionCount(e)
	for _, ref := range []string{"file:/tmp/x", "systemd-creds:bmc", "secretservice:a=b", "nonsense", "keychain:", "", "keychain:a/b\x1b[2J"} {
		if ref == "" {
			continue
		}
		_, _, err := e.run("session", "login", "--session-store", ref)
		if err == nil {
			t.Errorf("%q accepted", ref)
			continue
		}
		mustBeClean(t, "error for "+ref, err.Error())
	}
	if m.stores != 0 || sessionCount(e) != base {
		t.Errorf("a refused reference reached the manager or the BMC: stores=%d sessions=%d (was %d)", m.stores, sessionCount(e), base)
	}
	if _, err := os.Stat(mustSessionFile(t, e)); !os.IsNotExist(err) {
		t.Errorf("a file was written: %v", err)
	}
	if err := checkStoreRef("nonsense"); !errors.Is(err, secretsource.ErrBadRef) {
		t.Errorf("a malformed reference is a bad reference: %v", err)
	}
	if err := checkStoreRef("file:/tmp/x"); errors.Is(err, secretsource.ErrBadRef) || err == nil {
		t.Errorf("file: parses but is refused: %v", err)
	}
	for _, ok := range []string{"keychain:svc/acct", "secretservice:svc/acct", "keepassxc:https://bmc.example"} {
		if err := checkStoreRef(ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
}

func TestSessionStoreFailureClosesTheBMCSession(t *testing.T) {
	m := useFakeManager(t)
	m.storeErr = secretsource.ErrUnavailable
	e := newEnv(t)
	base := sessionCount(e)
	_, _, err := e.run("session", "login", "--session-store", testRef)
	if err == nil || !errors.Is(err, secretsource.ErrUnavailable) {
		t.Fatalf("want the store error: %v", err)
	}
	if n := sessionCount(e); n != base {
		t.Errorf("%d BMC session(s) left behind", n)
	}
	if _, err := os.Stat(mustSessionFile(t, e)); !os.IsNotExist(err) {
		t.Errorf("a file was written: %v", err)
	}
}

// When the store fails and the BMC session cannot be ended either, both are
// said: the session is named so the person can end it.
func TestSessionStoreFailureAndCloseFailure(t *testing.T) {
	m := useFakeManager(t)
	e := newEnv(t)
	storeSecretRef = func(context.Context, string, []byte, secretsource.StoreOptions) error {
		e.ok("session", "clear") // the session is gone: Close will fail
		return secretsource.ErrDenied
	}
	_, _, err := e.run("session", "login", "--session-store", testRef)
	if err == nil || !errors.Is(err, secretsource.ErrDenied) || !strings.Contains(err.Error(), "could not be ended either") {
		t.Errorf("store and close failures: %v", err)
	}
	if len(m.entries) != 0 {
		t.Errorf("entries %v", m.entries)
	}
}

func TestSessionStoreDeleteFailureWithUnreadableToken(t *testing.T) {
	m := useFakeManager(t)
	e := newEnv(t)
	sess := []string{"--endpoint", e.srv.URL, "--ca-file", e.ca, "--session"}
	e.ok("session", "login", "--session-store", testRef)
	m.openErr, m.delErr = secretsource.ErrUnavailable, secretsource.ErrDenied
	_, _, err := run(t, append(sess, "session", "logout")...)
	if err == nil || !errors.Is(err, secretsource.ErrUnavailable) || !strings.Contains(err.Error(), "was not deleted") {
		t.Errorf("both failures must be said: %v", err)
	}
	if _, err := os.Stat(mustSessionFile(t, e)); !os.IsNotExist(err) {
		t.Errorf("the file stays: %v", err)
	}
}

func TestSessionStoreClearReportsDeleteFailure(t *testing.T) {
	m := useFakeManager(t)
	e := newEnv(t)
	e.ok("session", "login", "--session-store", testRef)
	m.delErr = secretsource.ErrDenied
	_, _, err := run(t, "--endpoint", e.srv.URL, "--ca-file", e.ca, "--session", "session", "clear")
	if err == nil || !errors.Is(err, secretsource.ErrDenied) {
		t.Errorf("clear with a failing Delete: %v", err)
	}
	if _, err := os.Stat(mustSessionFile(t, e)); !os.IsNotExist(err) {
		t.Errorf("the file stays: %v", err)
	}
}

func TestSessionStoreLogoutAndClearCleanup(t *testing.T) {
	m := useFakeManager(t)
	e := newEnv(t)
	sess := []string{"--endpoint", e.srv.URL, "--ca-file", e.ca, "--session"}

	// Delete fails: reported, after the file went.
	e.ok("session", "login", "--session-store", testRef)
	m.delErr = secretsource.ErrAmbiguous
	_, _, err := run(t, append(sess, "session", "logout")...)
	if err == nil || !errors.Is(err, secretsource.ErrAmbiguous) || !strings.Contains(err.Error(), testRef) {
		t.Errorf("logout with a failing Delete: %v", err)
	}
	if _, err := os.Stat(mustSessionFile(t, e)); !os.IsNotExist(err) {
		t.Errorf("the file must go even when Delete fails: %v", err)
	}

	// Absent entry: already gone, fine.
	m.delErr = nil
	m.entries = map[string]string{}
	e.ok("session", "login", "--session-store", testRef)
	delete(m.entries, testRef)
	if _, _, err := run(t, append(sess, "session", "logout")...); err == nil {
		// the token is gone, so the BMC session cannot be ended: reported
		t.Error("logout without a readable token: want the report")
	}
	if _, err := os.Stat(mustSessionFile(t, e)); !os.IsNotExist(err) {
		t.Errorf("the file stays: %v", err)
	}

	// The entry is absent at Delete time only: not an error.
	e.ok("session", "login", "--session-store", testRef)
	deleteSecretRef = func(context.Context, string) error {
		return &fs.PathError{Op: "delete", Path: testRef, Err: fs.ErrNotExist}
	}
	if _, _, err := run(t, append(sess, "session", "logout")...); err != nil {
		t.Errorf("an absent entry: %v", err)
	}

	// The BMC call fails (the session already ended): the entry and the file go.
	useFakeManager(t)
	m2 := useFakeManager(t)
	e.ok("session", "login", "--session-store", testRef)
	e.ok("session", "clear")
	if _, _, err := run(t, append(sess, "session", "logout")...); err != nil {
		t.Errorf("logout of an ended session: %v", err)
	}
	if len(m2.entries) != 0 {
		t.Errorf("entry left: %v", m2.entries)
	}

	// clear with --session cleans up too.
	e.ok("session", "login", "--session-store", testRef)
	if _, _, err := run(t, append(sess, "session", "clear")...); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if len(m2.entries) != 0 {
		t.Errorf("clear left the entry: %v", m2.entries)
	}
	if _, err := os.Stat(mustSessionFile(t, e)); !os.IsNotExist(err) {
		t.Errorf("clear left the file: %v", err)
	}
}

func TestSessionFileShapes(t *testing.T) {
	m := useFakeManager(t)
	e := newEnv(t)
	g := &globals{endpoint: e.srv.URL}
	p := mustSessionFile(t, e)
	if err := os.MkdirAll(dirOf(p), 0o700); err != nil {
		t.Fatal(err)
	}
	write := func(doc string) {
		t.Helper()
		if err := os.WriteFile(p, []byte(doc), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ep := `"endpoint":"` + e.srv.URL + `"`
	ctx := context.Background()

	// An old file, token in it, still loads and never asks the manager.
	write(`{` + ep + `,"token":"old","uri":"/u"}`)
	if s, err := g.loadSession(ctx); err != nil || s.Token != "old" || m.opens != 0 {
		t.Errorf("old format: %+v %v opens=%d", s, err, m.opens)
	}
	// Both fields: refused.
	write(`{` + ep + `,"token":"old","token_ref":"` + testRef + `","uri":"/u"}`)
	if _, err := g.loadSession(ctx); err == nil || m.opens != 0 {
		t.Errorf("both fields: %v opens=%d", err, m.opens)
	}
	// Neither: refused.
	write(`{` + ep + `,"uri":"/u"}`)
	if _, err := g.loadSession(ctx); err == nil {
		t.Error("neither field")
	}
	// A reference file: and systemd-creds: are not stores.
	for _, ref := range []string{"file:/etc/passwd", "systemd-creds:x", "bad"} {
		write(`{` + ep + `,"token_ref":"` + ref + `","uri":"/u"}`)
		if _, err := g.loadSession(ctx); err == nil || m.opens != 0 {
			t.Errorf("%s: %v opens=%d", ref, err, m.opens)
		}
	}
	// A good reference.
	write(`{` + ep + `,"token_ref":"` + testRef + `","uri":"/u"}`)
	m.entries[testRef] = "tok"
	if s, err := g.loadSession(ctx); err != nil || s.Token != "tok" {
		t.Errorf("good: %+v %v", s, err)
	}
	// Failures of the manager: absent, ambiguous, unavailable, empty, huge, control bytes.
	for name, c := range map[string]struct {
		err   error
		bytes []byte
	}{
		"absent":    {err: secretsource.ErrNotFound},
		"ambiguous": {err: secretsource.ErrAmbiguous},
		"toolarge":  {err: secretsource.ErrTooLarge},
		"empty":     {bytes: []byte{}},
		"huge":      {bytes: []byte(strings.Repeat("x", maxToken+1))},
		"control":   {bytes: []byte("a\r\nX-Evil: 1")},
	} {
		m.openErr, m.openBytes = c.err, c.bytes
		if c.err == nil && c.bytes != nil {
			m.openBytes = append([]byte{}, c.bytes...)
			if len(c.bytes) == 0 {
				m.openBytes = []byte{}
			}
		}
		s, err := g.loadSession(ctx)
		if err == nil || s.Token != "" {
			t.Errorf("%s: %+v %v", name, s, err)
			continue
		}
		mustBeClean(t, name, err.Error())
		if c.err != nil && !errors.Is(err, c.err) && name != "toolarge" {
			t.Errorf("%s: the cause is lost: %v", name, err)
		}
		if name == "toolarge" && !strings.Contains(err.Error(), "larger than") {
			t.Errorf("toolarge: %v", err)
		}
		if strings.Contains(err.Error(), "X-Evil") {
			t.Errorf("%s: the secret is in the error: %v", name, err)
		}
	}
}

func dirOf(p string) string {
	i := strings.LastIndexAny(p, `/\`)
	return p[:i]
}
