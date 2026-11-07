package mockbmc

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"math/big"
	"net/http"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"
)

// The CertificateService of the mock (DSP0266 1.23.0 9.9.13, 13.1.3; CertificateService
// 1.2.2, Certificate 1.11.0, CertificateLocations 1.0.4). The rackmount mockup
// carries the service with GenerateCSR and ReplaceCertificate, its
// CertificateLocations and a few certificates; augmentCertificates makes the
// service really do what the schemas say, with crypto/x509:
//
//   - the certificate of the HTTPS collection of the manager is a real self-signed
//     certificate (ECDSA P-256) whose properties are read from it, and the service
//     holds its key;
//   - POST to a CertificateCollection installs a certificate: the string is
//     decoded (PEM or PEMchain; the formats of @Redfish.SupportedCertificates
//     are enforced), a private key in the string is checked against the
//     certificate and removed from every response, and a certificate that is not a
//     CA must be one whose key the service holds (a key it generated for a CSR, or
//     one sent with the certificate: 400 otherwise); the new member is added to
//     the CertificateLocations; DELETE of a member removes it from them;
//   - GenerateCSR makes a key pair (RSA or ECDSA) and returns a real PKCS#10 request
//     (RFC 2986) signed by that key, and keeps the key; Rekey does the same for a
//     certificate with a new key pair, Renew with its existing key;
//   - ReplaceCertificate puts a new certificate member (with its own URI, named in
//     the Location header) in place of the one at CertificateUri.
//
// The emulator tree has no CertificateService.

// certState is the state of the certificate area.
type certState struct {
	pending  []crypto.Signer          // keys generated for a CSR, waiting for the signed certificate
	held     map[string]crypto.Signer // certificate path -> its key, when the service holds it
	received []map[string]any         // the bodies of the installs and replacements, as sent
	csrs     []map[string]any         // the bodies of the GenerateCSR, Rekey and Renew requests
	nextID   int                      // the next certificate Id
}

const certLocations = root + "/CertificateService/CertificateLocations"

// CertificateRequests returns the bodies of the POSTs to certificate collections
// and of the ReplaceCertificate actions accepted so far, as sent (a private key
// included: for the tests that check what was sent).
func (b *BMC) CertificateRequests() []map[string]any {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(b.cs.received)
}

// CSRRequests returns the bodies of the GenerateCSR, Rekey and Renew actions
// accepted so far.
func (b *BMC) CSRRequests() []map[string]any {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(b.cs.csrs)
}

// HoldsKey reports whether the service holds the private key of the certificate
// at path p.
func (b *BMC) HoldsKey(p string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.cs.held[p] != nil
}

func (b *BMC) isCertColl(p string) bool {
	m := b.res[p]
	t, _ := m["@odata.type"].(string)
	_, hasMembers := m["Members"]
	return strings.HasPrefix(t, "#CertificateCollection.") && hasMembers
}

func (b *BMC) isCert(p string) bool {
	t, _ := b.res[p]["@odata.type"].(string)
	return strings.HasPrefix(t, "#Certificate.")
}

func colonHex(b []byte) string {
	if len(b) == 0 {
		return "00"
	}
	h := strings.ToUpper(hex.EncodeToString(b))
	parts := make([]string, 0, len(h)/2)
	for i := 0; i < len(h); i += 2 {
		parts = append(parts, h[i:i+2])
	}
	return strings.Join(parts, ":")
}

// selfSigned makes a self-signed ECDSA P-256 certificate.
func selfSigned(cn string, names []string) (string, crypto.Signer, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return "", nil, err
	}
	now := time.Now().UTC().Truncate(time.Second)
	tpl := &x509.Certificate{SerialNumber: serial.Add(serial, big.NewInt(1)),
		Subject: pkix.Name{CommonName: cn, Organization: []string{"Contoso"}, OrganizationalUnit: []string{"BMC"}, Country: []string{"US"},
			Province: []string{"Oregon"}, Locality: []string{"Portland"}},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(365 * 24 * time.Hour), DNSNames: names,
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, key.Public(), key)
	if err != nil {
		return "", nil, err
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), key, nil
}

