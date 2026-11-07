package redfish

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Certificates: the CertificateService, the certificates it locates, and the
// actions that change them. There is no Ansible oracle for this area: the
// references are DSP0266 1.23.0 (read as published 2025-09-05) 9.9.13 Supported
// certificates annotation (@Redfish.SupportedCertificates of a
// CertificateCollection), 13.1.3 Certificates (replacement of the default
// certificate shall be supported; X.509 v3 as RFC 5280 defines it) and 13.2
// Sensitive data (operations with sensitive data should use HTTPS only); and the
// schemas of DSP8010 2025.4: CertificateService 1.2.2 (actions GenerateCSR and
// ReplaceCertificate, property CertificateLocations), Certificate 1.11.0 (actions
// Rekey, Renew and ForceAutomaticRenew; the Identifier of Issuer and Subject),
// CertificateLocations 1.0.4 (Links.Certificates) and the CertificateCollection
// index. The certificate itself is decoded with crypto/x509 (RFC 5280), a CSR as
// RFC 2986 PKCS#10 (the schema: "PEM-encoded string, which contains RFC2986
// specified structures").
//
// A private key the client sends is the one secret here. The schema has the
// client put it in the CertificateString when the service does not know it
// (ReplaceCertificate.CertificateString: "the client shall provide the private
// key as part of the string in the POST request") and has the service remove it
// from every response. The key is a Secret, is checked to match the certificate
// before anything is sent, and an error that carries any part of it is redacted.
// A response that carries a private key (against the schema) is not retained.

const (
	// MaxCertificateString bounds a CertificateString read or sent (a chain of a
	// few certificates is a few KiB).
	MaxCertificateString = 64 << 10
	// MaxKeyPEM bounds a private key sent.
	MaxKeyPEM = 16 << 10
	// MaxCSRString bounds a CSRString read.
	MaxCSRString = 16 << 10
	// MaxCertificates bounds the certificates read of a service.
	MaxCertificates = 256
	// maxChain bounds the certificates of a PEM chain that are decoded.
	maxChain = 16
	maxField = 256
)

// ErrCertificateTooLarge: a certificate, key or CSR string is over its limit.
var ErrCertificateTooLarge = errors.New("redfish: the certificate data is larger than the limit")

// CertificateTypes are the formats of Certificate.CertificateType (Certificate
// 1.11.0, CertificateType).
var CertificateTypes = []string{"PEM", "PEMchain", "PKCS7", "PKCS12"}

// ---- The service and its locations ----

// CertificateService is the CertificateService resource (CertificateService 1.2.2).
type CertificateService struct {
	URI string `json:"uri"`
	// LocationsURI is the CertificateLocations resource ("" when the service has none).
	LocationsURI string `json:"locations_uri,omitempty"`
	// EnrollmentCACertificatesURI is the collection of the server certificates of the
	// automatic enrollment servers (1.2.0).
	EnrollmentCACertificatesURI string `json:"enrollment_ca_certificates_uri,omitempty"`
	// GenerateCSR and ReplaceCertificate are true when the service has the action.
	GenerateCSR        bool `json:"generate_csr"`
	ReplaceCertificate bool `json:"replace_certificate"`
	// AutomaticEnrollmentEnabled is nil when the service does not say (1.2.0).
	AutomaticEnrollmentEnabled *bool    `json:"automatic_enrollment_enabled,omitempty"`
	EnrollmentTypes            []string `json:"enrollment_types,omitempty"`
}

func (c *Client) certServiceURI(ctx context.Context) (string, error) {
	u, err := c.rootLink(ctx, "CertificateService")
	if errors.Is(err, ErrNotFound) {
		return "", fmt.Errorf("%w: the service root links no CertificateService", ErrUnsupported)
	}
	return u, err
}

// CertificateService reads the CertificateService (ErrUnsupported when the
// service root links none).
func (c *Client) CertificateService(ctx context.Context) (CertificateService, error) {
	uri, err := c.certServiceURI(ctx)
	if err != nil {
		return CertificateService{}, err
	}
	p, _, err := c.getProps(ctx, uri)
	if err != nil {
		return CertificateService{}, err
	}
	return parseCertificateService(uri, p), nil
}

func parseCertificateService(uri string, p Props) CertificateService {
	acts := sub(p, "Actions")
	_, csr := acts["#CertificateService.GenerateCSR"].(map[string]any)
	_, rep := acts["#CertificateService.ReplaceCertificate"].(map[string]any)
	s := CertificateService{URI: uri, LocationsURI: linkOf(p, "CertificateLocations"), EnrollmentCACertificatesURI: linkOf(p, "EnrollmentCACertificates"),
		GenerateCSR: csr, ReplaceCertificate: rep}
	ace := sub(p, "AutomaticCertificateEnrollment")
	s.AutomaticEnrollmentEnabled = boolOf(ace, "ServiceEnabled")
	s.EnrollmentTypes = stringList(ace["EnrollmentTypes"])
	return s
}

// ParseCertificateLocations decodes a CertificateLocations resource into the
// paths of the certificates it lists (Links.Certificates, CertificateLocations
// 1.0.4): at most MaxCertificates, each a path of the service, without
// duplicates. It never panics.
func ParseCertificateLocations(data []byte) ([]string, error) {
	p, err := decodeProps(data)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrMalformed, err)
	}
	l, _ := sub(p, "Links")["Certificates"].([]any)
	var out []string
	for _, x := range l {
		m, _ := x.(map[string]any)
		id, _ := m["@odata.id"].(string)
		if u := servicePath(id); u != "" && len(out) < MaxCertificates && !slices.Contains(out, u) {
			out = append(out, u)
		}
	}
	return out, nil
}

// CertificateLocations lists the certificates the service holds, as the
// CertificateLocations resource of the service gives them (every certificate
// installed on the service, whatever collection holds it).
func (c *Client) CertificateLocations(ctx context.Context) ([]string, error) {
	svc, err := c.CertificateService(ctx)
	if err != nil {
		return nil, err
	}
	if svc.LocationsURI == "" {
		return nil, fmt.Errorf("%w: the CertificateService links no CertificateLocations", ErrUnsupported)
	}
	r, err := c.Get(ctx, svc.LocationsURI)
	if err != nil {
		return nil, err
	}
	return ParseCertificateLocations(r.Body)
}

// ---- A certificate ----

