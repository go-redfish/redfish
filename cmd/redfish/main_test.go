package main

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"net/http/httptest"

	"github.com/go-redfish/redfish/mockbmc"
)

// env is a mock BMC over TLS, a CA file, a password file and a config dir.
type env struct {
	t        *testing.T
	bmc      *mockbmc.BMC
	srv      *httptest.Server
	ca, pw   string
	cfgDir   string
	baseArgs []string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	b, err := mockbmc.New(mockbmc.Options{})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewTLSServer(b.Handler())
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	ca := filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o644); err != nil {
		t.Fatal(err)
	}
	pw := filepath.Join(dir, "pw")
	if err := os.WriteFile(pw, []byte("password\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := userConfigDir
	userConfigDir = func() (string, error) { return filepath.Join(dir, "config"), nil }
	t.Cleanup(func() { userConfigDir = old })
	return &env{t: t, bmc: b, srv: srv, ca: ca, pw: pw, cfgDir: dir,
		baseArgs: []string{"--endpoint", srv.URL, "--user", "admin", "--password-file", pw, "--ca-file", ca}}
}

// run executes the CLI through SetArgs/SetOut/SetErr.
func run(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	var o, e bytes.Buffer
	cmd := RootCmd()
	cmd.SetOut(&o)
	cmd.SetErr(&e)
	cmd.SetArgs(args)
	err = cmd.Execute()
	return o.String(), e.String(), err
}

func (e *env) run(args ...string) (string, string, error) {
	e.t.Helper()
	return run(e.t, append(append([]string{}, e.baseArgs...), args...)...)
}

func TestRawGetJSONAndTable(t *testing.T) {
	e := newEnv(t)
	out, _, err := e.run("raw", "get", "/redfish/v1/")
	if err != nil || !strings.Contains(out, `"RedfishVersion"`) || !strings.HasPrefix(out, "{\n  ") {
		t.Fatalf("json: %v %q", err, out)
	}
	out, _, err = e.run("-o", "table", "raw", "get", "/redfish/v1/")
	if err != nil || !strings.Contains(out, "PROPERTY") || !strings.Contains(out, "RedfishVersion") {
		t.Fatalf("table: %v %q", err, out)
	}
	out, _, err = e.run("-o", "table", "raw", "get", "/redfish/v1/Systems")
	if err != nil || !strings.Contains(out, "/redfish/v1/Systems/437XR1138R2") || !strings.Contains(out, "MEMBER") {
		t.Fatalf("collection table: %v %q", err, out)
	}
	if _, _, err := e.run("-o", "yaml", "raw", "get", "/redfish/v1/"); err == nil {
		t.Error("an unknown output format")
	}
}

func TestSecretsAndTLS(t *testing.T) {
	e := newEnv(t)
	// A password file readable by others is refused.
	if err := os.Chmod(e.pw, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.run("raw", "get", "/redfish/v1/"); err == nil || !strings.Contains(err.Error(), "readable by group or others") {
		t.Errorf("loose password file: %v", err)
	}
	_ = os.Chmod(e.pw, 0o600)
	// No password flag, no environment variable.
	for _, a := range [][]string{{"--password", "x"}, {"--token", "x"}} {
		if _, _, err := run(t, append([]string{"raw", "get", "/redfish/v1/"}, a...)...); err == nil {
			t.Errorf("flag %v exists", a)
		}
	}
	t.Setenv("REDFISH_PASSWORD", "password")
	t.Setenv("REDFISH_USER", "admin")
	if _, _, err := run(t, "--endpoint", e.srv.URL, "--ca-file", e.ca, "raw", "get", "/redfish/v1/"); err == nil {
		t.Error("the credentials came from the environment")
	}
	// TLS is verified by default: the mock's certificate is not in the roots.
	args := []string{"--endpoint", e.srv.URL, "--user", "admin", "--password-file", e.pw, "raw", "get", "/redfish/v1/"}
	if _, _, err := run(t, args...); err == nil || !strings.Contains(err.Error(), "certificate") {
		t.Errorf("an unknown CA was accepted: %v", err)
	}
	// --insecure is explicit and warns.
	out, errOut, err := run(t, append([]string{"--insecure"}, args...)...)
	if err != nil || !strings.Contains(out, "RedfishVersion") || !strings.Contains(errOut, "NOT verified") {
		t.Fatalf("insecure: %v %q %q", err, out, errOut)
	}
	if _, _, err := e.run("--insecure", "raw", "get", "/redfish/v1/"); err == nil {
		t.Error("--insecure with --ca-file")
	}
	// Only https.
	if _, _, err := run(t, "--endpoint", "http://bmc.example", "--user", "a", "--password-file", e.pw, "raw", "get", "/redfish/v1/"); err == nil {
		t.Error("an http endpoint")
	}
	if _, _, err := run(t, "raw", "get", "/redfish/v1/"); err == nil || !strings.Contains(err.Error(), "--endpoint") {
		t.Errorf("no endpoint: %v", err)
	}
}

func TestSessionLoginUseLogout(t *testing.T) {
	e := newEnv(t)
	out, _, err := e.run("session", "login")
	if err != nil || !strings.Contains(out, "session opened: /redfish/v1/SessionService/Sessions/") {
		t.Fatalf("login: %v %q", err, out)
	}
	files, _ := filepath.Glob(filepath.Join(e.cfgDir, "config", "redfish", "sessions", "*.json"))
	if len(files) != 1 {
		t.Fatalf("session files: %v", files)
	}
	st, _ := os.Stat(files[0])
	if st.Mode().Perm() != 0o600 {
		t.Errorf("session file mode %v", st.Mode().Perm())
	}
	saved, _ := os.ReadFile(files[0])
	tok := regexp.MustCompile(`"token":"([^"]+)"`).FindSubmatch(saved)
	if tok == nil {
		t.Fatalf("no token in the session file: %s", saved)
	}
	if strings.Contains(out, string(tok[1])) {
		t.Error("the token was printed")
	}
	// Used without a password.
	sargs := []string{"--endpoint", e.srv.URL, "--ca-file", e.ca, "--session"}
	out, errOut, err := run(t, append(sargs, "raw", "get", "/redfish/v1/Systems")...)
	if err != nil || !strings.Contains(out, "Members") || strings.Contains(out+errOut, string(tok[1])) {
		t.Fatalf("with the session: %v %q %q", err, out, errOut)
	}
	if out, _, err := run(t, append(sargs, "session", "status")...); err != nil || !strings.Contains(out, "session saved") {
		t.Errorf("status: %v %q", err, out)
	}
	if _, _, err := run(t, append(sargs, "session", "logout")...); err != nil {
		t.Fatalf("logout: %v", err)
	}
	if files, _ := filepath.Glob(filepath.Join(e.cfgDir, "config", "redfish", "sessions", "*.json")); len(files) != 0 {
		t.Errorf("the session file remains: %v", files)
	}
	log := strings.Join(e.bmc.Log(), "\n")
	if !strings.Contains(log, "DELETE /redfish/v1/SessionService/Sessions/") {
		t.Errorf("the session was not ended at the BMC:\n%s", log)
	}
	if _, _, err := run(t, append(sargs, "raw", "get", "/redfish/v1/Systems")...); err == nil || !strings.Contains(err.Error(), "no saved session") {
		t.Errorf("after logout: %v", err)
	}
	// A session file others can read is not used.
	if _, _, err := e.run("session", "login"); err != nil {
		t.Fatal(err)
	}
	files, _ = filepath.Glob(filepath.Join(e.cfgDir, "config", "redfish", "sessions", "*.json"))
	_ = os.Chmod(files[0], 0o644)
	if _, _, err := run(t, append(sargs, "raw", "get", "/redfish/v1/Systems")...); err == nil || !strings.Contains(err.Error(), "readable by group or others") {
		t.Errorf("loose session file: %v", err)
	}
}

func TestRawPatchPostDelete(t *testing.T) {
	e := newEnv(t)
	sys := "/redfish/v1/Systems/437XR1138R2"
	_, errOut, err := e.run("raw", "get", sys)
	tag := regexp.MustCompile(`etag: (\S+)`).FindStringSubmatch(errOut)
	if err != nil || tag == nil {
		t.Fatalf("get: %v %q", err, errOut)
	}
	if _, _, err := e.run("raw", "patch", sys, "--data", `{"AssetTag":"x"}`, "--if-match", tag[1]); err != nil {
		t.Fatalf("patch: %v", err)
	}
	// Stale: 412 with the service's message.
	_, _, err = e.run("raw", "patch", sys, "--data", `{"AssetTag":"y"}`, "--if-match", tag[1])
	if err == nil || !strings.Contains(err.Error(), "412") || !strings.Contains(err.Error(), "ETag") {
		t.Errorf("stale if-match: %v", err)
	}
	// An action outside AllowableValues: the service's message comes back.
	_, _, err = e.run("raw", "post", sys+"/Actions/ComputerSystem.Reset", "--data", `{"ResetType":"Explode"}`)
	if err == nil || !strings.Contains(err.Error(), "400") || !strings.Contains(err.Error(), "acceptable values") {
		t.Errorf("bad action value: %v", err)
	}
	if _, _, err := e.run("raw", "post", sys+"/Actions/ComputerSystem.Reset", "--data", `{"ResetType":"ForceOff"}`); err != nil {
		t.Errorf("reset: %v", err)
	}
	// A task: the monitor is on stderr.
	_, errOut, err = e.run("raw", "post", "/redfish/v1/UpdateService/Actions/SimpleUpdate", "--data", `{"ImageURI":"https://fw.example/x.bin"}`)
	if err != nil || !strings.Contains(errOut, "monitor: /redfish/v1/TaskService/Tasks/") {
		t.Errorf("task: %v %q", err, errOut)
	}
	// Create and delete a member; the body from a file.
	body := filepath.Join(e.cfgDir, "acct.json")
	_ = os.WriteFile(body, []byte(`{"UserName":"carol","Password":"pw-carol","RoleId":"ReadOnly"}`), 0o600)
	_, errOut, err = e.run("raw", "post", "/redfish/v1/AccountService/Accounts", "--data", "@"+body)
	loc := regexp.MustCompile(`created: (\S+)`).FindStringSubmatch(errOut)
	if err != nil || loc == nil {
		t.Fatalf("create: %v %q", err, errOut)
	}
	if _, _, err := e.run("raw", "delete", loc[1]); err != nil {
		t.Errorf("delete: %v", err)
	}
	if _, _, err := e.run("raw", "get", loc[1]); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("the deleted member: %v", err)
	}
	// Paths of the service only.
	for _, p := range []string{"https://evil.example/redfish/v1/", "/etc/passwd", "/redfish/../x"} {
		if _, _, err := e.run("raw", "get", p); err == nil {
			t.Errorf("accepted %q", p)
		}
	}
}

type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}
func (l *lockedBuf) String() string { l.mu.Lock(); defer l.mu.Unlock(); return l.b.String() }

