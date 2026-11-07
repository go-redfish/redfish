package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-redfish/redfish"
	"github.com/go-redfish/redfish/mockbmc"
)

const (
	cmdHTTPS     = "/redfish/v1/Managers/BMC/NetworkProtocol/HTTPS/Certificates"
	cmdHTTPSCert = cmdHTTPS + "/1"
	challenge    = "Zq9-challenge-secret-42"
)

func pemOf(typ string, der []byte) string {
	return string(pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: der}))
}

// signed signs the CSR PEM with a new CA.
func signed(t *testing.T, csr string) string {
	t.Helper()
	b, _ := pem.Decode([]byte(csr))
	if b == nil {
		t.Fatalf("not a PEM CSR: %q", csr)
	}
	req, err := x509.ParseCertificateRequest(b.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	caTpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Test CA"}, NotBefore: time.Now().Add(-time.Hour),
		NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	caDER, _ := x509.CreateCertificate(rand.Reader, caTpl, caTpl, caKey.Public(), caKey)
	ca, _ := x509.ParseCertificate(caDER)
	tpl := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: req.Subject, DNSNames: req.DNSNames,
		NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature}
	der, err := x509.CreateCertificate(rand.Reader, tpl, ca, req.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	return pemOf("CERTIFICATE", der)
}

func outsideCert(t *testing.T, cn string) (cert, key string) {
	t.Helper()
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tpl := &x509.Certificate{SerialNumber: big.NewInt(5), Subject: pkix.Name{CommonName: cn}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, _ := x509.CreateCertificate(rand.Reader, tpl, tpl, k.Public(), k)
	kd, _ := x509.MarshalPKCS8PrivateKey(k)
	return pemOf("CERTIFICATE", der), pemOf("PRIVATE KEY", kd)
}

func TestCertsReads(t *testing.T) {
	e := newEnv(t)
	out, _, err := e.run("certs", "status")
	st := jsonOut(t, out)
	if err != nil || st["generate_csr"] != true || st["replace_certificate"] != true || st["locations_uri"] != "/redfish/v1/CertificateService/CertificateLocations" {
		t.Fatalf("status: %v\n%s", err, out)
	}
	out, _, err = e.run("certs", "list")
	var rows []map[string]any
	if err != nil || json.Unmarshal([]byte(out), &rows) != nil || len(rows) != 3 {
		t.Fatalf("list: %v\n%s", err, out)
	}
	out, _, err = e.run("certs", "list", "-o", "table")
	if err != nil || !strings.Contains(out, "subject") || !strings.Contains(out, cmdHTTPSCert) {
		t.Fatalf("list table: %v\n%s", err, out)
	}
	out, _, err = e.run("certs", "list", "--uris")
	var uris []string
	if err != nil || json.Unmarshal([]byte(out), &uris) != nil || len(uris) != 3 {
		t.Fatalf("list --uris: %v\n%s", err, out)
	}
	out, _, err = e.run("certs", "show", cmdHTTPSCert)
	doc := jsonOut(t, out)
	x, _ := doc["x509"].(map[string]any)
	if err != nil || x == nil || !strings.Contains(x["subject"].(string), "CN=manager.contoso.org") || x["is_ca"] != false || x["public_key_algorithm"] != "ECDSA" ||
		x["key_bits"] != float64(256) || x["sha256_fingerprint"] == "" || doc["rekey"] != true {
		t.Fatalf("show: %v\n%s", err, out)
	}
	out, _, err = e.run("certs", "show", cmdHTTPSCert, "--pem")
	if err != nil || !strings.HasPrefix(out, "-----BEGIN CERTIFICATE-----\n") || strings.Contains(out, "PRIVATE") {
		t.Fatalf("show --pem: %v\n%s", err, out)
	}
	for name, args := range map[string][]string{"ambiguous Id": {"certs", "show", "1"}, "foreign": {"certs", "show", "http://evil/x"},
		"traversal": {"certs", "show", "../x"}, "two": {"certs", "show", "a", "b"}, "not a certificate": {"certs", "show", "/redfish/v1/Systems/437XR1138R2"}} {
		if _, _, err := e.run(args...); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// The emulator tree has no CertificateService.
	e2 := newEnvTree(t, mockbmc.TreeEmulator)
	for _, a := range [][]string{{"certs", "status"}, {"certs", "list"}, {"certs", "csr", "--collection", cmdHTTPS, "--common-name", "x"}} {
		if _, _, err := e2.run(a...); err == nil || !strings.Contains(err.Error(), "not supported") {
			t.Errorf("emulator %v: %v", a, err)
		}
	}
}

func TestCertsCSRInstallDelete(t *testing.T) {
	e := newEnv(t)
	cf := e.writePW("challenge", challenge+"\n", 0o600)
	csrOut := filepath.Join(e.cfgDir, "bmc.csr")
	out, errOut, err := e.run("certs", "csr", "--collection", cmdHTTPS, "--common-name", "bmc9.example.org", "--alt-name", "bmc9", "--country", "FR",
		"--organization", "Example", "--algorithm", "TPM_ALG_ECDSA", "--curve", "TPM_ECC_NIST_P384", "--challenge-password-file", cf)
	if err != nil || !strings.HasPrefix(out, "-----BEGIN CERTIFICATE REQUEST-----\n") || !strings.Contains(errOut, "signature valid: true") || !strings.Contains(errOut, "CN=bmc9.example.org") {
		t.Fatalf("csr: %v\n%s\n%s", err, out, errOut)
	}
	if strings.Contains(out+errOut, challenge) {
		t.Fatal("the challenge password was printed")
	}
	if got := e.bmc.CSRRequests(); len(got) != 1 || got[0]["ChallengePassword"] != challenge {
		t.Fatalf("the service received %v", got)
	}
	// --out writes a new file and never overwrites one.
	out2, errOut2, err := e.run("certs", "csr", "--collection", cmdHTTPS, "--common-name", "bmc9.example.org", "--out", csrOut)
	if err != nil || out2 != "" || !strings.Contains(errOut2, "wrote") {
		t.Fatalf("csr --out: %v %q %q", err, out2, errOut2)
	}
	b, err := os.ReadFile(csrOut)
	if err != nil || !strings.HasPrefix(string(b), "-----BEGIN CERTIFICATE REQUEST-----") {
		t.Fatalf("the CSR file: %v %q", err, b)
	}
	if _, _, err := e.run("certs", "csr", "--collection", cmdHTTPS, "--common-name", "x", "--out", csrOut); err == nil {
		t.Error("an existing --out file was overwritten")
	}
	// Refusals: a challenge file others can read, no collection, a bad country.
	open := e.writePW("open", challenge+"\n", 0o644)
	for name, args := range map[string][]string{
		"open challenge file": {"certs", "csr", "--collection", cmdHTTPS, "--challenge-password-file", open},
		"no collection":       {"certs", "csr", "--common-name", "x"},
		"bad country":         {"certs", "csr", "--collection", cmdHTTPS, "--country", "FRA"},
		"unlisted algorithm":  {"certs", "csr", "--collection", cmdHTTPS, "--algorithm", "NOPE"},
	} {
		if _, _, err := e.run(args...); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// Install the signed certificate (the service holds the key), then delete it.
	e.run("certs", "csr", "--collection", cmdHTTPS, "--common-name", "bmc7.example.org", "--out", filepath.Join(e.cfgDir, "bmc7.csr"))
	csr, _ := os.ReadFile(filepath.Join(e.cfgDir, "bmc7.csr"))
	cert := e.writePW("bmc7.pem", signed(t, string(csr)), 0o644)
	out, _, err = e.run("certs", "install", "--collection", cmdHTTPS, "--cert-file", cert)
	doc := jsonOut(t, out)
	c, _ := doc["certificate"].(map[string]any)
	if err != nil || doc["changed"] != true || c == nil || c["uri"] != cmdHTTPS+"/2" {
		t.Fatalf("install: %v\n%s", err, out)
	}
	out, _, err = e.run("certs", "delete", cmdHTTPS+"/2")
	if err != nil || jsonOut(t, out)["changed"] != true {
		t.Fatalf("delete: %v\n%s", err, out)
	}
	out, _, err = e.run("certs", "delete", cmdHTTPS+"/2")
	if err != nil || jsonOut(t, out)["changed"] != false {
		t.Fatalf("second delete: %v\n%s", err, out)
	}
	if _, _, err := e.run("certs", "install", "--collection", cmdHTTPS, "--cert-file", filepath.Join(e.cfgDir, "missing")); err == nil {
		t.Error("a missing certificate file")
	}
	if _, _, err := e.run("certs", "install", "--collection", cmdHTTPS); err == nil {
		t.Error("install without --cert-file")
	}
}

func TestCertsReplaceWithAKeyNeverShowsIt(t *testing.T) {
	e := newEnv(t)
	cs, ks := outsideCert(t, "own.example.org")
	cert := e.writePW("own.pem", cs, 0o644)
	keyFile := e.writePW("own.key", ks, 0o600)
	// The service has no key for it: refused.
	if _, _, err := e.run("certs", "replace", cmdHTTPSCert, "--cert-file", cert); err == nil {
		t.Fatal("replaced without a key")
	}
	check := func(what, out, errOut string) {
		t.Helper()
		for _, l := range strings.Split(ks, "\n") {
			if len(l) >= 16 && !strings.HasPrefix(l, "-----") && strings.Contains(out+errOut, l) {
				t.Fatalf("%s: the key was printed", what)
			}
		}
	}
	// A key file others can read, two sources, a mismatched key, and a huge key.
	open := e.writePW("open.key", ks, 0o644)
	_, other := outsideCert(t, "other.example.org")
	for name, c := range map[string]struct {
		args  []string
		stdin string
	}{
		"open key file":   {[]string{"--key-file", open}, ""},
		"two sources":     {[]string{"--key-file", keyFile, "--key-stdin"}, ks},
		"mismatched key":  {[]string{"--key-stdin"}, other},
		"huge key":        {[]string{"--key-stdin"}, strings.Repeat("A", redfish.MaxKeyPEM+10)},
		"empty key":       {[]string{"--key-stdin"}, "\n"},
		"not a key":       {[]string{"--key-stdin"}, "hello"},
		"missing file":    {[]string{"--key-file", filepath.Join(e.cfgDir, "none")}, ""},
		"password on PEM": {[]string{"--key-file", keyFile, "--p12-password-file", e.writePW("p12", "x\n", 0o600)}, ""},
	} {
		out, errOut, err := e.runIn(c.stdin, append([]string{"certs", "replace", cmdHTTPSCert, "--cert-file", cert}, c.args...)...)
		if err == nil {
			t.Errorf("%s: accepted", name)
		}
		check(name, out+err2s(err), errOut)
	}
	// The key from a file, then from stdin.
	out, errOut, err := e.run("certs", "replace", cmdHTTPSCert, "--cert-file", cert, "--key-file", keyFile)
	if err != nil || jsonOut(t, out)["uri"] != cmdHTTPS+"/2" {
		t.Fatalf("replace with a key file: %v\n%s", err, out)
	}
	check("file", out, errOut)
	out, errOut, err = e.runIn(ks, "certs", "replace", cmdHTTPS+"/2", "--cert-file", cert, "--key-stdin")
	if err != nil || jsonOut(t, out)["uri"] != cmdHTTPS+"/3" {
		t.Fatalf("replace with a key on stdin: %v\n%s", err, out)
	}
	check("stdin", out, errOut)
	out, _, err = e.run("certs", "show", cmdHTTPS+"/3")
	if err != nil || strings.Contains(out, "PRIVATE KEY") || !strings.Contains(out, "CN=own.example.org") {
		t.Fatalf("show after replace: %v\n%s", err, out)
	}
	// A PEMchain is refused before the POST: the service lists PEM.
	if _, _, err := e.run("certs", "replace", cmdHTTPS+"/3", "--cert-file", e.writePW("two.pem", cs+cs, 0o644), "--type", "PEMchain"); err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Errorf("PEMchain replace: %v", err)
	}
}

func err2s(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func TestCertsRekeyAndRenew(t *testing.T) {
	e := newEnv(t)
	out, errOut, err := e.run("certs", "renew", cmdHTTPSCert)
	if err != nil || !strings.HasPrefix(out, "-----BEGIN CERTIFICATE REQUEST-----") || !strings.Contains(errOut, "replaces "+cmdHTTPSCert) || !strings.Contains(errOut, "CN=manager.contoso.org") {
		t.Fatalf("renew: %v\n%s\n%s", err, out, errOut)
	}
	outFile := filepath.Join(e.cfgDir, "rekey.csr")
	_, errOut, err = e.run("certs", "rekey", cmdHTTPSCert, "--algorithm", "TPM_ALG_ECDSA", "--curve", "TPM_ECC_NIST_P384", "--out", outFile)
	if err != nil || !strings.Contains(errOut, "P-384") {
		t.Fatalf("rekey: %v\n%s", err, errOut)
	}
	if b, err := os.ReadFile(outFile); err != nil || !strings.Contains(string(b), "CERTIFICATE REQUEST") {
		t.Fatalf("rekey --out: %v", err)
	}
	for name, args := range map[string][]string{"unlisted algorithm": {"certs", "rekey", cmdHTTPSCert, "--algorithm", "NOPE"},
		"no action": {"certs", "renew", "/redfish/v1/Systems/437XR1138R2/Certificates/contoso-root"}, "none": {"certs", "renew"}} {
		if _, _, err := e.run(args...); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func FuzzReadKey(f *testing.F) {
	f.Add("-----BEGIN PRIVATE KEY-----\nAAAAAAAAAAAAAAAAAAAA\n-----END PRIVATE KEY-----\n")
	f.Add("")
	f.Add("\n\n  ")
	f.Add(strings.Repeat("A", redfish.MaxKeyPEM+1))
	f.Fuzz(func(t *testing.T, s string) {
		k, err := readKey(strings.NewReader(s))
		if err != nil {
			if len(s) >= 16 && strings.Contains(err.Error(), s) {
				t.Fatalf("the error carries the input: %v", err)
			}
			return
		}
		if len(k) > redfish.MaxKeyPEM || strings.TrimSpace(k.Reveal()) == "" || k.Reveal() != s {
			t.Fatalf("accepted %d bytes", len(k))
		}
	})
}
