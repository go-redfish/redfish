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

// keyString is the key material the DMTF mockup serves in NVMeoFSecrets/0.
const keyString = "DHHC-1:00:ia6zGodOr4SEG0Zzaw398rpY0wqipUWj4jWjUh4HWUz6aQ2n:"

func secMock(t *testing.T, tree string, cfg redfish.Config) (*mockbmc.BMC, *redfish.Client) {
	t.Helper()
	b, srv := newMock(t, mockbmc.Options{Tree: tree})
	return b, newClient(t, srv, cfg)
}

func TestDecodeDigest(t *testing.T) {
	d, raw, err := redfish.DecodeDigest("AAECAw==", redfish.MaxDigestText)
	if err != nil || d.Hex != "00010203" || d.Size != 4 || len(raw) != 4 {
		t.Fatalf("DecodeDigest = %+v, %v", d, err)
	}
	for _, s := range []string{"", "AAECAw", "AAECAw=", "AAECAw===", "AA ECAw==", "AAEC\nAw==", "AAEE==", "_-_-", "AAECAw==\x00", strings.Repeat("A", redfish.MaxDigestText+4)} {
		if _, _, err := redfish.DecodeDigest(s, redfish.MaxDigestText); !errors.Is(err, redfish.ErrBadDigest) {
			t.Errorf("%q: err = %v, want ErrBadDigest", clipTest(s), err)
		}
	}
	// A digest longer than 64 bytes shows the first 64 in Hex and its real Size.
	long := strings.Repeat("A", 4*100)
	if d, _, err := redfish.DecodeDigest(long, redfish.MaxDigestText); err != nil || d.Size != 300 || len(d.Hex) != 128 {
		t.Errorf("long = %+v, %v", d, err)
	}
}

func clipTest(s string) string {
	if len(s) > 20 {
		return s[:20]
	}
	return s
}

func TestComponentIntegrityReads(t *testing.T) {
	for _, noExpand := range []bool{false, true} {
		_, c := secMock(t, mockbmc.TreeRackmount, redfish.Config{NoExpand: noExpand})
		ctx := context.Background()
		all, err := c.ComponentIntegrities(ctx)
		if err != nil || len(all) != 4 {
			t.Fatalf("noExpand=%v: ComponentIntegrities = %d, %v", noExpand, len(all), err)
		}
		spdm, err := c.ComponentIntegrity(ctx, "SS-SPDM-0")
		if err != nil {
			t.Fatal(err)
		}
		if spdm.Type != "SPDM" || spdm.TypeVersion != "1.1.0" || spdm.Enabled == nil || !*spdm.Enabled || spdm.TargetComponentURI != "/redfish/v1/Chassis/1U/TrustedComponents/AC-RoT0" ||
			spdm.LastUpdated != "2021-11-02T14:05:39-07:00" || !spdm.SPDMGetSignedMeasurements || spdm.TPMGetSignedMeasurements {
			t.Fatalf("SS-SPDM-0 = %+v", spdm)
		}
		s := spdm.SPDM
		if s == nil || s.RequesterURI != "/redfish/v1/Managers/BMC" || s.MeasurementSpecification != "DMTF" || s.SummaryType != "TCB" || s.SummaryHashAlgorithm != "TPM_ALG_SHA_384" ||
			s.Summary == nil || s.Summary.Size != 48 || len(s.Measurements) != 3 || len(s.Sessions) != 2 || s.Sessions[0].Type != "EncryptedAuthenticated" ||
			s.ResponderCertificateURI != "/redfish/v1/Chassis/1U/TrustedComponents/AC-RoT0/Certificates/DeviceCert" {
			t.Fatalf("SPDM = %+v", s)
		}
		m := s.Measurements[1]
		if m.Index == nil || *m.Index != 3 || m.Type != "ImmutableROM" || m.PartOfSummary == nil || !*m.PartOfSummary || m.Measurement == nil || m.Measurement.Size != 64 ||
			len(m.Measurement.Hex) != 128 || m.SizeMatches == nil || !*m.SizeMatches || m.HashAlgorithm != "TPM_ALG_SHA_512" {
			t.Fatalf("measurement 3 = %+v", m)
		}
		if got := spdm.CertificateURIs(); len(got) != 1 {
			t.Errorf("CertificateURIs = %v", got)
		}
		tpm, err := c.ComponentIntegrity(ctx, "/redfish/v1/ComponentIntegrity/TPM-0")
		if err != nil || tpm.Type != "TPM" || tpm.TPM == nil || len(tpm.TPM.Measurements) != 4 || tpm.SPDM != nil || tpm.SPDMGetSignedMeasurements {
			t.Fatalf("TPM-0 = %+v, %v", tpm, err)
		}
		if p := tpm.TPM.Measurements[2]; p.PCR == nil || *p.PCR != 1 || p.Measurement == nil || p.Measurement.Size != 64 || p.SizeMatches == nil || !*p.SizeMatches {
			t.Errorf("TPM measurement = %+v", p)
		}
		if tpm.TPM.VerificationStatus != "Success" || tpm.TPM.CertificateURI == "" {
			t.Errorf("TPM identity = %+v", tpm.TPM)
		}
		usb, err := c.ComponentIntegrity(ctx, "USB-Integrity")
		if err != nil || usb.Enabled == nil || *usb.Enabled || usb.SPDM != nil {
			t.Fatalf("USB-Integrity = %+v, %v", usb, err)
		}
		for _, bad := range []string{"../x", "a/b", "", "Nope"} {
			if _, err := c.ComponentIntegrity(ctx, bad); err == nil {
				t.Errorf("ComponentIntegrity(%q) succeeded", bad)
			}
		}
	}
}

