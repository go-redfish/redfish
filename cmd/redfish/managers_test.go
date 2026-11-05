package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-redfish/redfish"
	"github.com/go-redfish/redfish/mockbmc"
)

const mgrPath = "/redfish/v1/Managers/BMC"

func TestManagersReadsAndReset(t *testing.T) {
	forEachTree(t, func(t *testing.T, e *env) {
		for _, tc := range []struct {
			args []string
			want string
		}{
			{[]string{"managers", "list"}, mgrPath},
			{[]string{"managers", "inventory"}, `"manager_uri": "` + mgrPath + `"`},
			{[]string{"managers", "--id", "BMC", "inventory"}, "FirmwareVersion"},
			{[]string{"managers", "health"}, `"Manager"`},
			{[]string{"managers", "nic", "get"}, "MACAddress"},
			{[]string{"managers", "network-protocols", "get"}, "HTTPS"},
			{[]string{"managers", "datetime", "get"}, "DateTimeLocalOffset"},
			{[]string{"managers", "service-id", "get"}, "mock-bmc-"},
			{[]string{"managers", "logs"}, "Service"},
		} {
			if out := e.ok(tc.args...); !strings.Contains(out, tc.want) {
				t.Errorf("%v: %q lacks %q", tc.args, out, tc.want)
			}
		}
		if out := e.ok("-o", "table", "managers", "inventory"); !strings.Contains(out, "manager_uri") || strings.Contains(out, "map[") {
			t.Errorf("table: %q", out)
		}
		for _, args := range [][]string{{"managers", "--id", "nope", "inventory"}, {"managers", "--id", "nope", "power", "GracefulRestart"}, {"managers", "power", "PowerBogus"}, {"managers", "power"}} {
			if _, _, err := e.run(args...); err == nil {
				t.Errorf("%v: want an error", args)
			}
		}

		// power: GracefulRestart is PowerGracefulRestart; --wait blocks until the
		// service answers.
		if out := e.ok("managers", "power", "GracefulRestart"); !strings.Contains(out, `"changed": true`) || e.resource(mgrPath)["LastResetType"] != "GracefulRestart" {
			t.Errorf("GracefulRestart: %q", out)
		}
		e.ok("managers", "--id", "BMC", "power", "PowerForceRestart")
		if e.resource(mgrPath)["LastResetType"] != "ForceRestart" {
			t.Error("PowerForceRestart sent another ResetType")
		}
		if out := e.ok("managers", "power", "PowerReboot", "--wait", "--wait-delay", "0s", "--wait-timeout", "5s"); !strings.Contains(out, `"changed": true`) {
			t.Errorf("--wait: %q", out)
		}
		if out := e.ok("managers", "reset-to-defaults", "ResetAll"); !strings.Contains(out, `"changed": true`) || e.resource(mgrPath)["ResetToDefaultsType"] != "ResetAll" {
			t.Errorf("reset-to-defaults: %q", out)
		}
		if _, _, err := e.run("managers", "reset-to-defaults", "Everything"); err == nil {
			t.Error("an unknown mode")
		}
	})
}

