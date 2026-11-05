package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/go-redfish/redfish"
	"github.com/go-redfish/redfish/mockbmc"
	"github.com/spf13/cobra"
)

const sysPath = "/redfish/v1/Systems/437XR1138R2"

func forEachTree(t *testing.T, fn func(t *testing.T, e *env)) {
	for _, tree := range []string{mockbmc.TreeRackmount, mockbmc.TreeEmulator} {
		t.Run(tree, func(t *testing.T) { fn(t, newEnvTree(t, tree)) })
	}
}

func (e *env) resource(p string) map[string]any {
	e.t.Helper()
	var m map[string]any
	if err := json.Unmarshal(e.bmc.Resource(p), &m); err != nil {
		e.t.Fatalf("%s: %v", p, err)
	}
	return m
}

// ok runs a command that must succeed and returns its stdout.
func (e *env) ok(args ...string) string {
	e.t.Helper()
	out, _, err := e.run(args...)
	if err != nil {
		e.t.Fatalf("%v: %v", args, err)
	}
	return out
}

func TestSystemsListAndPower(t *testing.T) {
	forEachTree(t, func(t *testing.T, e *env) {
		if out := e.ok("systems", "list"); !strings.Contains(out, sysPath) {
			t.Fatalf("list: %q", out)
		}
		if out := e.ok("systems", "--id", "437XR1138R2", "power", "PowerOn"); !strings.Contains(out, `"changed": false`) {
			t.Fatalf("PowerOn on a system that is On: %q", out)
		}
		if out := e.ok("systems", "power", "PowerForceOff"); !strings.Contains(out, `"changed": true`) || e.resource(sysPath)["PowerState"] != "Off" {
			t.Fatalf("PowerForceOff: %q", out)
		}
		e.ok("systems", "power", "PowerOn")
		if e.resource(sysPath)["PowerState"] != "On" {
			t.Fatal("not On")
		}
		if _, _, err := e.run("systems", "power", "PowerBogus"); err == nil {
			t.Error("an unknown power command")
		}
		if _, _, err := e.run("systems", "--id", "nope", "power", "PowerOn"); !errors.Is(err, redfish.ErrNotFound) {
			t.Errorf("an unknown id: %v", err)
		}
		if out := e.ok("-o", "table", "systems", "inventory", "system"); !strings.Contains(out, "PROPERTY") {
			t.Errorf("table: %q", out)
		}
	})
}

func TestSystemsBoot(t *testing.T) {
	forEachTree(t, func(t *testing.T, e *env) {
		e.ok("systems", "boot-override", "set", "Cd", "--continuous")
		boot := e.resource(sysPath)["Boot"].(map[string]any)
		if boot["BootSourceOverrideTarget"] != "Cd" || boot["BootSourceOverrideEnabled"] != "Continuous" {
			t.Fatalf("Boot = %v", boot)
		}
		if out := e.ok("systems", "boot-override", "get"); !strings.Contains(out, "Continuous") {
			t.Errorf("get: %q", out)
		}
		if _, _, err := e.run("systems", "boot-override", "set", "Cd", "--once", "--continuous"); err == nil {
			t.Error("--once with --continuous")
		}
		if _, _, err := e.run("systems", "boot-override", "set", "UefiTarget"); err == nil {
			t.Error("UefiTarget without --uefi-target")
		}
		e.ok("systems", "boot-override", "set", "UefiTarget", "--uefi-target", "/0x1/0x2")
		e.ok("systems", "boot-override", "disable")
		if out := e.ok("systems", "boot-override", "get"); !strings.Contains(out, "Disabled") {
			t.Errorf("after disable: %q", out)
		}
		if out := e.ok("systems", "boot-order", "get"); !strings.Contains(out, "Boot0001") {
			t.Errorf("boot-order get: %q", out)
		}
		e.ok("systems", "boot-order", "set", "Boot0002", "Boot0001")
		if o := e.resource(sysPath)["Boot"].(map[string]any)["BootOrder"].([]any); o[0] != "Boot0002" {
			t.Errorf("BootOrder = %v", o)
		}
		if _, _, err := e.run("systems", "boot-order", "set", "Boot9999"); err == nil {
			t.Error("an unknown reference")
		}
		e.ok("systems", "boot-order", "default")
		if o := e.resource(sysPath)["Boot"].(map[string]any)["BootOrder"].([]any); o[0] != "Boot0001" {
			t.Errorf("BootOrder after default = %v", o)
		}
	})
}