var usageNames = []struct {
	bit  x509.KeyUsage
	name string
}{{x509.KeyUsageDigitalSignature, "DigitalSignature"}, {x509.KeyUsageContentCommitment, "NonRepudiation"}, {x509.KeyUsageKeyEncipherment, "KeyEncipherment"},
	{x509.KeyUsageDataEncipherment, "DataEncipherment"}, {x509.KeyUsageKeyAgreement, "KeyAgreement"}, {x509.KeyUsageCertSign, "KeyCertSign"},
	{x509.KeyUsageCRLSign, "CRLSigning"}}

func identifier(n pkix.Name, names []string) map[string]any {
	m := map[string]any{"CommonName": n.CommonName}
	for k, v := range map[string][]string{"Organization": n.Organization, "OrganizationalUnit": n.OrganizationalUnit, "City": n.Locality, "State": n.Province, "Country": n.Country} {
		if len(v) > 0 {
			m[k] = v[0]
		}
	}
	if len(names) > 0 {
		l := make([]any, len(names))
		for i, n := range names {
			l[i] = n
		}
		m["AlternativeNames"] = l
	}
	return m
}

// certResource is the Certificate resource of a certificate whose properties are
// read from the certificate itself.
func certResource(uri, certPEM, typ string, leaf *x509.Certificate) map[string]any {
	sum := sha256.Sum256(leaf.Raw)
	var usage []any
	for _, u := range usageNames {
		if leaf.KeyUsage&u.bit != 0 {
			usage = append(usage, u.name)
		}
	}
	for _, u := range leaf.ExtKeyUsage {
		switch u {
		case x509.ExtKeyUsageServerAuth:
			usage = append(usage, "ServerAuthentication")
		case x509.ExtKeyUsageClientAuth:
			usage = append(usage, "ClientAuthentication")
		}
	}
	rekeyInfo := uri + "/RekeyActionInfo"
	return map[string]any{"@odata.type": "#Certificate.v1_11_0.Certificate", "Id": path.Base(uri), "Name": "Certificate " + path.Base(uri),
		"CertificateString": certPEM, "CertificateType": typ, "CertificateUsageTypes": []any{"Web"},
		"Subject": identifier(leaf.Subject, leaf.DNSNames), "Issuer": identifier(leaf.Issuer, nil),
		"ValidNotBefore": leaf.NotBefore.UTC().Format(time.RFC3339), "ValidNotAfter": leaf.NotAfter.UTC().Format(time.RFC3339),
		"KeyUsage": usage, "SerialNumber": colonHex(leaf.SerialNumber.Bytes()), "Fingerprint": colonHex(sum[:]), "FingerprintHashAlgorithm": "TPM_ALG_SHA256",
		"SignatureAlgorithm": leaf.SignatureAlgorithm.String(), "Status": map[string]any{"State": "Enabled", "Health": "OK"},
		"Actions": map[string]any{
			"#Certificate.Rekey": map[string]any{"target": uri + "/Actions/Certificate.Rekey", "@Redfish.ActionInfo": rekeyInfo},
			"#Certificate.Renew": map[string]any{"target": uri + "/Actions/Certificate.Renew"},
		}}
}

