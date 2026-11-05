package redfish_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-redfish/redfish"
	"github.com/go-redfish/redfish/mockbmc"
)

const mgrPath = "/redfish/v1/Managers/BMC"

// forEachManagerTree runs a test against each mockup, with the manager.
func forEachManagerTree(t *testing.T, fn func(t *testing.T, b *mockbmc.BMC, c *redfish.Client, tree string)) {
	for _, tree := range trees {
		t.Run(tree, func(t *testing.T) {
			b, srv := newMock(t, mockbmc.Options{Tree: tree})
			c := newClient(t, srv, redfish.Config{})
			mgr, err := c.ResolveManager(context.Background(), "")
			if err != nil || mgr != mgrPath {
				t.Fatalf("ResolveManager = %q, %v", mgr, err)
			}
			fn(t, b, c, tree)
		})
	}
}

func TestManagerResolveInventoryHealth(t *testing.T) {
	forEachManagerTree(t, func(t *testing.T, b *mockbmc.BMC, c *redfish.Client, tree string) {
		ctx := context.Background()
		if u, err := c.ManagerURIs(ctx); err != nil || len(u) != 1 || u[0] != mgrPath {
			t.Fatalf("ManagerURIs = %v, %v", u, err)
		}
		if u, err := c.ResolveManager(ctx, "BMC"); err != nil || u != mgrPath {
			t.Fatalf("ResolveManager(BMC) = %q, %v", u, err)
		}
		if _, err := c.ResolveManager(ctx, "nope"); !errors.Is(err, redfish.ErrNotFound) {
			t.Fatalf("an unknown manager: %v", err)
		}
		inv, err := c.ManagerInventory(ctx, mgrPath)
		if err != nil || inv["Id"] != "BMC" || inv["ManagerType"] != "BMC" || inv["FirmwareVersion"] == nil || inv["ServiceIdentification"] != "mock-bmc-"+tree || inv["Name"] != nil {
			t.Fatalf("ManagerInventory = %v, %v", inv, err)
		}
		h, err := c.ManagerHealthReport(ctx, mgrPath)
		if st, _ := h["Manager"].(redfish.Props); err != nil || st["Status"] == nil {
			t.Fatalf("ManagerHealthReport = %v, %v", h, err)
		}
		all, err := c.EachManager(ctx, nil, func(ctx context.Context, u string) (any, error) { return c.ManagerInventory(ctx, u) })
		if err != nil || len(all) != 1 || all[0].ManagerURI != mgrPath {
			t.Fatalf("EachManager = %v, %v", all, err)
		}
		if _, err := c.EachManager(ctx, []string{"/redfish/v1/Managers/nope"}, func(ctx context.Context, u string) (any, error) { return c.ManagerInventory(ctx, u) }); !errors.Is(err, redfish.ErrNotFound) {
			t.Fatalf("EachManager on a manager that does not exist: %v", err)
		}
	})
}

func TestManagerPower(t *testing.T) {
	forEachManagerTree(t, func(t *testing.T, b *mockbmc.BMC, c *redfish.Client, _ string) {
		ctx := context.Background()
		for cmd, want := range map[string]string{"PowerReboot": "GracefulRestart", "PowerGracefulRestart": "GracefulRestart", "GracefulRestart": "GracefulRestart", "PowerForceRestart": "ForceRestart"} {
			if o, err := c.ManagerPower(ctx, mgrPath, cmd); err != nil || !o.Changed {
				t.Fatalf("%s: %+v %v", cmd, o, err)
			}
			if got := resourceOf(t, b, mgrPath)["LastResetType"]; got != want {
				t.Errorf("%s sent ResetType %v, want %s", cmd, got, want)
			}
		}
		// The manager allows ForceRestart and GracefulRestart only: ForceOff is not
		// one of them and has no equivalent among them; the service refuses it.
		var se *redfish.StatusError
		if _, err := c.ManagerPower(ctx, mgrPath, "PowerForceOff"); !errors.As(err, &se) || se.Code != http.StatusBadRequest {
			t.Errorf("PowerForceOff: %v", err)
		}
		if _, err := c.ManagerPower(ctx, mgrPath, "PowerBogus"); err == nil {
			t.Error("an unknown command was accepted")
		}
		// A manager that reports On is not powered on again.
		if resourceOf(t, b, mgrPath)["PowerState"] == "On" {
			if o, err := c.ManagerPower(ctx, mgrPath, "PowerOn"); err != nil || o.Changed {
				t.Errorf("PowerOn on a manager that is On: %+v %v", o, err)
			}
		}
	})
}