// Identifier is the Issuer or the Subject of a Certificate (Certificate 1.11.0,
// Identifier), as the service states it.
type Identifier struct {
	CommonName         string   `json:"common_name,omitempty"`
	Organization       string   `json:"organization,omitempty"`
	OrganizationalUnit string   `json:"organizational_unit,omitempty"`
	City               string   `json:"city,omitempty"`
	State              string   `json:"state,omitempty"`
	Country            string   `json:"country,omitempty"`
	Email              string   `json:"email,omitempty"`
	DisplayString      string   `json:"display_string,omitempty"`
	AlternativeNames   []string `json:"alternative_names,omitempty"`
}

func parseIdentifier(p Props) *Identifier {
	if p == nil {
		return nil
	}
	return &Identifier{CommonName: str(p, "CommonName"), Organization: str(p, "Organization"), OrganizationalUnit: str(p, "OrganizationalUnit"),
		City: str(p, "City"), State: str(p, "State"), Country: str(p, "Country"), Email: str(p, "Email"),
		DisplayString: str(p, "DisplayString"), AlternativeNames: stringList(p["AlternativeNames"])}
}

// Certificate is a Certificate resource (Certificate 1.11.0). The private key and
// the Password are never part of it: a CertificateString that carries a private
// key (the schema has the service remove it) is reduced to its certificates.
type Certificate struct {
	URI                      string      `json:"uri"`
	ID                       string      `json:"id"`
	Name                     string      `json:"name,omitempty"`
	Type                     string      `json:"type,omitempty"`
	UsageTypes               []string    `json:"usage_types,omitempty"`
	Subject                  *Identifier `json:"subject,omitempty"`
	Issuer                   *Identifier `json:"issuer,omitempty"`
	ValidNotBefore           string      `json:"valid_not_before,omitempty"`
	ValidNotAfter            string      `json:"valid_not_after,omitempty"`
	KeyUsage                 []string    `json:"key_usage,omitempty"`
	SerialNumber             string      `json:"serial_number,omitempty"`
	Fingerprint              string      `json:"fingerprint,omitempty"`
	FingerprintHashAlgorithm string      `json:"fingerprint_hash_algorithm,omitempty"`
	SignatureAlgorithm       string      `json:"signature_algorithm,omitempty"`
	State                    string      `json:"state,omitempty"`
	Health                   string      `json:"health,omitempty"`
	// Rekey and Renew are true when the certificate has the action.
	Rekey bool `json:"rekey"`
	Renew bool `json:"renew"`
	// PEM is the certificate string (certificates only), kept for `certs show --pem`.
	PEM string `json:"-"`
	// X509 is the certificate decoded by this library from the CertificateString:
	// what the certificate really says, which a client can compare with what the
	// service states above. X509Error says why it is absent.
	X509      *X509Info `json:"x509,omitempty"`
	X509Error string    `json:"x509_error,omitempty"`
}

// ParseCertificateResource decodes a Certificate resource. It never panics, and
// reads what the CertificateString holds with the bounds of ParseX509.
func ParseCertificateResource(uri string, data []byte) (Certificate, error) {
	p, err := decodeProps(data)
	if err != nil {
		return Certificate{}, fmt.Errorf("%w: %v", ErrMalformed, err)
	}
	return parseCertificate(uri, p), nil
}

func parseCertificate(uri string, p Props) Certificate {
	acts := sub(p, "Actions")
	_, rekey := acts["#Certificate.Rekey"].(map[string]any)
	_, renew := acts["#Certificate.Renew"].(map[string]any)
	cert := Certificate{URI: uri, ID: str(p, "Id"), Name: str(p, "Name"), Type: str(p, "CertificateType"), UsageTypes: stringList(p["CertificateUsageTypes"]),
		Subject: parseIdentifier(sub(p, "Subject")), Issuer: parseIdentifier(sub(p, "Issuer")),
		ValidNotBefore: str(p, "ValidNotBefore"), ValidNotAfter: str(p, "ValidNotAfter"), KeyUsage: stringList(p["KeyUsage"]),
		SerialNumber: str(p, "SerialNumber"), Fingerprint: str(p, "Fingerprint"), FingerprintHashAlgorithm: str(p, "FingerprintHashAlgorithm"),
		SignatureAlgorithm: str(p, "SignatureAlgorithm"), State: str(sub(p, "Status"), "State"), Health: str(sub(p, "Status"), "Health"),
		Rekey: rekey, Renew: renew}
	if cert.ID == "" {
		cert.ID = path.Base(uri)
	}
	s := str(p, "CertificateString")
	switch {
	case s == "":
		cert.X509Error = "the resource has no CertificateString"
	case cert.Type == "PKCS7" || cert.Type == "PKCS12":
		cert.X509Error = "a " + cert.Type + " certificate is not decoded by this client"
	default:
		cert.PEM = certificatesOnly(s)
		if info, err := ParseX509(s); err != nil {
			cert.X509Error = err.Error()
		} else {
			cert.X509 = &info
		}
	}
	return cert
}

// certificatesOnly keeps the CERTIFICATE blocks of a PEM string and nothing
// else (a private key a service should not send is dropped), "" past the bound.
func certificatesOnly(s string) string {
	if len(s) > MaxCertificateString {
		return ""
	}
	var out strings.Builder
	rest := []byte(s)
	for n := 0; n < maxChain; n++ {
		var b *pem.Block
		b, rest = pem.Decode(rest)
		if b == nil {
			break
		}
		if b.Type == "CERTIFICATE" {
			_ = pem.Encode(&out, &pem.Block{Type: "CERTIFICATE", Bytes: b.Bytes})
		}
	}
	return out.String()
}

