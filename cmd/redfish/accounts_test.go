package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-redfish/redfish/mockbmc"
)

// runIn is run with a standard input.
func (e *env) runIn(stdin string, args ...string) (string, string, error) {
	e.t.Helper()
	var o, er bytes.Buffer
	cmd := RootCmd()
	cmd.SetIn(strings.NewReader(stdin))
	cmd.SetOut(&o)
	cmd.SetErr(&er)
	cmd.SetArgs(append(append([]string{}, e.baseArgs...), args...))
	err := cmd.Execute()
	return o.String(), er.String(), err
}

func (e *env) writePW(name, content string, mode os.FileMode) string {
	e.t.Helper()
	p := filepath.Join(e.cfgDir, name)
	if err := os.WriteFile(p, []byte(content), mode); err != nil {
		e.t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil {
		e.t.Fatal(err)
	}
	return p
}

func TestAccountsCommands(t *testing.T) {
	for _, tc := range []struct {
		name string
		o    mockbmc.Options
	}{
		{"rackmount", mockbmc.Options{}},
		{"rackmount-slots", mockbmc.Options{FixedSlots: 3}},
		{"emulator-slots", mockbmc.Options{Tree: mockbmc.TreeEmulator, FixedSlots: 3}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnvOpts(t, tc.o)
			role, ro := "Operator", "ReadOnly"
			if tc.o.Tree == mockbmc.TreeEmulator {
				ro = "ReadOnlyUser"
			}
			if out := e.ok("accounts", "list"); !strings.Contains(out, "UserName") {
				t.Errorf("list: %q", out)
			}
			if out := e.ok("-o", "table", "accounts", "list"); !strings.Contains(out, "UserName") || strings.Contains(out, "map[") {
				t.Errorf("list table: %q", out)
			}
			if out := e.ok("accounts", "roles"); !strings.Contains(out, `"Id": "`+ro+`"`) {
				t.Errorf("roles: %q", out)
			}
			pw1 := e.writePW("pw1", "bob-password-1\n", 0o600)
			out := e.ok("accounts", "add", "bob", "--role", role, "--type", "Redfish", "--new-password-file", pw1)
			if !strings.Contains(out, `"changed": true`) || strings.Contains(out, "bob-password-1") {
				t.Errorf("add: %q", out)
			}
			if out := e.ok("accounts", "add", "bob", "--role", role, "--new-password-file", pw1); !strings.Contains(out, `"changed": false`) {
				t.Errorf("add again: %q", out)
			}
			// The new account logs in with the password the file held.
			login := []string{"--endpoint", e.srv.URL, "--user", "bob", "--password-file", pw1, "--ca-file", e.ca, "accounts", "list"}
			if _, _, err := run(t, login...); err != nil {
				t.Fatalf("bob cannot log in: %v", err)
			}

			// The password from stdin, then the old one is refused.
			if out, errOut, err := e.runIn("bob-password-2\n", "accounts", "password", "bob", "--new-password-stdin"); err != nil || !strings.Contains(out, `"changed": true`) || strings.Contains(out+errOut, "bob-password-2") {
				t.Fatalf("password: %v %q %q", err, out, errOut)
			}
			if _, _, err := run(t, login...); err == nil {
				t.Error("the old password still works")
			}
			e.ok("accounts", "role", "bob", ro)
			if out := e.ok("accounts", "role", "bob", ro); !strings.Contains(out, `"changed": false`) {
				t.Errorf("role again: %q", out)
			}
			e.ok("accounts", "disable", "bob")
			e.ok("accounts", "enable", "bob")
			e.ok("accounts", "types", "bob", "--type", "Redfish", "--type", "WebUI")
			e.ok("accounts", "rename", "bob", "robert")
			// by --id: the account list says which Id robert has
			out = e.ok("accounts", "list")
			i := strings.Index(out, `"UserName": "robert"`)
			if i < 0 {
				t.Fatalf("robert is not listed: %q", out)
			}
			j := strings.LastIndex(out[:i], `"Id": "`)
			if j < 0 {
				t.Fatalf("no Id before robert: %q", out)
			}
			id := strings.SplitN(out[j+len(`"Id": "`):], `"`, 2)[0]
			if out := e.ok("accounts", "--id", id, "rename", "bob"); !strings.Contains(out, `"changed": true`) {
				t.Errorf("rename by id: %q", out)
			}
			if out := e.ok("accounts", "--id", id, "role", role); !strings.Contains(out, `"changed": true`) {
				t.Errorf("role by id: %q", out)
			}
			if out := e.ok("accounts", "delete", "bob"); !strings.Contains(out, `"changed": true`) {
				t.Errorf("delete: %q", out)
			}
			if out := e.ok("accounts", "delete", "bob"); !strings.Contains(out, `"changed": false`) {
				t.Errorf("delete again: %q", out)
			}

			// The service.
			if out := e.ok("accounts", "config"); !strings.Contains(out, "MinPasswordLength") {
				t.Errorf("config: %q", out)
			}
			if out := e.ok("accounts", "config", "set", "--set", "MinPasswordLength=10"); !strings.Contains(out, `"changed": true`) || e.resource("/redfish/v1/AccountService")["MinPasswordLength"] != float64(10) {
				t.Errorf("config set: %q", out)
			}
			for _, args := range [][]string{
				{"accounts", "config", "set"},
				{"accounts", "config", "set", "--set", "Nope=1"},
				{"accounts", "add"},
				{"accounts", "add", "x", "--new-password-file", pw1, "--new-password-stdin"},
				{"accounts", "password", "bob"},
				{"accounts", "role", "bob"},
				{"accounts", "role", "ReadOnly"},
				{"accounts", "rename", "robert"},
				{"accounts", "types", "bob"},
				{"accounts", "enable"},
				{"accounts", "enable", "nobody"},
			} {
				if _, _, err := e.run(args...); err == nil {
					t.Errorf("%v: want an error", args)
				}
			}
		})
	}
}