func TestManagerResetToDefaults(t *testing.T) {
	forEachManagerTree(t, func(t *testing.T, b *mockbmc.BMC, c *redfish.Client, tree string) {
		ctx := context.Background()
		if o, err := c.ManagerResetToDefaults(ctx, mgrPath, "ResetAll"); err != nil || !o.Changed {
			t.Fatalf("ResetAll: %+v %v", o, err)
		}
		if got := resourceOf(t, b, mgrPath)["ResetToDefaultsType"]; got != "ResetAll" {
			t.Errorf("the service got %v", got)
		}
		_, err := c.ManagerResetToDefaults(ctx, mgrPath, "PreserveNetworkAndUsers")
		if tree == mockbmc.TreeRackmount && err != nil {
			t.Errorf("PreserveNetworkAndUsers on the rackmount: %v", err)
		}
		if tree == mockbmc.TreeEmulator { // its ActionInfo does not allow it: nothing is sent
			before := len(b.Log())
			if _, err := c.ManagerResetToDefaults(ctx, mgrPath, "PreserveNetworkAndUsers"); !errors.Is(err, redfish.ErrUnsupported) {
				t.Errorf("a mode the ActionInfo does not allow: %v", err)
			}
			for _, l := range b.Log()[before:] {
				if strings.HasPrefix(l, "POST") {
					t.Errorf("a POST was sent: %s", l)
				}
			}
		}
		if _, err := c.ManagerResetToDefaults(ctx, mgrPath, "Everything"); err == nil {
			t.Error("an unknown mode was accepted")
		}
	})
}

func TestWaitAvailable(t *testing.T) {
	_, srv := newMock(t, mockbmc.Options{})
	c := newClient(t, srv, redfish.Config{})
	if err := c.WaitAvailable(context.Background(), 0, 5*time.Millisecond, time.Second); err != nil {
		t.Fatalf("a service that answers: %v", err)
	}
	down := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "down", http.StatusServiceUnavailable) }))
	t.Cleanup(down.Close)
	d := newClient(t, down, redfish.Config{})
	if err := d.WaitAvailable(context.Background(), 0, 5*time.Millisecond, 40*time.Millisecond); err == nil || !strings.Contains(err.Error(), "did not become available") {
		t.Fatalf("a service that never answers: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := d.WaitAvailable(ctx, time.Hour, time.Hour, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatalf("a cancelled wait: %v", err)
	}
}