func TestSystemsBios(t *testing.T) {
	forEachTree(t, func(t *testing.T, e *env) {
		if out := e.ok("systems", "bios", "get"); !strings.Contains(out, "ProcTurboMode") {
			t.Fatalf("get: %q", out)
		}
		out, errOut, err := e.run("systems", "bios", "set", "--attr", "ProcTurboMode=Disabled", "--attr", "NoSuchAttr=1")
		if err != nil || !strings.Contains(out, `"changed": true`) || !strings.Contains(errOut, "NoSuchAttr") {
			t.Fatalf("set: %q %q %v", out, errOut, err)
		}
		e.ok("systems", "bios", "set", "--attr", "ProcTurboMode=Disabled", "--apply-time", "Immediate")
		e.ok("systems", "bios", "verify", "--attr", "ProcTurboMode=Disabled", "--attr", "ProcCoreDisable=0")
		if _, _, err := e.run("systems", "bios", "verify", "--attr", "ProcTurboMode=Enabled"); !errors.Is(err, redfish.ErrMismatch) {
			t.Errorf("verify mismatch: %v", err)
		}
		if _, _, err := e.run("systems", "bios", "set"); err == nil {
			t.Error("set without --attr")
		}
		if _, _, err := e.run("systems", "bios", "set", "--attr", "novalue"); err == nil {
			t.Error("a malformed --attr")
		}
		e.ok("systems", "bios", "reset")
		if out := e.ok("systems", "bios", "registry"); !strings.Contains(out, "RegistryEntries") {
			t.Errorf("registry: %q", out)
		}
	})
}

func TestParseAttrs(t *testing.T) {
	got, err := parseAttrs([]string{"A=1", "B=true", "C=text", "D=null", `E="quoted"`, "F=1 2", "G="})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(got)
	want := `{"A":1,"B":true,"C":"text","D":null,"E":"\"quoted\"","F":"1 2","G":""}`
	if string(b) != want {
		t.Errorf("parseAttrs = %s, want %s", b, want)
	}
}

func TestSystemsSecureBootPolicyLedAssetTagInventoryHealth(t *testing.T) {
	forEachTree(t, func(t *testing.T, e *env) {
		_, _, err := e.run("systems", "secure-boot", "enable")
		if e.bmc.Resource(sysPath+"/SecureBoot") == nil {
			if !errors.Is(err, redfish.ErrUnsupported) {
				t.Errorf("SecureBoot absent: %v", err)
			}
		} else if err != nil || e.resource(sysPath + "/SecureBoot")["SecureBootEnable"] != true {
			t.Errorf("secure-boot enable: %v", err)
		}
		if out := e.ok("systems", "power-restore-policy", "get"); !strings.Contains(out, "LastState") {
			t.Errorf("get: %q", out)
		}
		e.ok("systems", "power-restore-policy", "set", "AlwaysOn")
		if e.resource(sysPath)["PowerRestorePolicy"] != "AlwaysOn" {
			t.Error("policy not set")
		}
		for arg, want := range map[string]string{"on": "Lit", "blink": "Blinking", "off": "Off"} {
			e.ok("systems", "led", arg)
			if got := e.resource(sysPath)["IndicatorLED"]; got != want {
				t.Errorf("systems led %s: %v, want %s", arg, got, want)
			}
		}
		for arg, want := range map[string]string{"blink": "Blinking", "off": "Off"} {
			e.ok("chassis", "led", arg)
			if got := e.resource("/redfish/v1/Chassis/1U")["IndicatorLED"]; got != want {
				t.Errorf("chassis led %s: %v, want %s", arg, got, want)
			}
		}
		e.ok("chassis", "--id", "1U", "led", "off")
		if _, _, err := e.run("chassis", "--id", "nope", "led", "on"); !errors.Is(err, redfish.ErrNotFound) {
			t.Errorf("an unknown chassis: %v", err)
		}
		e.ok("systems", "asset-tag", "rack-7")
		if e.resource(sysPath)["AssetTag"] != "rack-7" {
			t.Error("asset tag not set")
		}
		for _, k := range []string{"system", "cpu", "memory", "nic", "psu"} {
			if out := e.ok("systems", "inventory", k); len(out) < 10 {
				t.Errorf("inventory %s: %q", k, out)
			}
		}
		if out := e.ok("systems", "health"); !strings.Contains(out, `"System"`) || !strings.Contains(out, "processor_uri") {
			t.Errorf("health: %q", out)
		}
	})
}

