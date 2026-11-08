package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/go-redfish/redfish/mockbmc"
)

func TestSecurityReadCommands(t *testing.T) {
	e := newEnvTree(t, mockbmc.TreeRackmount)
	out := e.ok("security", "integrity")
	if l := jsonList(t, out); len(l) != 4 {
		t.Fatalf("integrity: %d items: %s", len(l), out)
	}
	out = e.ok("security", "integrity-show", "SS-SPDM-0")
	var ci map[string]any
	if err := json.Unmarshal([]byte(out), &ci); err != nil || ci["type"] != "SPDM" || ci["spdm_get_signed_measurements"] != true {
		t.Fatalf("integrity-show: %v %s", err, out)
	}
	if !strings.Contains(out, `"hex": "e301`) || !strings.Contains(out, `"base64": "4wEF1nnub2oz`) {
		t.Errorf("a measurement is not given as Base64 and hex: %s", out)
	}
	if strings.Contains(out, `"certificates"`) {
		t.Error("certificates without --certs")
	}
	out = e.ok("security", "integrity-show", "SS-SPDM-0", "--certs")
	if !strings.Contains(out, `"certificates"`) || !strings.Contains(out, "DeviceCert") {
		t.Errorf("--certs: %s", out)
	}
	out = e.ok("security", "trusted")
	if l := jsonList(t, out); len(l) != 3 {
		t.Fatalf("trusted: %d items", len(l))
	}
	out = e.ok("security", "trusted", "TPM", "--certs", "--chassis", "1U")
	if !strings.Contains(out, `"tpm_capabilities_vendor_id": "SIM0"`) || !strings.Contains(out, `"certificates"`) {
		t.Errorf("trusted TPM: %s", out)
	}
	if _, _, err := e.run("security", "trusted", "TPM", "--chassis", "Nope"); err == nil {
		t.Error("an unknown chassis")
	}
	out = e.ok("security", "policy")
	if !strings.Contains(out, `"verify_certificate": true`) || !strings.Contains(out, `"spdm.trusted"`) || !strings.Contains(out, "contoso") {
		t.Errorf("policy: %s", out)
	}
	if out = e.ok("security", "policy", "--no-certs"); strings.Contains(out, `"certificates"`) {
		t.Error("--no-certs reads certificates")
	}
	out = e.ok("security", "key-policies")
	if l := jsonList(t, out); len(l) != 1 {
		t.Fatalf("key-policies: %d", len(l))
	}
	if out = e.ok("security", "key-policies", "0"); !strings.Contains(out, "FFDHE2048") {
		t.Errorf("key-policies 0: %s", out)
	}
}

func TestSecurityKeysNeverShowTheKey(t *testing.T) {
	e := newEnvTree(t, mockbmc.TreeRackmount)
	for _, args := range [][]string{{"security", "keys"}, {"security", "keys", "0"}, {"security", "keys", "-o", "table"}, {"security", "keys", "0", "-o", "table"}} {
		out, errOut, err := e.run(args...)
		if err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		if strings.Contains(out+errOut, "DHHC-1:") || strings.Contains(out+errOut, "ia6zGodOr4") {
			t.Errorf("%v shows the key: %s %s", args, out, errOut)
		}
		if !strings.Contains(errOut, "not shown or kept") {
			t.Errorf("%v: no note on stderr: %q", args, errOut)
		}
	}
	out := e.ok("security", "keys", "0")
	if !strings.Contains(out, `"key_present": true`) || !strings.Contains(out, "target-subsystem-0001") {
		t.Errorf("keys 0: %s", out)
	}
	if _, _, err := e.run("security", "keys", "99"); err == nil {
		t.Error("an unknown secret")
	}
}

func TestSecurityMeasurementsCommand(t *testing.T) {
	e := newEnvTree(t, mockbmc.TreeRackmount)
	out, errOut, err := e.run("security", "measurements", "SS-SPDM-0", "--index", "1,3", "--nonce", strings.Repeat("0f", 32), "--slot", "1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"signature_verified": false`) || !strings.Contains(out, "not verified") || !strings.Contains(out, `"nonce_in_blob": true`) ||
		!strings.Contains(errOut, "signature is not verified") {
		t.Errorf("measurements: %s / %s", out, errOut)
	}
	out, _, err = e.run("security", "measurements", "SS-SPDM-0", "-o", "table")
	if err != nil || !strings.Contains(out, "signature_verified") || !strings.Contains(out, "false") {
		t.Errorf("table: %v %s", err, out)
	}
	for _, args := range [][]string{{"--slot", "3"}, {"--index", "300"}, {"--nonce", "zz"}, {"--index", "2,2"}} {
		if _, _, err := e.run(append([]string{"security", "measurements", "SS-SPDM-0"}, args...)...); err == nil {
			t.Errorf("%v accepted", args)
		}
	}
	if _, _, err := e.run("security", "measurements", "TPM-0"); err == nil {
		t.Error("measurements of a TPM resource")
	}
}

func TestSecurityTables(t *testing.T) {
	e := newEnvTree(t, mockbmc.TreeRackmount)
	out := e.ok("security", "integrity", "-o", "table")
	if !strings.Contains(out, "SS-SPDM-0") || !strings.Contains(out, "MEASUREMENTS") && !strings.Contains(strings.ToLower(out), "measurements") {
		t.Errorf("integrity table: %s", out)
	}
	out = e.ok("security", "integrity-show", "SS-SPDM-0", "--certs", "-o", "table")
	for _, want := range []string{"INDEX/PCR", "ImmutableROM", "TPM_ALG_SHA_512", "e30105d679ee", "summary (hex)", "DeviceCert"} {
		if !strings.Contains(out, want) {
			t.Errorf("integrity-show table lacks %q:\n%s", want, out)
		}
	}
	out = e.ok("security", "integrity-show", "TPM-0", "-o", "table")
	if !strings.Contains(out, "PCR1") || !strings.Contains(out, "TPM_ALG_SHA256") {
		t.Errorf("TPM table:\n%s", out)
	}
	out = e.ok("security", "policy", "-o", "table")
	for _, want := range []string{"spdm.allowed.base_asym", "tls_server.denied.versions", "spdm.trusted", "SHA-256"} {
		if !strings.Contains(out, want) {
			t.Errorf("policy table lacks %q:\n%s", want, out)
		}
	}
	for _, args := range [][]string{{"trusted"}, {"key-policies"}} {
		if _, _, err := e.run(append([]string{"security"}, append(args, "-o", "table")...)...); err != nil {
			t.Errorf("%v: %v", args, err)
		}
	}
}

func TestSecurityUnsupportedOnTheEmulatorTree(t *testing.T) {
	e := newEnvTree(t, mockbmc.TreeEmulator)
	for _, args := range [][]string{{"integrity"}, {"integrity-show", "x"}, {"measurements", "x"}, {"trusted"}, {"policy"}, {"keys"}, {"key-policies"}} {
		_, _, err := e.run(append([]string{"security"}, args...)...)
		if err == nil || !strings.Contains(err.Error(), "not supported") {
			t.Errorf("%v: err = %v, want a not-supported error", args, err)
		}
	}
}
