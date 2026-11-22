package main

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/go-authn/secretsource"
	"github.com/spf13/cobra"
)

const refLeak = "leaky-Passw0rd-xyz"

// refArgs is the connection of the env with the password given by reference.
func (e *env) refArgs(ref string) []string {
	return []string{"--endpoint", e.srv.URL, "--user", "admin", "--password-from", ref, "--ca-file", e.ca}
}

func ctxCmd() *cobra.Command {
	c := &cobra.Command{}
	c.SetContext(context.Background())
	return c
}

// --password-from file:PATH logs in like --password-file.
func TestPasswordFromFileRef(t *testing.T) {
	e := newEnv(t)
	out, _, err := run(t, append(e.refArgs("file:"+e.pw), "raw", "get", "/redfish/v1/")...)
	if err != nil || !strings.Contains(out, "RedfishVersion") {
		t.Fatalf("file: ref: %v %q", err, out)
	}
	// With a session (token) login too: the password is fetched when needed.
	if _, _, err := run(t, append(e.refArgs("file:"+e.pw), "session", "login")...); err != nil {
		t.Fatalf("session login: %v", err)
	}
}

// A reference that cannot be resolved fails, and nothing shows the secret.
func TestPasswordFromNoLeak(t *testing.T) {
	e := newEnv(t)
	loose := e.writePW("loose", refLeak+"\n", 0o644)
	long := e.writePW("long", strings.Repeat(refLeak, 400), 0o600)
	multi := e.writePW("multi", refLeak+"\n"+refLeak+"\n", 0o600)
	for name, ref := range map[string]string{
		"loose mode": "file:" + loose,
		"oversize":   "file:" + long,
		"two lines":  "file:" + multi,
		"missing":    "file:" + filepath.Join(e.cfgDir, "nope"),
		"bad scheme": "nope:" + refLeak,
		"no scheme":  refLeak,
	} {
		out, errOut, err := run(t, append(e.refArgs(ref), "raw", "get", "/redfish/v1/")...)
		if err == nil {
			t.Errorf("%s: accepted", name)
			continue
		}
		if all := out + errOut + err.Error(); strings.Contains(all, refLeak) && name != "bad scheme" && name != "no scheme" {
			t.Errorf("%s: the password shows: %s", name, all)
		}
	}
}

func TestPasswordFromExclusive(t *testing.T) {
	e := newEnv(t)
	args := append(e.refArgs("file:"+e.pw), "--password-file", e.pw, "raw", "get", "/redfish/v1/")
	if _, _, err := run(t, args...); err == nil || !strings.Contains(err.Error(), "--password-file and --password-from are exclusive") {
		t.Errorf("both: %v", err)
	}
	// A reference that does not parse is refused before any request.
	if _, _, err := run(t, append(e.refArgs("no-scheme-here"), "raw", "get", "/redfish/v1/")...); err == nil || !strings.HasPrefix(err.Error(), "--password-from: secretsource:") {
		t.Errorf("unparsable ref: %v", err)
	}
	// The old flag alone still works, and nothing at all is asked for.
	if _, _, err := e.run("raw", "get", "/redfish/v1/"); err != nil {
		t.Errorf("--password-file: %v", err)
	}
	if _, _, err := run(t, "--endpoint", e.srv.URL, "--user", "admin", "--ca-file", e.ca, "raw", "get", "/redfish/v1/"); err == nil || !strings.Contains(err.Error(), "--password-from") {
		t.Errorf("neither: %v", err)
	}
}

// A systemd credential is read through the library from CREDENTIALS_DIRECTORY.
func TestPasswordFromSystemdCreds(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("systemd-creds: references are Linux only")
	}
	e := newEnv(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "bmc-admin"), []byte("password\n"), 0o400); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o700); err != nil { // the library refuses a directory others can read
		t.Fatal(err)
	}
	t.Setenv("CREDENTIALS_DIRECTORY", dir)
	out, _, err := run(t, append(e.refArgs("systemd-creds:bmc-admin"), "raw", "get", "/redfish/v1/")...)
	if err != nil || !strings.Contains(out, "RedfishVersion") {
		t.Fatalf("systemd-creds: %v %q", err, out)
	}
}

