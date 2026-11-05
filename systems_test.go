package redfish_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/go-redfish/redfish"
	"github.com/go-redfish/redfish/mockbmc"
)

var trees = []string{mockbmc.TreeRackmount, mockbmc.TreeEmulator}

// forEachTree runs a test against each mockup: the commands must not be tuned
// to one vendor tree.
func forEachTree(t *testing.T, fn func(t *testing.T, b *mockbmc.BMC, c *redfish.Client, sys string)) {
	for _, tree := range trees {
		t.Run(tree, func(t *testing.T) {
			b, srv := newMock(t, mockbmc.Options{Tree: tree})
			c := newClient(t, srv, redfish.Config{})
			sys, err := c.ResolveSystem(context.Background(), "")
			if err != nil || sys != sysPath {
				t.Fatalf("ResolveSystem = %q, %v", sys, err)
			}
			fn(t, b, c, sys)
		})
	}
}

func resourceOf(t *testing.T, b *mockbmc.BMC, p string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(b.Resource(p), &m); err != nil {
		t.Fatalf("%s: %v", p, err)
	}
	return m
}

func TestPowerCommands(t *testing.T) {
	forEachTree(t, func(t *testing.T, b *mockbmc.BMC, c *redfish.Client, sys string) {
		ctx := context.Background()
		if o, err := c.SystemPower(ctx, sys, "PowerOn"); err != nil || o.Changed {
			t.Fatalf("PowerOn on a system that is On: %+v %v", o, err)
		}
		if o, err := c.SystemPower(ctx, sys, "PowerForceOff"); err != nil || !o.Changed {
			t.Fatalf("PowerForceOff: %+v %v", o, err)
		}
		if resourceOf(t, b, sys)["PowerState"] != "Off" {
			t.Fatal("the system is not Off")
		}
		// PowerOn: "On" is not allowed by the rackmount's ActionInfo, which lists
		// ForceOn; the mock refuses a value outside it, so success shows the mapping.
		if o, err := c.SystemPower(ctx, sys, "PowerOn"); err != nil || !o.Changed {
			t.Fatalf("PowerOn: %+v %v", o, err)
		}
		if resourceOf(t, b, sys)["PowerState"] != "On" {
			t.Fatal("the system is not On")
		}
		for _, cmd := range []string{"PowerReboot", "PowerGracefulRestart", "PowerForceRestart", "PowerGracefulShutdown"} {
			if _, err := c.SystemPower(ctx, sys, cmd); err != nil {
				t.Errorf("%s: %v", cmd, err)
			}
		}
		if _, err := c.SystemPower(ctx, sys, "PowerBogus"); err == nil {
			t.Error("an unknown command was accepted")
		}
		if _, err := c.SystemPower(ctx, sys, "Reboot"); err == nil {
			t.Error("a command without the Power prefix was accepted")
		}
	})
}

func TestResetTypeOf(t *testing.T) {
	for cmd, want := range map[string]string{"PowerOn": "On", "PowerForceOff": "ForceOff", "PowerReboot": "GracefulRestart", "PowerCycle": "PowerCycle", "PowerFullPowerCycle": "FullPowerCycle", "PowerGracefulShutdown": "GracefulShutdown"} {
		if got, ok := redfish.ResetTypeOf(cmd); !ok || got != want {
			t.Errorf("ResetTypeOf(%s) = %q, %v; want %q", cmd, got, ok, want)
		}
	}
	for _, cmd := range []string{"", "On", "Power", "PowerNope"} {
		if _, ok := redfish.ResetTypeOf(cmd); ok {
			t.Errorf("ResetTypeOf(%q) accepted", cmd)
		}
	}
}