// The password source is a file of mode 0600 or the standard input: nothing
// else, and nothing it holds shows in an answer or an error.
func TestAccountPasswordSources(t *testing.T) {
	e := newEnv(t)
	const secret = "leaky-Passw0rd-xyz"
	for name, tc := range map[string]struct {
		file    string
		mode    os.FileMode
		content string
		stdin   string
	}{
		"group readable": {file: "g", mode: 0o640, content: secret},
		"world readable": {file: "w", mode: 0o644, content: secret},
		"empty file":     {file: "e", mode: 0o600, content: ""},
		"two lines":      {file: "l", mode: 0o600, content: secret + "\n" + secret},
		"control char":   {file: "c", mode: 0o600, content: secret + "\x07"},
		"too large":      {file: "b", mode: 0o600, content: strings.Repeat("x", maxPasswordFile+1)},
	} {
		p := e.writePW(tc.file, tc.content, tc.mode)
		out, errOut, err := e.run("accounts", "add", "zed", "--new-password-file", p)
		if err == nil {
			t.Errorf("%s: accepted", name)
		}
		if all := out + errOut + errString(err); strings.Contains(all, secret) {
			t.Errorf("%s: the password shows: %s", name, all)
		}
	}
	if _, _, err := e.run("accounts", "add", "zed", "--new-password-file", filepath.Join(e.cfgDir, "missing")); err == nil {
		t.Error("a missing file")
	}
	if _, _, err := e.run("accounts", "add", "zed", "--new-password-file", e.cfgDir); err == nil {
		t.Error("a directory")
	}
	if _, _, err := e.runIn("", "accounts", "add", "zed", "--new-password-stdin"); err == nil {
		t.Error("an empty stdin")
	}
	// The mock refuses a password that is too short; neither stream nor error
	// has it.
	out, errOut, err := e.runIn("sh0rt\n", "accounts", "add", "zed", "--role", "Operator", "--new-password-stdin")
	if err == nil || strings.Contains(out+errOut+errString(err), "sh0rt") {
		t.Errorf("a short password: %v %q %q", err, out, errOut)
	}
	// There is no flag that takes the password itself.
	for _, flag := range []string{"--password", "--new-password", "--account-password"} {
		if _, _, err := e.run("accounts", "add", "zed", flag, secret); err == nil {
			t.Errorf("%s was accepted", flag)
		}
	}
	// A password in the environment is ignored.
	t.Setenv("REDFISH_NEW_PASSWORD", secret)
	if out, _, err := e.run("accounts", "add", "zed", "--role", "Operator"); err == nil || strings.Contains(out, secret) {
		// no password is "none given": the mock needs one, and refuses
		t.Errorf("environment: %v %q", err, out)
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func TestSessionCommands(t *testing.T) {
	e := newEnv(t)
	if out := e.ok("session", "list"); !strings.Contains(out, "1234567890ABCDEF") {
		t.Errorf("session list: %q", out)
	}
	if out := e.ok("-o", "table", "session", "list"); !strings.Contains(out, "UserName") || strings.Contains(out, "map[") {
		t.Errorf("session list table: %q", out)
	}
	if out := e.ok("session", "config", "--set", "SessionTimeout=90"); !strings.Contains(out, `"changed": true`) || e.resource("/redfish/v1/SessionService")["SessionTimeout"] != float64(90) {
		t.Errorf("session config: %q", out)
	}
	if out := e.ok("session", "config", "--set", "SessionTimeout=90"); !strings.Contains(out, `"changed": false`) {
		t.Errorf("session config again: %q", out)
	}
	for _, args := range [][]string{{"session", "config"}, {"session", "config", "--set", "Nope=1"}, {"session", "clear", "extra"}} {
		if _, _, err := e.run(args...); err == nil {
			t.Errorf("%v: want an error", args)
		}
	}

	// With a saved session: clear ends it last and removes its file.
	e.ok("session", "login")
	if _, err := os.Stat(mustSessionFile(t, e)); err != nil {
		t.Fatalf("no saved session: %v", err)
	}
	args := []string{"--endpoint", e.srv.URL, "--ca-file", e.ca, "--session", "session", "clear"}
	if out, _, err := run(t, args...); err != nil || !strings.Contains(out, `"changed": true`) {
		t.Fatalf("clear with a session: %v %q", err, out)
	}
	if _, err := os.Stat(mustSessionFile(t, e)); !os.IsNotExist(err) {
		t.Errorf("the saved session of an ended session stays: %v", err)
	}
	if out := e.ok("session", "clear"); !strings.Contains(out, `"changed": false`) {
		t.Errorf("clear with none: %q", out)
	}
}

func mustSessionFile(t *testing.T, e *env) string {
	t.Helper()
	g := &globals{endpoint: e.srv.URL}
	p, err := g.sessionFile()
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// Whatever a stream holds, readPassword neither panics nor returns a password
// with a control character or a line break, and its error never has the input.
func FuzzReadPassword(f *testing.F) {
	for _, s := range []string{"pw\n", "pw\r\n", "", "\n", "a\nb", "a\x00b", "\xff\xfe", strings.Repeat("x", maxPasswordFile+1), "pässwörd\n"} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		pw, err := readPassword(bytes.NewReader(data))
		if err != nil {
			if len(data) > 8 && strings.Contains(err.Error(), string(data)) {
				t.Fatalf("the error has the input: %v", err)
			}
			return
		}
		if pw == "" || len(pw) > maxPasswordFile {
			t.Fatalf("a password of %d bytes", len(pw))
		}
		for _, c := range []byte(pw.Reveal()) {
			if c < 0x20 || c == 0x7f {
				t.Fatalf("control character in %q", pw.Reveal())
			}
		}
	})
}