func TestNetworkProtocolsAndNTP(t *testing.T) {
	forEachManagerTree(t, func(t *testing.T, b *mockbmc.BMC, c *redfish.Client, _ string) {
		ctx := context.Background()
		np, err := c.NetworkProtocols(ctx, mgrPath)
		if err != nil || np["HTTPS"] == nil || np["SSH"] == nil || np["NTP"] == nil || np["Status"] != nil || np["Proxy"] != nil {
			t.Fatalf("NetworkProtocols = %v, %v", np, err)
		}
		npPath := mgrPath + "/NetworkProtocol"
		o, err := c.SetNetworkProtocols(ctx, mgrPath, redfish.Props{"SSH": map[string]any{"protocolenabled": "off", "port": "2222"}})
		if err != nil || !o.Changed || o.Message != "Modified manager network protocol settings" {
			t.Fatalf("SetNetworkProtocols: %+v %v", o, err)
		}
		ssh, _ := resourceOf(t, b, npPath)["SSH"].(map[string]any)
		if ssh["ProtocolEnabled"] != false || ssh["Port"] != float64(2222) {
			t.Fatalf("SSH = %v", ssh)
		}
		if o, err := c.SetNetworkProtocols(ctx, mgrPath, redfish.Props{"SSH": map[string]any{"ProtocolEnabled": false, "Port": 2222}}); err != nil || o.Changed {
			t.Fatalf("the same values again: %+v %v", o, err)
		}
		for name, bad := range map[string]redfish.Props{
			"unknown service":   {"FTP": map[string]any{"ProtocolEnabled": true}},
			"bad state":         {"SSH": map[string]any{"ProtocolEnabled": "maybe"}},
			"bad port":          {"SSH": map[string]any{"Port": "22x"}},
			"service not a map": {"SSH": "on"},
		} {
			if _, err := c.SetNetworkProtocols(ctx, mgrPath, bad); err == nil {
				t.Errorf("%s was accepted", name)
			}
		}
		// A service the resource does not have: it is refused, not invented.
		if _, err := c.SetNetworkProtocols(ctx, mgrPath, redfish.Props{"RDP": map[string]any{"ProtocolEnabled": true}}); !errors.Is(err, redfish.ErrUnsupported) {
			t.Errorf("a service the resource lacks: %v", err)
		}
		on := true
		if o, err := c.SetNTP(ctx, mgrPath, []string{"0.pool.ntp.org", "1.pool.ntp.org"}, &on); err != nil || !o.Changed {
			t.Fatalf("SetNTP: %+v %v", o, err)
		}
		ntp, _ := resourceOf(t, b, npPath)["NTP"].(map[string]any)
		if servers, _ := ntp["NTPServers"].([]any); ntp["ProtocolEnabled"] != true || len(servers) != 2 || servers[1] != "1.pool.ntp.org" {
			t.Fatalf("NTP = %v", ntp)
		}
		if _, err := c.SetNTP(ctx, mgrPath, nil, nil); err == nil {
			t.Error("SetNTP with nothing to set")
		}
		if _, err := c.NetworkProtocols(ctx, "/redfish/v1/Chassis/1U"); !errors.Is(err, redfish.ErrUnsupported) {
			t.Errorf("a resource without NetworkProtocol: %v", err)
		}
	})
}

func TestManagerDateTimeAndServiceIdentification(t *testing.T) {
	forEachManagerTree(t, func(t *testing.T, b *mockbmc.BMC, c *redfish.Client, tree string) {
		ctx := context.Background()
		dt, err := c.ManagerDateTime(ctx, mgrPath)
		if err != nil || dt["DateTime"] == nil || dt["DateTimeLocalOffset"] != "+06:00" {
			t.Fatalf("ManagerDateTime = %v, %v", dt, err)
		}
		if o, err := c.SetManagerDateTime(ctx, mgrPath, "2026-11-05T17:00:00+01:00", "+01:00"); err != nil || !o.Changed {
			t.Fatalf("SetManagerDateTime: %+v %v", o, err)
		}
		if m := resourceOf(t, b, mgrPath); m["DateTime"] != "2026-11-05T17:00:00+01:00" || m["DateTimeLocalOffset"] != "+01:00" {
			t.Errorf("manager = %v %v", m["DateTime"], m["DateTimeLocalOffset"])
		}
		if o, err := c.SetManagerDateTime(ctx, mgrPath, "", "+01:00"); err != nil || o.Changed {
			t.Errorf("the same offset: %+v %v", o, err)
		}
		for _, bad := range [][2]string{{"yesterday", ""}, {"", "1h"}, {"", "+25:00"}, {"", ""}} {
			if _, err := c.SetManagerDateTime(ctx, mgrPath, bad[0], bad[1]); err == nil {
				t.Errorf("%q was accepted", bad)
			}
		}

		if id, err := c.ServiceIdentification(ctx, mgrPath); err != nil || id != "mock-bmc-"+tree {
			t.Fatalf("ServiceIdentification = %q, %v", id, err)
		}
		if u, err := c.ResolveServiceIdentification(ctx, ""); err != nil || u != mgrPath {
			t.Errorf("ResolveServiceIdentification = %q, %v", u, err)
		}
		if o, err := c.SetServiceIdentification(ctx, mgrPath, "rack-7"); err != nil || !o.Changed {
			t.Fatalf("SetServiceIdentification: %+v %v", o, err)
		}
		if resourceOf(t, b, mgrPath)["ServiceIdentification"] != "rack-7" {
			t.Error("not set")
		}
		if _, err := c.ServiceIdentification(ctx, "/redfish/v1/Chassis/1U"); !errors.Is(err, redfish.ErrUnsupported) {
			t.Errorf("a resource without ServiceIdentification: %v", err)
		}
	})
}