func (b *BMC) rekeyInfo(uri string) {
	info := uri + "/RekeyActionInfo"
	b.res[info] = map[string]any{"@odata.type": "#ActionInfo.v1_4_1.ActionInfo", "@odata.id": info, "Id": "RekeyActionInfo", "Name": "Rekey Action Info",
		"Parameters": []any{
			map[string]any{"Name": "KeyPairAlgorithm", "Required": false, "DataType": "String", "AllowableValues": []any{"TPM_ALG_RSA", "TPM_ALG_ECDSA"}},
			map[string]any{"Name": "KeyBitLength", "Required": false, "DataType": "Number"},
			map[string]any{"Name": "KeyCurveId", "Required": false, "DataType": "String", "AllowableValues": []any{"TPM_ECC_NIST_P256", "TPM_ECC_NIST_P384", "TPM_ECC_NIST_P521"}},
			map[string]any{"Name": "ChallengePassword", "Required": false, "DataType": "String"}}}
}

func (b *BMC) augmentCertificates() {
	b.cs = &certState{held: map[string]crypto.Signer{}, nextID: 2}
	const https = root + "/Managers/BMC/NetworkProtocol/HTTPS/Certificates/1"
	if b.res[root+"/CertificateService"] == nil || b.res[https] == nil {
		return
	}
	// The mockup's GenerateCSR ActionInfo lacks the parameters of the schema that
	// are not subject names of the first kind.
	if ai := b.res[root+"/CertificateService/GenerateCSRActionInfo"]; ai != nil {
		params, _ := ai["Parameters"].([]any)
		for _, n := range []string{"ChallengePassword", "GivenName", "Surname", "Initials", "ContactPerson", "UnstructuredName"} {
			params = append(params, map[string]any{"Name": n, "Required": false, "DataType": "String"})
		}
		ai["Parameters"] = params
	}
	certPEM, key, err := selfSigned("manager.contoso.org", []string{"manager.contoso.org", "manager.contoso.com"})
	if err != nil {
		panic(err) // the standard library cannot fail on these inputs
	}
	leaf, _ := leafOf(certPEM)
	res := certResource(https, certPEM, "PEM", leaf)
	res["@odata.id"] = https
	b.res[https] = res
	b.rekeyInfo(https)
	b.cs.held[https] = key
}

func leafOf(s string) (*x509.Certificate, error) {
	rest := []byte(s)
	for {
		var blk *pem.Block
		blk, rest = pem.Decode(rest)
		if blk == nil {
			return nil, fmt.Errorf("no certificate")
		}
		if blk.Type == "CERTIFICATE" {
			return x509.ParseCertificate(blk.Bytes)
		}
	}
}

// accepted is a certificate string the service took.
type accepted struct {
	pem  string // the certificates only
	leaf *x509.Certificate
	key  crypto.Signer // the key the service holds for it
}