// X509Info is what crypto/x509 reads in a certificate (RFC 5280): the first
// certificate of the string is the leaf, the others make the chain.
type X509Info struct {
	Version            int       `json:"version"`
	SerialNumber       string    `json:"serial_number"`
	Subject            string    `json:"subject"`
	Issuer             string    `json:"issuer"`
	NotBefore          time.Time `json:"not_before"`
	NotAfter           time.Time `json:"not_after"`
	SignatureAlgorithm string    `json:"signature_algorithm"`
	PublicKeyAlgorithm string    `json:"public_key_algorithm"`
	KeyBits            int       `json:"key_bits,omitempty"`
	KeyCurve           string    `json:"key_curve,omitempty"`
	// KeyUsage names the usages as the Certificate schema's KeyUsage enumeration does:
	// the Key Usage bits and the Extended Key Usage values of RFC 5280 4.2.1.3, 4.2.1.12.
	KeyUsage       []string `json:"key_usage,omitempty"`
	DNSNames       []string `json:"dns_names,omitempty"`
	IPAddresses    []string `json:"ip_addresses,omitempty"`
	EmailAddresses []string `json:"email_addresses,omitempty"`
	IsCA           bool     `json:"is_ca"`
	SelfSigned     bool     `json:"self_signed"`
	// SHA256Fingerprint is the SHA-256 of the DER, colon-separated hex.
	SHA256Fingerprint string `json:"sha256_fingerprint"`
	// ChainLength is the number of certificates in the string.
	ChainLength int `json:"chain_length"`
	// PrivateKeyPresent is true when the string carries a private key block, which
	// the schema has a service remove from its responses; the key is not read.
	PrivateKeyPresent bool `json:"private_key_present,omitempty"`
}

var keyUsageNames = []struct {
	bit  x509.KeyUsage
	name string
}{{x509.KeyUsageDigitalSignature, "DigitalSignature"}, {x509.KeyUsageContentCommitment, "NonRepudiation"},
	{x509.KeyUsageKeyEncipherment, "KeyEncipherment"}, {x509.KeyUsageDataEncipherment, "DataEncipherment"},
	{x509.KeyUsageKeyAgreement, "KeyAgreement"}, {x509.KeyUsageCertSign, "KeyCertSign"}, {x509.KeyUsageCRLSign, "CRLSigning"},
	{x509.KeyUsageEncipherOnly, "EncipherOnly"}, {x509.KeyUsageDecipherOnly, "DecipherOnly"}}

var extKeyUsageNames = map[x509.ExtKeyUsage]string{x509.ExtKeyUsageServerAuth: "ServerAuthentication", x509.ExtKeyUsageClientAuth: "ClientAuthentication",
	x509.ExtKeyUsageCodeSigning: "CodeSigning", x509.ExtKeyUsageEmailProtection: "EmailProtection", x509.ExtKeyUsageTimeStamping: "Timestamping",
	x509.ExtKeyUsageOCSPSigning: "OCSPSigning"}

func isPrivateKeyBlock(t string) bool { return strings.HasSuffix(t, "PRIVATE KEY") }

func colonHex(b []byte) string {
	if len(b) == 0 {
		return "00"
	}
	h := strings.ToUpper(hex.EncodeToString(b))
	var sb strings.Builder
	for i := 0; i < len(h); i += 2 {
		if i > 0 {
			sb.WriteByte(':')
		}
		sb.WriteString(h[i : i+2])
	}
	return sb.String()
}

func keyShape(pub crypto.PublicKey) (alg string, bits int, curve string) {
	switch k := pub.(type) {
	case *rsa.PublicKey:
		return "RSA", k.N.BitLen(), ""
	case *ecdsa.PublicKey:
		return "ECDSA", k.Curve.Params().BitSize, k.Curve.Params().Name
	case ed25519.PublicKey:
		return "Ed25519", 256, ""
	}
	return "unknown", 0, ""
}

// ParseX509 decodes the PEM string of a Certificate (type PEM or PEMchain, RFC
// 5280 structures) with crypto/x509. It is read-only and bounded: a string over
// MaxCertificateString or with more than 16 certificates is refused, and private
// key blocks are skipped without being parsed. It never panics.
func ParseX509(certString string) (X509Info, error) {
	if len(certString) > MaxCertificateString {
		return X509Info{}, ErrCertificateTooLarge
	}
	var leaf *x509.Certificate
	info := X509Info{}
	rest := []byte(certString)
	for blocks := 0; ; blocks++ {
		var b *pem.Block
		b, rest = pem.Decode(rest)
		if b == nil {
			break
		}
		if blocks >= 2*maxChain {
			return X509Info{}, fmt.Errorf("%w: more than %d PEM blocks", ErrMalformed, 2*maxChain)
		}
		switch {
		case isPrivateKeyBlock(b.Type):
			info.PrivateKeyPresent = true
		case b.Type == "CERTIFICATE":
			if info.ChainLength >= maxChain {
				return X509Info{}, fmt.Errorf("%w: more than %d certificates in the chain", ErrMalformed, maxChain)
			}
			info.ChainLength++
			if leaf != nil {
				continue
			}
			c, err := x509.ParseCertificate(b.Bytes)
			if err != nil {
				return X509Info{}, fmt.Errorf("%w: the certificate does not decode: %v", ErrMalformed, err)
			}
			leaf = c
		}
	}
	if leaf == nil {
		return X509Info{}, fmt.Errorf("%w: no CERTIFICATE block in the string", ErrMalformed)
	}
	sum := sha256.Sum256(leaf.Raw)
	info.Version, info.SerialNumber = leaf.Version, colonHex(leaf.SerialNumber.Bytes())
	info.Subject, info.Issuer = leaf.Subject.String(), leaf.Issuer.String()
	info.NotBefore, info.NotAfter = leaf.NotBefore.UTC(), leaf.NotAfter.UTC()
	info.SignatureAlgorithm = leaf.SignatureAlgorithm.String()
	info.PublicKeyAlgorithm, info.KeyBits, info.KeyCurve = keyShape(leaf.PublicKey)
	for _, u := range keyUsageNames {
		if leaf.KeyUsage&u.bit != 0 {
			info.KeyUsage = append(info.KeyUsage, u.name)
		}
	}
	for _, u := range leaf.ExtKeyUsage {
		if n, ok := extKeyUsageNames[u]; ok {
			info.KeyUsage = append(info.KeyUsage, n)
		}
	}
	info.DNSNames, info.EmailAddresses = leaf.DNSNames, leaf.EmailAddresses
	for _, ip := range leaf.IPAddresses {
		info.IPAddresses = append(info.IPAddresses, ip.String())
	}
	info.IsCA = leaf.IsCA
	info.SelfSigned = leaf.CheckSignatureFrom(leaf) == nil
	info.SHA256Fingerprint = colonHex(sum[:])
	return info, nil
}

var certID = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)