func TestManagerNICs(t *testing.T) {
	forEachManagerTree(t, func(t *testing.T, b *mockbmc.BMC, c *redfish.Client, tree string) {
		ctx := context.Background()
		nics, err := c.ManagerNICInventory(ctx, mgrPath)
		if err != nil || len(nics) == 0 || nics[0]["MACAddress"] != "23:11:8A:33:CF:EA" && nics[len(nics)-1]["MACAddress"] == nil {
			t.Fatalf("ManagerNICInventory = %v, %v", nics, err)
		}
		nicPath := mgrPath + "/EthernetInterfaces/eth0"
		if tree == mockbmc.TreeEmulator {
			nicPath = mgrPath + "/NICs/Dedicated"
		}
		// The interface is found by an address it holds, whatever the case.
		o, err := c.SetManagerNIC(ctx, mgrPath, "23:11:8a:33:cf:ea", redfish.Props{
			"MTUSize":             1400,
			"IPv6StaticAddresses": map[string]any{"Address": "fe80::2", "PrefixLength": 64},
		})
		if err != nil || !o.Changed || o.Message != "Modified manager NIC" {
			t.Fatalf("SetManagerNIC: %+v %v", o, err)
		}
		nic := resourceOf(t, b, nicPath)
		l, _ := nic["IPv6StaticAddresses"].([]any)
		if nic["MTUSize"] != float64(1400) || len(l) != 1 || l[0].(map[string]any)["Address"] != "fe80::2" {
			t.Errorf("nic = %v %v", nic["MTUSize"], nic["IPv6StaticAddresses"])
		}
		if o, err := c.SetManagerNIC(ctx, mgrPath, "192.168.0.10", redfish.Props{"MTUSize": 1400}); err != nil || o.Changed {
			t.Errorf("the same MTU: %+v %v", o, err)
		}
		if _, err := c.SetManagerNIC(ctx, mgrPath, "10.9.9.9", redfish.Props{"MTUSize": 1400}); !errors.Is(err, redfish.ErrNotFound) {
			t.Errorf("an address no interface has: %v", err)
		}
		if _, err := c.SetManagerNIC(ctx, mgrPath, "192.168.0.10", redfish.Props{"NoSuchProperty": 1}); !errors.Is(err, redfish.ErrUnsupported) {
			t.Errorf("a property the interface lacks: %v", err)
		}
		// Without an address, the host of the client's URL is looked for: the
		// mock listens on 127.0.0.1, which no interface holds.
		if _, err := c.SetManagerNIC(ctx, mgrPath, "", redfish.Props{"MTUSize": 1400}); !errors.Is(err, redfish.ErrNotFound) {
			t.Errorf("no address: %v", err)
		}
	})
}