func TestAccountNewPasswordFrom(t *testing.T) {
	e := newEnv(t)
	p := e.writePW("new", "Str0ng-Passw0rd-xyz\n", 0o600)
	out := e.ok("accounts", "add", "bob", "--role", "ReadOnly", "--new-password-from", "file:"+p)
	if !strings.Contains(out, `"changed": true`) {
		t.Errorf("add: %q", out)
	}
	_, _, err := e.run("accounts", "add", "x", "--new-password-from", "file:"+p, "--new-password-file", p)
	if err == nil || err.Error() != "--new-password-file and --new-password-from are exclusive" {
		t.Errorf("new exclusive message: %v", err)
	}
	for _, args := range [][]string{
		{"accounts", "add", "x", "--new-password-from", "file:" + p, "--new-password-file", p},
		{"accounts", "add", "x", "--new-password-from", "file:" + p, "--new-password-stdin"},
		{"accounts", "add", "x", "--new-password-from", "file:" + e.writePW("big", strings.Repeat("x", 5000), 0o600)},
		{"accounts", "add", "x", "--new-password-from", "file:" + e.writePW("two", refLeak+"\n"+refLeak, 0o600)},
	} {
		out, errOut, err := e.run(args...)
		if err == nil || strings.Contains(out+errOut+err.Error(), refLeak) {
			t.Errorf("%v: %v", args, err)
		}
	}
}

func TestRefSecretsDirect(t *testing.T) {
	dir := t.TempDir()
	w := func(name, content string, mode os.FileMode) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), mode); err != nil {
			t.Fatal(err)
		}
		return "file:" + p
	}
	ctx := context.Background()
	cmd := ctxCmd()

	// The key: a PEM-sized one goes through, one over secretsource.MaxSize is
	// refused with the way out, and never printed.
	pemKey := "-----BEGIN PRIVATE KEY-----\n" + refLeak + "\n-----END PRIVATE KEY-----\n"
	s := secretFlags{keyFrom: w("k", pemKey, 0o600)}
	if k, err := s.key(cmd); err != nil || strings.TrimSpace(string(k)) != strings.TrimSpace(pemKey) {
		t.Fatalf("key: %v %q", err, strings.ReplaceAll(string(k), refLeak, "X"))
	}
	big := strings.Repeat("A", secretsource.MaxSize+1)
	s = secretFlags{keyFrom: w("big", big, 0o600)}
	if _, err := s.key(cmd); err == nil || !strings.Contains(err.Error(), "larger than 4096 bytes") || !strings.Contains(err.Error(), "file or stdin") || strings.Contains(err.Error(), "AAAA") {
		t.Errorf("oversize key: %v", err)
	}
	s = secretFlags{keyFrom: w("blank", "  \t \n \n", 0o600)}
	if k, err := s.key(cmd); err == nil || k != "" {
		t.Errorf("blank key: %v", err)
	}
	s = secretFlags{keyFrom: w("empty", "", 0o600)}
	if _, err := s.key(cmd); err == nil {
		t.Error("empty key")
	}
	s = secretFlags{keyFrom: w("k2", pemKey, 0o600), keyFile: "x"}
	if _, err := s.key(cmd); err == nil || !strings.Contains(err.Error(), "--key-file and --key-from are exclusive") {
		t.Errorf("key exclusive: %v", err)
	}
	s = secretFlags{keyFrom: w("k3", pemKey, 0o600), keyStdin: true}
	if _, err := s.key(cmd); err == nil || !strings.Contains(err.Error(), "--key-from and --key-stdin are exclusive") {
		t.Errorf("key stdin exclusive: %v", err)
	}

	// The PKCS12 and challenge passwords.
	s = secretFlags{p12From: w("p12", "p12-pw\n", 0o600), challengeFrom: w("ch", "challenge\n", 0o600)}
	if pw, err := s.p12(ctx); err != nil || pw != "p12-pw" {
		t.Errorf("p12: %q %v", pw, err)
	}
	if pw, err := s.challenge(ctx); err != nil || pw != "challenge" {
		t.Errorf("challenge: %q %v", pw, err)
	}
	s = secretFlags{p12From: w("p12b", "x", 0o600), p12File: "f", challengeFrom: w("chb", "x", 0o600), challengeFile: "f"}
	if _, err := s.p12(ctx); err == nil || !strings.Contains(err.Error(), "--p12-password-file and --p12-password-from are exclusive") {
		t.Errorf("p12 exclusive: %v", err)
	}
	if _, err := s.challenge(ctx); err == nil || !strings.Contains(err.Error(), "--challenge-password-file and --challenge-password-from are exclusive") {
		t.Errorf("challenge exclusive: %v", err)
	}
	s = secretFlags{p12From: w("loose", refLeak, 0o644)}
	if _, err := s.p12(ctx); err == nil || strings.Contains(err.Error(), refLeak) {
		t.Errorf("loose p12: %v", err)
	}
	if pw, err := (&secretFlags{}).p12(ctx); err != nil || pw != "" {
		t.Errorf("none: %q %v", pw, err)
	}

	// The event headers.
	h, err := headersFrom(cmd, "", w("h", "Authorization: Bearer "+refLeak+"\n", 0o600), false)
	if err != nil || string(h["Authorization"]) != "Bearer "+refLeak {
		t.Errorf("headers: %v %v", h, err)
	}
	if _, err := headersFrom(cmd, "f", w("h2", "A: b\n", 0o600), false); err == nil || !strings.Contains(err.Error(), "--headers-file and --headers-from are exclusive") {
		t.Errorf("headers exclusive: %v", err)
	}
	if _, err := headersFrom(cmd, "", w("h3", "A: b\n", 0o600), true); err == nil || !strings.Contains(err.Error(), "--headers-from and --headers-stdin are exclusive") {
		t.Errorf("headers stdin exclusive: %v", err)
	}
	if _, err := headersFrom(cmd, "", w("h4", "no colon "+refLeak+"\n", 0o600), false); err == nil || strings.Contains(err.Error(), refLeak) {
		t.Errorf("bad headers: %v", err)
	}
	if _, err := headersFrom(cmd, "", w("h5", strings.Repeat("A: b\n", 1000), 0o600), false); err == nil {
		t.Error("oversize headers")
	}

	// The image password.
	if pw, err := imagePassword(cmd, "", w("i", "img-pw\n", 0o600), false); err != nil || pw != "img-pw" {
		t.Errorf("image: %q %v", pw, err)
	}
	if _, err := imagePassword(cmd, "f", w("i2", "x", 0o600), false); err == nil || !strings.Contains(err.Error(), "--image-password-file and --image-password-from are exclusive") {
		t.Errorf("image exclusive: %v", err)
	}
	if _, err := imagePassword(cmd, "", w("i3", "x", 0o600), true); err == nil || !strings.Contains(err.Error(), "--image-password-from and --image-password-stdin are exclusive") {
		t.Errorf("image stdin exclusive: %v", err)
	}

	// The mock's password, and the virtual media's (the same helper).
	if _, pw, err := fileOrRefPassword(ctx, "admin", "", "mock-password-file", w("m", "mock-pw\n", 0o600), "mock-password-from"); err != nil || pw != "mock-pw" {
		t.Errorf("mock: %q %v", pw, err)
	}
	if _, _, err := fileOrRefPassword(ctx, "admin", "f", "mock-password-file", w("m2", "x", 0o600), "mock-password-from"); err == nil || !strings.Contains(err.Error(), "--mock-password-file and --mock-password-from are exclusive") {
		t.Errorf("mock exclusive: %v", err)
	}
	if _, _, err := fileOrRefPassword(ctx, "admin", "", "mock-password-file", w("m3", refLeak+"\n"+refLeak, 0o600), "mock-password-from"); err == nil || strings.Contains(err.Error(), refLeak) {
		t.Errorf("mock two lines: %v", err)
	}
}