func TestMockServe(t *testing.T) {
	dir := t.TempDir()
	ca := filepath.Join(dir, "mock-ca.pem")
	ctx, cancel := context.WithCancel(context.Background())
	out := &lockedBuf{}
	cmd := RootCmd()
	cmd.SetOut(out)
	cmd.SetErr(&lockedBuf{})
	cmd.SetArgs([]string{"mock", "serve", "--ca-out", ca})
	done := make(chan error, 1)
	go func() { done <- cmd.ExecuteContext(ctx) }()
	var endpoint string
	for i := 0; i < 100 && endpoint == ""; i++ {
		if m := regexp.MustCompile(`endpoint: (https://\S+)`).FindStringSubmatch(out.String()); m != nil {
			endpoint = m[1]
		}
		time.Sleep(20 * time.Millisecond)
	}
	if endpoint == "" {
		cancel()
		t.Fatalf("the mock did not start: %q", out.String())
	}
	pw := filepath.Join(dir, "pw")
	_ = os.WriteFile(pw, []byte("password"), 0o600)
	got, _, err := run(t, "--endpoint", endpoint, "--user", "admin", "--password-file", pw, "--ca-file", ca, "raw", "get", "/redfish/v1/Chassis")
	if err != nil || !strings.Contains(got, "/redfish/v1/Chassis/1U") {
		t.Errorf("against `mock serve`: %v %q", err, got)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("shutdown: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the mock did not stop")
	}
}