// resolveCertificate turns an Id or a path into the path of a certificate: a
// path of the service is used as given (the answer's @odata.type is checked when
// it is read); an Id must name exactly one of the certificates the service
// locates.
func (c *Client) resolveCertificate(ctx context.Context, idOrURI string) (string, error) {
	if strings.HasPrefix(idOrURI, "/") {
		if !isServicePath(idOrURI) {
			return "", fmt.Errorf("redfish: %q is not a path of the service (/redfish/...)", clip(idOrURI))
		}
		return idOrURI, nil
	}
	if !certID.MatchString(idOrURI) {
		return "", fmt.Errorf("redfish: %q is neither a certificate Id nor a path of the service", clip(idOrURI))
	}
	uris, err := c.CertificateLocations(ctx)
	if err != nil {
		return "", err
	}
	var hit []string
	for _, u := range uris {
		if path.Base(u) == idOrURI {
			hit = append(hit, u)
		}
	}
	switch len(hit) {
	case 0:
		return "", fmt.Errorf("%w: no certificate %q in the CertificateLocations", ErrNotFound, idOrURI)
	case 1:
		return hit[0], nil
	}
	return "", fmt.Errorf("%w: %d certificates have the Id %q (%s): give the path", ErrAmbiguous, len(hit), idOrURI, strings.Join(hit, ", "))
}

func (c *Client) readCertificate(ctx context.Context, uri string) (Certificate, error) {
	p, _, err := c.getProps(ctx, uri)
	if err != nil {
		return Certificate{}, err
	}
	if t := str(p, "@odata.type"); !strings.HasPrefix(t, "#Certificate.") {
		return Certificate{}, fmt.Errorf("%w: %s is not a Certificate (@odata.type %q)", ErrMalformed, uri, clip(t))
	}
	return parseCertificate(uri, p), nil
}

// Certificate reads one certificate, named by its Id (one of the CertificateLocations) or its path,
// and decodes its CertificateString with ParseX509.
func (c *Client) Certificate(ctx context.Context, idOrURI string) (Certificate, error) {
	uri, err := c.resolveCertificate(ctx, idOrURI)
	if err != nil {
		return Certificate{}, err
	}
	return c.readCertificate(ctx, uri)
}

// Certificates reads every certificate the service locates (at most
// MaxCertificates; more is an error, read them one by one then). One that
// disappeared since the listing is skipped.
func (c *Client) Certificates(ctx context.Context) ([]Certificate, error) {
	uris, err := c.CertificateLocations(ctx)
	if err != nil {
		return nil, err
	}
	if len(uris) >= MaxCertificates {
		return nil, fmt.Errorf("redfish: the service locates %d or more certificates, over the limit of %d: read them with Certificate", len(uris), MaxCertificates)
	}
	var out []Certificate
	for _, u := range uris {
		cert, err := c.readCertificate(ctx, u)
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out = append(out, cert)
	}
	return out, nil
}

// ---- What is sent: a certificate and, perhaps, its private key ----

// CertificateInput is a certificate to install or to put in place of another.
type CertificateInput struct {
	// CertificateString is the certificate: PEM, a PEM chain, a PEM PKCS7 or a
	// Base64 PKCS12, as CertificateType says. It must not carry a private key (give
	// PrivateKey), so that the key can be kept out of errors.
	CertificateString string
	// CertificateType is one of CertificateTypes; empty: PEM.
	CertificateType string
	// PrivateKey is the PEM private key of the certificate, for a service that does
	// not know it (a certificate that was not made from a CSR of this service). It
	// is appended to the CertificateString (the schema's rule), is checked to match the
	// certificate, is never printed and is redacted from errors. An encrypted key is
	// refused: the schema has no parameter for its passphrase. Not for PKCS12, which
	// carries its key.
	PrivateKey Secret
	// Password is the password of a PKCS12 bundle (Password of ReplaceCertificate, 1.1.0).
	Password Secret
}

func (in CertificateInput) typ() string {
	if in.CertificateType == "" {
		return "PEM"
	}
	return in.CertificateType
}

func hasControl(s string) bool { return strings.ContainsAny(s, "\x00") }

// CheckPrivateKey verifies a PEM private key against the certificate it is for:
// one block, of an accepted type (PKCS#8, PKCS#1 RSA or SEC 1 EC; not encrypted),
// within MaxKeyPEM, whose public key is the certificate's. The error never holds
// any of the key.
func CheckPrivateKey(key Secret, certString string) error {
	k := key.Reveal()
	if len(k) > MaxKeyPEM {
		return fmt.Errorf("%w: the private key is larger than %d bytes", ErrCertificateTooLarge, MaxKeyPEM)
	}
	b, rest := pem.Decode([]byte(k))
	if b == nil || !isPrivateKeyBlock(b.Type) {
		return errors.New("redfish: the private key is not a PEM PRIVATE KEY block")
	}
	if extra, _ := pem.Decode(rest); extra != nil {
		return errors.New("redfish: the private key holds more than one PEM block")
	}
	if b.Type == "ENCRYPTED PRIVATE KEY" || b.Headers["DEK-Info"] != "" || b.Headers["Proc-Type"] != "" {
		return errors.New("redfish: an encrypted private key is refused: the schema has no parameter for its passphrase")
	}
	var priv crypto.PrivateKey
	var err error
	switch b.Type {
	case "PRIVATE KEY":
		priv, err = x509.ParsePKCS8PrivateKey(b.Bytes)
	case "RSA PRIVATE KEY":
		priv, err = x509.ParsePKCS1PrivateKey(b.Bytes)
	case "EC PRIVATE KEY":
		priv, err = x509.ParseECPrivateKey(b.Bytes)
	default:
		return fmt.Errorf("redfish: the private key block type %q is not accepted", clip(b.Type))
	}
	if err != nil {
		return errors.New("redfish: the private key does not decode")
	}
	signer, ok := priv.(interface{ Public() crypto.PublicKey })
	if !ok {
		return errors.New("redfish: the private key has no public part")
	}
	leaf, err := leafOf(certString)
	if err != nil {
		return err
	}
	pub, ok := leaf.PublicKey.(interface{ Equal(crypto.PublicKey) bool })
	if !ok || !pub.Equal(signer.Public()) {
		return errors.New("redfish: the private key does not belong to the certificate")
	}
	return nil
}