func TestHostInterfaces(t *testing.T) {
	forEachManagerTree(t, func(t *testing.T, b *mockbmc.BMC, c *redfish.Client, tree string) {
		ctx := context.Background()
		hi, err := c.HostInterfaces(ctx)
		if tree == mockbmc.TreeEmulator { // no HostInterfaces
			if !errors.Is(err, redfish.ErrNotFound) {
				t.Fatalf("HostInterfaces without any: %v", err)
			}
			if _, err := c.SetHostInterface(ctx, mgrPath, "", redfish.Props{"InterfaceEnabled": false}); !errors.Is(err, redfish.ErrUnsupported) {
				t.Errorf("SetHostInterface without any: %v", err)
			}
			return
		}
		if err != nil || len(hi) != 1 || hi[0]["HostInterfaceType"] != "NetworkHostInterface" || hi[0]["Name"] != "Host Interface" {
			t.Fatalf("HostInterfaces = %v, %v", hi, err)
		}
		if nic, _ := hi[0]["ManagerEthernetInterface"].(redfish.Props); nic["Id"] != "ToHost" {
			t.Errorf("ManagerEthernetInterface = %v", hi[0]["ManagerEthernetInterface"])
		}
		if _, ok := hi[0]["HostEthernetInterfaces"]; ok {
			if l, _ := hi[0]["HostEthernetInterfaces"].([]redfish.Props); len(l) == 0 {
				t.Errorf("an empty HostEthernetInterfaces: %v", hi[0])
			}
		}
		hiPath := mgrPath + "/HostInterfaces/1"
		if o, err := c.SetHostInterface(ctx, mgrPath, "1", redfish.Props{"InterfaceEnabled": false}); err != nil || !o.Changed || o.Message != "Modified host interface" {
			t.Fatalf("SetHostInterface: %+v %v", o, err)
		}
		if resourceOf(t, b, hiPath)["InterfaceEnabled"] != false {
			t.Error("not disabled")
		}
		if o, err := c.SetHostInterface(ctx, mgrPath, "", redfish.Props{"InterfaceEnabled": false}); err != nil || o.Changed {
			t.Errorf("the only interface, same value: %+v %v", o, err)
		}
		if _, err := c.SetHostInterface(ctx, mgrPath, "9", redfish.Props{"InterfaceEnabled": true}); !errors.Is(err, redfish.ErrNotFound) {
			t.Errorf("an id that is not there: %v", err)
		}
		if _, err := c.SetHostInterface(ctx, mgrPath, "", nil); err == nil {
			t.Error("no configuration was accepted")
		}
	})
}

func TestLogsAndClearLogs(t *testing.T) {
	forEachManagerTree(t, func(t *testing.T, b *mockbmc.BMC, c *redfish.Client, _ string) {
		ctx := context.Background()
		for _, res := range []string{mgrPath, sysPath} {
			logs, err := c.Logs(ctx, res, 0)
			if err != nil || len(logs) == 0 {
				t.Fatalf("Logs(%s) = %v, %v", res, logs, err)
			}
			n := 0
			for _, l := range logs {
				if l.Service == "" || l.Description == "" {
					t.Errorf("a log without service or description: %+v", l)
				}
				n += len(l.Entries)
			}
			if res == mgrPath && n == 0 {
				t.Fatalf("the manager's logs have no entries: %+v", logs)
			}
			if n > 0 {
				if e := logs[0].Entries; len(e) > 0 && e[0]["Message"] == nil && e[0]["MessageId"] == nil && e[0]["Created"] == nil {
					t.Errorf("an entry without its properties: %v", e[0])
				}
			}
			if short, err := c.Logs(ctx, res, 1); err != nil || len(short) != len(logs) || len(short[0].Entries) > 1 {
				t.Errorf("Logs(max 1) = %v, %v", short, err)
			}
			o, err := c.ClearLogs(ctx, res)
			if err != nil || !o.Changed {
				t.Fatalf("ClearLogs(%s): %+v %v", res, o, err)
			}
			after, err := c.Logs(ctx, res, 0)
			if err != nil {
				t.Fatal(err)
			}
			for _, l := range after {
				if len(l.Entries) != 0 {
					t.Errorf("%s: entries left after ClearLogs: %v", res, l.Entries)
				}
			}
		}
		if _, err := c.Logs(ctx, chassisPath, 0); !errors.Is(err, redfish.ErrUnsupported) {
			t.Errorf("Logs of a chassis without LogServices: %v", err)
		}
		if _, err := c.ClearLogs(ctx, chassisPath); !errors.Is(err, redfish.ErrUnsupported) {
			t.Errorf("ClearLogs of a chassis without LogServices: %v", err)
		}
	})
}

// virtualMediaHost is the resource that holds the VirtualMedia on each tree: the
// system on the rackmount, the manager on the emulator; the other has none.
func virtualMediaHost(tree string) (host, other string) {
	if tree == mockbmc.TreeRackmount {
		return sysPath, mgrPath
	}
	return mgrPath, sysPath
}