// accept decodes the CertificateString of an install or a replacement.
func (b *BMC) accept(w http.ResponseWriter, in map[string]any, supported []any) (accepted, bool) {
	bad := func(msg, id string) (accepted, bool) {
		b.fail(w, http.StatusBadRequest, "", msg, id)
		return accepted{}, false
	}
	s, _ := in["CertificateString"].(string)
	typ, _ := in["CertificateType"].(string)
	if s == "" {
		return bad("The property CertificateString is a required property and must be included in the request.", "Base.1.19.PropertyMissing")
	}
	if typ == "" {
		return bad("The property CertificateType is a required property and must be included in the request.", "Base.1.19.PropertyMissing")
	}
	if len(supported) > 0 && !slices.Contains(supported, any(typ)) {
		return bad(fmt.Sprintf("The value %s for the property CertificateType is not in the list of acceptable values.", typ), "Base.1.19.PropertyValueNotInList")
	}
	if typ != "PEM" && typ != "PEMchain" {
		return bad("The mock decodes PEM and PEMchain certificates only.", "Base.1.19.PropertyValueNotInList")
	}
	var certs []string
	var leaf *x509.Certificate
	var key crypto.Signer
	for rest := []byte(s); ; {
		var blk *pem.Block
		blk, rest = pem.Decode(rest)
		if blk == nil {
			break
		}
		switch {
		case blk.Type == "CERTIFICATE":
			if leaf == nil {
				c, err := x509.ParseCertificate(blk.Bytes)
				if err != nil {
					return bad("The certificate is not a valid X.509 certificate.", "Base.1.19.PropertyValueFormatError")
				}
				leaf = c
			}
			certs = append(certs, string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: blk.Bytes})))
		case strings.HasSuffix(blk.Type, "PRIVATE KEY"):
			var k any
			var err error
			switch blk.Type {
			case "PRIVATE KEY":
				k, err = x509.ParsePKCS8PrivateKey(blk.Bytes)
			case "RSA PRIVATE KEY":
				k, err = x509.ParsePKCS1PrivateKey(blk.Bytes)
			case "EC PRIVATE KEY":
				k, err = x509.ParseECPrivateKey(blk.Bytes)
			default:
				err = fmt.Errorf("unsupported")
			}
			signer, ok := k.(crypto.Signer)
			if err != nil || !ok {
				return bad("The private key is not a valid unencrypted private key.", "Base.1.19.PropertyValueFormatError")
			}
			key = signer
		}
	}
	if leaf == nil {
		return bad("The CertificateString holds no PEM certificate.", "Base.1.19.PropertyValueFormatError")
	}
	if typ == "PEM" && len(certs) != 1 {
		return bad("A PEM certificate is one certificate: use PEMchain for a chain.", "Base.1.19.PropertyValueFormatError")
	}
	same := func(k crypto.Signer) bool {
		pub, ok := leaf.PublicKey.(interface{ Equal(crypto.PublicKey) bool })
		return ok && pub.Equal(k.Public())
	}
	if key != nil && !same(key) {
		return bad("The private key does not match the certificate.", "Base.1.19.PropertyValueFormatError")
	}
	if key == nil {
		for i, k := range b.cs.pending {
			if same(k) {
				key = k
				b.cs.pending = slices.Delete(slices.Clone(b.cs.pending), i, i+1)
				break
			}
		}
	}
	if key == nil && !leaf.IsCA {
		return bad("The service does not hold the private key of this certificate: it was not made from a request of this service, so the key shall be sent as part of CertificateString.", "Base.1.19.PropertyValueFormatError")
	}
	sent := map[string]any{}
	for k, v := range in {
		sent[k] = v
	}
	b.cs.received = append(b.cs.received, sent)
	return accepted{pem: strings.Join(certs, ""), leaf: leaf, key: key}, true
}

func (b *BMC) locationsAdd(uri string) {
	loc := b.res[certLocations]
	if loc == nil {
		return
	}
	links, _ := loc["Links"].(map[string]any)
	l, _ := links["Certificates"].([]any)
	links["Certificates"] = append(l, map[string]any{"@odata.id": uri})
	links["Certificates@odata.count"] = len(l) + 1
}

func (b *BMC) locationsDel(uri string) {
	loc := b.res[certLocations]
	if loc == nil {
		return
	}
	links, _ := loc["Links"].(map[string]any)
	l, _ := links["Certificates"].([]any)
	var keep []any
	for _, x := range l {
		if m, _ := x.(map[string]any); m["@odata.id"] != uri {
			keep = append(keep, x)
		}
	}
	links["Certificates"], links["Certificates@odata.count"] = keep, len(keep)
}

// newCertificate adds an accepted certificate to a collection and returns its path.
func (b *BMC) newCertificate(coll string, a accepted, typ string) string {
	// Ids are never reused, even after a delete or a replacement.
	n := max(b.cs.nextID, len(b.res[coll]["Members"].([]any))+1)
	for b.res[coll+"/"+strconv.Itoa(n)] != nil {
		n++
	}
	b.cs.nextID = n + 1
	uri := coll + "/" + strconv.Itoa(n)
	b.addMember(coll, uri, certResource(uri, a.pem, typ, a.leaf))
	b.rekeyInfo(uri)
	if a.key != nil {
		b.cs.held[uri] = a.key
	}
	b.locationsAdd(uri)
	return uri
}