func leafOf(certString string) (*x509.Certificate, error) {
	rest := []byte(certString)
	for n := 0; n < 2*maxChain; n++ {
		var b *pem.Block
		b, rest = pem.Decode(rest)
		if b == nil {
			break
		}
		if b.Type == "CERTIFICATE" {
			c, err := x509.ParseCertificate(b.Bytes)
			if err != nil {
				return nil, fmt.Errorf("%w: the certificate does not decode", ErrMalformed)
			}
			return c, nil
		}
	}
	return nil, fmt.Errorf("%w: no CERTIFICATE block in the string", ErrMalformed)
}

// keyNeedles are the pieces of a private key an error must not carry: the whole
// text and each line of its body.
func keyNeedles(key Secret) []Secret {
	if key == "" {
		return nil
	}
	out := []Secret{key}
	for _, ln := range strings.Split(strings.ReplaceAll(key.Reveal(), "\r", ""), "\n") {
		if ln = strings.TrimSpace(ln); len(ln) >= 16 && !strings.HasPrefix(ln, "-----") {
			out = append(out, Secret(ln))
		}
	}
	return out
}

func redactAll(err error, secrets ...Secret) error {
	for _, s := range secrets {
		err = redact(err, s)
	}
	return err
}

// sensitive are the secrets of an input, for redactAll.
func (in CertificateInput) sensitive() []Secret {
	return append(keyNeedles(in.PrivateKey), in.Password)
}

// payload checks a CertificateInput and builds the CertificateString and the
// type to send.
func (in CertificateInput) payload() (certString, typ string, err error) {
	typ = in.typ()
	if !slices.Contains(CertificateTypes, typ) {
		return "", "", fmt.Errorf("redfish: CertificateType %q is not one of %s", clip(typ), strings.Join(CertificateTypes, ", "))
	}
	s := in.CertificateString
	switch {
	case s == "" || hasControl(s):
		return "", "", errors.New("redfish: the certificate string is empty or holds a NUL")
	case len(s) > MaxCertificateString:
		return "", "", fmt.Errorf("%w: the certificate string is larger than %d bytes", ErrCertificateTooLarge, MaxCertificateString)
	case len(in.Password) > 1024 || hasControl(in.Password.Reveal()):
		return "", "", errors.New("redfish: the password is longer than 1024 bytes or holds a NUL")
	case in.Password != "" && typ != "PKCS12":
		return "", "", errors.New("redfish: a Password is for a PKCS12 certificate only")
	}
	switch typ {
	case "PEM", "PEMchain":
		info, err := ParseX509(s)
		if err != nil {
			return "", "", err
		}
		if info.PrivateKeyPresent {
			return "", "", errors.New("redfish: the certificate string carries a private key: give it as the PrivateKey, apart")
		}
		if typ == "PEM" && info.ChainLength != 1 {
			return "", "", fmt.Errorf("redfish: a PEM certificate is one certificate, the string has %d (use PEMchain)", info.ChainLength)
		}
		if in.PrivateKey != "" {
			if err := CheckPrivateKey(in.PrivateKey, s); err != nil {
				return "", "", err
			}
			s = strings.TrimRight(s, "\r\n") + "\n" + strings.TrimRight(in.PrivateKey.Reveal(), "\r\n") + "\n"
		}
	case "PKCS7":
		if b, _ := pem.Decode([]byte(s)); b == nil {
			return "", "", errors.New("redfish: a PKCS7 certificate is a PEM string")
		}
		if in.PrivateKey != "" {
			return "", "", errors.New("redfish: a private key is not sent with a PKCS7 certificate")
		}
	case "PKCS12":
		if _, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(s), "")); err != nil {
			return "", "", errors.New("redfish: a PKCS12 certificate is a Base64 string with padding")
		}
		if in.PrivateKey != "" {
			return "", "", errors.New("redfish: a PKCS12 bundle carries its key: no PrivateKey")
		}
	}
	return s, typ, nil
}

// ---- Install, replace, delete ----

// supportedCertificates reads @Redfish.SupportedCertificates of a collection
// (DSP0266 9.9.13) after checking that it is a CertificateCollection.
func (c *Client) certificateCollection(ctx context.Context, coll string) (supported []string, err error) {
	if !isServicePath(coll) {
		return nil, fmt.Errorf("redfish: %q is not a path of the service (/redfish/...)", clip(coll))
	}
	p, _, err := c.getProps(ctx, coll)
	if err != nil {
		return nil, err
	}
	if t := str(p, "@odata.type"); !strings.HasPrefix(t, "#CertificateCollection.") {
		return nil, fmt.Errorf("%w: %s is not a CertificateCollection (@odata.type %q)", ErrMalformed, coll, clip(t))
	}
	return stringList(p["@Redfish.SupportedCertificates"]), nil
}

// InstallCertificate adds a certificate to a certificate collection: a POST to
// the collection with CertificateString and CertificateType (requiredOnCreate of
// Certificate 1.11.0; DSP0266 9.9.13: the formats the collection supports are
// checked first). The service shall answer 201 with the Location of the new
// resource, which is read; a 202 is returned as the Outcome's Monitor.
func (c *Client) InstallCertificate(ctx context.Context, collection string, in CertificateInput) (Certificate, Outcome, error) {
	s, typ, err := in.payload()
	if err != nil {
		return Certificate{}, Outcome{}, err
	}
	supported, err := c.certificateCollection(ctx, collection)
	if err != nil {
		return Certificate{}, Outcome{}, err
	}
	if len(supported) > 0 && !slices.Contains(supported, typ) {
		return Certificate{}, Outcome{}, fmt.Errorf("%w: the collection supports %s, not %s", ErrUnsupported, strings.Join(supported, ", "), typ)
	}
	body := Props{"CertificateString": s, "CertificateType": typ}
	if in.Password != "" {
		body["Password"] = in.Password.Reveal()
	}
	r, err := c.Post(ctx, collection, body)
	if err != nil {
		return Certificate{}, Outcome{}, redactAll(err, in.sensitive()...)
	}
	if r.IsTask() {
		return Certificate{}, outcomeOf(r, Outcome{Changed: true, Message: "Accepted the certificate for " + collection}), nil
	}
	loc := r.Location()
	if loc == "" {
		if p, derr := decodeProps(r.Body); derr == nil {
			loc = servicePath(str(p, "@odata.id"))
		}
	}
	if loc == "" || path.Dir(loc) != collection {
		return Certificate{}, Outcome{}, fmt.Errorf("%w: the answer to the certificate is not the Location of a member of %s", ErrMalformed, collection)
	}
	cert, err := c.readCertificate(ctx, loc)
	if err != nil {
		return Certificate{}, Outcome{}, err
	}
	return cert, Outcome{Changed: true, Message: "Installed certificate " + loc}, nil
}

