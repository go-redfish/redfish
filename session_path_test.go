package redfish

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSessionPath(t *testing.T) {
	ok := map[string]string{
		"/redfish/v1/SessionService/Sessions/1":                    "/redfish/v1/SessionService/Sessions/1",
		"https://bmc.example/redfish/v1/SessionService/Sessions/1": "/redfish/v1/SessionService/Sessions/1",
		"/redfish/v1/SessionService/Sessions/a%20b":                "/redfish/v1/SessionService/Sessions/a b",
		"":             "",
		"/elsewhere/1": "",
	}
	for in, want := range ok {
		if got, err := sessionPath(in); err != nil || got != want {
			t.Errorf("sessionPath(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{
		"/redfish/v1/S/1%1b%5b2J",
		"/redfish/v1/S/1%00",
		"/redfish/v1/S/1%7f",
		"/redfish/v1/S/1%c2%9b",
		"/redfish/v1/S/1%ff",
		"/redfish/v1/S/../../AccountService/Accounts/1",
		"/redfish/v1/S/%2e%2e/x",
		"/redfish/v1/S/./1",
		"/redfish/v1/S/..",
		"/redfish/v1/S/1%3Fx=1",
		"/redfish/v1/S/1%23x",
	} {
		got, err := sessionPath(in)
		if err == nil || got != "" {
			t.Errorf("sessionPath(%q) = %q, %v; want a refusal", in, got, err)
		}
		if err != nil && strings.ContainsAny(err.Error(), "\x00\x1b\x7f") {
			t.Errorf("the refusal echoes a control byte: %q", err)
		}
	}
}

// A saved session whose URI holds a control character or a dot segment is
// refused by New, with the reason (not "not under /redfish/").
func TestNewRefusesAHostileSessionURI(t *testing.T) {
	for uri, why := range map[string]string{
		"/redfish/v1/S/1%1b%5b2J":           "control",
		"/redfish/v1/S/../../Accounts/1":    `".." segment`,
		"/redfish/v1/S/1%3Fx":               "delimiter",
		"https://elsewhere.example/other/1": "not under /redfish/",
	} {
		_, err := New(Config{BaseURL: "https://bmc.example", Token: "t", SessionURI: uri})
		if !errors.Is(err, ErrInsecure) || !strings.Contains(err.Error(), why) {
			t.Errorf("New(SessionURI=%q) = %v; want ErrInsecure mentioning %q", uri, err, why)
		}
		if err != nil && strings.ContainsAny(err.Error(), "\x1b\x7f") {
			t.Errorf("the refusal echoes a control byte: %q", err)
		}
	}
}

// Whatever Location a BMC sends, what sessionPath accepts is a path under
// /redfish/ with no control character and no dot segment.
func FuzzSessionPath(f *testing.F) {
	for _, s := range []string{
		"/redfish/v1/SessionService/Sessions/1", "/redfish/v1/S/1%1b%5b2J", "/redfish/v1/S/../../x",
		"/redfish/v1/S/%2e%2e/x", "/redfish/v1/S/1%c2%9b", "/redfish/v1/S/1%3Fx", "/redfish/v1/S/\x1b",
		"https://h/redfish/v1/S/1", "/redfish/v1/S/1%ff", "/redfish/\x7f",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, loc string) {
		got, err := sessionPath(loc)
		if err != nil {
			if got != "" {
				t.Fatalf("an error with a path: %q", got)
			}
			return
		}
		if got == "" {
			return
		}
		if !strings.HasPrefix(got, "/redfish/") || !utf8.ValidString(got) {
			t.Fatalf("not under /redfish/ or not UTF-8: %q", got)
		}
		for _, r := range got {
			if r < 0x20 || (r >= 0x7f && r <= 0x9f) || r == '?' || r == '#' {
				t.Fatalf("U+%04X in %q", r, got)
			}
		}
		for _, seg := range strings.Split(got, "/") {
			if seg == ".." || seg == "." {
				t.Fatalf("dot segment in %q", got)
			}
		}
	})
}
