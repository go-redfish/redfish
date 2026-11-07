package redfish_test

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/go-redfish/redfish"
	"github.com/go-redfish/redfish/mockbmc"
)

const (
	httpsColl = "/redfish/v1/Managers/BMC/NetworkProtocol/HTTPS/Certificates"
	httpsCert = httpsColl + "/1"
)

// testCA makes a certificate authority.
func testCA(t *testing.T) (*x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Test CA", Organization: []string{"Test"}},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour), IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, key.Public(), key)
	if err != nil {
		t.Fatal(err)
	}
	ca, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return ca, key
}

func certPEM(der []byte) string {
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

// signCSR signs a request the way a CA does.
func signCSR(t *testing.T, csrPEM string, ca *x509.Certificate, caKey crypto.Signer) (string, *x509.Certificate) {
	t.Helper()
	b, _ := pem.Decode([]byte(csrPEM))
	if b == nil {
		t.Fatal("the CSR is not PEM")
	}
	csr, err := x509.ParseCertificateRequest(b.Bytes)
	if err != nil || csr.CheckSignature() != nil {
		t.Fatalf("the CSR does not verify: %v", err)
	}
	tpl := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: csr.Subject, DNSNames: csr.DNSNames,
		NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(48 * time.Hour), KeyUsage: x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tpl, ca, csr.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := x509.ParseCertificate(der)
	return certPEM(der), c
}

// selfSigned makes a certificate with a key the service has never seen, and the
// key as PKCS#8 PEM.
func selfSigned(t testing.TB, cn string) (cert, key string) {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{SerialNumber: big.NewInt(77), Subject: pkix.Name{CommonName: cn}, DNSNames: []string{cn},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour), KeyUsage: x509.KeyUsageDigitalSignature}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, k.Public(), k)
	if err != nil {
		t.Fatal(err)
	}
	kd, err := x509.MarshalPKCS8PrivateKey(k)
	if err != nil {
		t.Fatal(err)
	}
	return certPEM(der), string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: kd}))
}

func keyLines(key string) []string {
	var out []string
	for _, l := range strings.Split(key, "\n") {
		if len(l) >= 16 && !strings.HasPrefix(l, "-----") {
			out = append(out, l)
		}
	}
	return out
}

func TestCertificateServiceRead(t *testing.T) {
	_, srv := newMock(t, mockbmc.Options{})
	c := newClient(t, srv, redfish.Config{})
	ctx := context.Background()
	svc, err := c.CertificateService(ctx)
	if err != nil || svc.URI != "/redfish/v1/CertificateService" || svc.LocationsURI != "/redfish/v1/CertificateService/CertificateLocations" || !svc.GenerateCSR || !svc.ReplaceCertificate {
		t.Fatalf("CertificateService = %+v, %v", svc, err)
	}
	// The emulator tree has no CertificateService.
	_, srv2 := newMock(t, mockbmc.Options{Tree: mockbmc.TreeEmulator})
	c2 := newClient(t, srv2, redfish.Config{})
	if _, err := c2.CertificateService(ctx); !errors.Is(err, redfish.ErrUnsupported) {
		t.Errorf("CertificateService on the emulator: %v", err)
	}
	if _, err := c2.Certificates(ctx); !errors.Is(err, redfish.ErrUnsupported) {
		t.Errorf("Certificates on the emulator: %v", err)
	}
	if _, _, err := c2.GenerateCSR(ctx, redfish.CSRRequest{CertificateCollection: httpsColl, CommonName: "x"}); !errors.Is(err, redfish.ErrUnsupported) {
		t.Errorf("GenerateCSR on the emulator: %v", err)
	}
}