// actionOf finds an action of a resource, its target and the values its
// parameters may take (ActionInfo or AllowableValues, DSP0266 9.9.2, 9.9.6).
func (c *Client) actionOf(ctx context.Context, res string, p Props, name string) (target string, values map[string][]string, err error) {
	act := sub(sub(p, "Actions"), name)
	if act == nil {
		return "", nil, fmt.Errorf("%w: action %s not found on %s", ErrUnsupported, name, res)
	}
	target = servicePath(str(act, "target"))
	if target == "" {
		return "", nil, fmt.Errorf("%w: the target of action %s is missing", ErrMalformed, name)
	}
	return target, c.ActionValues(ctx, act), nil
}

// checkValues refuses a parameter the service does not list and a value it does
// not allow, before anything is sent.
func checkValues(name string, body Props, values map[string][]string) error {
	for k, v := range body {
		allowed, listed := values[k]
		if len(values) > 0 && !listed {
			return fmt.Errorf("%w: the service does not list the parameter %s of %s", ErrUnsupported, k, name)
		}
		var vs []string
		switch x := v.(type) {
		case string:
			vs = []string{x}
		case []string:
			vs = x
		}
		for _, s := range vs {
			if len(allowed) > 0 && !slices.Contains(allowed, s) {
				return fmt.Errorf("%w: %s %q is not supported by the service (supported: %s)", ErrUnsupported, k, clip(s), strings.Join(allowed, ", "))
			}
		}
	}
	return nil
}

// ReplaceCertificate runs the CertificateService.ReplaceCertificate action:
// the certificate at certURI (an Id or a path) is replaced by the one given. The
// service shall answer with the Location of the new certificate resource, which
// is returned (the old URI when the service names none). CertificateType is
// checked against the ActionInfo or AllowableValues. A certificate made from the
// CSR of this service needs no PrivateKey: the service kept the key.
func (c *Client) ReplaceCertificate(ctx context.Context, certURI string, in CertificateInput) (string, Outcome, error) {
	const name = "#CertificateService.ReplaceCertificate"
	s, typ, err := in.payload()
	if err != nil {
		return "", Outcome{}, err
	}
	svcURI, err := c.certServiceURI(ctx)
	if err != nil {
		return "", Outcome{}, err
	}
	p, _, err := c.getProps(ctx, svcURI)
	if err != nil {
		return "", Outcome{}, err
	}
	target, values, err := c.actionOf(ctx, svcURI, p, name)
	if err != nil {
		return "", Outcome{}, err
	}
	old, err := c.resolveCertificate(ctx, certURI)
	if err != nil {
		return "", Outcome{}, err
	}
	if _, err := c.readCertificate(ctx, old); err != nil {
		return "", Outcome{}, err
	}
	body := Props{"CertificateString": s, "CertificateType": typ, "CertificateUri": map[string]any{"@odata.id": old}}
	if in.Password != "" {
		body["Password"] = in.Password.Reveal()
	}
	check := Props{"CertificateType": typ, "CertificateUri": nil, "CertificateString": nil}
	if in.Password != "" {
		check["Password"] = nil
	}
	if err := checkValues(name, check, values); err != nil {
		return "", Outcome{}, err
	}
	r, err := c.Post(ctx, target, body)
	if err != nil {
		return "", Outcome{}, redactAll(err, in.sensitive()...)
	}
	uri := r.Location()
	if uri == "" {
		uri = old
	}
	return uri, outcomeOf(r, Outcome{Changed: true, Message: "Replaced " + old + " with " + uri}), nil
}

// DeleteCertificate deletes a certificate, named by its Id or path (Certificate:
// deletable). It is read first, so that only a Certificate resource is deleted.
// A certificate that is already gone (404) is not a change.
func (c *Client) DeleteCertificate(ctx context.Context, idOrURI string) (Outcome, error) {
	uri, err := c.resolveCertificate(ctx, idOrURI)
	if err != nil {
		return Outcome{}, err
	}
	if _, err := c.readCertificate(ctx, uri); err != nil {
		if errors.Is(err, ErrNotFound) {
			return Outcome{Message: uri + " does not exist"}, nil
		}
		return Outcome{}, err
	}
	r, err := c.Delete(ctx, uri, "")
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return Outcome{Message: uri + " does not exist"}, nil
		}
		return Outcome{}, err
	}
	return outcomeOf(r, Outcome{Changed: true, Message: "Deleted certificate " + uri}), nil
}

// ---- Certificate signing requests ----

// CSRRequest is the parameters of CertificateService.GenerateCSR (1.2.2). Only
// CertificateCollection is required by the schema; a service lists the ones it
// needs in its ActionInfo.
type CSRRequest struct {
	// CertificateCollection is the collection the signed certificate will be
	// installed in (required).
	CertificateCollection string
	CommonName            string
	AlternativeNames      []string
	Organization          string
	OrganizationalUnit    string
	City, State           string
	// Country is the two-letter ISO code.
	Country string
	Email   string
	// GivenName, Surname, Initials, ContactPerson and UnstructuredName are the
	// other subject attributes of the schema.
	GivenName, Surname, Initials, ContactPerson, UnstructuredName string
	// KeyPairAlgorithm is a name of the TCG algorithm registry (TPM_ALG_RSA,
	// TPM_ALG_ECDSA), KeyBitLength and KeyCurveID (TPM_ECC_NIST_P256...) go with it.
	KeyPairAlgorithm string
	KeyBitLength     int
	KeyCurveID       string
	// KeyUsage names are the values of the Certificate schema's KeyUsage.
	KeyUsage []string
	// ChallengePassword is the RFC 2985 challengePassword attribute: a secret, never
	// printed, redacted from errors.
	ChallengePassword Secret
}

// KeyUsageValues are the values of KeyUsage (Certificate 1.11.0, KeyUsage).
var KeyUsageValues = []string{"DigitalSignature", "NonRepudiation", "KeyEncipherment", "DataEncipherment", "KeyAgreement", "KeyCertSign", "CRLSigning",
	"EncipherOnly", "DecipherOnly", "ServerAuthentication", "ClientAuthentication", "CodeSigning", "EmailProtection", "Timestamping", "OCSPSigning"}