// postCertificate serves POST to a CertificateCollection.
func (b *BMC) postCertificate(w http.ResponseWriter, in map[string]any, coll string) {
	supported, _ := b.res[coll]["@Redfish.SupportedCertificates"].([]any)
	a, ok := b.accept(w, in, supported)
	if !ok {
		return
	}
	typ, _ := in["CertificateType"].(string)
	uri := b.newCertificate(coll, a, typ)
	w.Header().Set("Location", uri)
	writeJSON(w, http.StatusCreated, b.res[uri], etagOf(b.res[uri]))
}

// deleteCertificate drops what the service keeps of a certificate that is deleted.
func (b *BMC) deleteCertificate(p string) {
	if b.isCert(p) {
		b.locationsDel(p)
		delete(b.cs.held, p)
	}
}

// replaceCertificate serves ReplaceCertificate: a new member takes the place of
// the one at CertificateUri; its path is the Location of the answer.
func (b *BMC) replaceCertificate(w http.ResponseWriter, in map[string]any) {
	ref, _ := in["CertificateUri"].(map[string]any)
	old, _ := ref["@odata.id"].(string)
	old = clean(old)
	if !b.isCert(old) {
		b.fail(w, http.StatusBadRequest, "", "The value for the parameter CertificateUri is not a certificate of this service.", "Base.1.19.ActionParameterValueFormatError")
		return
	}
	coll := path.Dir(old)
	supported, _ := b.res[coll]["@Redfish.SupportedCertificates"].([]any)
	a, ok := b.accept(w, in, supported)
	if !ok {
		return
	}
	typ, _ := in["CertificateType"].(string)
	var keep []any
	for _, x := range b.res[coll]["Members"].([]any) {
		if x.(map[string]any)["@odata.id"] != old {
			keep = append(keep, x)
		}
	}
	b.res[coll]["Members"], b.res[coll]["Members@odata.count"] = keep, len(keep)
	delete(b.res, old)
	delete(b.res, old+"/RekeyActionInfo")
	delete(b.cs.held, old)
	b.locationsDel(old)
	uri := b.newCertificate(coll, a, typ)
	w.Header().Set("Location", uri)
	writeJSON(w, http.StatusOK, b.res[uri], etagOf(b.res[uri]))
}

var curves = map[string]elliptic.Curve{"TPM_ECC_NIST_P256": elliptic.P256(), "TPM_ECC_NIST_P384": elliptic.P384(), "TPM_ECC_NIST_P521": elliptic.P521()}

// newKey makes the key pair KeyPairAlgorithm, KeyBitLength and KeyCurveId ask for.
func newKey(in map[string]any) (crypto.Signer, string) {
	alg, _ := in["KeyPairAlgorithm"].(string)
	switch alg {
	case "", "TPM_ALG_ECDSA":
		curve := elliptic.P256()
		if id, _ := in["KeyCurveId"].(string); id != "" {
			c, ok := curves[id]
			if !ok {
				return nil, "The value " + id + " for the parameter KeyCurveId is not in the list of acceptable values."
			}
			curve = c
		}
		k, err := ecdsa.GenerateKey(curve, rand.Reader)
		if err != nil {
			return nil, err.Error()
		}
		return k, ""
	case "TPM_ALG_RSA":
		bits := 2048
		if n, ok := in["KeyBitLength"].(float64); ok {
			bits = int(n)
		}
		if bits < 2048 || bits > 4096 {
			return nil, "The value for the parameter KeyBitLength is not in the supported range 2048 to 4096."
		}
		k, err := rsa.GenerateKey(rand.Reader, bits)
		if err != nil {
			return nil, err.Error()
		}
		return k, ""
	}
	return nil, "The value " + alg + " for the parameter KeyPairAlgorithm is not in the list of acceptable values."
}

func strOf(in map[string]any, k string) string { s, _ := in[k].(string); return s }