func TestCertificateLocationsAndRead(t *testing.T) {
	_, srv := newMock(t, mockbmc.Options{})
	c := newClient(t, srv, redfish.Config{})
	ctx := context.Background()
	uris, err := c.CertificateLocations(ctx)
	if err != nil || len(uris) != 3 || !slices.Contains(uris, httpsCert) {
		t.Fatalf("CertificateLocations = %v, %v", uris, err)
	}
	all, err := c.Certificates(ctx)
	if err != nil || len(all) != 3 {
		t.Fatalf("Certificates = %d, %v", len(all), err)
	}
	cert, err := c.Certificate(ctx, httpsCert)
	if err != nil {
		t.Fatal(err)
	}
	x := cert.X509
	if x == nil || cert.Type != "PEM" || cert.Subject == nil || cert.Subject.CommonName != "manager.contoso.org" || !cert.Rekey || !cert.Renew {
		t.Fatalf("certificate = %+v", cert)
	}
	if !strings.Contains(x.Subject, "CN=manager.contoso.org") || !x.SelfSigned || x.IsCA || x.KeyBits != 256 || x.KeyCurve != "P-256" || x.PublicKeyAlgorithm != "ECDSA" ||
		!slices.Contains(x.KeyUsage, "DigitalSignature") || !slices.Contains(x.KeyUsage, "ServerAuthentication") || !slices.Contains(x.DNSNames, "manager.contoso.com") ||
		x.ChainLength != 1 || x.PrivateKeyPresent || len(x.SHA256Fingerprint) != 95 || !x.NotAfter.After(x.NotBefore) {
		t.Fatalf("x509 = %+v", x)
	}
	// What the service states agrees with what the certificate says.
	if cert.Fingerprint != x.SHA256Fingerprint || cert.SerialNumber != x.SerialNumber {
		t.Errorf("the service says %s %s, the certificate %s %s", cert.Fingerprint, cert.SerialNumber, x.SHA256Fingerprint, x.SerialNumber)
	}
	if !strings.HasPrefix(cert.PEM, "-----BEGIN CERTIFICATE-----") {
		t.Errorf("PEM = %q", cert.PEM)
	}
	// Three certificates have the Id 1: an Id must name one.
	if _, err := c.Certificate(ctx, "1"); !errors.Is(err, redfish.ErrAmbiguous) {
		t.Errorf("Certificate(1): %v", err)
	}
	if _, err := c.Certificate(ctx, "nope"); !errors.Is(err, redfish.ErrNotFound) {
		t.Errorf("Certificate(nope): %v", err)
	}
	for _, bad := range []string{"../x", "http://evil.example/redfish/v1/x", "/etc/passwd", "a b", "/redfish/v1/../x"} {
		if _, err := c.Certificate(ctx, bad); err == nil {
			t.Errorf("Certificate(%q) accepted", bad)
		}
	}
	// A resource that is not a Certificate is refused, not decoded.
	if _, err := c.Certificate(ctx, sysPath); !errors.Is(err, redfish.ErrMalformed) {
		t.Errorf("Certificate(system): %v", err)
	}
}