// Rendered help: a backticked word in a flag's usage becomes the flag's value
// name, and cobra appends the default itself.
func TestHelpIsRenderedAsWritten(t *testing.T) {
	out, _, err := run(t, "--help")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "`") {
		t.Errorf("a backtick in the help: %q", out)
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "--session ") && (strings.Contains(line, "--session redfish") || !strings.Contains(line, "use the session saved by 'redfish session login'")) {
			t.Errorf("--session usage: %q", line)
		}
	}
	out, _, err = run(t, "mock", "serve", "--help")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "--listen") && strings.Count(line, "default") != 1 {
			t.Errorf("--listen: want one default, got %q", line)
		}
	}
}

// An error is printed once on stderr, by execute; cobra stays silent.
func TestErrorIsPrintedOnce(t *testing.T) {
	var stderr bytes.Buffer
	root := RootCmd()
	root.SetOut(&stderr)
	root.SetErr(&stderr)
	root.SetArgs([]string{"raw", "get", "/redfish/v1/"}) // no --endpoint
	if code := execute(context.Background(), root, &stderr); code != 1 {
		t.Fatalf("exit status %d", code)
	}
	if n := strings.Count(stderr.String(), "--endpoint is required"); n != 1 {
		t.Errorf("the message is printed %d times: %q", n, stderr.String())
	}
	var _ *cobra.Command = root
}

func TestChassisCommands(t *testing.T) {
	forEachTree(t, func(t *testing.T, e *env) {
		for _, tc := range []struct {
			args []string
			want string
		}{
			{[]string{"chassis", "list"}, "/redfish/v1/Chassis/1U"},
			{[]string{"chassis", "inventory"}, `"ChassisType"`},
			{[]string{"chassis", "--id", "1U", "inventory"}, `"1U"`},
			{[]string{"chassis", "fans"}, "BaseBoard System Fan"},
			{[]string{"chassis", "power"}, "{"},
			{[]string{"chassis", "thermals"}, "CPU1 Temp"},
			{[]string{"chassis", "psu"}, `"Name"`},
			{[]string{"chassis", "health"}, "chassis_uri"},
			{[]string{"chassis", "sensors"}, "CPUFan1"},
			{[]string{"chassis", "power-subsystem"}, "CapacityWatts"},
			{[]string{"chassis", "thermal-subsystem"}, "ThermalMetrics"},
			{[]string{"chassis", "metrics"}, "PowerWatts"},
		} {
			if out := e.ok(tc.args...); !strings.Contains(out, tc.want) {
				t.Errorf("%v: %q lacks %q", tc.args, out, tc.want)
			}
		}
		if out := e.ok("-o", "table", "chassis", "thermals"); !strings.Contains(out, "CPU1 Temp") || !strings.Contains(out, "Name") || strings.Contains(out, "map[") {
			t.Errorf("table: %q", out)
		}
		for _, args := range [][]string{{"chassis", "hpe", "thermal-config"}, {"chassis", "hpe", "fan-percent-min"}, {"chassis", "--id", "nope", "sensors"}} {
			if _, _, err := e.run(args...); err == nil {
				t.Errorf("%v: want an error", args)
			}
		}
	})
}

// -o table shows a list as a table, not as a Go slice.
func TestTableOfAList(t *testing.T) {
	e := newEnv(t)
	out := e.ok("-o", "table", "systems", "list")
	if strings.Contains(out, "[/redfish") || !strings.Contains(out, "VALUE") || !strings.Contains(out, sysPath) {
		t.Errorf("systems list as a table: %q", out)
	}
	out = e.ok("-o", "table", "systems", "inventory", "cpu")
	if strings.Contains(out, "map[") || !strings.Contains(out, "Manufacturer") {
		t.Errorf("cpu table: %q", out)
	}
}

// Help text names flags that exist.
func TestMockHelpNamesItsFlags(t *testing.T) {
	out, _, err := run(t, "mock", "serve", "--help")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "--mock-password-file") {
		t.Errorf("the help does not name --mock-password-file: %q", out)
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "--password-file") && !strings.Contains(line, "Global") && !strings.Contains(line, "file holding the password") {
			t.Errorf("the mock's help names --password-file: %q", line)
		}
	}
}