func TestManagersConfig(t *testing.T) {
	forEachTree(t, func(t *testing.T, e *env) {
		np := mgrPath + "/NetworkProtocol"
		e.ok("managers", "network-protocols", "set", "--set", "SSH.ProtocolEnabled=off", "--set", "SSH.Port=2222")
		if ssh, _ := e.resource(np)["SSH"].(map[string]any); ssh["ProtocolEnabled"] != false || ssh["Port"] != float64(2222) {
			t.Errorf("SSH = %v", ssh)
		}
		e.ok("managers", "network-protocols", "set", "--json", `{"HTTP":{"Port":"8080"}}`)
		if http, _ := e.resource(np)["HTTP"].(map[string]any); http["Port"] != float64(8080) {
			t.Errorf("HTTP = %v", http)
		}
		for _, args := range [][]string{
			{"managers", "network-protocols", "set"},
			{"managers", "network-protocols", "set", "--set", "FTP.Port=21"},
			{"managers", "network-protocols", "set", "--json", "[1]"},
			{"managers", "network-protocols", "set", "--json", `{"SSH":{}} {}`},
			{"managers", "network-protocols", "set", "--set", "noequals"},
		} {
			if _, _, err := e.run(args...); err == nil {
				t.Errorf("%v: want an error", args)
			}
		}

		e.ok("managers", "ntp", "set", "--server", "0.pool.ntp.org", "--server", "1.pool.ntp.org", "--enable")
		if ntp, _ := e.resource(np)["NTP"].(map[string]any); ntp["ProtocolEnabled"] != true || len(ntp["NTPServers"].([]any)) != 2 {
			t.Errorf("NTP = %v", ntp)
		}
		e.ok("managers", "ntp", "set", "--disable")
		if ntp, _ := e.resource(np)["NTP"].(map[string]any); ntp["ProtocolEnabled"] != false {
			t.Errorf("NTP = %v", ntp)
		}
		if _, _, err := e.run("managers", "ntp", "set", "--enable", "--disable"); err == nil {
			t.Error("--enable and --disable")
		}
		if _, _, err := e.run("managers", "ntp", "set"); err == nil {
			t.Error("ntp set with nothing to set")
		}

		e.ok("managers", "datetime", "set", "--time", "2026-11-05T17:00:00+01:00", "--offset", "+01:00")
		if m := e.resource(mgrPath); m["DateTime"] != "2026-11-05T17:00:00+01:00" || m["DateTimeLocalOffset"] != "+01:00" {
			t.Errorf("manager = %v", m["DateTime"])
		}
		if _, _, err := e.run("managers", "datetime", "set", "--time", "noon"); err == nil {
			t.Error("a bad time")
		}

		e.ok("managers", "service-id", "set", "rack-7")
		if out := e.ok("managers", "service-id", "get"); !strings.Contains(out, "rack-7") {
			t.Errorf("service-id get: %q", out)
		}

		nic := mgrPath + "/EthernetInterfaces/eth0"
		if e.bmc.Resource(nic) == nil {
			nic = mgrPath + "/NICs/Dedicated"
		}
		e.ok("managers", "nic", "set", "--address", "192.168.0.10", "--set", "MTUSize=1400", "--json", `{"IPv6StaticAddresses":{"Address":"fe80::2","PrefixLength":64}}`)
		if r := e.resource(nic); r["MTUSize"] != float64(1400) || len(r["IPv6StaticAddresses"].([]any)) != 1 {
			t.Errorf("nic = %v", r["MTUSize"])
		}
		if _, _, err := e.run("managers", "nic", "set", "--address", "10.9.9.9", "--set", "MTUSize=1"); !errors.Is(err, redfish.ErrNotFound) {
			t.Errorf("an address no interface has: %v", err)
		}
		if _, _, err := e.run("managers", "nic", "set", "--address", "192.168.0.10"); err == nil {
			t.Error("nic set with nothing to set")
		}

		if e.bmc.Resource(mgrPath+"/HostInterfaces") == nil { // the emulator has none
			if _, _, err := e.run("managers", "host-interface", "get"); !errors.Is(err, redfish.ErrNotFound) {
				t.Errorf("host-interface get without any: %v", err)
			}
			return
		}
		if out := e.ok("managers", "host-interface", "get"); !strings.Contains(out, "NetworkHostInterface") || !strings.Contains(out, "ToHost") {
			t.Errorf("host-interface get: %q", out)
		}
		e.ok("managers", "host-interface", "set", "--interface", "1", "--set", "InterfaceEnabled=false")
		if e.resource(mgrPath + "/HostInterfaces/1")["InterfaceEnabled"] != false {
			t.Error("host interface not disabled")
		}
		if _, _, err := e.run("managers", "host-interface", "set", "--interface", "9", "--set", "InterfaceEnabled=true"); !errors.Is(err, redfish.ErrNotFound) {
			t.Errorf("an unknown host interface: %v", err)
		}
	})
}