func checkText(name, v string) error {
	if len(v) > maxField || strings.ContainsAny(v, "\x00\r\n") {
		return fmt.Errorf("redfish: %s is longer than %d bytes or holds a control character", name, maxField)
	}
	return nil
}

func (r CSRRequest) body() (Props, error) {
	if !isServicePath(r.CertificateCollection) {
		return nil, fmt.Errorf("redfish: the certificate collection %q is not a path of the service (/redfish/...)", clip(r.CertificateCollection))
	}
	body := Props{"CertificateCollection": map[string]any{"@odata.id": r.CertificateCollection}}
	for k, v := range map[string]string{"CommonName": r.CommonName, "Organization": r.Organization, "OrganizationalUnit": r.OrganizationalUnit,
		"City": r.City, "State": r.State, "Country": r.Country, "Email": r.Email, "GivenName": r.GivenName, "Surname": r.Surname,
		"Initials": r.Initials, "ContactPerson": r.ContactPerson, "UnstructuredName": r.UnstructuredName,
		"KeyPairAlgorithm": r.KeyPairAlgorithm, "KeyCurveId": r.KeyCurveID} {
		if err := checkText(k, v); err != nil {
			return nil, err
		}
		if v != "" {
			body[k] = v
		}
	}
	if c := r.Country; c != "" && (len(c) != 2 || strings.Trim(c, "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz") != "") {
		return nil, errors.New("redfish: Country is a two-letter ISO code")
	}
	if len(r.AlternativeNames) > maxParams || len(r.KeyUsage) > len(KeyUsageValues) {
		return nil, errors.New("redfish: too many alternative names or key usages")
	}
	for _, n := range r.AlternativeNames {
		if n == "" {
			return nil, errors.New("redfish: an empty alternative name")
		}
		if err := checkText("AlternativeNames", n); err != nil {
			return nil, err
		}
	}
	for _, u := range r.KeyUsage {
		if !slices.Contains(KeyUsageValues, u) {
			return nil, fmt.Errorf("redfish: KeyUsage %q is not one of %s", clip(u), strings.Join(KeyUsageValues, ", "))
		}
	}
	if r.KeyBitLength < 0 || r.KeyBitLength > 16384 {
		return nil, errors.New("redfish: KeyBitLength is out of range")
	}
	if len(r.ChallengePassword) > 1024 || hasControl(r.ChallengePassword.Reveal()) {
		return nil, errors.New("redfish: the challenge password is longer than 1024 bytes or holds a NUL")
	}
	if len(r.AlternativeNames) > 0 {
		body["AlternativeNames"] = r.AlternativeNames
	}
	if len(r.KeyUsage) > 0 {
		body["KeyUsage"] = r.KeyUsage
	}
	if r.KeyBitLength > 0 {
		body["KeyBitLength"] = r.KeyBitLength
	}
	if r.ChallengePassword != "" {
		body["ChallengePassword"] = r.ChallengePassword.Reveal()
	}
	return body, nil
}

// CSR is the answer of a GenerateCSR, Rekey or Renew action: the PEM
// PKCS#10 request a CA signs. The private key stays in the service ("should not
// be part of the response"); an answer that carries one is refused.
type CSR struct {
	// PEM is the CSRString.
	PEM string `json:"pem"`
	// CollectionURI is where the signed certificate is installed (GenerateCSR).
	CollectionURI string `json:"collection_uri,omitempty"`
	// CertificateURI is the certificate the signed request replaces (Rekey, Renew).
	CertificateURI string `json:"certificate_uri,omitempty"`
	// Info is the request decoded by this library; InfoError says why it is absent.
	Info      *CSRInfo `json:"info,omitempty"`
	InfoError string   `json:"info_error,omitempty"`
}

// CSRInfo is what crypto/x509 reads in a PKCS#10 request (RFC 2986).
type CSRInfo struct {
	Subject            string   `json:"subject"`
	SignatureAlgorithm string   `json:"signature_algorithm"`
	PublicKeyAlgorithm string   `json:"public_key_algorithm"`
	KeyBits            int      `json:"key_bits,omitempty"`
	KeyCurve           string   `json:"key_curve,omitempty"`
	DNSNames           []string `json:"dns_names,omitempty"`
	IPAddresses        []string `json:"ip_addresses,omitempty"`
	EmailAddresses     []string `json:"email_addresses,omitempty"`
	// SignatureValid is the result of CheckSignature: the request proves possession of the key.
	SignatureValid bool `json:"signature_valid"`
}

// ParseCSR decodes a PEM certificate signing request with crypto/x509, bounded
// by MaxCSRString. It never panics.
func ParseCSR(csrPEM string) (CSRInfo, error) {
	if len(csrPEM) > MaxCSRString {
		return CSRInfo{}, ErrCertificateTooLarge
	}
	b, _ := pem.Decode([]byte(csrPEM))
	if b == nil || (b.Type != "CERTIFICATE REQUEST" && b.Type != "NEW CERTIFICATE REQUEST") {
		return CSRInfo{}, fmt.Errorf("%w: no CERTIFICATE REQUEST block in the string", ErrMalformed)
	}
	r, err := x509.ParseCertificateRequest(b.Bytes)
	if err != nil {
		return CSRInfo{}, fmt.Errorf("%w: the request does not decode: %v", ErrMalformed, err)
	}
	info := CSRInfo{Subject: r.Subject.String(), SignatureAlgorithm: r.SignatureAlgorithm.String(), DNSNames: r.DNSNames,
		EmailAddresses: r.EmailAddresses, SignatureValid: r.CheckSignature() == nil}
	info.PublicKeyAlgorithm, info.KeyBits, info.KeyCurve = keyShape(r.PublicKey)
	for _, ip := range r.IPAddresses {
		info.IPAddresses = append(info.IPAddresses, ip.String())
	}
	return info, nil
}

