package redfish

import (
	"strings"
	"testing"
)

// servicePath answers "" or a path under /redfish/ with no dot segment written
// as "..", no backslash and no line break, and answering it again changes nothing.
func FuzzServicePath(f *testing.F) {
	for _, s := range []string{
		"/redfish/v1/Systems/1", "/redfish", "/redfish/", "https://h/redfish/v1/x", "http://h", "https://h/", "://x/redfish/a",
		"/redfish/../x", "/redfish/v1/a\r\nSet-Cookie: x", "/redfish/\\x", "//h/redfish/v1", "https://h/https://h2/redfish/x",
		"/redfish/%2e%2e/x", "/redfish/v1/a?b=c#d", "https://a://b/redfish/c", "",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, loc string) {
		got := servicePath(loc)
		if got == "" {
			return
		}
		if !strings.HasPrefix(got, "/redfish/") || strings.Contains(got, "..") || strings.ContainsAny(got, "\\\r\n") {
			t.Fatalf("servicePath(%q) = %q", loc, got)
		}
		if servicePath(got) != got {
			t.Fatalf("servicePath(%q) = %q, not stable", loc, got)
		}
	})
}