func one(s string) []string {
	if s == "" {
		return nil
	}
	return []string{s}
}

// csrAnswer makes the request, keeps the key and answers 200 with the CSRString.
func (b *BMC) csrAnswer(w http.ResponseWriter, in map[string]any, key crypto.Signer, tpl *x509.CertificateRequest, link map[string]any) {
	der, err := x509.CreateCertificateRequest(rand.Reader, tpl, key)
	if err != nil {
		b.fail(w, http.StatusBadRequest, "", "The request could not be made: "+err.Error(), "Base.1.19.GeneralError")
		return
	}
	sent := map[string]any{}
	for k, v := range in {
		sent[k] = v
	}
	b.cs.csrs = append(b.cs.csrs, sent)
	b.cs.pending = append(b.cs.pending, key)
	body := map[string]any{"CSRString": string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der}))}
	for k, v := range link {
		body[k] = v
	}
	writeJSON(w, http.StatusOK, body, "")
}

// generateCSR serves CertificateService.GenerateCSR.
func (b *BMC) generateCSR(w http.ResponseWriter, in map[string]any) {
	ref, _ := in["CertificateCollection"].(map[string]any)
	coll, _ := ref["@odata.id"].(string)
	coll = clean(coll)
	if !b.isCertColl(coll) {
		b.fail(w, http.StatusBadRequest, "", "The value for the parameter CertificateCollection is not a certificate collection of this service.", "Base.1.19.ActionParameterValueFormatError")
		return
	}
	cn := strOf(in, "CommonName")
	if cn == "" {
		b.fail(w, http.StatusBadRequest, "", "The action parameter CommonName is required.", "Base.1.19.ActionParameterMissing")
		return
	}
	key, msg := newKey(in)
	if key == nil {
		b.fail(w, http.StatusBadRequest, "", msg, "Base.1.19.ActionParameterValueNotInList")
		return
	}
	tpl := &x509.CertificateRequest{Subject: pkix.Name{CommonName: cn, Organization: one(strOf(in, "Organization")), OrganizationalUnit: one(strOf(in, "OrganizationalUnit")),
		Locality: one(strOf(in, "City")), Province: one(strOf(in, "State")), Country: one(strOf(in, "Country"))}}
	if e := strOf(in, "Email"); e != "" {
		tpl.EmailAddresses = []string{e}
	}
	alts, _ := in["AlternativeNames"].([]any)
	for _, a := range alts {
		if s, ok := a.(string); ok {
			tpl.DNSNames = append(tpl.DNSNames, s)
		}
	}
	b.csrAnswer(w, in, key, tpl, map[string]any{"CertificateCollection": map[string]any{"@odata.id": coll}})
}

// certAction serves Certificate.Rekey (a new key pair) and Certificate.Renew
// (the existing one) of the certificate at p.
func (b *BMC) certAction(w http.ResponseWriter, in map[string]any, p string, rekey bool) {
	leaf, err := leafOf(strOf(b.res[p], "CertificateString"))
	if err != nil {
		b.fail(w, http.StatusBadRequest, "", "The certificate cannot be read.", "Base.1.19.GeneralError")
		return
	}
	var key crypto.Signer
	if rekey {
		var msg string
		if key, msg = newKey(in); key == nil {
			b.fail(w, http.StatusBadRequest, "", msg, "Base.1.19.ActionParameterValueNotInList")
			return
		}
	} else if key = b.cs.held[p]; key == nil {
		b.fail(w, http.StatusBadRequest, "", "The service does not hold the key pair of this certificate.", "Base.1.19.ActionNotSupported")
		return
	}
	tpl := &x509.CertificateRequest{Subject: leaf.Subject, DNSNames: leaf.DNSNames, EmailAddresses: leaf.EmailAddresses}
	b.csrAnswer(w, in, key, tpl, map[string]any{"Certificate": map[string]any{"@odata.id": p}})
}