func TestVirtualMedia(t *testing.T) {
	forEachManagerTree(t, func(t *testing.T, b *mockbmc.BMC, c *redfish.Client, tree string) {
		ctx := context.Background()
		host, other := virtualMediaHost(tree)
		if _, err := c.VirtualMedia(ctx, other); !errors.Is(err, redfish.ErrUnsupported) {
			t.Fatalf("VirtualMedia of a resource that has none: %v", err)
		}
		if _, err := c.VirtualMediaInsert(ctx, other, redfish.VirtualMediaOptions{ImageURL: "https://x/a.iso"}); !errors.Is(err, redfish.ErrUnsupported) {
			t.Errorf("insert on a resource without VirtualMedia: %v", err)
		}
		vm, err := c.VirtualMedia(ctx, host)
		if err != nil || len(vm) != 2 || vm[0]["Id"] == nil || vm[0]["MediaTypes"] == nil || vm[0]["Certificates"] != nil {
			t.Fatalf("VirtualMedia = %v, %v", vm, err)
		}
		cd, floppy := host+"/VirtualMedia/CD1", host+"/VirtualMedia/Floppy1"
		const cdImage = "redfish.dmtf.org/freeImages/freeOS.1.1.iso"
		const floppyImage = "https://www.dmtf.org/freeImages/Sardine.img"
		yes := true

		if o, err := c.VirtualMediaInsert(ctx, host, redfish.VirtualMediaOptions{ImageURL: cdImage}); err != nil || o.Changed {
			t.Fatalf("an image already inserted: %+v %v", o, err)
		}
		if _, err := c.VirtualMediaInsert(ctx, host, redfish.VirtualMediaOptions{ImageURL: "https://x/a.iso", MediaTypes: []string{"CD"}}); !errors.Is(err, redfish.ErrNotFound) {
			t.Fatalf("no free slot: %v", err)
		}
		if _, err := c.VirtualMediaInsert(ctx, host, redfish.VirtualMediaOptions{}); err == nil {
			t.Error("insert without an image")
		}
		if _, err := c.VirtualMediaEject(ctx, host, ""); err == nil {
			t.Error("eject without an image")
		}
		if _, err := c.VirtualMediaEject(ctx, host, "https://x/nothing.iso"); !errors.Is(err, redfish.ErrNotFound) {
			t.Fatalf("eject of an image no slot holds: %v", err)
		}

		// CD1 has the EjectMedia and InsertMedia actions.
		if o, err := c.VirtualMediaEject(ctx, host, cdImage); err != nil || !o.Changed || o.Message != "VirtualMedia ejected" {
			t.Fatalf("eject CD1: %+v %v", o, err)
		}
		if m := resourceOf(t, b, cd); m["Inserted"] != false || m["Image"] != nil {
			t.Fatalf("CD1 after the eject: %v %v", m["Inserted"], m["Image"])
		}
		if _, err := c.VirtualMediaEject(ctx, host, cdImage); !errors.Is(err, redfish.ErrNotFound) {
			t.Errorf("a second eject: %v", err)
		}
		// A value the action does not allow is refused before anything is sent.
		before := len(b.Log())
		if _, err := c.VirtualMediaInsert(ctx, host, redfish.VirtualMediaOptions{ImageURL: "https://x/a.iso", MediaTypes: []string{"DVD"}, TransferProtocolType: "FTP"}); err == nil || !strings.Contains(err.Error(), "AllowableValues") {
			t.Errorf("a protocol the action does not allow: %v", err)
		}
		for _, l := range b.Log()[before:] {
			if !strings.HasPrefix(l, "GET") {
				t.Errorf("a request was sent: %s", l)
			}
		}
		o, err := c.VirtualMediaInsert(ctx, host, redfish.VirtualMediaOptions{ImageURL: "https://x/a.iso", MediaTypes: []string{"DVD"}, Inserted: &yes, WriteProtected: &yes, TransferProtocolType: "NFS", Username: "u", Password: "p"})
		if err != nil || !o.Changed || o.Message != "VirtualMedia inserted" {
			t.Fatalf("insert in CD1: %+v %v", o, err)
		}
		if m := resourceOf(t, b, cd); m["Inserted"] != true || m["Image"] != "https://x/a.iso" || m["ImageName"] != "a.iso" || m["TransferProtocolType"] != "NFS" || m["WriteProtected"] != true {
			t.Fatalf("CD1 after the insert: %v", m)
		}
		if o, err := c.VirtualMediaInsert(ctx, host, redfish.VirtualMediaOptions{ImageURL: "https://x/a.iso"}); err != nil || o.Changed {
			t.Errorf("the same image again: %+v %v", o, err)
		}

		// Floppy1 has no action: a PATCH of Image and Inserted. The resource lists
		// the TransferMethod values it allows.
		if o, err := c.VirtualMediaEject(ctx, host, floppyImage); err != nil || !o.Changed {
			t.Fatalf("eject Floppy1: %+v %v", o, err)
		}
		if m := resourceOf(t, b, floppy); m["Inserted"] != false || m["Image"] != nil || m["ImageName"] != nil {
			t.Fatalf("Floppy1 after the eject: %v", m)
		}
		if _, err := c.VirtualMediaInsert(ctx, host, redfish.VirtualMediaOptions{ImageURL: "https://x/f.img", MediaTypes: []string{"Floppy"}, TransferMethod: "Bogus"}); err == nil || !strings.Contains(err.Error(), "AllowableValues") {
			t.Errorf("a method the resource does not allow: %v", err)
		}
		if o, err := c.VirtualMediaInsert(ctx, host, redfish.VirtualMediaOptions{ImageURL: "https://x/f.img", MediaTypes: []string{"Floppy"}, TransferMethod: "Upload"}); err != nil || !o.Changed {
			t.Fatalf("insert in Floppy1: %+v %v", o, err)
		}
		if m := resourceOf(t, b, floppy); m["Inserted"] != true || m["Image"] != "https://x/f.img" || m["TransferMethod"] != "Upload" {
			t.Fatalf("Floppy1 after the insert: %v", m)
		}

		// An image that stays in a slot that is not inserted is "already ejected".
		if _, err := c.Patch(ctx, floppy, redfish.Props{"Inserted": false}, ""); err != nil {
			t.Fatal(err)
		}
		if o, err := c.VirtualMediaEject(ctx, host, "https://x/f.img"); err != nil || o.Changed || !strings.Contains(o.Message, "already ejected") {
			t.Errorf("an image already ejected: %+v %v", o, err)
		}

		// Without media types, any free slot does: the loose match.
		if _, err := c.VirtualMediaEject(ctx, host, "https://x/a.iso"); err != nil {
			t.Fatal(err)
		}
		if o, err := c.VirtualMediaInsert(ctx, host, redfish.VirtualMediaOptions{ImageURL: "https://x/b.iso"}); err != nil || !o.Changed {
			t.Fatalf("insert without media types: %+v %v", o, err)
		}
		if resourceOf(t, b, cd)["Image"] != "https://x/b.iso" {
			t.Error("CD1 does not hold b.iso")
		}
	})
}

