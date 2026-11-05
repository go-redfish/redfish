package mockbmc_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-redfish/redfish/mockbmc"
)

func do(t *testing.T, srv *httptest.Server, method, path, body string, hdr ...string) (int, string, http.Header) {
	t.Helper()
	req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b), resp.Header
}

func TestAuthenticationAndErrors(t *testing.T) {
	b, err := mockbmc.New(mockbmc.Options{User: "root", Password: "pw"})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewTLSServer(b.Handler())
	defer srv.Close()
	// The root and /redfish are open (DSP0266 13.3).
	for _, p := range []string{"/redfish", "/redfish/v1/", "/redfish/v1"} {
		if c, _, _ := do(t, srv, "GET", p, ""); c != 200 {
			t.Errorf("GET %s: %d", p, c)
		}
	}
	// Everything else needs authentication, and says so in the Redfish way.
	c, body, h := do(t, srv, "GET", "/redfish/v1/Systems", "")
	if c != 401 || !strings.Contains(body, "@Message.ExtendedInfo") || h.Get("WWW-Authenticate") == "" {
		t.Errorf("unauthenticated: %d %s", c, body)
	}
	if c, _, _ := do(t, srv, "GET", "/redfish/v1/Systems", "", "Authorization", "Basic cm9vdDp3cm9uZw=="); c != 401 {
		t.Errorf("wrong password: %d", c)
	}
	if c, _, _ := do(t, srv, "GET", "/redfish/v1/Systems", "", "X-Auth-Token", "made-up"); c != 401 {
		t.Errorf("made-up token: %d", c)
	}
	basic := []string{"Authorization", "Basic cm9vdDpwdw=="}
	for _, tc := range []struct {
		method, path, body string
		want               int
	}{
		{"GET", "/redfish/v1/Nope", "", 404},
		{"PATCH", "/redfish/v1/Nope", "{}", 404},
		{"PATCH", "/redfish/v1/Systems/437XR1138R2", "not json", 400},
		{"POST", "/redfish/v1/Systems/437XR1138R2", "{}", 405},
		{"DELETE", "/redfish/v1/Systems/437XR1138R2", "", 405},
		{"POST", "/redfish/v1/Systems/437XR1138R2/Actions/Nope.Nope", "{}", 404},
		{"POST", "/redfish/v1/SessionService/Sessions", `{"UserName":"root","Password":"bad"}`, 401},
		{"POST", "/redfish/v1/UpdateService/Actions/SimpleUpdate", "{}", 400},
		{"TRACE", "/redfish/v1/Systems", "", 405},
	} {
		if c, _, _ := do(t, srv, tc.method, tc.path, tc.body, basic...); c != tc.want {
			t.Errorf("%s %s: %d, want %d", tc.method, tc.path, c, tc.want)
		}
	}
	if c, _, _ := do(t, srv, "GET", "/redfish/v1/Systems", "", append(basic, "If-None-Match", "W/\"x\"")...); c != 200 {
		t.Errorf("a mismatching If-None-Match: %d", c)
	}
	if len(b.Log()) == 0 {
		t.Error("no log")
	}
}
