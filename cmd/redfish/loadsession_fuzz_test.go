package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// loadSession reads the saved session file whatever it holds: it never panics,
// refuses more than maxSessionFile, and returns a session only with a token (in
// the file, or read from a store reference of an accepted scheme, but not both)
// and for the endpoint asked for. Its errors carry no control byte.
func FuzzLoadSession(f *testing.F) {
	for _, s := range []string{
		`{"endpoint":"https://bmc.example","token":"t","uri":"/redfish/v1/SessionService/Sessions/1"}`,
		`{"endpoint":"https://bmc.example","token":""}`, `{"endpoint":"https://other.example","token":"t"}`,
		`{"endpoint":"https://bmc.example","token":"t","uri":"\u001b[2J"}`, `{"endpoint":5,"token":["t"]}`,
		`[]`, `null`, `{`, ``, "\x00\x01", `{"endpoint":"https://bmc.example","token":"t"}{"x":1}`,
		`{"endpoint":"https://bmc.example","token_ref":"keychain:redfish/bmc1","uri":"/u"}`,
		`{"endpoint":"https://bmc.example","token":"t","token_ref":"keychain:redfish/bmc1"}`,
		`{"endpoint":"https://bmc.example","token_ref":"file:/etc/passwd"}`,
		`{"endpoint":"https://bmc.example","token_ref":"systemd-creds:x"}`,
		`{"endpoint":"https://bmc.example","token_ref":"keychain:a\u001b[2J/b"}`,
		`{"endpoint":"https://bmc.example","token_ref":5}`,
		`{"endpoint":"https://bmc.example","token_ref":"keepassxc:https://bmc.example/"}`,
		`{"endpoint":"https://bmc.example","token_ref":"secretservice:svc/acct"}`,
	} {
		f.Add([]byte(s))
	}
	dir := f.TempDir()
	old := userConfigDir
	userConfigDir = func() (string, error) { return dir, nil }
	f.Cleanup(func() { userConfigDir = old })
	oo := openSecretRef
	// The manager answers every reference with a fixed token.
	openSecretRef = func(context.Context, string) (opened, error) {
		return fakeSecret{[]byte("from-store"), &fakeManager{}}, nil
	}
	f.Cleanup(func() { openSecretRef = oo })
	g := &globals{endpoint: "https://bmc.example"}
	p, err := g.sessionFile()
	if err != nil {
		f.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if err := writeSecret(p, data); err != nil {
			t.Fatal(err)
		}
		s, err := g.loadSession(context.Background())
		if err != nil {
			mustBeClean(t, "loadSession error", err.Error())
			return
		}
		if s.Token == "" || s.Endpoint != g.endpoint || len(data) > maxSessionFile {
			t.Fatalf("accepted %d bytes as %+v", len(data), s)
		}
		if s.TokenRef != "" {
			if s.Token != "from-store" || checkStoreRef(s.TokenRef) != nil {
				t.Fatalf("a reference accepted wrongly: %+v", s)
			}
		}
	})
}
