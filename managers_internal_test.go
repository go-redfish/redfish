package redfish

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func props(t testing.TB, doc string) Props {
	t.Helper()
	p, err := decodeProps([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestNetworkProtocolPayload(t *testing.T) {
	ok := func(doc string, want string) {
		t.Helper()
		got, err := NetworkProtocolPayload(props(t, doc))
		if err != nil {
			t.Fatalf("%s: %v", doc, err)
		}
		b, _ := json.Marshal(got)
		if string(b) != want {
			t.Errorf("%s -> %s, want %s", doc, b, want)
		}
	}
	// the words the module reads as on and off, both spellings of the keys
	ok(`{"SSH":{"ProtocolEnabled":"on","Port":"22"}}`, `{"SSH":{"Port":22,"ProtocolEnabled":true}}`)
	ok(`{"SSH":{"protocolenabled":"False","port":2222}}`, `{"SSH":{"Port":2222,"ProtocolEnabled":false}}`)
	ok(`{"HTTP":{"ProtocolEnabled":1},"HTTPS":{"ProtocolEnabled":0}}`, `{"HTTP":{"ProtocolEnabled":true},"HTTPS":{"ProtocolEnabled":false}}`)
	ok(`{"NTP":{"NTPServers":["a","b"],"ProtocolEnabled":"true"}}`, `{"NTP":{"NTPServers":["a","b"],"ProtocolEnabled":true}}`)
	ok(`{"SNMP":{}}`, `{"SNMP":{}}`)
	ok(`{}`, `{}`)
	for _, doc := range []string{
		`{"FTP":{}}`, `{"SSH":1}`, `{"SSH":{"ProtocolEnabled":"yes"}}`, `{"SSH":{"ProtocolEnabled":2}}`, `{"SSH":{"ProtocolEnabled":null}}`,
		`{"SSH":{"Port":"-1"}}`, `{"SSH":{"Port":"1e3"}}`, `{"SSH":{"Port":true}}`, `{"SSH":{"Port":1.5}}`, `{"SSH":{"Port":""}}`, `{"SSH":{"Port":[1]}}`,
	} {
		if p, err := NetworkProtocolPayload(props(t, doc)); err == nil {
			t.Errorf("%s was accepted: %v", doc, p)
		}
	}
}

func FuzzNetworkProtocolPayload(f *testing.F) {
	f.Add(`{"SSH":{"ProtocolEnabled":"on","Port":"22"}}`)
	f.Add(`{"NTP":{"NTPServers":[1,null],"port":{}}}`)
	f.Add(`{"SSH":1,"x":[]}`)
	f.Fuzz(func(t *testing.T, doc string) {
		p, err := decodeProps([]byte(doc))
		if err != nil {
			return
		}
		out, err := NetworkProtocolPayload(p)
		if err != nil {
			return
		}
		for name := range out { // what is built only names services of the list
			found := false
			for _, s := range ProtocolServices {
				found = found || s == name
			}
			if !found {
				t.Fatalf("service %q in the payload", name)
			}
		}
	})
}

func TestParseLogEntries(t *testing.T) {
	d, m, next, err := ParseLogEntries([]byte(`{"Description":"d","Members":[{"Message":"a"},{"@odata.id":"/redfish/v1/x"},3,null],"Members@odata.nextLink":"/redfish/v1/p2"}`))
	if err != nil || d != "d" || len(m) != 2 || next != "/redfish/v1/p2" {
		t.Fatalf("ParseLogEntries = %q %v %q %v", d, m, next, err)
	}
	if d, m, _, err := ParseLogEntries([]byte(`{}`)); err != nil || d != "Collection of log entries" || len(m) != 0 {
		t.Errorf("an empty collection: %q %v %v", d, m, err)
	}
	for _, bad := range []string{``, `[]`, `null`, `{"Members":`} {
		if _, _, _, err := ParseLogEntries([]byte(bad)); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
}

func FuzzParseLogEntries(f *testing.F) {
	f.Add([]byte(`{"Description":"d","Members":[{"Message":"a"}],"Members@odata.nextLink":"/redfish/v1/p"}`))
	f.Add([]byte(`{"Members":[1,[],{"@odata.id":5}]}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		_, m, _, err := ParseLogEntries(data)
		if err == nil && len(m) > MaxLogEntries {
			t.Fatalf("%d members", len(m))
		}
	})
}

func TestSameImage(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{"https://h/a.iso", "https://h/a.iso", true},
		{"HTTPS://h/a.iso", "https://h/a.iso", true},
		{"https://h/a.iso", "https://h/b.iso", false},
		{"https://h/a.iso", "http://h/a.iso", false},
		{"https://h/a.iso?x=1", "https://h/a.iso", false},
		{"https://u:p@h/a.iso", "https://h/a.iso", false},
		{"redfish.dmtf.org/freeImages/freeOS.1.1.iso", "redfish.dmtf.org/freeImages/freeOS.1.1.iso", true},
		{"h:80/a", "h:80/a", true}, // not a URL Go parses: compared as text
		{"h:80/a", "h:80/b", false},
	} {
		if got := sameImage(tc.a, tc.b); got != tc.want {
			t.Errorf("sameImage(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestVirtualMediaSlotRules(t *testing.T) {
	sl := func(uri, doc string) slot { return slot{uri: uri, data: props(t, doc)} }
	slots := []slot{
		sl("/RDOC1", `{"MediaTypes":["CD"],"Inserted":false}`),
		sl("/Slot2", `{"MediaTypes":["Floppy"],"Inserted":false,"ImageName":null}`),
		sl("/Slot3", `{"MediaTypes":["CD"],"Inserted":false,"ImageName":"left over","ConnectedVia":"NotConnected"}`),
		sl("/Slot4", `{"Inserted":false}`),
	}
	name := func(s slot, ok bool) string {
		if !ok {
			return ""
		}
		return s.uri
	}
	for _, tc := range []struct {
		types  []string
		strict bool
		vendor string
		want   string
	}{
		{[]string{"CD"}, true, "", "/RDOC1"},
		{[]string{"CD"}, true, "Lenovo", ""}, // RDOC and Remote slots do not take Insert/Eject
		{[]string{"CD"}, true, "Supermicro", "/RDOC1"},
		{[]string{"Floppy"}, true, "", "/Slot2"},
		{[]string{"DVD"}, true, "", ""},
		{[]string{"DVD"}, false, "", "/Slot4"}, // the slot that says nothing of its types
		{nil, true, "", ""},
		{nil, false, "", "/RDOC1"},
	} {
		if got := name(findEmptySlot(slots, tc.types, tc.strict, tc.vendor)); got != tc.want {
			t.Errorf("findEmptySlot(%v, strict %v, %q) = %q, want %q", tc.types, tc.strict, tc.vendor, got, tc.want)
		}
	}
	// Supermicro does not clear ImageName: NotConnected says the slot is free.
	if s, ok := findEmptySlot(slots[2:3], []string{"CD"}, true, "Supermicro"); !ok || s.uri != "/Slot3" {
		t.Error("a Supermicro slot that is NotConnected is free")
	}
	if _, ok := findEmptySlot(slots[2:3], []string{"CD"}, true, ""); ok {
		t.Error("a slot that still names an image is not free")
	}

	held := []slot{
		sl("/A", `{"Image":"https://h/a.iso","Inserted":true,"ImageName":"a"}`),
		sl("/B", `{"Image":"https://h/b.iso","Inserted":false,"ImageName":"b"}`),
		sl("/C", `{"Image":"https://h/c.iso"}`),
	}
	if !imageInserted(held, "https://h/a.iso") || imageInserted(held, "https://h/b.iso") || imageInserted(held, "https://h/c.iso") {
		t.Error("imageInserted")
	}
	if s, found, eject := findToEject(held, "https://h/a.iso"); !found || !eject || s.uri != "/A" {
		t.Errorf("eject A: %v %v %v", s.uri, found, eject)
	}
	if s, found, eject := findToEject(held, "https://h/b.iso"); !found || eject || s.uri != "/B" {
		t.Errorf("B is already ejected: %v %v %v", s.uri, found, eject)
	}
	if _, found, eject := findToEject(held, "https://h/c.iso"); !found || !eject { // Inserted and ImageName default to true
		t.Errorf("C: %v %v", found, eject)
	}
	if _, found, _ := findToEject(held, "https://h/z.iso"); found {
		t.Error("z")
	}
	for allow, want := range map[string]bool{"": true, "GET, PATCH": true, "GET,HEAD": false, "PATCH": true} {
		if patchAllowed(allow) != want {
			t.Errorf("patchAllowed(%q) != %v", allow, want)
		}
	}
}

// A service that tells its vendor (HPE, Supermicro) and refuses Inserted and
// WriteProtected is asked again without them; another one is not.
func TestVirtualMediaQuirkyVendors(t *testing.T) {
	for _, tc := range []struct {
		vendor string
		ok     bool
	}{{"HPE", true}, {"Supermicro", true}, {"Contoso", false}} {
		t.Run(tc.vendor, func(t *testing.T) {
			var mu sync.Mutex
			var bodies []string
			slotDoc := `{"Id":"1","MediaTypes":["CD"],"Image":null,"Inserted":false,"WriteProtected":true,"Actions":{"#VirtualMedia.InsertMedia":{"target":"/redfish/v1/Managers/1/VirtualMedia/1/Actions/VirtualMedia.InsertMedia"}}}`
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				body, _ := io.ReadAll(r.Body)
				switch {
				case r.Method == http.MethodPost:
					bodies = append(bodies, string(body))
					if strings.Contains(string(body), "Inserted") || strings.Contains(string(body), "WriteProtected") {
						http.Error(w, `{"error":{"code":"Base.1.0.PropertyUnknown","message":"no"}}`, http.StatusBadRequest)
						return
					}
					w.WriteHeader(http.StatusNoContent)
				case r.URL.Path == "/redfish/v1/":
					_, _ = w.Write([]byte(`{"Vendor":"` + tc.vendor + `","Managers":{"@odata.id":"/redfish/v1/Managers"}}`))
				case r.URL.Path == "/redfish/v1/Managers":
					_, _ = w.Write([]byte(`{"Members":[{"@odata.id":"/redfish/v1/Managers/1"}]}`))
				case r.URL.Path == "/redfish/v1/Managers/1":
					_, _ = w.Write([]byte(`{"VirtualMedia":{"@odata.id":"/redfish/v1/Managers/1/VirtualMedia"}}`))
				case r.URL.Path == "/redfish/v1/Managers/1/VirtualMedia":
					_, _ = w.Write([]byte(`{"Members":[{"@odata.id":"/redfish/v1/Managers/1/VirtualMedia/1"}]}`))
				case r.URL.Path == "/redfish/v1/Managers/1/VirtualMedia/1":
					_, _ = w.Write([]byte(slotDoc))
				default:
					http.NotFound(w, r)
				}
			}))
			t.Cleanup(srv.Close)
			c, err := New(Config{BaseURL: srv.URL, HTTP: srv.Client(), Credentials: Static{User: "u", Password: "p"}})
			if err != nil {
				t.Fatal(err)
			}
			yes := true
			o, err := c.VirtualMediaInsert(context.Background(), "/redfish/v1/Managers/1", VirtualMediaOptions{ImageURL: "https://h/a.iso", Inserted: &yes, WriteProtected: &yes})
			if (err == nil) != tc.ok || (tc.ok && !o.Changed) {
				t.Fatalf("insert: %+v %v", o, err)
			}
			if tc.ok && (len(bodies) != 2 || bodies[1] != `{"Image":"https://h/a.iso"}`) {
				t.Errorf("bodies = %q", bodies)
			}
			if !tc.ok && len(bodies) != 1 {
				t.Errorf("a service of vendor %s was asked %d times", tc.vendor, len(bodies))
			}
		})
	}
}

// Whatever the manager and the resources it links hold, the Managers reads and
// the VirtualMedia commands neither panic nor hang.
func FuzzManagerReads(f *testing.F) {
	f.Add(`{"Members":[{"@odata.id":"/redfish/v1/Managers/1/x"}],"Image":"a","Inserted":true,"MediaTypes":["CD"],"ImageName":"a","Actions":{"#VirtualMedia.InsertMedia":{"target":"/redfish/v1/Managers/1/x"},"#LogService.ClearLog":{"target":"/redfish/v1/Managers/1/x"}},"Entries":{"@odata.id":"/redfish/v1/Managers/1/x"},"ManagerEthernetInterface":{"@odata.id":"/redfish/v1/Managers/1/x"}}`)
	f.Add(`{"Members":5,"Actions":[],"MediaTypes":"CD","Image":1,"Inserted":"yes","Entries":{"@odata.id":5},"NTP":[]}`)
	f.Add(`[]`)
	links := `{"NetworkProtocol":{"@odata.id":"/redfish/v1/Managers/1/np"},"EthernetInterfaces":{"@odata.id":"/redfish/v1/Managers/1/nics"},"HostInterfaces":{"@odata.id":"/redfish/v1/Managers/1/hi"},"LogServices":{"@odata.id":"/redfish/v1/Managers/1/logs"},"VirtualMedia":{"@odata.id":"/redfish/v1/Managers/1/vm"},"Actions":{"#Manager.ResetToDefaults":{"target":"/redfish/v1/Managers/1/reset"}}}`
	r := routes(`{}`, map[string]string{
		"/redfish/v1/":                       `{"Managers":{"@odata.id":"/redfish/v1/Managers"}}`,
		"/redfish/v1/Managers":               `{"Members":[{"@odata.id":"/redfish/v1/Managers/1"}]}`,
		"/redfish/v1/Managers/1":             links,
		"/redfish/v1/Managers/1/np":          `{}`,
		"/redfish/v1/Managers/1/nics":        `{}`,
		"/redfish/v1/Managers/1/hi":          `{}`,
		"/redfish/v1/Managers/1/logs":        `{}`,
		"/redfish/v1/Managers/1/vm":          `{}`,
		"/redfish/v1/Managers/1/x":           `{}`,
		"/redfish/v1/Managers/1/reset":       `{}`,
		"/redfish/v1/Managers/1/vm/1":        `{}`,
		"/redfish/v1/Managers/1/nics/1":      `{}`,
		"/redfish/v1/Managers/1/logs/1":      `{}`,
		"/redfish/v1/Managers/1/logs/1/Ents": `{}`,
	})
	b := newBMC(f, r)
	c := b.client(f, nil)
	f.Fuzz(func(t *testing.T, doc string) {
		b.mu.Lock()
		for _, k := range []string{"np", "nics", "hi", "logs", "vm", "x", "vm/1", "nics/1", "logs/1"} {
			b.routes["/redfish/v1/Managers/1/"+k] = doc
		}
		b.mu.Unlock()
		ctx := context.Background()
		const m = "/redfish/v1/Managers/1"
		_, _ = c.ManagerInventory(ctx, m)
		_, _ = c.ManagerHealthReport(ctx, m)
		_, _ = c.NetworkProtocols(ctx, m)
		_, _ = c.ManagerNICInventory(ctx, m)
		_, _ = c.HostInterfaces(ctx)
		_, _ = c.Logs(ctx, m, 4)
		_, _ = c.ClearLogs(ctx, m)
		_, _ = c.VirtualMedia(ctx, m)
		_, _ = c.VirtualMediaInsert(ctx, m, VirtualMediaOptions{ImageURL: "https://h/a.iso", MediaTypes: []string{"CD"}})
		_, _ = c.VirtualMediaEject(ctx, m, "https://h/a.iso")
		_, _ = c.SetNTP(ctx, m, []string{"a"}, nil)
		_, _ = c.SetManagerNIC(ctx, m, "a", Props{"MTUSize": 1})
		_, _ = c.SetHostInterface(ctx, m, "", Props{"InterfaceEnabled": true})
		_, _ = c.ManagerResetToDefaults(ctx, m, "ResetAll")
		_, _ = c.ServiceIdentification(ctx, m)
		_, _ = c.ResolveServiceIdentification(ctx, "")
	})
}
