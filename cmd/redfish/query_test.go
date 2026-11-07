package main

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/go-redfish/redfish"
	"github.com/go-redfish/redfish/mockbmc"
)

const sensorsPath = "/redfish/v1/Chassis/1U/Sensors"

func (e *env) json(args ...string) map[string]any {
	e.t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(e.ok(args...)), &m); err != nil {
		e.t.Fatalf("%v: %v", args, err)
	}
	return m
}

func TestRawGetQueryParameters(t *testing.T) {
	e := newEnv(t)
	all := e.json("raw", "get", sensorsPath)["Members"].([]any)

	m := e.json("raw", "get", sensorsPath, "--top", "3", "--skip", "2")
	if got := m["Members"].([]any); len(got) != 3 || got[0].(map[string]any)["@odata.id"] != all[2].(map[string]any)["@odata.id"] || int(m["Members@odata.count"].(float64)) != len(all) {
		t.Errorf("--top 3 --skip 2: %v", m)
	}
	if m := e.json("raw", "get", sensorsPath, "--top", "0"); len(m["Members"].([]any)) != 0 {
		t.Errorf("--top 0: %v", m)
	}
	m = e.json("raw", "get", sensorsPath, "--expand", "1")
	if mem := m["Members"].([]any)[0].(map[string]any); mem["Reading"] == nil {
		t.Errorf("--expand 1: %v", mem)
	}
	m = e.json("raw", "get", "/redfish/v1/Systems", "--select", "Members/UUID", "--expand", "1")
	if mem := m["Members"].([]any)[0].(map[string]any); mem["UUID"] == nil || mem["Name"] != nil {
		t.Errorf("--select Members/UUID --expand 1: %v", m)
	}
	m = e.json("raw", "get", "/redfish/v1/Systems/437XR1138R2", "--select", "Name,Status/State")
	if m["Name"] == nil || m["Status"].(map[string]any)["State"] == nil || m["PowerState"] != nil {
		t.Errorf("--select: %v", m)
	}
	m = e.json("raw", "get", "/redfish/v1/Chassis/1U", "--expand", "2", "--expand-kind", "all")
	if _, ok := m["Links"].(map[string]any)["ComputerSystems"].([]any)[0].(map[string]any)["Processors"]; !ok {
		t.Errorf("--expand 2 --expand-kind all: %v", m["Links"])
	}
	if m := e.json("raw", "get", "/redfish/v1/Systems", "--only"); m["Id"] != "437XR1138R2" {
		t.Errorf("--only: %v", m["@odata.id"])
	}
	m = e.json("raw", "get", sensorsPath, "--filter", "ReadingUnits eq '%' and Status/State eq 'Enabled'")
	if n := len(m["Members"].([]any)); n == 0 || n >= len(all) {
		t.Errorf("--filter: %d of %d", n, len(all))
	}
	e.json("raw", "get", sensorsPath, "--excerpt")
}

func TestRawGetQueryRefusals(t *testing.T) {
	// a service that does not advertise the parameters: each is refused, none is sent
	for _, o := range []mockbmc.Options{{Tree: mockbmc.TreeEmulator}, {NoQueries: true}} {
		e := newEnvOpts(t, o)
		for _, args := range [][]string{{"--expand", "1"}, {"--expand", "2", "--expand-kind", "all"}, {"--select", "Id"}, {"--top", "1"}, {"--skip", "1"}, {"--only"}, {"--excerpt"}, {"--filter", "Id eq 'a'"}} {
			_, _, err := e.run(append([]string{"raw", "get", "/redfish/v1/Systems"}, args...)...)
			if !errors.Is(err, redfish.ErrUnsupported) || !strings.Contains(err.Error(), "ProtocolFeaturesSupported.") {
				t.Errorf("%v: %v", args, err)
			}
		}
		for i, q := range e.bmc.Queries() {
			if q != "" {
				t.Errorf("a query was sent: %s ?%s", e.bmc.Log()[i], q)
			}
		}
	}
	// values the grammar forbids, on a service that advertises everything
	e := newEnv(t)
	for _, args := range [][]string{{"--filter", "Id eq"}, {"--filter", "Id eq 'a'&$expand=*"}, {"--select", "Name&x"}, {"--top", "-1"},
		{"--only", "--top", "1"}, {"--expand", "70"}, {"--expand", "7"}, {"--expand-kind", "bogus"}} {
		_, _, err := e.run(append([]string{"raw", "get", "/redfish/v1/Systems"}, args...)...)
		if err == nil {
			t.Errorf("%v accepted", args)
		}
	}
	if _, _, err := e.run("raw", "get", "/redfish/v1/Systems?only", "--top", "1"); !errors.Is(err, redfish.ErrQuery) {
		t.Errorf("a query in the path and flags: %v", err)
	}
	// without a query flag the path is sent as it is
	e.ok("raw", "get", "/redfish/v1/Systems?$top=1")
}

// The converted reads use $expand by default when it is advertised, and
// --no-expand forces the per-member reads: same answer, more requests.
func TestNoExpandFlag(t *testing.T) {
	e := newEnv(t)
	count := func(args ...string) (string, int) {
		mark := len(e.bmc.Log())
		out := e.ok(args...)
		return out, len(e.bmc.Log()) - mark
	}
	e.ok("raw", "get", "/redfish/v1/") // nothing cached across processes: each run reads the root itself
	on, nOn := count("systems", "inventory", "cpu")
	off, nOff := count("--no-expand", "systems", "inventory", "cpu")
	if on != off || nOn >= nOff {
		t.Errorf("cpu: %d requests with $expand, %d with --no-expand; same answer: %v", nOn, nOff, on == off)
	}
	on, nOn = count("chassis", "sensors")
	off, nOff = count("--no-expand", "chassis", "sensors")
	if on != off || nOn >= nOff {
		t.Errorf("sensors: %d requests with $expand, %d with --no-expand; same answer: %v", nOn, nOff, on == off)
	}
	queries := 0
	for _, q := range e.bmc.Queries() {
		if strings.Contains(q, "$expand") {
			queries++
		}
	}
	if queries == 0 {
		t.Error("$expand was never sent")
	}
	// on a service that does not advertise it, the flag changes nothing
	e2 := newEnvTree(t, mockbmc.TreeEmulator)
	n := func(args ...string) int {
		mark := len(e2.bmc.Log())
		e2.ok(args...)
		return len(e2.bmc.Log()) - mark
	}
	if a, b := n("systems", "inventory", "cpu"), n("--no-expand", "systems", "inventory", "cpu"); a != b {
		t.Errorf("emulator: %d and %d requests", a, b)
	}
}
