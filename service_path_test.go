package redfish

import "testing"

// A dot segment is refused whatever its spelling, an encoded slash and a
// malformed escape too (issue 26); an escape inside a query is not a segment.
func TestServicePathRefusesEncodedDotSegments(t *testing.T) {
	for _, in := range []string{
		"/redfish/%2e%2e/x", "/redfish/%2E%2E/x", "/redfish/%2e./x", "/redfish/.%2E/x",
		"/redfish/v1/%2e/x", "/redfish/v1/%2E", "/redfish/v1/..", "/redfish/v1/.",
		"/redfish/a%2fb", "/redfish/a%2Fb", "/redfish/a%zz", "/redfish/a%",
		"https://h/redfish/%2e%2e/x",
	} {
		if got := servicePath(in); got != "" {
			t.Errorf("servicePath(%q) = %q, want a refusal", in, got)
		}
	}
	for in, want := range map[string]string{
		"/redfish/v1/a..b":       "/redfish/v1/a..b",
		"/redfish/v1/a?x=%2e%2e": "/redfish/v1/a?x=%2e%2e",
		"/redfish/v1/a%20b":      "/redfish/v1/a%20b",
	} {
		if got := servicePath(in); got != want {
			t.Errorf("servicePath(%q) = %q, want %q", in, got, want)
		}
	}
}

// The bare "/redfish" is the version document, not a resource under the service
// root: a Location or a link naming it is not one to follow (the fuzzer found a
// task handle "/redfish" this way), while a request may still ask for it.
func TestServicePathRefusesTheBareRoot(t *testing.T) {
	for in, want := range map[string]string{
		"/redfish":                 "",
		"http://bmc/redfish":       "",
		"/redfish/":                "/redfish/",
		"/redfish/v1/Tasks/1":      "/redfish/v1/Tasks/1",
		"https://bmc/redfish/v1/x": "/redfish/v1/x",
		"/redfishx/v1":             "",
		"/redfish/../etc":          "",
	} {
		if got := servicePath(in); got != want {
			t.Errorf("servicePath(%q) = %q, want %q", in, got, want)
		}
	}
	if st := ParseUpdateStatus(202, "/redfish", nil, "/redfish/v1/Tasks/h"); st.Handle != "/redfish/v1/Tasks/h" {
		t.Errorf("a Location of /redfish replaced the handle: %q", st.Handle)
	}
}