func TestSecurityUnsupportedOnTheEmulatorTree(t *testing.T) {
	_, c := secMock(t, mockbmc.TreeEmulator, redfish.Config{})
	ctx := context.Background()
	checks := map[string]error{}
	_, checks["ComponentIntegrities"] = c.ComponentIntegrities(ctx)
	_, checks["ComponentIntegrity"] = c.ComponentIntegrity(ctx, "x")
	_, checks["GetSignedMeasurements"] = c.GetSignedMeasurements(ctx, "x", redfish.SignedMeasurementsRequest{})
	_, checks["TrustedComponents"] = c.TrustedComponents(ctx)
	_, checks["SecurityPolicy"] = c.SecurityPolicy(ctx, "")
	_, checks["KeyService"] = c.KeyService(ctx)
	_, checks["NVMeoFSecrets"] = c.NVMeoFSecrets(ctx)
	_, checks["NVMeoFKeyPolicies"] = c.NVMeoFKeyPolicies(ctx)
	for name, err := range checks {
		if !errors.Is(err, redfish.ErrUnsupported) {
			t.Errorf("%s: err = %v, want ErrUnsupported", name, err)
		}
	}
}

func TestLinkedCertificates(t *testing.T) {
	_, c := secMock(t, mockbmc.TreeRackmount, redfish.Config{})
	ctx := context.Background()
	ci, err := c.ComponentIntegrity(ctx, "SS-SPDM-0")
	if err != nil {
		t.Fatal(err)
	}
	got := c.LinkedCertificates(ctx, append(ci.CertificateURIs(), "/redfish/v1/Nope", "http://evil/x"))
	if len(got) != 3 || got[0].Certificate == nil || got[0].Error != "" {
		t.Fatalf("LinkedCertificates = %+v", got)
	}
	if got[0].Certificate.ID != "DeviceCert" || (got[0].Certificate.X509 == nil && got[0].Certificate.X509Error == "") {
		t.Errorf("certificate = %+v", got[0].Certificate)
	}
	if got[1].Error == "" || got[2].Error == "" || got[1].Certificate != nil {
		t.Errorf("failures not reported: %+v %+v", got[1], got[2])
	}
}

