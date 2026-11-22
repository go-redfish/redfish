package mockbmc_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-redfish/redfish/mockbmc"
)

const maxFuzzAnswer = 8 << 20

// Whatever the method, path, query and body of a request, the mock BMC's handler
// does not panic, answers with a valid status and a bounded body, never serves
// an unauthenticated request outside the open resources, and answers a 401 in
// the Redfish way (an error object).
func FuzzHandler(f *testing.F) {
	type seed struct {
		method, path, body string
		auth               bool
	}
	for _, s := range []seed{
		{"GET", "/redfish/v1/", "", false}, {"GET", "/redfish/v1/Systems", "", false}, {"GET", "/redfish/v1/Systems", "", true},
		{"GET", "/redfish/v1/Systems?$expand=*($levels=2)&$select=Name&$filter=Id%20eq%20'x'&only", "", true},
		{"POST", "/redfish/v1/SessionService/Sessions", `{"UserName":"admin","Password":"password"}`, false},
		{"POST", "/redfish/v1/SessionService/Sessions", `{"UserName":5}`, false},
		{"PATCH", "/redfish/v1/Systems/437XR1138R2", `{"AssetTag":"x"}`, true}, {"PATCH", "/redfish/v1/Systems/437XR1138R2", `[1`, true},
		{"POST", "/redfish/v1/Systems/437XR1138R2/Actions/ComputerSystem.Reset", `{"ResetType":"ForceOff"}`, true},
		{"POST", "/redfish/v1/UpdateService/Actions/UpdateService.SimpleUpdate", `{"ImageURI":"https://fw.example/x"}`, true},
		{"POST", "/redfish/v1/AccountService/Accounts", `{"UserName":"u","Password":"p","RoleId":"Administrator"}`, true},
		{"DELETE", "/redfish/v1/SessionService/Sessions/1", "", true}, {"DELETE", "/redfish/v1/", "", true},
		{"PUT", "/redfish/v1/Chassis/1U", `{}`, true}, {"OPTIONS", "/redfish/v1/", "", true}, {"HEAD", "/redfish/v1/", "", false},
		{"GET", "/redfish/v1/../../etc/passwd", "", true}, {"GET", "/redfish/v1/%00", "", true}, {"GET", "/redfish/v1/$metadata", "", false},
		{"POST", "/redfish/v1/TelemetryService/MetricReportDefinitions", `{"Id":"d","MetricReportDefinitionType":"OnRequest"}`, true},
	} {
		f.Add(s.method, s.path, s.body, s.auth)
	}
	b, err := mockbmc.New(mockbmc.Options{})
	if err != nil {
		f.Fatal(err)
	}
	h := b.Handler()
	f.Fuzz(func(t *testing.T, method, path, body string, auth bool) {
		if strings.HasSuffix(strings.SplitN(path, "?", 2)[0], "/EventService/SSE") {
			t.Skip("the event stream does not end on its own")
		}
		if !strings.HasPrefix(path, "/") {
			path = "/" + path
		}
		req, err := http.NewRequest(method, "https://bmc.example"+path, strings.NewReader(body))
		if err != nil {
			t.Skip() // not a request a server can receive
		}
		if auth {
			req.SetBasicAuth("admin", "password")
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code < 100 || rec.Code > 599 {
			t.Fatalf("%s %q: status %d", method, path, rec.Code)
		}
		if rec.Body.Len() > maxFuzzAnswer {
			t.Fatalf("%s %q: %d bytes answered to a %d byte request", method, path, rec.Body.Len(), len(body))
		}
		if rec.Code == http.StatusUnauthorized && req.Method != "HEAD" {
			var e struct {
				Error map[string]any `json:"error"`
			}
			if json.Unmarshal(rec.Body.Bytes(), &e) != nil || e.Error == nil {
				t.Fatalf("%s %q: a 401 that is not a Redfish error: %q", method, path, rec.Body.String())
			}
		}
		if !auth && rec.Code < 400 {
			p := strings.TrimRight(req.URL.Path, "/")
			m := method // net/http reads an empty Method as GET (http.Request)
			if m == "" {
				m = http.MethodGet
			}
			open := (m == "GET" && (p == "/redfish" || p == "/redfish/v1" || p == "/redfish/v1/$metadata" || p == "/redfish/v1/odata")) ||
				(m == "POST" && p == "/redfish/v1/SessionService/Sessions") || (m == "HEAD" && p == "/redfish/v1")
			if !open {
				t.Fatalf("%s %q served without authentication: %d", method, path, rec.Code)
			}
		}
	})
}