func TestBootOverride(t *testing.T) {
	forEachTree(t, func(t *testing.T, b *mockbmc.BMC, c *redfish.Client, sys string) {
		ctx := context.Background()
		o, err := c.SetBootOverride(ctx, sys, redfish.BootOverride{Device: "Cd", Enabled: "Continuous"})
		if err != nil || !o.Changed {
			t.Fatalf("Continuous Cd: %+v %v", o, err)
		}
		boot := resourceOf(t, b, sys)["Boot"].(map[string]any)
		if boot["BootSourceOverrideTarget"] != "Cd" || boot["BootSourceOverrideEnabled"] != "Continuous" {
			t.Fatalf("Boot = %v", boot)
		}
		if o, err := c.SetBootOverride(ctx, sys, redfish.BootOverride{Device: "Cd", Enabled: "Continuous"}); err != nil || o.Changed {
			t.Fatalf("again: %+v %v (want unchanged)", o, err)
		}
		if _, err := c.SetBootOverride(ctx, sys, redfish.BootOverride{Device: "Floppy9", Enabled: "Once"}); !errors.Is(err, redfish.ErrUnsupported) {
			t.Errorf("a device outside the allowable values: %v", err)
		}
		if _, err := c.SetBootOverride(ctx, sys, redfish.BootOverride{Device: "UefiTarget", Enabled: "Once"}); err == nil {
			t.Error("UefiTarget without a target")
		}
		if _, err := c.SetBootOverride(ctx, sys, redfish.BootOverride{Enabled: "Once"}); err == nil {
			t.Error("no device")
		}
		if o, err := c.SetBootOverride(ctx, sys, redfish.BootOverride{Device: "UefiTarget", UefiTarget: "/0x1/0x2", Enabled: "Once"}); err != nil || !o.Changed {
			t.Fatalf("UefiTarget: %+v %v", o, err)
		}
		if o, err := c.SetBootOverride(ctx, sys, redfish.BootOverride{Enabled: "Disabled"}); err != nil || !o.Changed {
			t.Fatalf("Disabled: %+v %v", o, err)
		}
		got, err := c.BootOverrideOf(ctx, sys)
		if err != nil || got["BootSourceOverrideEnabled"] != "Disabled" || got["BootSourceOverrideTarget"] != "None" {
			t.Fatalf("BootOverrideOf = %v, %v", got, err)
		}
	})
}

func TestBootOrder(t *testing.T) {
	forEachTree(t, func(t *testing.T, b *mockbmc.BMC, c *redfish.Client, sys string) {
		ctx := context.Background()
		order, err := c.BootOrder(ctx, sys)
		if err != nil || len(order) != 2 || order[0]["BootOptionReference"] != "Boot0001" || order[0]["DisplayName"] != "UEFI Network Boot" {
			t.Fatalf("BootOrder = %v, %v", order, err)
		}
		if o, err := c.SetBootOrder(ctx, sys, []string{"Boot0002", "Boot0001"}); err != nil || !o.Changed {
			t.Fatalf("SetBootOrder: %+v %v", o, err)
		}
		if order, _ = c.BootOrder(ctx, sys); order[0]["BootOptionReference"] != "Boot0002" {
			t.Fatalf("after: %v", order)
		}
		if o, err := c.SetBootOrder(ctx, sys, []string{"Boot0002", "Boot0001"}); err != nil || o.Changed {
			t.Fatalf("same order again: %+v %v", o, err)
		}
		if _, err := c.SetBootOrder(ctx, sys, []string{"Boot9999"}); !errors.Is(err, redfish.ErrUnsupported) {
			t.Errorf("an unknown reference: %v", err)
		}
		if _, err := c.SetBootOrder(ctx, sys, nil); err == nil {
			t.Error("an empty list")
		}
		if o, err := c.SetDefaultBootOrder(ctx, sys); err != nil || !o.Changed {
			t.Fatalf("SetDefaultBootOrder: %+v %v", o, err)
		}
		if order, _ = c.BootOrder(ctx, sys); order[0]["BootOptionReference"] != "Boot0001" {
			t.Fatalf("after default: %v", order)
		}
	})
}

func TestBios(t *testing.T) {
	forEachTree(t, func(t *testing.T, b *mockbmc.BMC, c *redfish.Client, sys string) {
		ctx := context.Background()
		attrs, err := c.BiosAttributes(ctx, sys)
		if err != nil || attrs["ProcTurboMode"] != "Enabled" {
			t.Fatalf("BiosAttributes = %v, %v", attrs, err)
		}
		// pending (no ApplyTime): the Settings object takes it, the Bios resource does not
		o, err := c.SetBiosAttributes(ctx, sys, redfish.Props{"ProcTurboMode": "Disabled", "NoSuchAttr": 1}, "")
		if err != nil || !o.Changed || !strings.Contains(o.Warning, "NoSuchAttr") {
			t.Fatalf("SetBiosAttributes: %+v %v", o, err)
		}
		settings := resourceOf(t, b, sysPath+"/Bios/Settings")["Attributes"].(map[string]any)
		if settings["ProcTurboMode"] != "Disabled" {
			t.Fatalf("Settings = %v", settings)
		}
		if a, _ := c.BiosAttributes(ctx, sys); a["ProcTurboMode"] != "Enabled" {
			t.Fatal("the attribute changed before the apply time")
		}
		// Immediate: in force at once
		if o, err = c.SetBiosAttributes(ctx, sys, redfish.Props{"ProcTurboMode": "Disabled"}, "Immediate"); err != nil || !o.Changed {
			t.Fatalf("Immediate: %+v %v", o, err)
		}
		if _, err := c.VerifyBiosAttributes(ctx, sys, redfish.Props{"ProcTurboMode": "Disabled", "ProcCoreDisable": 0}); err != nil {
			t.Errorf("verify: %v", err)
		}
		if _, err := c.VerifyBiosAttributes(ctx, sys, redfish.Props{"ProcTurboMode": "Enabled"}); !errors.Is(err, redfish.ErrMismatch) {
			t.Errorf("verify mismatch: %v", err)
		}
		if _, err := c.VerifyBiosAttributes(ctx, sys, redfish.Props{"Nope": 1}); !errors.Is(err, redfish.ErrUnsupported) {
			t.Errorf("verify wrong: %v", err)
		}
		if o, err = c.SetBiosAttributes(ctx, sys, redfish.Props{"ProcTurboMode": "Disabled"}, ""); err != nil || o.Changed {
			t.Fatalf("already set: %+v %v", o, err)
		}
		if o, err = c.ResetBios(ctx, sys); err != nil || !o.Changed {
			t.Fatalf("ResetBios: %+v %v", o, err)
		}
		reg, uri, err := c.BiosRegistry(ctx, sys)
		if err != nil || !strings.HasSuffix(uri, "/en") || reg["RegistryEntries"] == nil {
			t.Fatalf("BiosRegistry = %v %q %v", reg, uri, err)
		}
	})
}