// ParseCSRResponse decodes the body of the answer to GenerateCSR, Rekey or Renew
// (GenerateCSRResponse, RekeyResponse, RenewResponse): CSRString is required, as
// PEM and within MaxCSRString, and carries no private key. It never panics.
func ParseCSRResponse(data []byte) (CSR, error) {
	p, err := decodeProps(data)
	if err != nil {
		return CSR{}, fmt.Errorf("%w: the CSR answer: %v", ErrMalformed, err)
	}
	s := str(p, "CSRString")
	if s == "" {
		return CSR{}, fmt.Errorf("%w: the answer has no CSRString", ErrMalformed)
	}
	if len(s) > MaxCSRString {
		return CSR{}, ErrCertificateTooLarge
	}
	for rest := []byte(s); ; {
		var b *pem.Block
		b, rest = pem.Decode(rest)
		if b == nil {
			break
		}
		if isPrivateKeyBlock(b.Type) {
			return CSR{}, fmt.Errorf("%w: the CSRString carries a private key, which the schema forbids: it is not kept", ErrMalformed)
		}
	}
	csr := CSR{PEM: s, CollectionURI: linkOf(p, "CertificateCollection"), CertificateURI: linkOf(p, "Certificate")}
	info, err := ParseCSR(s)
	if err != nil {
		if b, _ := pem.Decode([]byte(s)); b == nil {
			return CSR{}, fmt.Errorf("%w: the CSRString is not PEM", ErrMalformed)
		}
		csr.InfoError = err.Error()
		return csr, nil
	}
	csr.Info = &info
	return csr, nil
}

// GenerateCSR runs CertificateService.GenerateCSR: the service makes a key pair,
// keeps the private key for the installation of the signed certificate, and
// returns the request. Each parameter is checked against the service's
// ActionInfo (or AllowableValues) and the collection must be a
// CertificateCollection of the service.
func (c *Client) GenerateCSR(ctx context.Context, req CSRRequest) (CSR, Outcome, error) {
	const name = "#CertificateService.GenerateCSR"
	body, err := req.body()
	if err != nil {
		return CSR{}, Outcome{}, err
	}
	svcURI, err := c.certServiceURI(ctx)
	if err != nil {
		return CSR{}, Outcome{}, err
	}
	p, _, err := c.getProps(ctx, svcURI)
	if err != nil {
		return CSR{}, Outcome{}, err
	}
	target, values, err := c.actionOf(ctx, svcURI, p, name)
	if err != nil {
		return CSR{}, Outcome{}, err
	}
	if _, err := c.certificateCollection(ctx, req.CertificateCollection); err != nil {
		return CSR{}, Outcome{}, err
	}
	if err := checkValues(name, body, values); err != nil {
		return CSR{}, Outcome{}, err
	}
	return c.csrAction(ctx, target, body, req.ChallengePassword, "Generated a certificate signing request for "+req.CertificateCollection)
}

func (c *Client) csrAction(ctx context.Context, target string, body Props, challenge Secret, msg string) (CSR, Outcome, error) {
	r, err := c.Post(ctx, target, body)
	if err != nil {
		return CSR{}, Outcome{}, redact(err, challenge)
	}
	csr, err := ParseCSRResponse(r.Body)
	if err != nil {
		return CSR{}, Outcome{}, err
	}
	return csr, outcomeOf(r, Outcome{Changed: true, Message: msg}), nil
}

// RekeyOptions are the parameters of Certificate.Rekey (1.11.0).
type RekeyOptions struct {
	KeyPairAlgorithm  string
	KeyBitLength      int
	KeyCurveID        string
	ChallengePassword Secret
}

func (c *Client) certificateAction(ctx context.Context, idOrURI, name string, body Props, challenge Secret, msg string) (CSR, Outcome, error) {
	uri, err := c.resolveCertificate(ctx, idOrURI)
	if err != nil {
		return CSR{}, Outcome{}, err
	}
	p, _, err := c.getProps(ctx, uri)
	if err != nil {
		return CSR{}, Outcome{}, err
	}
	if t := str(p, "@odata.type"); !strings.HasPrefix(t, "#Certificate.") {
		return CSR{}, Outcome{}, fmt.Errorf("%w: %s is not a Certificate (@odata.type %q)", ErrMalformed, uri, clip(t))
	}
	target, values, err := c.actionOf(ctx, uri, p, name)
	if err != nil {
		return CSR{}, Outcome{}, err
	}
	if err := checkValues(name, body, values); err != nil {
		return CSR{}, Outcome{}, err
	}
	return c.csrAction(ctx, target, body, challenge, msg+" "+uri)
}

// RekeyCertificate runs Certificate.Rekey: the service makes a new key pair for
// the certificate and returns a CSR for it (the private key stays in the
// service). ErrUnsupported when the certificate has no such action.
func (c *Client) RekeyCertificate(ctx context.Context, idOrURI string, o RekeyOptions) (CSR, Outcome, error) {
	body := Props{}
	for k, v := range map[string]string{"KeyPairAlgorithm": o.KeyPairAlgorithm, "KeyCurveId": o.KeyCurveID} {
		if err := checkText(k, v); err != nil {
			return CSR{}, Outcome{}, err
		}
		if v != "" {
			body[k] = v
		}
	}
	if o.KeyBitLength < 0 || o.KeyBitLength > 16384 || len(o.ChallengePassword) > 1024 || hasControl(o.ChallengePassword.Reveal()) {
		return CSR{}, Outcome{}, errors.New("redfish: KeyBitLength is out of range, or the challenge password is too long")
	}
	if o.KeyBitLength > 0 {
		body["KeyBitLength"] = o.KeyBitLength
	}
	if o.ChallengePassword != "" {
		body["ChallengePassword"] = o.ChallengePassword.Reveal()
	}
	return c.certificateAction(ctx, idOrURI, "#Certificate.Rekey", body, o.ChallengePassword, "Generated a CSR with a new key pair for")
}

// RenewCertificate runs Certificate.Renew: the service returns a CSR made from
// the existing information and key pair of the certificate. ErrUnsupported when
// the certificate has no such action.
func (c *Client) RenewCertificate(ctx context.Context, idOrURI string, challenge Secret) (CSR, Outcome, error) {
	if len(challenge) > 1024 || hasControl(challenge.Reveal()) {
		return CSR{}, Outcome{}, errors.New("redfish: the challenge password is longer than 1024 bytes or holds a NUL")
	}
	body := Props{}
	if challenge != "" {
		body["ChallengePassword"] = challenge.Reveal()
	}
	return c.certificateAction(ctx, idOrURI, "#Certificate.Renew", body, challenge, "Generated a CSR with the existing key pair for")
}