func TestCSRThenInstallWithTheKeyOfTheService(t *testing.T) {
	b, srv := newMock(t, mockbmc.Options{})
	c := newClient(t, srv, redfish.Config{})
	ctx := context.Background()
	csr, out, err := c.GenerateCSR(ctx, redfish.CSRRequest{CertificateCollection: httpsColl, CommonName: "bmc1.example.org", AlternativeNames: []string{"bmc1", "bmc1.example.com"},
		Organization: "Example", OrganizationalUnit: "Ops", City: "Lyon", State: "Rhone", Country: "FR", Email: "ops@example.org",
		KeyPairAlgorithm: "TPM_ALG_ECDSA", KeyCurveID: "TPM_ECC_NIST_P384", KeyUsage: []string{"DigitalSignature", "ServerAuthentication"}})
	if err != nil || !out.Changed {
		t.Fatalf("GenerateCSR = %+v, %+v, %v", csr, out, err)
	}
	if csr.CollectionURI != httpsColl || csr.Info == nil || !csr.Info.SignatureValid || !strings.Contains(csr.Info.Subject, "CN=bmc1.example.org") ||
		csr.Info.KeyCurve != "P-384" || !slices.Equal(csr.Info.DNSNames, []string{"bmc1", "bmc1.example.com"}) || !strings.Contains(csr.Info.Subject, "C=FR") {
		t.Fatalf("CSR = %+v %+v", csr, csr.Info)
	}
	if strings.Contains(csr.PEM, "PRIVATE KEY") {
		t.Fatal("the CSR carries a private key")
	}
	sent := b.CSRRequests()
	if len(sent) != 1 || sent[0]["CommonName"] != "bmc1.example.org" || sent[0]["KeyCurveId"] != "TPM_ECC_NIST_P384" {
		t.Fatalf("the service received %v", sent)
	}
	ca, caKey := testCA(t)
	signed, leaf := signCSR(t, csr.PEM, ca, caKey)
	cert, out, err := c.InstallCertificate(ctx, httpsColl, redfish.CertificateInput{CertificateString: signed})
	if err != nil || !out.Changed {
		t.Fatalf("InstallCertificate = %+v, %+v, %v", cert, out, err)
	}
	if cert.URI != httpsColl+"/2" || cert.X509 == nil || cert.X509.SerialNumber == "" || !strings.Contains(cert.X509.Subject, "CN=bmc1.example.org") ||
		cert.X509.SelfSigned || !strings.Contains(cert.X509.Issuer, "CN=Test CA") || cert.X509.NotAfter.Unix() != leaf.NotAfter.Unix() {
		t.Fatalf("installed = %+v %+v", cert, cert.X509)
	}
	if !b.HoldsKey(cert.URI) {
		t.Error("the service does not hold the key it generated")
	}
	if got := b.CertificateRequests(); len(got) != 1 || strings.Contains(got[0]["CertificateString"].(string), "PRIVATE KEY") {
		t.Errorf("the service received %v", got)
	}
	uris, _ := c.CertificateLocations(ctx)
	if len(uris) != 4 || !slices.Contains(uris, cert.URI) {
		t.Fatalf("locations after the install = %v", uris)
	}
	if c2, err := c.Certificate(ctx, "2"); err != nil || c2.URI != cert.URI {
		t.Errorf("Certificate(2) = %+v, %v", c2, err)
	}
	// Delete.
	out, err = c.DeleteCertificate(ctx, cert.URI)
	if err != nil || !out.Changed {
		t.Fatalf("DeleteCertificate = %+v, %v", out, err)
	}
	if uris, _ = c.CertificateLocations(ctx); len(uris) != 3 || slices.Contains(uris, cert.URI) {
		t.Fatalf("locations after the delete = %v", uris)
	}
	if out, err = c.DeleteCertificate(ctx, cert.URI); err != nil || out.Changed {
		t.Errorf("second delete = %+v, %v", out, err)
	}
	if _, err := c.DeleteCertificate(ctx, sysPath); !errors.Is(err, redfish.ErrMalformed) {
		t.Errorf("delete of a system: %v", err)
	}
}