func TestGetSignedMeasurements(t *testing.T) {
	b, c := secMock(t, mockbmc.TreeRackmount, redfish.Config{})
	_ = b
	ctx := context.Background()
	slot := 15
	sm, err := c.GetSignedMeasurements(ctx, "SS-SPDM-0", redfish.SignedMeasurementsRequest{Indices: []int{1, 3}, Nonce: strings.Repeat("AB", 32), SlotID: &slot})
	if err != nil {
		t.Fatal(err)
	}
	if sm.Verified || !strings.HasPrefix(sm.Verification, "not verified") || sm.Nonce != strings.Repeat("ab", 32) || !sm.NonceInBlob ||
		sm.SigningAlgorithm != "TPM_ALG_ECDSA_ECC_NIST_P384" || sm.HashingAlgorithm != "TPM_ALG_SHA_384" || sm.Version != "1.1" ||
		sm.Blob.Size == 0 || sm.Blob.Hex == "" || !strings.Contains(sm.PublicKeyPEM, "BEGIN PUBLIC KEY") || sm.ComponentIntegrityURI != "/redfish/v1/ComponentIntegrity/SS-SPDM-0" {
		t.Fatalf("SignedMeasurements = %+v", sm)
	}
	if j, _ := json.Marshal(sm); !strings.Contains(string(j), `"signature_verified":false`) || !strings.Contains(string(j), "not verified") {
		t.Errorf("JSON does not say not verified: %s", j)
	}
	// Defaults: a generated nonce (fresh each time), the certificate link, all measurements.
	a, err := c.GetSignedMeasurements(ctx, "SS-SPDM-0", redfish.SignedMeasurementsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	a2, _ := c.GetSignedMeasurements(ctx, "SS-SPDM-0", redfish.SignedMeasurementsRequest{})
	if len(a.Nonce) != 64 || a.Nonce == a2.Nonce || !a.NonceInBlob || a.CertificateURI == "" || a.PublicKeyPEM != "" {
		t.Errorf("defaults = %+v / %q", a, a2.Nonce)
	}
	bad := map[string]redfish.SignedMeasurementsRequest{
		"index 255 with another": {Indices: []int{255, 1}}, "index 255 twice": {Indices: []int{255, 255}}, "index 300": {Indices: []int{300}}, "negative": {Indices: []int{-1}},
		"duplicate": {Indices: []int{2, 2}}, "short nonce": {Nonce: "ab"}, "non hex nonce": {Nonce: strings.Repeat("zz", 32)}, "negative slot": {SlotID: &[]int{-1}[0]},
	}
	for name, req := range bad {
		if _, err := c.GetSignedMeasurements(ctx, "SS-SPDM-0", req); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	// A slot the ActionInfo does not allow is refused before anything is sent.
	three := 3
	if _, err := c.GetSignedMeasurements(ctx, "SS-SPDM-0", redfish.SignedMeasurementsRequest{SlotID: &three}); !errors.Is(err, redfish.ErrUnsupported) {
		t.Errorf("slot 3: err = %v, want ErrUnsupported", err)
	}
	// The TPM resource has no SPDM action.
	if _, err := c.GetSignedMeasurements(ctx, "TPM-0", redfish.SignedMeasurementsRequest{}); !errors.Is(err, redfish.ErrUnsupported) {
		t.Errorf("TPM-0: err = %v, want ErrUnsupported", err)
	}
}

func TestParseSignedMeasurements(t *testing.T) {
	sm, err := redfish.ParseSignedMeasurements([]byte(`{"SignedMeasurements":"AAECAw==","Version":"1.2","SigningAlgorithm":"X","AdditionalSigningAlgorithms":["Y",null],"Certificate":{"@odata.id":"/redfish/v1/c"}}`))
	if err != nil || sm.Blob.Hex != "00010203" || sm.Verified || sm.CertificateURI != "/redfish/v1/c" || len(sm.AdditionalSigningAlgorithms) != 1 {
		t.Fatalf("= %+v, %v", sm, err)
	}
	for _, in := range []string{``, `[]`, `{}`, `{"SignedMeasurements":5}`, `{"SignedMeasurements":"not base64!"}`, `{"SignedMeasurements":"AAECAw"}`} {
		if _, err := redfish.ParseSignedMeasurements([]byte(in)); !errors.Is(err, redfish.ErrMalformed) {
			t.Errorf("%q: err = %v, want ErrMalformed", in, err)
		}
	}
	big := `{"SignedMeasurements":"` + strings.Repeat("A", redfish.MaxSignedBlobText+4) + `"}`
	if _, err := redfish.ParseSignedMeasurements([]byte(big)); !errors.Is(err, redfish.ErrMalformed) {
		t.Errorf("a blob over the limit: err = %v", err)
	}
}

func TestMeasurementThatIsNotBase64IsKeptWithItsError(t *testing.T) {
	ci, err := redfish.ParseComponentIntegrity("/redfish/v1/ComponentIntegrity/X", []byte(`{"Id":"X","ComponentIntegrityType":"SPDM","SPDM":{"MeasurementSet":{"MeasurementSummary":"xx","Measurements":[
	  {"MeasurementIndex":1,"Measurement":"4wEF1nnub2ozuYTWyjyvFGrYnSn0eidy54uaIYXjp/HKRByaco9UtCZeyNfA3rQ3YsRD/6csLcqGw9Nd9/HPAw==","MeasurementHashAlgorithm":"TPM_ALG_SHA_256"},
	  {"MeasurementIndex":2,"Measurement":"dead-beef","MeasurementHashAlgorithm":"TPM_ALG_SHA_512"},
	  {"MeasurementIndex":"3","Measurement":5}]}}}`))
	if err != nil {
		t.Fatal(err)
	}
	ms := ci.SPDM.Measurements
	if len(ms) != 3 || ms[0].SizeMatches == nil || *ms[0].SizeMatches || ms[0].DecodeError != "" {
		t.Errorf("a 64-byte digest declared SHA-256 must not match: %+v", ms[0])
	}
	if ms[1].Measurement != nil || ms[1].DecodeError == "" || ms[2].Index != nil || ms[2].Measurement != nil {
		t.Errorf("bad measurements: %+v %+v", ms[1], ms[2])
	}
	if ci.SPDM.Summary != nil || ci.SPDM.SummaryError == "" {
		t.Errorf("summary = %+v / %q", ci.SPDM.Summary, ci.SPDM.SummaryError)
	}
}

func TestTrustedComponents(t *testing.T) {
	for _, noExpand := range []bool{false, true} {
		_, c := secMock(t, mockbmc.TreeRackmount, redfish.Config{NoExpand: noExpand})
		ctx := context.Background()
		all, err := c.TrustedComponents(ctx)
		if err != nil || len(all) != 3 {
			t.Fatalf("noExpand=%v: TrustedComponents = %d, %v", noExpand, len(all), err)
		}
		tpm, err := c.TrustedComponent(ctx, "TPM")
		if err != nil {
			t.Fatal(err)
		}
		if tpm.Type != "Discrete" || tpm.UUID != "D0810BE9-EB91-4CE2-BADE-42B42326700C" || tpm.TPMCapabilitiesVendorID != "SIM0" || tpm.TPMHardwareInterfaceVendorID != "0xABCD" ||
			tpm.ChassisURI != "/redfish/v1/Chassis/1U" || len(tpm.ComponentIntegrity) != 1 || tpm.ComponentIntegrity[0] != "/redfish/v1/ComponentIntegrity/TPM-0" ||
			tpm.CertificatesURI != "/redfish/v1/Chassis/1U/TrustedComponents/TPM/Certificates" || len(tpm.ComponentsProtected) != 1 {
			t.Fatalf("TPM = %+v", tpm)
		}
		certs, err := c.CertificatesOf(ctx, tpm.CertificatesURI)
		if err != nil || len(certs) == 0 {
			t.Errorf("certificates of the TPM = %d, %v", len(certs), err)
		}
		if _, err := c.TrustedComponent(ctx, "/redfish/v1/Chassis/1U/TrustedComponents/iRoT0"); err != nil {
			t.Error(err)
		}
		if _, err := c.TrustedComponent(ctx, "/redfish/v1/ComponentIntegrity/TPM-0"); !errors.Is(err, redfish.ErrMalformed) {
			t.Errorf("a path that is not a TrustedComponent: %v", err)
		}
		if _, err := c.TrustedComponent(ctx, "Nope"); !errors.Is(err, redfish.ErrNotFound) {
			t.Errorf("Nope: %v", err)
		}
		if _, err := c.TrustedComponent(ctx, "a/b"); err == nil {
			t.Error("a name with a slash")
		}
		if got, err := c.TrustedComponents(ctx, "/redfish/v1/Chassis/1U"); err != nil || len(got) != 3 {
			t.Errorf("one chassis: %d, %v", len(got), err)
		}
	}
}

func TestSecurityPolicyRead(t *testing.T) {
	_, c := secMock(t, mockbmc.TreeRackmount, redfish.Config{})
	ctx := context.Background()
	sp, err := c.SecurityPolicy(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if sp.State != "Enabled" || sp.OverrideParentManager == nil || !*sp.OverrideParentManager || sp.ManagerURI != "/redfish/v1/Managers/BMC" || sp.SPDM == nil || sp.TLSClient == nil || sp.TLSServer == nil {
		t.Fatalf("policy = %+v", sp)
	}
	s := sp.SPDM
	if s.Enabled == nil || !*s.Enabled || s.VerifyCertificate == nil || !*s.VerifyCertificate || s.AllowExtendedAlgorithms == nil || *s.AllowExtendedAlgorithms ||
		s.Allowed == nil || len(s.Allowed.BaseAsym) != 3 || len(s.Allowed.AEAD) != 2 || s.Denied == nil || s.Denied.Versions[0] != "NONE" || s.TrustedCertificatesURI == "" || s.RevokedCertificatesURI == "" {
		t.Errorf("SPDM = %+v %+v", s, s.Allowed)
	}
	if sp.TLSServer.Allowed == nil || sp.TLSServer.Allowed.Versions[0] != "1.3" || len(sp.TLSServer.Denied.Versions) != 2 || sp.TLSClient.VerifyCertificate == nil || !*sp.TLSClient.VerifyCertificate {
		t.Errorf("TLS = %+v %+v", sp.TLSClient, sp.TLSServer)
	}
	pc, err := c.PolicyCertificates(ctx, sp)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]int{}
	for _, p := range pc {
		byName[p.Collection] = len(p.Certificates)
	}
	if len(pc) != 6 || byName["spdm.trusted"] != 1 || byName["spdm.revoked"] != 0 || byName["tls_client.trusted"] != 1 {
		t.Errorf("PolicyCertificates = %v", byName)
	}
	for _, p := range pc {
		for _, cert := range p.Certificates {
			if cert.X509 == nil && cert.X509Error == "" {
				t.Errorf("%s: a certificate neither decoded nor explained: %+v", p.Collection, cert)
			}
		}
	}
	if _, err := c.SecurityPolicy(ctx, "Nope"); !errors.Is(err, redfish.ErrNotFound) {
		t.Errorf("Nope: %v", err)
	}
}

func TestKeyServiceNeverShowsTheKey(t *testing.T) {
	_, c := secMock(t, mockbmc.TreeRackmount, redfish.Config{})
	ctx := context.Background()
	ks, err := c.KeyService(ctx)
	if err != nil || ks.NVMeoFSecretsURI == "" || ks.NVMeoFKeyPoliciesURI == "" {
		t.Fatalf("KeyService = %+v, %v", ks, err)
	}
	secrets, err := c.NVMeoFSecrets(ctx)
	if err != nil || len(secrets) != 2 {
		t.Fatalf("NVMeoFSecrets = %d, %v", len(secrets), err)
	}
	s := secrets[0]
	if !s.KeyPresent || s.KeyType != "NVMeoF" || s.NQN != "nqn.corp.com:nvme:target-subsystem-0001" || s.SecurityProtocolType != "DHHC" || s.HostKeyID != "1" || len(s.SecureHashAllowList) != 2 {
		t.Errorf("secret = %+v", s)
	}
	// The mockup does serve the key: it is in no output the library makes.
	j, _ := json.Marshal(secrets)
	if strings.Contains(string(j), "ia6zGodOr4SEG0") || strings.Contains(string(j), "DHHC-1:") {
		t.Errorf("the key is in the JSON: %s", j)
	}
	if got := strings.Join([]string{string(j)}, ""); strings.Contains(got, keyString) {
		t.Error("the key is in the output")
	}
	pols, err := c.NVMeoFKeyPolicies(ctx)
	if err != nil || len(pols) != 1 {
		t.Fatalf("NVMeoFKeyPolicies = %d, %v", len(pols), err)
	}
	if p := pols[0]; p.IsDefault == nil || !*p.IsDefault || p.Type != "NVMeoF" || len(p.CipherSuites) != 2 || len(p.DHGroups) != 5 || len(p.SecurityTransports) != 2 || len(p.SecureHashes) != 2 {
		t.Errorf("policy = %+v", p)
	}
}

func TestSecurityReadsDoNotWrite(t *testing.T) {
	b, c := secMock(t, mockbmc.TreeRackmount, redfish.Config{})
	ctx := context.Background()
	_, _ = c.ComponentIntegrities(ctx)
	_, _ = c.TrustedComponents(ctx)
	sp, _ := c.SecurityPolicy(ctx, "")
	_, _ = c.PolicyCertificates(ctx, sp)
	_, _ = c.NVMeoFSecrets(ctx)
	_, _ = c.NVMeoFKeyPolicies(ctx)
	// The only POST of this area is the action, and only on request.
	for _, l := range b.Log() {
		if strings.HasPrefix(l, "POST ") || strings.HasPrefix(l, "PATCH ") || strings.HasPrefix(l, "DELETE ") || strings.HasPrefix(l, "PUT ") {
			t.Errorf("a read wrote: %s", l)
		}
	}
}

func FuzzParseComponentIntegrity(f *testing.F) {
	f.Add([]byte(`{"Id":"X","ComponentIntegrityType":"SPDM","ComponentIntegrityEnabled":true,"TargetComponentURI":"/redfish/v1/x","Actions":{"#ComponentIntegrity.SPDMGetSignedMeasurements":{"target":"/x"}},"SPDM":{"Requester":{"@odata.id":"/redfish/v1/Managers/BMC"},"MeasurementSet":{"MeasurementSummary":"oylHo8JAT1RqtB0Vlx9dCzogavikhSvIZpVEDhI1X1+TKeyZZvwuUb5qF9uWqPVa","MeasurementSummaryHashAlgorithm":"TPM_ALG_SHA_384","Measurements":[{"MeasurementIndex":1,"Measurement":"K0zqzG/MFbeV7OVAhvDugxEzc7nMnRvdoKy8g2zU5JovPrUNSeFJxibTZE+0kOR9QhDR5RzumnomFECF8zI/Lw==","MeasurementHashAlgorithm":"TPM_ALG_SHA_512"}]},"IdentityAuthentication":{"ResponderAuthentication":{"ComponentCertificate":{"@odata.id":"/redfish/v1/c"}}},"ComponentCommunication":{"Sessions":[{"SessionId":1,"SessionType":"Plain"}]}}}`))
	f.Add([]byte(`{"ComponentIntegrityType":"TPM","TPM":{"NonceSizeBytesMaximum":"x","MeasurementSet":{"Measurements":[{"PCR":1,"Measurement":"h6spEuxbyOtGhP35UoGhTcVX3iRaZQGDw4Yk5oQcabw=","MeasurementHashAlgorithm":"TPM_ALG_SHA256"},null,5,{"PCR":-1,"Measurement":"==="}]}}}`))
	f.Add([]byte(`{"SPDM":{"MeasurementSet":{"Measurements":[{"Measurement":"AAAA","MeasurementIndex":1e999}],"MeasurementSummary":5}},"Links":{"ComponentsProtected":[null,{"@odata.id":5}]}}`))
	f.Add([]byte(`[]`))
	f.Fuzz(func(t *testing.T, data []byte) {
		ci, err := redfish.ParseComponentIntegrity("/redfish/v1/ComponentIntegrity/F", data)
		if err != nil {
			return
		}
		var all []redfish.Measurement
		if ci.SPDM != nil {
			all = append(all, ci.SPDM.Measurements...)
			if ci.SPDM.Summary != nil && ci.SPDM.Summary.Size == 0 {
				t.Fatalf("an empty summary: %+v", ci.SPDM.Summary)
			}
		}
		if ci.TPM != nil {
			all = append(all, ci.TPM.Measurements...)
		}
		if len(all) > 2*redfish.MaxMeasurements {
			t.Fatalf("%d measurements", len(all))
		}
		for _, m := range all {
			if m.Measurement != nil && (m.DecodeError != "" || len(m.Measurement.Base64) > redfish.MaxDigestText || len(m.Measurement.Hex) > 128 || len(m.Measurement.Hex) != min(2*m.Measurement.Size, 128)) {
				t.Fatalf("measurement = %+v", m)
			}
			if m.Measurement == nil && m.DecodeError != "" && m.SizeMatches != nil {
				t.Fatalf("a size verdict on an undecoded measurement: %+v", m)
			}
		}
		if len(ci.CertificateURIs()) > 3 {
			t.Fatalf("%v", ci.CertificateURIs())
		}
	})
}

func FuzzDecodeDigest(f *testing.F) {
	for _, s := range []string{"AAECAw==", "", "AAECAw", "====", "oylHo8JAT1RqtB0Vlx9dCzogavikhSvIZpVEDhI1X1+TKeyZZvwuUb5qF9uWqPVa", "AAEE==", "a b", "\xff\xfe"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		d, raw, err := redfish.DecodeDigest(s, redfish.MaxDigestText)
		if err != nil {
			if !errors.Is(err, redfish.ErrBadDigest) || d != (redfish.Digest{}) || raw != nil {
				t.Fatalf("error %v with %+v", err, d)
			}
			return
		}
		if d.Base64 != s || d.Size != len(raw) || len(d.Hex) != min(2*len(raw), 128) || len(s) > redfish.MaxDigestText {
			t.Fatalf("digest %+v for %q", d, s)
		}
	})
}

func FuzzParseSignedMeasurements(f *testing.F) {
	f.Add([]byte(`{"SignedMeasurements":"AAECAw==","Version":"1.1","SigningAlgorithm":"TPM_ALG_ECDSA_ECC_NIST_P384","HashingAlgorithm":"TPM_ALG_SHA_384","Certificate":{"@odata.id":"/redfish/v1/c"},"PublicKey":"-----BEGIN PUBLIC KEY-----\nAA==\n-----END PUBLIC KEY-----\n"}`))
	f.Add([]byte(`{"SignedMeasurements":"not base64","AdditionalSigningAlgorithms":[1,null]}`))
	f.Add([]byte(`{"SignedMeasurements":""}`))
	f.Add([]byte(`null`))
	f.Fuzz(func(t *testing.T, data []byte) {
		sm, err := redfish.ParseSignedMeasurements(data)
		if err != nil {
			if !errors.Is(err, redfish.ErrMalformed) {
				t.Fatalf("error %v is not ErrMalformed", err)
			}
			return
		}
		if sm.Verified || !strings.HasPrefix(sm.Verification, "not verified") || sm.Blob.Size == 0 || len(sm.Blob.Base64) > redfish.MaxSignedBlobText {
			t.Fatalf("signed measurements = %+v", sm)
		}
	})
}

func FuzzParseSecurityPolicy(f *testing.F) {
	f.Add([]byte(`{"Name":"P","OverrideParentManager":true,"SPDM":{"Enabled":true,"Allowed":{"Versions":["1.1"],"Algorithms":{"AEAD":["A"],"BaseAsym":[1,null],"BaseHash":["H"]}},"Denied":{},"TrustedCertificates":{"@odata.id":"/redfish/v1/t"}},"TLS":{"Client":{"VerifyCertificate":"x","Allowed":{"Versions":["1.3"],"Algorithms":{"CipherSuites":["S"]}}},"Server":{"RevokedCertificates":{"@odata.id":5}}}}`))
	f.Add([]byte(`{"SPDM":5,"TLS":{"Client":[],"Server":null}}`))
	f.Add([]byte(`{"TLS":{"Client":{"Allowed":{"Algorithms":5,"Versions":{}}}}}`))
	f.Add([]byte(`[]`))
	f.Fuzz(func(t *testing.T, data []byte) {
		sp, err := redfish.ParseSecurityPolicy("/redfish/v1/Managers/M/SecurityPolicy", data)
		if err != nil {
			return
		}
		var uris []string
		for _, p := range []*redfish.TLSPolicy{sp.TLSClient, sp.TLSServer} {
			if p != nil {
				uris = append(uris, p.TrustedCertificatesURI, p.RevokedCertificatesURI)
			}
		}
		if sp.SPDM != nil {
			uris = append(uris, sp.SPDM.TrustedCertificatesURI, sp.SPDM.RevokedCertificatesURI)
		}
		for _, u := range uris {
			if u != "" && !strings.HasPrefix(u, "/redfish/") {
				t.Fatalf("a link that is not a path of the service: %q", u)
			}
		}
	})
}

func FuzzParseKeyResources(f *testing.F) {
	f.Add([]byte(`{"Id":"0","KeyType":"NVMeoF","KeyString":"DHHC-1:00:SECRETSECRET:","NVMeoF":{"NQN":"nqn.x","SecurityProtocolType":"DHHC","SecureHashAllowList":["SHA384",1]}}`))
	f.Add([]byte(`{"Id":"0","KeyPolicyType":"NVMeoF","IsDefault":"yes","NVMeoF":{"CipherSuiteAllowList":["A",null],"DHGroupAllowList":5}}`))
	f.Add([]byte(`{"KeyString":5,"NVMeoF":[]}`))
	f.Add([]byte(`[]`))
	f.Fuzz(func(t *testing.T, data []byte) {
		if k, err := redfish.ParseNVMeoFSecret("/redfish/v1/KeyService/NVMeoFSecrets/0", data); err == nil {
			var in map[string]any
			if json.Unmarshal(data, &in) == nil {
				// Keys of the NVMe-oF formats (DHHC-1:...) or marked ones: a shorter text
				// could be, by chance, the name of a field of the output.
				if s, _ := in["KeyString"].(string); strings.HasPrefix(s, "DHHC-1:") || strings.HasPrefix(s, "SECRET") {
					j, _ := json.Marshal(k)
					// The key is in no field, unless the service sent the same text as
					// another property (which is then that property).
					other := false
					for kk, v := range in {
						if vs, ok := v.(string); ok && kk != "KeyString" && vs == s {
							other = true
						}
					}
					if !other && strings.Contains(string(j), s) {
						t.Fatalf("the key is in %s", j)
					}
					if !k.KeyPresent {
						t.Fatalf("a key was sent and KeyPresent is false")
					}
				}
			}
		}
		_, _ = redfish.ParseKeyPolicy("/redfish/v1/KeyService/NVMeoFKeyPolicies/0", data)
		_, _ = redfish.ParseTrustedComponent("/redfish/v1/Chassis/1U/TrustedComponents/T", data)
	})
}
