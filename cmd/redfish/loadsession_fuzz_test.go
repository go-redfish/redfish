package main

import (
	"os"
	"path/filepath"
	"testing"
)

// loadSession reads the saved session file whatever it holds: it never panics,
// refuses more than maxSessionFile, and returns a session only with a token and
// for the endpoint asked for. Its errors carry no control byte.
func FuzzLoadSession(f *testing.F) {
	for _, s := range []string{
		`{"endpoint":"https://bmc.example","token":"t","uri":"/redfish/v1/SessionService/Sessions/1"}`,
		`{"endpoint":"https://bmc.example","token":""}`, `{"endpoint":"https://other.example","token":"t"}`,
		`{"endpoint":"https://bmc.example","token":"t","uri":"\u001b[2J"}`, `{"endpoint":5,"token":["t"]}`,
		`[]`, `null`, `{`, ``, "\x00\x01", `{"endpoint":"https://bmc.example","token":"t"}{"x":1}`,
	} {
		f.Add([]byte(s))
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
	f.Fuzz(func(t *testing.T, data []byte) {
		if err := writeSecret(p, data); err != nil {
			t.Fatal(err)
		}
		s, err := g.loadSession()
		if err != nil {
			mustBeClean(t, "loadSession error", err.Error())
			return
		}
		if s.Token == "" || s.Endpoint != g.endpoint || len(data) > maxSessionFile {
			t.Fatalf("accepted %d bytes as %+v", len(data), s)
		}
	})
}