func TestInstallNeedsTheKeyWhenTheServiceDoesNotKnowIt(t *testing.T) {
	b, srv := newMock(t, mockbmc.Options{})
	c := newClient(t, srv, redfish.Config{})
	ctx := context.Background()
	cs, ks := selfSigned(t, "outside.example.org")
	// The service has no key for it: it refuses.
	var se *redfish.StatusError
	if _, _, err := c.InstallCertificate(ctx, httpsColl, redfish.CertificateInput{CertificateString: cs}); !errors.As(err, &se) || se.Code != 400 {
		t.Fatalf("install without the key: %v", err)
	}
	// A key that is not the certificate's is refused before anything is sent.
	_, other := selfSigned(t, "other.example.org")
	n := len(b.CertificateRequests())
	_, _, err := c.InstallCertificate(ctx, httpsColl, redfish.CertificateInput{CertificateString: cs, PrivateKey: redfish.Secret(other)})
	if err == nil || !strings.Contains(err.Error(), "does not belong") || len(b.CertificateRequests()) != n {
		t.Fatalf("install with another key: %v", err)
	}
	for _, l := range keyLines(other) {
		if strings.Contains(err.Error(), l) {
			t.Fatal("the error carries the key")
		}
	}
	// The key that belongs to it is sent, with the certificate, and never comes back.
	cert, _, err := c.InstallCertificate(ctx, httpsColl, redfish.CertificateInput{CertificateString: cs, PrivateKey: redfish.Secret(ks)})
	if err != nil {
		t.Fatal(err)
	}
	got := b.CertificateRequests()
	if len(got) != n+1 || !strings.Contains(got[n]["CertificateString"].(string), strings.TrimSpace(keyLines(ks)[0])) {
		t.Fatalf("the key was not sent: %v", got)
	}
	r, err := c.Get(ctx, cert.URI)
	if err != nil || strings.Contains(string(r.Body), "PRIVATE KEY") || strings.Contains(string(r.Body), keyLines(ks)[0]) {
		t.Fatalf("the service shows the key: %v %s", err, r.Body)
	}
	if cert.X509 == nil || cert.X509.PrivateKeyPresent || cert.X509.IsCA {
		t.Fatalf("installed = %+v", cert)
	}
	// Refusals before the POST.
	enc := string(pem.EncodeToMemory(&pem.Block{Type: "ENCRYPTED PRIVATE KEY", Bytes: []byte("0123456789abcdef0123456789abcdef")}))
	combined := cs + ks
	for name, in := range map[string]redfish.CertificateInput{
		"encrypted key":     {CertificateString: cs, PrivateKey: redfish.Secret(enc)},
		"key not PEM":       {CertificateString: cs, PrivateKey: "not a key"},
		"key in the string": {CertificateString: combined},
		"empty":             {},
		"not a cert":        {CertificateString: "hello"},
		"bad type":          {CertificateString: cs, CertificateType: "DER"},
		"password on PEM":   {CertificateString: cs, Password: "x"},
		"key with PKCS12":   {CertificateString: "AAAA", CertificateType: "PKCS12", PrivateKey: redfish.Secret(ks)},
		"huge key":          {CertificateString: cs, PrivateKey: redfish.Secret(strings.Repeat("A", redfish.MaxKeyPEM+1))},
		"huge cert":         {CertificateString: strings.Repeat("A", redfish.MaxCertificateString+1)},
		"PEM with a chain":  {CertificateString: cs + cs},
	} {
		if _, _, err := c.InstallCertificate(ctx, httpsColl, in); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// The collection supports PEM only.
	if _, _, err := c.InstallCertificate(ctx, httpsColl, redfish.CertificateInput{CertificateString: cs + cs, CertificateType: "PEMchain"}); !errors.Is(err, redfish.ErrUnsupported) {
		t.Errorf("PEMchain: %v", err)
	}
	for _, p := range []string{"/redfish/v1/Systems", "/redfish/v1/nothing", "Systems", "http://evil/x"} {
		if _, _, err := c.InstallCertificate(ctx, p, redfish.CertificateInput{CertificateString: cs}); err == nil {
			t.Errorf("install in %q accepted", p)
		}
	}
	if len(b.CertificateRequests()) != n+1 {
		t.Errorf("a refused install reached the service: %v", len(b.CertificateRequests()))
	}
}

func TestReplaceCertificate(t *testing.T) {
	b, srv := newMock(t, mockbmc.Options{})
	c := newClient(t, srv, redfish.Config{})
	ctx := context.Background()
	csr, _, err := c.GenerateCSR(ctx, redfish.CSRRequest{CertificateCollection: httpsColl, CommonName: "replaced.example.org", KeyPairAlgorithm: "TPM_ALG_RSA", KeyBitLength: 2048})
	if err != nil || csr.Info == nil || csr.Info.PublicKeyAlgorithm != "RSA" || csr.Info.KeyBits != 2048 {
		t.Fatalf("GenerateCSR = %+v, %v", csr, err)
	}
	ca, caKey := testCA(t)
	signed, _ := signCSR(t, csr.PEM, ca, caKey)
	uri, out, err := c.ReplaceCertificate(ctx, httpsCert, redfish.CertificateInput{CertificateString: signed})
	if err != nil || !out.Changed || uri != httpsColl+"/2" {
		t.Fatalf("ReplaceCertificate = %q, %+v, %v", uri, out, err)
	}
	if _, err := c.Certificate(ctx, httpsCert); !errors.Is(err, redfish.ErrNotFound) {
		t.Errorf("the old certificate: %v", err)
	}
	cert, err := c.Certificate(ctx, uri)
	if err != nil || cert.X509 == nil || !strings.Contains(cert.X509.Subject, "CN=replaced.example.org") || cert.X509.KeyBits != 2048 {
		t.Fatalf("the new certificate = %+v, %v", cert, err)
	}
	locs, _ := c.CertificateLocations(ctx)
	if len(locs) != 3 || !slices.Contains(locs, uri) || slices.Contains(locs, httpsCert) {
		t.Errorf("locations = %v", locs)
	}
	if !b.HoldsKey(uri) || b.HoldsKey(httpsCert) {
		t.Error("the keys the service holds are not the new certificate's")
	}
	// Replacing needs a certificate the service can use.
	cs, ks := selfSigned(t, "own.example.org")
	if _, _, err := c.ReplaceCertificate(ctx, uri, redfish.CertificateInput{CertificateString: cs}); err == nil {
		t.Error("a certificate without a key replaced one")
	}
	uri2, _, err := c.ReplaceCertificate(ctx, uri, redfish.CertificateInput{CertificateString: cs, PrivateKey: redfish.Secret(ks)})
	if err != nil || uri2 != httpsColl+"/3" {
		t.Fatalf("replace with a key = %q, %v", uri2, err)
	}
	// What the ActionInfo does not allow is refused before the POST.
	n := len(b.CertificateRequests())
	if _, _, err := c.ReplaceCertificate(ctx, uri2, redfish.CertificateInput{CertificateString: cs + cs, CertificateType: "PEMchain"}); !errors.Is(err, redfish.ErrUnsupported) {
		t.Errorf("PEMchain: %v", err)
	}
	if _, _, err := c.ReplaceCertificate(ctx, "/redfish/v1/Systems/437XR1138R2", redfish.CertificateInput{CertificateString: cs}); err == nil {
		t.Error("a system was replaced")
	}
	if _, _, err := c.ReplaceCertificate(ctx, "nope", redfish.CertificateInput{CertificateString: cs}); !errors.Is(err, redfish.ErrNotFound) {
		t.Errorf("an unknown Id: %v", err)
	}
	if len(b.CertificateRequests()) != n {
		t.Error("a refused replacement reached the service")
	}
}

func TestRekeyAndRenew(t *testing.T) {
	b, srv := newMock(t, mockbmc.Options{})
	c := newClient(t, srv, redfish.Config{})
	ctx := context.Background()
	cur, err := c.Certificate(ctx, httpsCert)
	if err != nil {
		t.Fatal(err)
	}
	ca, caKey := testCA(t)
	renew, out, err := c.RenewCertificate(ctx, httpsCert, "c0nfidential-challenge")
	if err != nil || !out.Changed || renew.CertificateURI != httpsCert || renew.Info == nil || !renew.Info.SignatureValid || renew.Info.KeyCurve != "P-256" {
		t.Fatalf("Renew = %+v, %+v, %v", renew, out, err)
	}
	if !strings.Contains(renew.Info.Subject, "CN=manager.contoso.org") {
		t.Errorf("the renewal request is for %s", renew.Info.Subject)
	}
	// Renew keeps the key pair: the signed certificate has the key of the old one.
	signed, leaf := signCSR(t, renew.PEM, ca, caKey)
	old, _ := c.Get(ctx, httpsCert)
	var doc struct{ CertificateString string }
	_ = json.Unmarshal(old.Body, &doc)
	oldLeaf := leafOfPEM(t, doc.CertificateString)
	if !oldLeaf.PublicKey.(*ecdsa.PublicKey).Equal(leaf.PublicKey) {
		t.Error("Renew changed the key pair")
	}
	if last := b.CSRRequests(); len(last) != 1 || last[0]["ChallengePassword"] != "c0nfidential-challenge" {
		t.Errorf("the service received %v", last)
	}
	if _, _, err := c.ReplaceCertificate(ctx, httpsCert, redfish.CertificateInput{CertificateString: signed}); err != nil {
		t.Fatalf("replace with the renewed certificate: %v", err)
	}
	// Rekey makes a new key pair.
	rk, _, err := c.RekeyCertificate(ctx, httpsColl+"/2", redfish.RekeyOptions{KeyPairAlgorithm: "TPM_ALG_ECDSA", KeyCurveID: "TPM_ECC_NIST_P521"})
	if err != nil || rk.Info == nil || rk.Info.KeyCurve != "P-521" || rk.CertificateURI != httpsColl+"/2" {
		t.Fatalf("Rekey = %+v, %v", rk, err)
	}
	// Refused before the POST: a value the ActionInfo does not list, a certificate without the action.
	n := len(b.CSRRequests())
	if _, _, err := c.RekeyCertificate(ctx, httpsColl+"/2", redfish.RekeyOptions{KeyPairAlgorithm: "TPM_ALG_NOPE"}); !errors.Is(err, redfish.ErrUnsupported) {
		t.Errorf("Rekey with an unknown algorithm: %v", err)
	}
	if _, _, err := c.RenewCertificate(ctx, "/redfish/v1/Systems/437XR1138R2/Certificates/contoso-root", ""); !errors.Is(err, redfish.ErrUnsupported) {
		t.Errorf("Renew without the action: %v", err)
	}
	if _, _, err := c.RekeyCertificate(ctx, "nope", redfish.RekeyOptions{}); err == nil {
		t.Error("Rekey of nothing")
	}
	if len(b.CSRRequests()) != n {
		t.Error("a refused action reached the service")
	}
	if cur.X509 == nil {
		t.Error("no x509")
	}
}

func leafOfPEM(t *testing.T, s string) *x509.Certificate {
	t.Helper()
	b, _ := pem.Decode([]byte(s))
	if b == nil {
		t.Fatal("not PEM")
	}
	c, err := x509.ParseCertificate(b.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestGenerateCSRRefusals(t *testing.T) {
	b, srv := newMock(t, mockbmc.Options{})
	c := newClient(t, srv, redfish.Config{})
	ctx := context.Background()
	long := strings.Repeat("a", 300)
	for name, r := range map[string]redfish.CSRRequest{
		"no collection":        {CommonName: "x"},
		"foreign collection":   {CertificateCollection: "http://evil/redfish/v1/x", CommonName: "x"},
		"not a collection":     {CertificateCollection: "/redfish/v1/Systems", CommonName: "x"},
		"bad country":          {CertificateCollection: httpsColl, CommonName: "x", Country: "FRA"},
		"newline":              {CertificateCollection: httpsColl, CommonName: "x\ny"},
		"too long":             {CertificateCollection: httpsColl, CommonName: long},
		"bad usage":            {CertificateCollection: httpsColl, CommonName: "x", KeyUsage: []string{"Everything"}},
		"bad bits":             {CertificateCollection: httpsColl, CommonName: "x", KeyBitLength: -1},
		"empty alt name":       {CertificateCollection: httpsColl, CommonName: "x", AlternativeNames: []string{""}},
		"algorithm not listed": {CertificateCollection: httpsColl, CommonName: "x", KeyPairAlgorithm: "TPM_ALG_NOPE"},
		"curve not listed":     {CertificateCollection: httpsColl, CommonName: "x", KeyCurveID: "TPM_ECC_BN_P256"},
	} {
		if _, _, err := c.GenerateCSR(ctx, r); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if len(b.CSRRequests()) != 0 {
		t.Errorf("a refused request reached the service: %v", b.CSRRequests())
	}
	// The service says no: the error is the service's.
	var se *redfish.StatusError
	if _, _, err := c.GenerateCSR(ctx, redfish.CSRRequest{CertificateCollection: httpsColl}); !errors.As(err, &se) || se.Code != 400 {
		t.Errorf("no CommonName: %v", err)
	}
}

// echoing is a service that answers every POST with an error that repeats the
// body it received: the worst case for a secret.
func echoing(t *testing.T) *httptest.Server {
	t.Helper()
	b, err := mockbmc.New(mockbmc.Options{})
	if err != nil {
		t.Fatal(err)
	}
	h := b.Handler()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path == "/redfish/v1/SessionService/Sessions" {
			h.ServeHTTP(w, r)
			return
		}
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		msg := "bad request: " + string(body)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": "Base.1.19.GeneralError", "message": msg,
			"@Message.ExtendedInfo": []any{map[string]any{"MessageId": "Base.1.19.GeneralError", "Message": msg, "MessageArgs": []any{string(body)}}}}})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestCertificateErrorNeverCarriesTheKey(t *testing.T) {
	srv := echoing(t)
	c := newClient(t, srv, redfish.Config{})
	ctx := context.Background()
	cs, ks := selfSigned(t, "echo.example.org")
	const challenge = "ch4llenge-Zq9-secret"
	const pass = "p12-Pa55w0rd-Zq9"
	check := func(what string, err error) {
		t.Helper()
		var se *redfish.StatusError
		if err == nil || !errors.As(err, &se) || se.Code != 400 {
			t.Fatalf("%s: %v", what, err)
		}
		s := err.Error() + se.Message + se.Path + se.ErrorCode
		for _, i := range se.Info {
			s += i.Message + strings.Join(i.MessageArgs, " ")
		}
		for _, l := range append(keyLines(ks), challenge, pass) {
			if strings.Contains(s, l) {
				t.Fatalf("%s: the error carries a secret: %q", what, s)
			}
		}
		if strings.Contains(s, "PRIVATE KEY") {
			t.Fatalf("%s: the error carries the key block", what)
		}
	}
	_, _, err := c.InstallCertificate(ctx, httpsColl, redfish.CertificateInput{CertificateString: cs, PrivateKey: redfish.Secret(ks)})
	check("install", err)
	_, _, err = c.ReplaceCertificate(ctx, httpsCert, redfish.CertificateInput{CertificateString: cs, PrivateKey: redfish.Secret(ks)})
	check("replace", err)
	_, _, err = c.GenerateCSR(ctx, redfish.CSRRequest{CertificateCollection: httpsColl, CommonName: "x", ChallengePassword: challenge})
	check("csr", err)
	_, _, err = c.RekeyCertificate(ctx, httpsCert, redfish.RekeyOptions{ChallengePassword: challenge})
	check("rekey", err)
	_, _, err = c.RenewCertificate(ctx, httpsCert, challenge)
	check("renew", err)
	// The input does not print its key or its password, with any verb.
	in := redfish.CertificateInput{CertificateString: cs, PrivateKey: redfish.Secret(ks), Password: pass}
	b, _ := json.Marshal(in)
	if s := fmt.Sprintf("%v %+v %#v %s", in, in, in, b); strings.Contains(s, keyLines(ks)[0]) || strings.Contains(s, pass) {
		t.Error("the input prints a secret")
	}
}

func TestCSRAndX509Bounds(t *testing.T) {
	if _, err := redfish.ParseX509(strings.Repeat("A", redfish.MaxCertificateString+1)); !errors.Is(err, redfish.ErrCertificateTooLarge) {
		t.Errorf("a huge certificate: %v", err)
	}
	cs, ks := selfSigned(t, "bounds.example.org")
	var chain strings.Builder
	for i := 0; i < 17; i++ {
		chain.WriteString(cs)
	}
	if _, err := redfish.ParseX509(chain.String()); !errors.Is(err, redfish.ErrMalformed) {
		t.Errorf("a chain of 17: %v", err)
	}
	info, err := redfish.ParseX509(cs + cs + cs)
	if err != nil || info.ChainLength != 3 {
		t.Errorf("a chain of 3: %+v %v", info, err)
	}
	// A key in the string is noted, never read into the result.
	info, err = redfish.ParseX509(cs + ks)
	if err != nil || !info.PrivateKeyPresent {
		t.Errorf("with a key: %+v %v", info, err)
	}
	if b, _ := json.Marshal(info); strings.Contains(string(b), keyLines(ks)[0]) {
		t.Error("the key is in the result")
	}
	// A CSR answer with a key is refused, and the error does not carry it.
	body, _ := json.Marshal(map[string]any{"CSRString": "-----BEGIN CERTIFICATE REQUEST-----\nAAAA\n-----END CERTIFICATE REQUEST-----\n" + ks})
	_, err = redfish.ParseCSRResponse(body)
	if !errors.Is(err, redfish.ErrMalformed) || strings.Contains(err.Error(), keyLines(ks)[0]) {
		t.Errorf("a CSR with a key: %v", err)
	}
	// Not PEM, no string, too large.
	for name, doc := range map[string]string{"no string": `{}`, "not json": `[`, "not pem": `{"CSRString":"hello"}`,
		"huge": `{"CSRString":"` + strings.Repeat("A", redfish.MaxCSRString+1) + `"}`} {
		if _, err := redfish.ParseCSRResponse([]byte(doc)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// A request that does not decode is kept, with the reason.
	csr, err := redfish.ParseCSRResponse([]byte(`{"CSRString":"-----BEGIN CERTIFICATE REQUEST-----\nAAAA\n-----END CERTIFICATE REQUEST-----\n"}`))
	if err != nil || csr.Info != nil || csr.InfoError == "" {
		t.Errorf("an undecodable CSR: %+v %v", csr, err)
	}
	// The locations: bounded, deduplicated, service paths only.
	doc := `{"Links":{"Certificates":[{"@odata.id":"/redfish/v1/a"},{"@odata.id":"/redfish/v1/a"},{"@odata.id":"http://evil/x"},{"@odata.id":5},null]}}`
	l, err := redfish.ParseCertificateLocations([]byte(doc))
	if err != nil || len(l) != 1 || l[0] != "/redfish/v1/a" {
		t.Errorf("locations = %v %v", l, err)
	}
	// RSA keys are checked as well.
	rk, _ := rsa.GenerateKey(rand.Reader, 2048)
	kd := x509.MarshalPKCS1PrivateKey(rk)
	if err := redfish.CheckPrivateKey(redfish.Secret(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: kd})), cs); err == nil {
		t.Error("an RSA key matched an ECDSA certificate")
	}
	if err := redfish.CheckPrivateKey(redfish.Secret(ks), cs); err != nil {
		t.Errorf("the right key: %v", err)
	}
}

func FuzzParseX509(f *testing.F) {
	cs, ks := selfSigned(f, "seed.example.org")
	f.Add(cs)
	f.Add(cs + ks)
	f.Add(cs + cs)
	f.Add("-----BEGIN CERTIFICATE-----\nAAAA\n-----END CERTIFICATE-----\n")
	f.Add("")
	f.Fuzz(func(t *testing.T, s string) {
		info, err := redfish.ParseX509(s)
		if err != nil {
			return
		}
		if info.ChainLength < 1 || info.ChainLength > 16 || len(s) > redfish.MaxCertificateString {
			t.Fatalf("accepted %d certificates in %d bytes", info.ChainLength, len(s))
		}
	})
}

func FuzzParseCSRResponse(f *testing.F) {
	f.Add([]byte(`{"CSRString":"-----BEGIN CERTIFICATE REQUEST-----\nAAAA\n-----END CERTIFICATE REQUEST-----\n","CertificateCollection":{"@odata.id":"/redfish/v1/x"}}`))
	f.Add([]byte(`{"CSRString":"x","Certificate":{"@odata.id":5}}`))
	f.Add([]byte(`[]`))
	f.Fuzz(func(t *testing.T, data []byte) {
		csr, err := redfish.ParseCSRResponse(data)
		if err != nil {
			return
		}
		if len(csr.PEM) > redfish.MaxCSRString {
			t.Fatalf("accepted a CSR of %d bytes", len(csr.PEM))
		}
		// No private key block is accepted (the words alone, outside a block, are only text).
		for rest := []byte(csr.PEM); ; {
			var b *pem.Block
			if b, rest = pem.Decode(rest); b == nil {
				break
			}
			if strings.HasSuffix(b.Type, "PRIVATE KEY") {
				t.Fatalf("accepted a CSR with a %s block", b.Type)
			}
		}
	})
}

func FuzzParseCertificateLocations(f *testing.F) {
	f.Add([]byte(`{"Links":{"Certificates":[{"@odata.id":"/redfish/v1/a"}]}}`))
	f.Add([]byte(`{"Links":{"Certificates":[null,5,{"@odata.id":"//evil/x"}]}}`))
	f.Add([]byte(`{"Links":[]}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		l, err := redfish.ParseCertificateLocations(data)
		if err != nil {
			return
		}
		if len(l) > redfish.MaxCertificates {
			t.Fatalf("%d locations", len(l))
		}
		for _, u := range l {
			if !strings.HasPrefix(u, "/redfish/") {
				t.Fatalf("%q is not a path of the service", u)
			}
		}
	})
}

func FuzzParseCertificateResource(f *testing.F) {
	cs, ks := selfSigned(f, "seed.example.org")
	doc, _ := json.Marshal(map[string]any{"@odata.type": "#Certificate.v1_11_0.Certificate", "Id": "1", "CertificateType": "PEM", "CertificateString": cs + ks,
		"Subject": map[string]any{"CommonName": "x", "AlternativeNames": []any{"a", nil}}, "Actions": map[string]any{"#Certificate.Rekey": map[string]any{}}})
	f.Add("/redfish/v1/x/1", doc)
	f.Add("/redfish/v1/x/2", []byte(`{"CertificateType":"PKCS7","CertificateString":5,"Subject":[]}`))
	f.Add("", []byte(`null`))
	f.Fuzz(func(t *testing.T, uri string, data []byte) {
		cert, err := redfish.ParseCertificateResource(uri, data)
		if err != nil {
			return
		}
		if strings.Contains(cert.PEM, "PRIVATE KEY") {
			t.Fatal("the private key was kept")
		}
		if b, _ := json.Marshal(cert); strings.Contains(string(b), "PRIVATE KEY") && !strings.Contains(string(data), "PRIVATE KEY") {
			t.Fatal("a private key appeared")
		}
	})
}