func TestSecureBootPowerRestoreLedAndAssetTag(t *testing.T) {
	forEachTree(t, func(t *testing.T, b *mockbmc.BMC, c *redfish.Client, sys string) {
		ctx := context.Background()
		o, err := c.SetSecureBoot(ctx, sys, true)
		if b.Resource(sysPath+"/SecureBoot") == nil { // the emulator's tree has none
			if !errors.Is(err, redfish.ErrUnsupported) {
				t.Errorf("SecureBoot absent: %v", err)
			}
		} else if err != nil || !o.Changed || resourceOf(t, b, sysPath+"/SecureBoot")["SecureBootEnable"] != true {
			t.Errorf("SetSecureBoot: %+v %v", o, err)
		}
		if p, err := c.PowerRestorePolicy(ctx, sys); err != nil || p != "LastState" {
			t.Errorf("PowerRestorePolicy = %v, %v", p, err)
		}
		if o, err := c.SetPowerRestorePolicy(ctx, sys, "AlwaysOn"); err != nil || !o.Changed {
			t.Errorf("SetPowerRestorePolicy: %+v %v", o, err)
		}
		if o, err := c.SetPowerRestorePolicy(ctx, sys, "AlwaysOn"); err != nil || o.Changed {
			t.Errorf("again: %+v %v", o, err)
		}
		chassis, err := c.ResolveChassis(ctx, "")
		if err != nil || chassis != "/redfish/v1/Chassis/1U" {
			t.Fatalf("ResolveChassis = %q %v", chassis, err)
		}
		for uri, state := range map[string]map[string]string{sys: {"IndicatorLedOn": "Lit", "IndicatorLedBlink": "Blinking", "IndicatorLedOff": "Off"}, chassis: {"IndicatorLedBlink": "Blinking", "IndicatorLedOff": "Off"}} {
			for cmd, want := range state {
				if _, err := c.SetIndicatorLED(ctx, uri, cmd); err != nil {
					t.Errorf("%s %s: %v", cmd, uri, err)
				}
				if got := resourceOf(t, b, uri)["IndicatorLED"]; got != want {
					t.Errorf("%s %s: IndicatorLED = %v, want %s", cmd, uri, got, want)
				}
			}
		}
		if _, err := c.SetIndicatorLED(ctx, sys, "IndicatorLedPurple"); err == nil {
			t.Error("an unknown LED command")
		}
		if o, err := c.SetAssetTag(ctx, sys, "rack-7"); err != nil || !o.Changed || resourceOf(t, b, sys)["AssetTag"] != "rack-7" {
			t.Errorf("SetAssetTag: %+v %v", o, err)
		}
		if _, err := c.PatchChecked(ctx, sys, redfish.Props{"NoSuchProperty": 1}); !errors.Is(err, redfish.ErrUnsupported) {
			t.Errorf("an unsupported property: %v", err)
		}
	})
}