func TestMainExitsOnError(t *testing.T) {
	old, oldArgs := osExit, os.Args
	defer func() { osExit, os.Args = old, oldArgs }()
	code := -1
	osExit = func(c int) { code = c }
	os.Args = []string{"redfish", "raw", "get", "/redfish/v1/"} // no endpoint
	main()
	if code != 1 {
		t.Errorf("exit code %d", code)
	}
}

// redfishtool's raw methods and its versions, odata and metadata documents.
func TestRawPutHeadVersionsOdataMetadata(t *testing.T) {
	e := newEnv(t)
	if out, _, err := e.run("raw", "get", "/redfish"); err != nil || !strings.Contains(out, `"v1"`) {
		t.Errorf("versions: %v %q", err, out)
	}
	if out, _, err := e.run("raw", "get", "/redfish/v1/odata"); err != nil || !strings.Contains(out, "value") {
		t.Errorf("odata: %v %q", err, out)
	}
	if out, _, err := e.run("raw", "get", "/redfish/v1/$metadata"); err != nil || !strings.Contains(out, "edmx") {
		t.Errorf("metadata: %v %q", err, out)
	}
	out, _, err := e.run("raw", "head", "/redfish/v1/Systems/437XR1138R2")
	if err != nil || !strings.Contains(out, "status: 200") || !strings.Contains(out, `etag: W/"`) {
		t.Errorf("head: %v %q", err, out)
	}
	if _, _, err := e.run("raw", "put", "/redfish/v1/Systems/437XR1138R2", "--data", `{"AssetTag":"put"}`); err != nil {
		t.Errorf("put: %v", err)
	}
	if out, _, _ := e.run("raw", "get", "/redfish/v1/Systems/437XR1138R2"); !strings.Contains(out, `"put"`) {
		t.Errorf("the put was not applied: %q", out)
	}
}

// A BMC's JSON, whatever it is, renders without panic and without a control
// character reaching the terminal.
func FuzzRenderTable(f *testing.F) {
	f.Add(`{"Name":"x","Members":[{"@odata.id":"/a","Name":"b"}]}`)
	f.Add(`{"a":"\u001b[31mred","b":[1,2],"c":{"d":null}}`)
	f.Add(`[1,"two"]`)
	f.Fuzz(func(t *testing.T, doc string) {
		var b bytes.Buffer
		g := &globals{output: "table"}
		_ = g.printDoc(&b, []byte(doc))
		if strings.ContainsRune(b.String(), 0x1b) && !strings.Contains(doc, "\x1b") {
			t.Fatal("an escape appeared from nowhere")
		}
		var tb bytes.Buffer
		if json.Valid([]byte(doc)) {
			_ = g.printDoc(&tb, []byte(doc))
			if strings.ContainsRune(tb.String(), 0x1b) {
				t.Fatalf("an escape reached the output: %q", tb.String())
			}
		}
	})
}