func TestLogsAndVirtualMediaCommands(t *testing.T) {
	forEachTree(t, func(t *testing.T, e *env) {
		// logs of a manager, a system; a chassis has none on the mockups
		for _, group := range []string{"managers", "systems"} {
			if out := e.ok(group, "logs", "--max", "2"); !strings.Contains(out, "Service") {
				t.Errorf("%s logs: %q", group, out)
			}
			if out := e.ok(group, "logs", "clear"); !strings.Contains(out, `"changed": true`) {
				t.Errorf("%s logs clear: %q", group, out)
			}
			if out := e.ok(group, "logs"); strings.Contains(out, `"Message"`) {
				t.Errorf("%s logs after clear: %q", group, out)
			}
		}
		if _, _, err := e.run("chassis", "logs"); !errors.Is(err, redfish.ErrUnsupported) {
			t.Errorf("chassis logs: %v", err)
		}

		// virtual media: on the system (rackmount) or on the manager (emulator)
		group, other := "systems", "managers"
		if e.bmc.Resource(mgrPath+"/VirtualMedia") != nil {
			group, other = "managers", "systems"
		}
		if _, _, err := e.run(other, "virtual-media", "get"); !errors.Is(err, redfish.ErrUnsupported) {
			t.Errorf("%s virtual-media get: %v", other, err)
		}
		if out := e.ok(group, "virtual-media", "get"); !strings.Contains(out, "CD1") || !strings.Contains(out, "Floppy1") {
			t.Errorf("virtual-media get: %q", out)
		}
		if out := e.ok("-o", "table", group, "virtual-media", "get"); !strings.Contains(out, "MediaTypes") || strings.Contains(out, "map[") {
			t.Errorf("table: %q", out)
		}
		const cdImage = "redfish.dmtf.org/freeImages/freeOS.1.1.iso"
		if out := e.ok(group, "virtual-media", "insert", "--image", cdImage); !strings.Contains(out, `"changed": false`) {
			t.Errorf("an image already inserted: %q", out)
		}
		if out := e.ok(group, "virtual-media", "eject", "--image", cdImage); !strings.Contains(out, `"changed": true`) {
			t.Errorf("eject: %q", out)
		}
		pw := filepath.Join(t.TempDir(), "image-pw")
		if err := os.WriteFile(pw, []byte("secret\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		out := e.ok(group, "virtual-media", "insert", "--image", "https://x/new.iso", "--media-type", "CD", "--write-protected=false",
			"--transfer-protocol", "HTTPS", "--image-username", "u", "--image-password-file", pw)
		if !strings.Contains(out, `"changed": true`) || strings.Contains(out, "secret") {
			t.Errorf("insert: %q", out)
		}
		cd := e.resource(strings.TrimSuffix(mgrOrSystem(group), "/") + "/VirtualMedia/CD1")
		if cd["Image"] != "https://x/new.iso" || cd["Inserted"] != true || cd["WriteProtected"] != false {
			t.Errorf("CD1 = %v %v %v", cd["Image"], cd["Inserted"], cd["WriteProtected"])
		}
		bad := filepath.Join(t.TempDir(), "open-pw")
		if err := os.WriteFile(bad, []byte("secret\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		for _, args := range [][]string{
			{group, "virtual-media", "insert"},
			{group, "virtual-media", "eject"},
			{group, "virtual-media", "insert", "--image", "https://x/o.iso", "--image-password-file", bad},
			{group, "virtual-media", "eject", "--image", "https://x/nothing.iso"},
		} {
			if _, _, err := e.run(args...); err == nil {
				t.Errorf("%v: want an error", args)
			}
		}
	})
}

func mgrOrSystem(group string) string {
	if group == "managers" {
		return mgrPath
	}
	return sysPath
}

func TestPropFlags(t *testing.T) {
	p := propFlags{sets: []string{"A.B.C=1", "A.B.D=on", "X=null", "Y=text"}, js: `{"A":{"B":{"E":2}},"Z":[1]}`}
	got, err := p.props()
	if err != nil {
		t.Fatal(err)
	}
	b := got["A"].(map[string]any)["B"].(map[string]any)
	if len(b) != 3 || b["E"] == nil || b["C"] == nil || b["D"] != "on" || got["X"] != nil || got["Y"] != "text" || got["Z"] == nil {
		t.Errorf("props = %v", got)
	}
	for _, bad := range []propFlags{{js: "null"}, {js: "1"}, {js: `{} {}`}, {sets: []string{"=1"}}} {
		if _, err := bad.props(); err == nil {
			t.Errorf("%+v was accepted", bad)
		}
	}
	if got, err := (&propFlags{}).props(); err != nil || len(got) != 0 {
		t.Errorf("no flags: %v %v", got, err)
	}
}

// Whatever --set and --json hold, the properties are built or refused, and a
// dotted name never reaches a value that is not an object.
func FuzzPropFlags(f *testing.F) {
	f.Add("A.B=1", `{"A":{"B":2}}`)
	f.Add("A.B.C=x", `{"A":5}`)
	f.Add("..=1", `[`)
	f.Add("=", `{"":{}}`)
	f.Fuzz(func(t *testing.T, set, js string) {
		p := propFlags{sets: []string{set}, js: js}
		if got, err := p.props(); err == nil && got == nil {
			t.Fatal("no properties and no error")
		}
	})
}

// The chassis LED rows of PARITY.md: chassis led on, off and blink set the
// IndicatorLED of the chassis (the redfish_command Chassis commands
// IndicatorLedOn, IndicatorLedOff, IndicatorLedBlink).
func TestChassisLedCommands(t *testing.T) {
	forEachTree(t, func(t *testing.T, e *env) {
		const chassis = "/redfish/v1/Chassis/1U"
		// the mockups start with the LED lit: blink and off change it, then on does
		for _, args := range [][]string{{"blink"}, {"off"}, {"--id", "1U", "on"}} {
			sub, id := args[len(args)-1], []string{}
			if len(args) == 3 {
				id = args[:2]
			}
			want := map[string]string{"on": "Lit", "blink": "Blinking", "off": "Off"}[sub]
			out := e.ok(append(append([]string{"chassis"}, id...), "led", sub)...)
			if !strings.Contains(out, "Set IndicatorLED to "+want) {
				t.Errorf("chassis led %s: %q", sub, out)
			}
			if got := e.resource(chassis)["IndicatorLED"]; got != want {
				t.Errorf("chassis led %s: IndicatorLED = %v, want %s", sub, got, want)
			}
			if again := e.ok(append(append([]string{"chassis"}, id...), "led", sub)...); !strings.Contains(again, `"changed": false`) {
				t.Errorf("chassis led %s again: %q", sub, again)
			}
		}
		if _, _, err := e.run("chassis", "--id", "nope", "led", "on"); !errors.Is(err, redfish.ErrNotFound) {
			t.Errorf("an unknown chassis: %v", err)
		}
	})
}

var _ = mockbmc.TreeRackmount