func TestInventories(t *testing.T) {
	forEachTree(t, func(t *testing.T, b *mockbmc.BMC, c *redfish.Client, sys string) {
		ctx := context.Background()
		inv, err := c.SystemInventory(ctx, sys)
		if err != nil || inv["Id"] != "437XR1138R2" || inv["Manufacturer"] == nil {
			t.Fatalf("SystemInventory = %v, %v", inv, err)
		}
		cpus, err := c.CPUInventory(ctx, sys)
		if err != nil || len(cpus) < 2 || cpus[0]["Id"] == nil {
			t.Fatalf("CPUInventory = %v, %v", cpus, err)
		}
		mem, err := c.MemoryInventory(ctx, sys)
		if err != nil || len(mem) == 0 {
			t.Fatalf("MemoryInventory = %v, %v", mem, err)
		}
		for _, m := range mem {
			if st, _ := m["Status"].(map[string]any); st == nil || st["State"] == "Absent" {
				t.Errorf("an absent module in the inventory: %v", m)
			}
		}
		nics, err := c.NICInventory(ctx, sys)
		if err != nil || len(nics) == 0 || nics[0]["Id"] == nil {
			t.Fatalf("NICInventory = %v, %v", nics, err)
		}
		psus, err := c.PSUInventory(ctx)
		if err != nil || len(psus) == 0 || psus[0]["Name"] == nil {
			t.Fatalf("PSUInventory = %v, %v", psus, err)
		}
		h, err := c.SystemHealthReport(ctx, sys)
		if err != nil {
			t.Fatalf("SystemHealthReport: %v", err)
		}
		if st, _ := h["System"].(map[string]any); st == nil || st["Status"] == nil {
			t.Errorf("health[System] = %v", h["System"])
		}
		procs, _ := h["Processors"].([]redfish.Props)
		if len(procs) < 2 || procs[0]["processor_uri"] == nil {
			t.Errorf("health[Processors] = %v", h["Processors"])
		}
	})
}

func TestCheckPayload(t *testing.T) {
	cur := redfish.Props{"A": "x", "B": map[string]any{"C": json.Number("1"), "D": []any{"p"}}}
	cases := []struct {
		req     redfish.Props
		changes bool
		bad     string
	}{
		{redfish.Props{"A": "x"}, false, ""},
		{redfish.Props{"A": "y"}, true, ""},
		{redfish.Props{"B": redfish.Props{"C": 1}}, false, ""},
		{redfish.Props{"B": redfish.Props{"C": 2}}, true, ""},
		{redfish.Props{"B": redfish.Props{"D": []string{"p"}}}, false, ""},
		{redfish.Props{"B": redfish.Props{"D": []string{"q"}}}, true, ""},
		{redfish.Props{"Z": 1}, false, "Z"},
		{redfish.Props{"B": redfish.Props{"Z": 1}}, false, "Z"},
		{redfish.Props{"A": "x", "Z": 1}, false, "Z"},
	}
	for i, tc := range cases {
		if ch, bad := redfish.CheckPayload(tc.req, cur); ch != tc.changes || bad != tc.bad {
			t.Errorf("case %d %v: changes=%v bad=%q, want %v %q", i, tc.req, ch, bad, tc.changes, tc.bad)
		}
	}
}

func TestParseActionInfo(t *testing.T) {
	got, err := redfish.ParseActionInfo([]byte(`{"Parameters":[{"Name":"ResetType","AllowableValues":["On","ForceOff"]},{"Name":"Other"},{"AllowableValues":["x"]}]}`))
	if err != nil || len(got) != 2 || strings.Join(got["ResetType"], ",") != "On,ForceOff" || got["Other"] != nil {
		t.Fatalf("ParseActionInfo = %v, %v", got, err)
	}
	if _, err := redfish.ParseActionInfo([]byte(`[1]`)); err == nil {
		t.Error("a non-object was accepted")
	}
}

func FuzzCheckPayload(f *testing.F) {
	f.Add([]byte(`{"A":1,"B":{"C":"x"}}`), []byte(`{"A":1,"B":{"C":"y"}}`))
	f.Fuzz(func(t *testing.T, a, b []byte) {
		var req, cur map[string]any
		if json.Unmarshal(a, &req) != nil || json.Unmarshal(b, &cur) != nil {
			return
		}
		redfish.CheckPayload(req, cur)
	})
}

func FuzzParseActionInfo(f *testing.F) {
	f.Add([]byte(`{"Parameters":[{"Name":"ResetType","AllowableValues":["On"]}]}`))
	f.Add([]byte(`{"Parameters":[null,1,{"Name":2}]}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		v, err := redfish.ParseActionInfo(data)
		if err == nil {
			for _, l := range v {
				if len(l) > 256 {
					t.Fatalf("unbounded values: %d", len(l))
				}
			}
			if len(v) > 64 {
				t.Fatalf("unbounded parameters: %d", len(v))
			}
		}
	})
}

func FuzzPick(f *testing.F) {
	f.Add([]byte(`{"Id":"1","Status":{"State":"Enabled"}}`))
	f.Add([]byte(`null`))
	f.Fuzz(func(t *testing.T, data []byte) {
		if p, err := redfish.Pick(data, "Id", "Status", "Name"); err == nil && p == nil {
			t.Fatal("nil props without an error")
		}
	})
}
