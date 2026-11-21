package redfish

import "testing"

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