// Every secret option has its -from sibling on the command that has the other.
func TestFromFlagsExist(t *testing.T) {
	root := RootCmd()
	for _, tc := range []struct {
		path  []string
		flags []string
	}{
		{[]string{"mock", "serve"}, []string{"mock-password-from"}},
		{[]string{"managers", "virtual-media", "insert"}, []string{"image-password-from"}},
		{[]string{"update", "simple"}, []string{"image-password-from"}},
		{[]string{"accounts", "add"}, []string{"new-password-from"}},
		{[]string{"accounts", "password"}, []string{"new-password-from"}},
		{[]string{"events", "subscribe"}, []string{"headers-from"}},
		{[]string{"certs", "install"}, []string{"key-from", "p12-password-from"}},
		{[]string{"certs", "replace"}, []string{"key-from", "p12-password-from"}},
		{[]string{"certs", "csr"}, []string{"challenge-password-from"}},
		{[]string{"certs", "rekey"}, []string{"challenge-password-from"}},
		{[]string{"certs", "renew"}, []string{"challenge-password-from"}},
	} {
		c, _, err := root.Find(tc.path)
		if err != nil || c.Name() != tc.path[len(tc.path)-1] {
			t.Errorf("%v: %v", tc.path, err)
			continue
		}
		for _, f := range tc.flags {
			if c.Flags().Lookup(f) == nil {
				t.Errorf("%v: no --%s", tc.path, f)
			}
		}
	}
	if root.PersistentFlags().Lookup("password-from") == nil {
		t.Error("no --password-from")
	}
}