func TestChassisIndicatorLED(t *testing.T) {
	forEachTree(t, func(t *testing.T, b *mockbmc.BMC, c *redfish.Client, _ string) {
		ctx := context.Background()
		for cmd, want := range map[string]string{"IndicatorLedBlink": "Blinking", "IndicatorLedOn": "Lit", "IndicatorLedOff": "Off"} {
			if _, err := c.SetIndicatorLED(ctx, chassisPath, cmd); err != nil {
				t.Fatalf("%s: %v", cmd, err)
			}
			if got := resourceOf(t, b, chassisPath)["IndicatorLED"]; got != want {
				t.Errorf("%s: IndicatorLED = %v, want %s", cmd, got, want)
			}
			if again, err := c.SetIndicatorLED(ctx, chassisPath, cmd); err != nil || again.Changed {
				t.Errorf("%s again changed something: %+v %v", cmd, again, err)
			}
		}
		if _, err := c.SetIndicatorLED(ctx, chassisPath, "IndicatorLedPurple"); err == nil {
			t.Error("an unknown LED command was accepted")
		}
		if _, err := c.SetIndicatorLED(ctx, "/redfish/v1/Chassis/nope", "IndicatorLedOn"); !errors.Is(err, redfish.ErrNotFound) {
			t.Errorf("a chassis that does not exist: %v", err)
		}
	})
}
