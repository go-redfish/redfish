package redfish_test

import (
	"strings"
	"testing"

	"github.com/go-redfish/redfish"
)

// The decoders of the Storage area take what a BMC sends; the validators what a
// caller names. None may panic, and each keeps its bounds.

func FuzzStorageReads(f *testing.F) {
	f.Add([]byte(`{"Id":"Disk1","Name":"d","CapacityBytes":1000,"Links":{"Volumes":[{"@odata.id":"/redfish/v1/V/1"}]},"Status":{"State":"Enabled"}}`), "/redfish/v1/D/1")
	f.Add([]byte(`{"Id":"Vol1","RAIDType":"RAID1","Capacity":null,"Links":{"Drives":[{"@odata.id":"/redfish/v1/D/Disk1/"},5,null,{"@odata.id":7}]}}`), "")
	f.Add([]byte(`{"Links":[1],"@odata.id":"http://evil/x","Name":null}`), "/redfish/../x")
	f.Add([]byte(`{"Name":"c","FirmwareVersion":"1","SpeedGbps":12,"Identifiers":[{}]}`), "x")
	f.Fuzz(func(t *testing.T, data []byte, uri string) {
		if d, err := redfish.ParseDrive(data, uri); err == nil {
			if _, ok := d["RedfishURI"]; !ok {
				t.Fatal("a drive without RedfishURI")
			}
			if _, ok := d["Links"]; ok {
				t.Fatal("Links must be reported as Volumes")
			}
			if len(d) > 22 {
				t.Fatal("a property that was not asked for")
			}
		}
		if v, err := redfish.ParseVolume(data); err == nil {
			if len(v) > 17 {
				t.Fatal("a property that was not asked for")
			}
			for k, x := range v {
				if x == nil && k != "Linked_drives" {
					t.Fatalf("a null kept: %s", k)
				}
			}
		}
		if c, err := redfish.ParseController(data); err == nil && len(c) > 12 {
			t.Fatal("a property that was not asked for")
		}
	})
}

func FuzzVolumeRequest(f *testing.F) {
	f.Add("RAID1", "/redfish/v1/Systems/1/Storage/R/Drives/D1", "RAID0,RAID1", "/redfish/v1/Systems/1/Storage/R/Drives/D1")
	f.Add("", "http://evil/redfish/x", "", "")
	f.Add("RAID5", "/redfish/../x,/redfish/v1/a\r\nb", "RAID5", "/redfish/v1/b")
	f.Fuzz(func(t *testing.T, raid, drives, supported, own string) {
		ds := strings.Split(drives, ",")
		details := redfish.Props{"RAIDType": raid, "Name": "n"}
		p, err := redfish.BuildVolumePayload(details, ds)
		if err == nil {
			if raid == "" {
				t.Fatal("no RAIDType accepted")
			}
			links, _ := p["Links"].(map[string]any)["Drives"].([]any)
			if len(links) != len(ds) {
				t.Fatalf("%d drives sent for %d", len(links), len(ds))
			}
			for _, l := range links {
				u, _ := l.(map[string]any)["@odata.id"].(string)
				if !strings.HasPrefix(u, "/redfish/") || strings.Contains(u, "..") || strings.ContainsAny(u, "\r\n\\") {
					t.Fatalf("a drive outside the service: %q", u)
				}
			}
			if _, ok := details["Links"]; ok {
				t.Fatal("the caller's details were modified")
			}
		}
		err = redfish.CheckVolumeRequest(raid, ds, strings.Split(supported, ","), strings.Split(own, ","))
		if err == nil && supported != "" && !strings.Contains(","+supported+",", ","+raid+",") {
			t.Fatalf("RAID type %q passed against %q", raid, supported)
		}
	})
}
