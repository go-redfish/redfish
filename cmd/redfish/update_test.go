package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-redfish/redfish/mockbmc"
)

const cliSecret = "Zx9-image-server-secret"

func jsonOut(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("not a JSON object: %v\n%s", err, s)
	}
	return m
}

func TestUpdateCommands(t *testing.T) {
	e := newEnv(t)
	dir := t.TempDir()
	pwf := filepath.Join(dir, "imgpw")
	if err := os.WriteFile(pwf, []byte(cliSecret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	out, _, err := e.run("update", "capabilities")
	if err != nil || jsonOut(t, out)["multipart_supported"] != true {
		t.Fatalf("capabilities: %v\n%s", err, out)
	}
	out, _, err = e.run("update", "inventory")
	var items []map[string]any
	if err != nil || json.Unmarshal([]byte(out), &items) != nil || len(items) != 3 {
		t.Fatalf("inventory: %v\n%s", err, out)
	}
	out, _, err = e.run("update", "inventory", "--software", "-o", "table")
	if err != nil || !strings.Contains(out, "Contoso Drivers") {
		t.Fatalf("inventory --software: %v\n%s", err, out)
	}

	// Not blocking by default: the monitor is returned, not awaited.
	out, errOut, err := e.run("update", "simple", "--image", "https://images.example/fw.bin", "--protocol", "HTTPS",
		"--image-username", "fetcher", "--image-password-file", pwf, "--apply-time", "OnReset")
	if err != nil {
		t.Fatal(err)
	}
	st, _ := jsonOut(t, out)["update_status"].(map[string]any)
	handle, _ := st["handle"].(string)
	if handle == "" || st["status"] != "Running" || !strings.Contains(errOut, "monitor: "+handle) {
		t.Fatalf("simple: %s\nstderr: %s", out, errOut)
	}
	if strings.Contains(out+errOut, cliSecret) {
		t.Fatal("the password was printed")
	}
	if sent := e.bmc.SimpleUpdates(); len(sent) != 1 || sent[0]["Password"] != cliSecret || sent[0]["Username"] != "fetcher" {
		t.Fatalf("the service received %v", sent)
	}

	// status, and status --watch with progress on stderr.
	out, _, err = e.run("update", "status", handle)
	if err != nil || jsonOut(t, out)["status"] != "Running" {
		t.Fatalf("status: %v\n%s", err, out)
	}
	out, errOut, err = e.run("update", "status", handle, "--watch", "--poll", "100ms")
	if err != nil || jsonOut(t, out)["status"] != "Completed" {
		t.Fatalf("status --watch: %v\n%s", err, out)
	}
	_ = errOut

	// simple --watch, the password on stdin.
	var o, eb bytes.Buffer
	cmd := RootCmd()
	cmd.SetOut(&o)
	cmd.SetErr(&eb)
	cmd.SetIn(strings.NewReader(cliSecret + "\n"))
	cmd.SetArgs(append(append([]string{}, e.baseArgs...), "update", "simple", "--image", "https://images.example/fw.bin", "--image-password-stdin", "--watch", "--poll", "100ms"))
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if got := jsonOut(t, o.String())["update_status"].(map[string]any)["status"]; got != "Completed" || !strings.Contains(eb.String(), "task: Running") {
		t.Fatalf("simple --watch: %s\nstderr: %s", o.String(), eb.String())
	}
	if strings.Contains(o.String()+eb.String(), cliSecret) {
		t.Fatal("the password was printed")
	}

	// Refusals.
	for name, args := range map[string][]string{
		"both sources":        {"update", "simple", "--image", "https://h/x", "--image-password-file", pwf, "--image-password-stdin"},
		"password in the URI": {"update", "simple", "--image", "https://u:" + cliSecret + "@h/x"},
		"bad protocol":        {"update", "simple", "--image", "https://h/x", "--protocol", "NFS", "--image-password-file", pwf},
		"loose file":          {"update", "simple", "--image", "https://h/x", "--image-password-file", e.ca},
		"no image":            {"update", "simple"},
		"status no handle":    {"update", "status"},
		"status foreign":      {"update", "status", "/etc/passwd"},
		"push no file":        {"update", "push"},
	} {
		_, errOut, err := e.run(args...)
		if err == nil {
			t.Errorf("%s: accepted", name)
		} else if strings.Contains(err.Error()+errOut, cliSecret) {
			t.Errorf("%s: the password is in the error: %v", name, err)
		}
	}
}

func TestUpdatePushCommand(t *testing.T) {
	e := newEnv(t)
	img := filepath.Join(t.TempDir(), "fw.bin")
	if err := os.WriteFile(img, bytes.Repeat([]byte("firmware!"), 5000), 0o600); err != nil {
		t.Fatal(err)
	}
	out, errOut, err := e.run("update", "push", "--file", img, "--target", "/redfish/v1/Managers/BMC", "--apply-time", "OnReset",
		"--oem", `{"Contoso":{"Force":true}}`, "--oem-header", "OemPart", "--oem-params", "a=b", "--watch", "--poll", "100ms")
	if err != nil {
		t.Fatal(err)
	}
	if jsonOut(t, out)["update_status"].(map[string]any)["status"] != "Completed" || !strings.Contains(errOut, "task: Running") {
		t.Fatalf("push: %s\n%s", out, errOut)
	}
	ups := e.bmc.Uploads()
	if len(ups) != 1 || ups[0].Size != 45000 || ups[0].Filename != "fw.bin" || ups[0].Extra["OemPart"] != "a=b" {
		t.Fatalf("uploads = %+v", ups)
	}
	// Refused before anything is sent.
	n := len(e.bmc.Log())
	if _, _, err := e.run("update", "push", "--file", img, "--max-size", "100"); err == nil || !strings.Contains(err.Error(), "larger than the limit") {
		t.Fatalf("over --max-size: %v", err)
	}
	if len(e.bmc.Log()) != n+0 && len(e.bmc.Log()) != n {
		t.Fatalf("requests after a refusal: %v", e.bmc.Log()[n:])
	}
	if _, _, err := e.run("update", "push", "--file", img, "--oem", "[1]"); err == nil {
		t.Fatal("a non-object --oem was accepted")
	}
	// Not blocking by default.
	out, _, err = e.run("update", "push", "--file", img)
	if err != nil || jsonOut(t, out)["update_status"].(map[string]any)["handle"] == "" {
		t.Fatalf("push: %v\n%s", err, out)
	}
}

func TestUpdateOperationsCommand(t *testing.T) {
	e := newEnvOpts(t, mockbmc.Options{ResetRequired: true})
	out, _, err := e.run("update", "simple", "--image", "https://h/x")
	if err != nil {
		t.Fatal(err)
	}
	handle := jsonOut(t, out)["update_status"].(map[string]any)["handle"].(string)
	out, _, err = e.run("update", "operations", handle)
	if err != nil || jsonOut(t, out)["changed"] != true || len(e.bmc.Resets()) != 1 {
		t.Fatalf("operations: %v\n%s %v", err, out, e.bmc.Resets())
	}
	e2 := newEnv(t)
	out, _, _ = e2.run("update", "simple", "--image", "https://h/x")
	handle = jsonOut(t, out)["update_status"].(map[string]any)["handle"].(string)
	out, _, err = e2.run("update", "operations", handle)
	if err != nil || jsonOut(t, out)["changed"] != false {
		t.Fatalf("operations without a request: %v\n%s", err, out)
	}
	// The emulator tree has no UpdateService.
	e3 := newEnvTree(t, mockbmc.TreeEmulator)
	if _, _, err := e3.run("update", "capabilities"); err == nil {
		t.Fatal("the emulator has no UpdateService")
	}
}
