package redfish

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Security: the integrity, trust and key resources a BMC publishes, READ ONLY.
// There is no Ansible oracle for this area: the reference is the specification,
// read as published. DSP8010 2025.4 schemas (redfish.dmtf.org/schemas/v1):
// ComponentIntegrity 1.5.0 (SPDM and TPM identity and measurements, actions
// SPDMGetSignedMeasurements and TPMGetSignedMeasurements), TrustedComponent
// 1.5.0, SecurityPolicy 1.1.0 (SPDM and TLS policy, trusted and revoked
// certificates), KeyService 1.0.1, KeyPolicy 1.0.1 and Key 1.4.1 (the NVMe-oF
// secrets of KeyService.NVMeoFSecrets). DSP0266 1.23.0 only names the
// SecurityPolicy (12.9.3: "Services may perform additional verification based on
// other factors, such as the configuration of the SecurityPolicy resource") and
// 13.2 Sensitive data, cited by certs.go. DSP0274 (SPDM) was not opened: nothing
// here parses an SPDM message or checks a signature.
//
// What the schemas say, and the code follows:
//
//   - A measurement is a Base64 string with padding, not hex (ComponentIntegrity
//     1.5.0: "shall contain a Base64-encoded string, with padding characters, of
//     the measurement using the hash algorithm indicated by
//     MeasurementHashAlgorithm"; "shall not contain a raw bit stream"). Each
//     measurement is decoded strictly and given as hex as well (Digest).
//   - SPDMGetSignedMeasurements "shall generate a cryptographic signed statement
//     over the given nonce and measurements": it asks the Requester to run SPDM
//     GET_MEASUREMENTS exchanges with the device and returns the answer. It
//     changes no property of the service (the schema has no state in its
//     response), so it is run here, as a POST, with its parameters checked
//     against the ActionInfo or AllowableValues like every action. The response
//     SignedMeasurements "shall be a concatenation of SPDM 'VCA' and
//     'GET_MEASUREMENTS' requests and responses" whose last response carries the
//     signature: this client does NOT verify it (SignedMeasurements.Verified is
//     always false and says so); the blob and what the service says about the
//     signer are returned for a verifier that implements DSP0274.
//   - Key.KeyString is the key itself. A service may return it (the DMTF mockup
//     does): it is never kept, printed or put in an error (NVMeoFSecret.KeyPresent
//     says it was there).
//   - There is no write here: no policy change, no revocation, no key creation.

// Bounds of the security reads.
const (
	// MaxSecurityMembers bounds the members read of a security collection.
	MaxSecurityMembers = 256
	// MaxMeasurements bounds the measurements kept of one measurement set.
	MaxMeasurements = 1024
	// MaxDigestText bounds the Base64 text of one measurement or summary (a
	// digest is at most 64 bytes; this leaves room for a longer algorithm).
	MaxDigestText = 4096
	// MaxSignedBlobText bounds the Base64 text of SignedMeasurements.
	MaxSignedBlobText = 256 << 10
	// MaxSignedMeasurementsBytes bounds the answer of SPDMGetSignedMeasurements.
	MaxSignedMeasurementsBytes = 2 * MaxSignedBlobText
	// maxSecText bounds a text of these resources that is kept.
	maxSecText = 1024
)

// ErrBadDigest: a measurement or blob is not a Base64 string with padding.
var ErrBadDigest = errors.New("redfish: not a Base64 string with padding")

var base64Pattern = regexp.MustCompile(`^[A-Za-z0-9+/]+={0,2}$`)

// Digest is a binary value the schemas send as Base64 (a measurement, a
// summary, a signed statement): kept as sent, decoded, and given as hex.
type Digest struct {
	Base64 string `json:"base64"`
	// Hex is the decoded value, lower-case hex (for a signed statement, the
	// first 64 bytes only: see Size).
	Hex string `json:"hex"`
	// Size is the decoded length in bytes.
	Size int `json:"size"`
}

// DecodeDigest decodes a Base64 string with padding (the pattern
// ^[A-Za-z0-9+/]+={0,2}$ of ComponentIntegrity 1.5.0), strictly: the length is a
// multiple of 4 and the padding bits are zero. It is bounded by max encoded bytes.
func DecodeDigest(s string, max int) (Digest, []byte, error) {
	if len(s) == 0 || len(s) > max || !base64Pattern.MatchString(s) {
		return Digest{}, nil, fmt.Errorf("%w (%d bytes, at most %d)", ErrBadDigest, len(s), max)
	}
	raw, err := base64.StdEncoding.Strict().DecodeString(s)
	if err != nil {
		return Digest{}, nil, fmt.Errorf("%w: %v", ErrBadDigest, err)
	}
	h := raw
	if len(h) > 64 {
		h = h[:64]
	}
	return Digest{Base64: s, Hex: hex.EncodeToString(h), Size: len(raw)}, raw, nil
}

// digestSizes are the digest lengths of the hash algorithm names the DMTF
// mockup uses (TPM_ALG_SHA_384, TPM_ALG_SHA256, TPM_ALG_SHA3_512...): the bits
// are in the name. An algorithm not listed has no known size.
var digestSizes = map[string]int{"TPM_ALG_SHA_256": 32, "TPM_ALG_SHA_384": 48, "TPM_ALG_SHA_512": 64,
	"TPM_ALG_SHA256": 32, "TPM_ALG_SHA384": 48, "TPM_ALG_SHA512": 64,
	"TPM_ALG_SHA3_256": 32, "TPM_ALG_SHA3_384": 48, "TPM_ALG_SHA3_512": 64, "TPM_ALG_SM3_256": 32}

// Measurement is one measurement of a set, SPDM (Index, Type...) or TPM (PCR).
type Measurement struct {
	// Index is the SPDM measurement index; PCR the TPM register: one is set.
	Index *int64 `json:"index,omitempty"`
	PCR   *int64 `json:"pcr,omitempty"`
	// Type is the SPDM DMTFmeasurementTypes value as sent (ImmutableROM,
	// MutableFirmware...), empty for a TPM measurement.
	Type          string `json:"type,omitempty"`
	HashAlgorithm string `json:"hash_algorithm,omitempty"`
	// Measurement is the value; nil, with DecodeError, when it is not a valid
	// Base64 string.
	Measurement *Digest `json:"measurement,omitempty"`
	DecodeError string  `json:"decode_error,omitempty"`
	// SizeMatches is whether the value has the length of HashAlgorithm; nil when
	// the algorithm is not one this client knows.
	SizeMatches *bool `json:"size_matches_algorithm,omitempty"`
	// PartOfSummary is PartofSummaryHash (SPDM).
	PartOfSummary *bool `json:"part_of_summary,omitempty"`
	// SecurityVersionNumber is an 8-byte hex string, only for a
	// MutableFirmwareSecurityVersionNumber measurement (SPDM).
	SecurityVersionNumber string `json:"security_version_number,omitempty"`
	LastUpdated           string `json:"last_updated,omitempty"`
}

// Session is a SingleSessionInfo.
type Session struct {
	ID        *int64 `json:"id,omitempty"`
	Type      string `json:"type,omitempty"`
	Algorithm string `json:"algorithm,omitempty"`
}

// SPDMInfo is the SPDM object of a ComponentIntegrity: what the SPDM Requester
// reports of the SPDM Responder.
type SPDMInfo struct {
	// RequesterURI is the component that reports the integrity.
	RequesterURI             string `json:"requester_uri,omitempty"`
	MeasurementSpecification string `json:"measurement_specification,omitempty"`
	// Summary is the measurement summary; SummaryType TCB or All.
	Summary              *Digest       `json:"measurement_summary,omitempty"`
	SummaryError         string        `json:"measurement_summary_error,omitempty"`
	SummaryHashAlgorithm string        `json:"measurement_summary_hash_algorithm,omitempty"`
	SummaryType          string        `json:"measurement_summary_type,omitempty"`
	Measurements         []Measurement `json:"measurements,omitempty"`
	// The certificate links of the identity authentication, and the status of
	// the verification of the responder's identity (Success or Failed).
	ResponderCertificateURI string    `json:"responder_certificate_uri,omitempty"`
	ResponderVerification   string    `json:"responder_verification_status,omitempty"`
	RequesterCertificateURI string    `json:"requester_certificate_uri,omitempty"`
	Sessions                []Session `json:"sessions,omitempty"`
}

// TPMInfo is the TPM object of a ComponentIntegrity.
type TPMInfo struct {
	// NonceSizeBytesMaximum is the longest Nonce of TPMGetSignedMeasurements.
	NonceSizeBytesMaximum *int64        `json:"nonce_size_bytes_maximum,omitempty"`
	Measurements          []Measurement `json:"measurements,omitempty"`
	CertificateURI        string        `json:"certificate_uri,omitempty"`
	VerificationStatus    string        `json:"verification_status,omitempty"`
	Sessions              []Session     `json:"sessions,omitempty"`
}

// ComponentIntegrity is a ComponentIntegrity resource (1.5.0).
type ComponentIntegrity struct {
	URI  string `json:"uri"`
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
	// Type is SPDM, TPM, TCM, TPCM or OEM; TypeVersion the version of the technology.
	Type        string `json:"type,omitempty"`
	TypeVersion string `json:"type_version,omitempty"`
	// Enabled is nil when the service does not say.
	Enabled *bool `json:"enabled,omitempty"`
	State   string `json:"state,omitempty"`
	Health  string `json:"health,omitempty"`
	// LastUpdated is as the service wrote it.
	LastUpdated string `json:"last_updated,omitempty"`
	// TargetComponentURI is the component whose integrity this reports (a
	// TrustedComponent, in the DMTF mockup).
	TargetComponentURI  string    `json:"target_component_uri,omitempty"`
	ComponentsProtected []string  `json:"components_protected,omitempty"`
	SPDM                *SPDMInfo `json:"spdm,omitempty"`
	TPM                 *TPMInfo  `json:"tpm,omitempty"`
	// SPDMGetSignedMeasurements and TPMGetSignedMeasurements are true when the
	// resource has the action.
	SPDMGetSignedMeasurements bool `json:"spdm_get_signed_measurements"`
	TPMGetSignedMeasurements  bool `json:"tpm_get_signed_measurements"`
}

// CertificateURIs are the certificates a ComponentIntegrity links, in the order
// responder, requester, TPM.
func (ci ComponentIntegrity) CertificateURIs() []string {
	var out []string
	add := func(u string) {
		if u != "" && !slices.Contains(out, u) {
			out = append(out, u)
		}
	}
	if ci.SPDM != nil {
		add(ci.SPDM.ResponderCertificateURI)
		add(ci.SPDM.RequesterCertificateURI)
	}
	if ci.TPM != nil {
		add(ci.TPM.CertificateURI)
	}
	return out
}

func clipSec(s string) string {
	if len(s) > maxSecText {
		return s[:maxSecText]
	}
	return s
}

func strSec(p Props, key string) string { return clipSec(str(p, key)) }

func sessionsOf(v any) []Session {
	l, _ := v.([]any)
	var out []Session
	for _, x := range l {
		m, _ := x.(map[string]any)
		if m == nil || len(out) >= maxParams {
			continue
		}
		out = append(out, Session{ID: intOf(m, "SessionId"), Type: strSec(m, "SessionType"), Algorithm: strSec(m, "SessionAlgorithm")})
	}
	return out
}

func measurementsOf(v any, tpm bool) []Measurement {
	l, _ := v.([]any)
	var out []Measurement
	for _, x := range l {
		m, _ := x.(map[string]any)
		if m == nil || len(out) >= MaxMeasurements {
			continue
		}
		ms := Measurement{HashAlgorithm: strSec(m, "MeasurementHashAlgorithm"), PartOfSummary: boolOf(m, "PartofSummaryHash"),
			Type: strSec(m, "MeasurementType"), SecurityVersionNumber: strSec(m, "SecurityVersionNumber"), LastUpdated: strSec(m, "LastUpdated")}
		if tpm {
			ms.PCR = intOf(m, "PCR")
		} else {
			ms.Index = intOf(m, "MeasurementIndex")
		}
		if s := str(m, "Measurement"); s != "" {
			if d, raw, err := DecodeDigest(s, MaxDigestText); err != nil {
				ms.DecodeError = err.Error()
			} else {
				ms.Measurement = &d
				if want, ok := digestSizes[ms.HashAlgorithm]; ok {
					ok := len(raw) == want
					ms.SizeMatches = &ok
				}
			}
		}
		out = append(out, ms)
	}
	return out
}

// ParseComponentIntegrity decodes a ComponentIntegrity resource read at uri. It
// never panics; a measurement that is not Base64 is kept with its DecodeError.
func ParseComponentIntegrity(uri string, data []byte) (ComponentIntegrity, error) {
	p, err := decodeProps(data)
	if err != nil {
		return ComponentIntegrity{}, fmt.Errorf("%w: %v", ErrMalformed, err)
	}
	return parseComponentIntegrity(uri, p), nil
}

func parseComponentIntegrity(uri string, p Props) ComponentIntegrity {
	acts := sub(p, "Actions")
	_, spdmAct := acts["#ComponentIntegrity.SPDMGetSignedMeasurements"].(map[string]any)
	_, tpmAct := acts["#ComponentIntegrity.TPMGetSignedMeasurements"].(map[string]any)
	ci := ComponentIntegrity{URI: uri, ID: strSec(p, "Id"), Name: strSec(p, "Name"), Type: strSec(p, "ComponentIntegrityType"),
		TypeVersion: strSec(p, "ComponentIntegrityTypeVersion"), Enabled: boolOf(p, "ComponentIntegrityEnabled"),
		State: str(sub(p, "Status"), "State"), Health: str(sub(p, "Status"), "Health"), LastUpdated: strSec(p, "LastUpdated"),
		TargetComponentURI: servicePath(str(p, "TargetComponentURI")), ComponentsProtected: linksOf(sub(p, "Links")["ComponentsProtected"]),
		SPDMGetSignedMeasurements: spdmAct, TPMGetSignedMeasurements: tpmAct}
	if ci.ID == "" {
		ci.ID = path.Base(uri)
	}
	if s := sub(p, "SPDM"); s != nil {
		ms, auth, comm := sub(s, "MeasurementSet"), sub(sub(s, "IdentityAuthentication"), "ResponderAuthentication"), sub(s, "ComponentCommunication")
		info := &SPDMInfo{RequesterURI: linkOf(s, "Requester"), MeasurementSpecification: strSec(ms, "MeasurementSpecification"),
			SummaryHashAlgorithm: strSec(ms, "MeasurementSummaryHashAlgorithm"), SummaryType: strSec(ms, "MeasurementSummaryType"),
			Measurements: measurementsOf(ms["Measurements"], false), ResponderVerification: strSec(auth, "VerificationStatus"),
			Sessions: sessionsOf(comm["Sessions"])}
		info.ResponderCertificateURI = linkOf(auth, "ComponentCertificate")
		info.RequesterCertificateURI = linkOf(sub(sub(s, "IdentityAuthentication"), "RequesterAuthentication"), "ProvidedCertificate")
		if sum := str(ms, "MeasurementSummary"); sum != "" {
			if d, _, err := DecodeDigest(sum, MaxDigestText); err != nil {
				info.SummaryError = err.Error()
			} else {
				info.Summary = &d
			}
		}
		ci.SPDM = info
	}
	if t := sub(p, "TPM"); t != nil {
		auth := sub(t, "IdentityAuthentication")
		ci.TPM = &TPMInfo{NonceSizeBytesMaximum: intOf(t, "NonceSizeBytesMaximum"), Measurements: measurementsOf(sub(t, "MeasurementSet")["Measurements"], true),
			CertificateURI: linkOf(auth, "ComponentCertificate"), VerificationStatus: strSec(auth, "VerificationStatus"),
			Sessions: sessionsOf(sub(t, "ComponentCommunication")["Sessions"])}
	}
	return ci
}

// ---- Reads: ComponentIntegrity ----

func (c *Client) integrityCollection(ctx context.Context) (string, error) {
	u, err := c.rootLink(ctx, "ComponentIntegrity")
	if errors.Is(err, ErrNotFound) {
		return "", fmt.Errorf("%w: the service root links no ComponentIntegrity collection", ErrUnsupported)
	}
	return u, err
}

// ComponentIntegrities lists the ComponentIntegrity resources of the service (at
// most MaxSecurityMembers, $expand where the service advertises it).
// ErrUnsupported when the service root links none.
func (c *Client) ComponentIntegrities(ctx context.Context) ([]ComponentIntegrity, error) {
	coll, err := c.integrityCollection(ctx)
	if err != nil {
		return nil, err
	}
	ms, err := c.securityMembers(ctx, coll)
	if err != nil {
		return nil, err
	}
	out := make([]ComponentIntegrity, 0, len(ms))
	for _, m := range ms {
		out = append(out, parseComponentIntegrity(m.URI, m.Props))
	}
	return out, nil
}

// ComponentIntegrity reads one resource, named by its Id or its path.
func (c *Client) ComponentIntegrity(ctx context.Context, idOrURI string) (ComponentIntegrity, error) {
	coll, err := c.integrityCollection(ctx)
	if err != nil {
		return ComponentIntegrity{}, err
	}
	uri, err := resolveMember(coll, idOrURI, "component integrity")
	if err != nil {
		return ComponentIntegrity{}, err
	}
	p, _, err := c.getProps(ctx, uri)
	if err != nil {
		return ComponentIntegrity{}, err
	}
	return parseComponentIntegrity(uri, p), nil
}

func (c *Client) securityMembers(ctx context.Context, coll string) ([]member, error) {
	return c.memberResources(ctx, coll, MaxSecurityMembers, 8, func(err error) bool { return errors.Is(err, ErrNotFound) })
}

// LinkedCertificate is a certificate a resource links: the Certificate resource
// as certs.go reads it (decoded with ParseX509), or why it could not be read.
type LinkedCertificate struct {
	URI         string       `json:"uri"`
	Certificate *Certificate `json:"certificate,omitempty"`
	Error       string       `json:"error,omitempty"`
}

// LinkedCertificates reads the certificates at uris (a path of the service
// each; at most MaxSecurityMembers). A read that fails is reported in its
// entry, not as an error of the call: the others are still read.
func (c *Client) LinkedCertificates(ctx context.Context, uris []string) []LinkedCertificate {
	var out []LinkedCertificate
	for _, u := range uris {
		if len(out) >= MaxSecurityMembers {
			break
		}
		lc := LinkedCertificate{URI: u}
		if !isServicePath(u) {
			lc.Error = "not a path of the service"
		} else if cert, err := c.readCertificate(ctx, u); err != nil {
			lc.Error = err.Error()
		} else {
			lc.Certificate = &cert
		}
		out = append(out, lc)
	}
	return out
}

// ---- SPDMGetSignedMeasurements ----

// SignedMeasurementsRequest are the parameters of SPDMGetSignedMeasurements
// (ComponentIntegrity 1.5.0).
type SignedMeasurementsRequest struct {
	// Indices are the measurement blocks to sign: one or more unique values
	// 0..254, or the single value 255 (all); empty: the service assumes [255].
	Indices []int
	// Nonce is 32 bytes as 64 hex digits; empty: this client generates one
	// (crypto/rand), so the answer is fresh whatever the service does.
	Nonce string
	// SlotID is the SPDM slot of the certificate that signs; nil: the service
	// assumes 0.
	SlotID *int
	// RawBitstream asks for raw bitstreams (SPDM 1.2 and later; ignored by
	// older versions).
	RawBitstream bool
}

var noncePattern = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)

func (r SignedMeasurementsRequest) body() (Props, error) {
	body := Props{}
	if len(r.Indices) > 0 {
		if len(r.Indices) > 255 {
			return nil, errors.New("redfish: too many measurement indices")
		}
		if len(r.Indices) > 1 || r.Indices[0] != 255 {
			seen := map[int]bool{}
			for _, i := range r.Indices {
				if i < 0 || i > 254 || seen[i] {
					return nil, fmt.Errorf("redfish: measurement index %d: the indices are unique values from 0 to 254, or the single value 255", i)
				}
				seen[i] = true
			}
		}
		body["MeasurementIndices"] = slices.Clone(r.Indices)
	}
	if r.Nonce != "" {
		if !noncePattern.MatchString(r.Nonce) {
			return nil, errors.New("redfish: the nonce is 32 bytes as 64 hexadecimal digits")
		}
		body["Nonce"] = strings.ToLower(r.Nonce)
	}
	if r.SlotID != nil {
		if *r.SlotID < 0 || *r.SlotID > 255 {
			return nil, fmt.Errorf("redfish: slot %d is not a slot identifier", *r.SlotID)
		}
		body["SlotId"] = *r.SlotID
	}
	if r.RawBitstream {
		body["RawBitstream"] = true
	}
	return body, nil
}

// SignedMeasurements is the answer of SPDMGetSignedMeasurements. The signature
// is NOT verified: Verified is false, and Verification says so.
type SignedMeasurements struct {
	ComponentIntegrityURI string `json:"component_integrity_uri"`
	// Nonce is the nonce sent (given or generated).
	Nonce string `json:"nonce"`
	// Version is the SPDM version used; SigningAlgorithm, HashingAlgorithm and
	// AdditionalSigningAlgorithms are the names the service gives.
	Version                     string   `json:"version,omitempty"`
	SigningAlgorithm            string   `json:"signing_algorithm,omitempty"`
	HashingAlgorithm            string   `json:"hashing_algorithm,omitempty"`
	AdditionalSigningAlgorithms []string `json:"additional_signing_algorithms,omitempty"`
	// CertificateURI is the certificate of the slot that can validate the
	// signature; PublicKeyPEM the public key, when the service gives it instead
	// (slot 15).
	CertificateURI string `json:"certificate_uri,omitempty"`
	PublicKeyPEM   string `json:"public_key_pem,omitempty"`
	// Blob is SignedMeasurements: the transcript of the SPDM exchange whose last
	// response carries the signature; Hex shows its first 64 bytes.
	Blob Digest `json:"signed_measurements"`
	// NonceInBlob is whether the nonce bytes occur in the blob (the schema has
	// the Requester send it in the last GET_MEASUREMENTS request): a substring
	// test, not an SPDM parse.
	NonceInBlob bool `json:"nonce_in_blob"`
	// Verified is always false: this client does not verify the signature.
	Verified     bool   `json:"signature_verified"`
	Verification string `json:"verification"`
}

// notVerified is what a SignedMeasurements says about its signature.
const notVerified = "not verified: this client does not check the signature of the signed measurements against the SPDM certificate chain (DSP0274)"

// ParseSignedMeasurements decodes the answer of SPDMGetSignedMeasurements
// (SPDMGetSignedMeasurementsResponse). The blob must be Base64 with padding and
// is bounded by MaxSignedBlobText. The signature is not verified.
func ParseSignedMeasurements(data []byte) (SignedMeasurements, error) {
	if len(data) > MaxSignedMeasurementsBytes {
		return SignedMeasurements{}, fmt.Errorf("%w: the answer is over %d bytes", ErrMalformed, MaxSignedMeasurementsBytes)
	}
	p, err := decodeProps(data)
	if err != nil {
		return SignedMeasurements{}, fmt.Errorf("%w: %v", ErrMalformed, err)
	}
	d, _, err := DecodeDigest(str(p, "SignedMeasurements"), MaxSignedBlobText)
	if err != nil {
		return SignedMeasurements{}, fmt.Errorf("%w: SignedMeasurements: %v", ErrMalformed, err)
	}
	return SignedMeasurements{Version: strSec(p, "Version"), SigningAlgorithm: strSec(p, "SigningAlgorithm"), HashingAlgorithm: strSec(p, "HashingAlgorithm"),
		AdditionalSigningAlgorithms: stringList(p["AdditionalSigningAlgorithms"]), CertificateURI: linkOf(p, "Certificate"),
		PublicKeyPEM: clipSec(str(p, "PublicKey")), Blob: d, Verification: notVerified}, nil
}

// GetSignedMeasurements runs the SPDMGetSignedMeasurements action of a
// ComponentIntegrity (named by Id or path) and returns the signed statement of
// the service. The action does not change the service; it makes the BMC run SPDM
// GET_MEASUREMENTS exchanges with the device. The parameters are checked first
// (indices, nonce, slot) and against the ActionInfo or AllowableValues of the
// service. The signature is not verified (SignedMeasurements.Verified).
// ErrUnsupported when the resource has no such action (the schema has it only for
// type SPDM).
func (c *Client) GetSignedMeasurements(ctx context.Context, idOrURI string, req SignedMeasurementsRequest) (SignedMeasurements, error) {
	const action = "#ComponentIntegrity.SPDMGetSignedMeasurements"
	body, err := req.body()
	if err != nil {
		return SignedMeasurements{}, err
	}
	nonce, _ := body["Nonce"].(string)
	var nb []byte
	if nonce == "" {
		nb = make([]byte, 32)
		if _, err := rand.Read(nb); err != nil {
			return SignedMeasurements{}, fmt.Errorf("redfish: no nonce: %w", err)
		}
		nonce = hex.EncodeToString(nb)
		body["Nonce"] = nonce
	} else {
		nb, _ = hex.DecodeString(nonce)
	}
	coll, err := c.integrityCollection(ctx)
	if err != nil {
		return SignedMeasurements{}, err
	}
	uri, err := resolveMember(coll, idOrURI, "component integrity")
	if err != nil {
		return SignedMeasurements{}, err
	}
	p, _, err := c.getProps(ctx, uri)
	if err != nil {
		return SignedMeasurements{}, err
	}
	target, values, err := c.actionOf(ctx, uri, p, action)
	if err != nil {
		return SignedMeasurements{}, err
	}
	// ActionValues lists values as text: the indices and the slot are checked as
	// such (the Nonce is free text: it has no list of values).
	check := Props{}
	for k, v := range body {
		switch x := v.(type) {
		case []int:
			l := make([]string, len(x))
			for i, n := range x {
				l[i] = strconv.Itoa(n)
			}
			check[k] = l
		case int:
			check[k] = strconv.Itoa(x)
		case string:
			if k != "Nonce" {
				check[k] = x
			}
		default:
			check[k] = ""
		}
	}
	if err := checkValues(action, check, values); err != nil {
		return SignedMeasurements{}, err
	}
	r, err := c.Post(ctx, target, body)
	if err != nil {
		return SignedMeasurements{}, err
	}
	if r.IsTask() {
		return SignedMeasurements{}, fmt.Errorf("%w: the service answered asynchronously (monitor %s): not followed", ErrUnsupported, r.Location())
	}
	sm, err := ParseSignedMeasurements(r.Body)
	if err != nil {
		return SignedMeasurements{}, err
	}
	sm.ComponentIntegrityURI, sm.Nonce = uri, nonce
	if raw, err := base64.StdEncoding.DecodeString(sm.Blob.Base64); err == nil {
		sm.NonceInBlob = bytes.Contains(raw, nb)
	}
	return sm, nil
}

// ---- TrustedComponent ----

// TrustedComponent is a TrustedComponent resource (1.5.0) of a chassis.
type TrustedComponent struct {
	URI        string `json:"uri"`
	ID         string `json:"id"`
	ChassisURI string `json:"chassis_uri,omitempty"`
	Name       string `json:"name,omitempty"`
	// Type is Discrete or Integrated.
	Type            string `json:"type,omitempty"`
	FirmwareVersion string `json:"firmware_version,omitempty"`
	Manufacturer    string `json:"manufacturer,omitempty"`
	Model           string `json:"model,omitempty"`
	PartNumber      string `json:"part_number,omitempty"`
	SKU             string `json:"sku,omitempty"`
	SerialNumber    string `json:"serial_number,omitempty"`
	UUID            string `json:"uuid,omitempty"`
	State           string `json:"state,omitempty"`
	Health          string `json:"health,omitempty"`
	// TPM vendor identifiers, when the component is a TPM.
	TPMCapabilitiesVendorID      string `json:"tpm_capabilities_vendor_id,omitempty"`
	TPMHardwareInterfaceVendorID string `json:"tpm_hardware_interface_vendor_id,omitempty"`
	CertificatesURI              string `json:"certificates_uri,omitempty"`
	// Links: the ComponentIntegrity resources it is responsible for, the
	// components it protects, its active image, owner and host.
	ComponentIntegrity  []string `json:"component_integrity,omitempty"`
	ComponentsProtected []string `json:"components_protected,omitempty"`
	ActiveSoftwareImage string   `json:"active_software_image,omitempty"`
	IntegratedInto      string   `json:"integrated_into,omitempty"`
	Owner               string   `json:"owner,omitempty"`
	// TPMClear and TPMGetEventLog are true when the component has the action
	// (neither is run by this client).
	TPMClear       bool `json:"tpm_clear"`
	TPMGetEventLog bool `json:"tpm_get_event_log"`
}

// ParseTrustedComponent decodes a TrustedComponent resource read at uri.
func ParseTrustedComponent(uri string, data []byte) (TrustedComponent, error) {
	p, err := decodeProps(data)
	if err != nil {
		return TrustedComponent{}, fmt.Errorf("%w: %v", ErrMalformed, err)
	}
	return parseTrustedComponent(uri, p), nil
}

func parseTrustedComponent(uri string, p Props) TrustedComponent {
	links, acts, tpm := sub(p, "Links"), sub(p, "Actions"), sub(p, "TPM")
	_, clr := acts["#TrustedComponent.TPMClear"].(map[string]any)
	_, ev := acts["#TrustedComponent.TPMGetEventLog"].(map[string]any)
	t := TrustedComponent{URI: uri, ID: strSec(p, "Id"), Name: strSec(p, "Name"), Type: strSec(p, "TrustedComponentType"),
		FirmwareVersion: strSec(p, "FirmwareVersion"), Manufacturer: strSec(p, "Manufacturer"), Model: strSec(p, "Model"),
		PartNumber: strSec(p, "PartNumber"), SKU: strSec(p, "SKU"), SerialNumber: strSec(p, "SerialNumber"), UUID: strSec(p, "UUID"),
		State: str(sub(p, "Status"), "State"), Health: str(sub(p, "Status"), "Health"),
		TPMCapabilitiesVendorID: strSec(tpm, "CapabilitiesVendorID"), TPMHardwareInterfaceVendorID: strSec(tpm, "HardwareInterfaceVendorID"),
		CertificatesURI: linkOf(p, "Certificates"), ComponentIntegrity: linksOf(links["ComponentIntegrity"]),
		ComponentsProtected: linksOf(links["ComponentsProtected"]), ActiveSoftwareImage: linkOf(links, "ActiveSoftwareImage"),
		IntegratedInto: linkOf(links, "IntegratedInto"), Owner: linkOf(links, "Owner"), TPMClear: clr, TPMGetEventLog: ev}
	if t.ID == "" {
		t.ID = path.Base(uri)
	}
	return t
}

// TrustedComponents lists the TrustedComponents of the chassis given by uris (of
// every chassis when none is given): at most MaxSecurityMembers per chassis. A
// chassis without a TrustedComponents collection has none. ErrUnsupported when no
// chassis has the collection.
func (c *Client) TrustedComponents(ctx context.Context, chassis ...string) ([]TrustedComponent, error) {
	set, err := c.chassisSet(ctx, chassis)
	if err != nil {
		return nil, err
	}
	var out []TrustedComponent
	seen := false
	for _, ch := range set {
		p, _, err := c.getProps(ctx, ch)
		if err != nil {
			return nil, err
		}
		coll := linkOf(p, "TrustedComponents")
		if coll == "" {
			continue
		}
		seen = true
		ms, err := c.securityMembers(ctx, coll)
		if err != nil {
			return nil, err
		}
		for _, m := range ms {
			t := parseTrustedComponent(m.URI, m.Props)
			t.ChassisURI = ch
			out = append(out, t)
		}
	}
	if !seen {
		return nil, fmt.Errorf("%w: no chassis links a TrustedComponents collection", ErrUnsupported)
	}
	return out, nil
}

// TrustedComponent reads one trusted component, named by its Id (the only one
// of that Id among the chassis given or all) or its path.
func (c *Client) TrustedComponent(ctx context.Context, idOrURI string, chassis ...string) (TrustedComponent, error) {
	if isServicePath(idOrURI) {
		p, _, err := c.getProps(ctx, idOrURI)
		if err != nil {
			return TrustedComponent{}, err
		}
		if t := str(p, "@odata.type"); !strings.HasPrefix(t, "#TrustedComponent.") {
			return TrustedComponent{}, fmt.Errorf("%w: %s is not a TrustedComponent (@odata.type %q)", ErrMalformed, idOrURI, clip(t))
		}
		return parseTrustedComponent(idOrURI, p), nil
	}
	if !subID.MatchString(idOrURI) {
		return TrustedComponent{}, fmt.Errorf("redfish: %q is neither a trusted component Id nor a path of the service", clip(idOrURI))
	}
	all, err := c.TrustedComponents(ctx, chassis...)
	if err != nil {
		return TrustedComponent{}, err
	}
	var hit []TrustedComponent
	for _, t := range all {
		if t.ID == idOrURI {
			hit = append(hit, t)
		}
	}
	switch len(hit) {
	case 0:
		return TrustedComponent{}, fmt.Errorf("%w: no trusted component %q", ErrNotFound, idOrURI)
	case 1:
		return hit[0], nil
	}
	uris := make([]string, len(hit))
	for i, h := range hit {
		uris[i] = h.URI
	}
	return TrustedComponent{}, fmt.Errorf("%w: %d trusted components have the Id %q (%s): give the path", ErrAmbiguous, len(hit), idOrURI, strings.Join(uris, ", "))
}

// CertificatesOf reads the certificates of a collection (a TrustedComponent's
// Certificates, or one of a SecurityPolicy), each decoded with ParseX509: at most
// MaxSecurityMembers, $expand where advertised. An empty uri gives none.
func (c *Client) CertificatesOf(ctx context.Context, collURI string) ([]Certificate, error) {
	if collURI == "" {
		return nil, nil
	}
	ms, err := c.securityMembers(ctx, collURI)
	if err != nil {
		return nil, err
	}
	out := make([]Certificate, 0, len(ms))
	for _, m := range ms {
		out = append(out, parseCertificate(m.URI, m.Props))
	}
	return out, nil
}

// ---- SecurityPolicy ----

// ParameterSet is an allowed or denied set of an SPDM or TLS policy: versions
// and algorithms, as the service wrote them (SPDM: AEAD, BaseAsym, BaseHash; TLS:
// CipherSuites, SignatureAlgorithms).
type ParameterSet struct {
	Versions            []string `json:"versions,omitempty"`
	AEAD                []string `json:"aead,omitempty"`
	BaseAsym            []string `json:"base_asym,omitempty"`
	BaseHash            []string `json:"base_hash,omitempty"`
	CipherSuites        []string `json:"cipher_suites,omitempty"`
	SignatureAlgorithms []string `json:"signature_algorithms,omitempty"`
}

func parameterSetOf(m Props) *ParameterSet {
	if m == nil {
		return nil
	}
	a := sub(m, "Algorithms")
	return &ParameterSet{Versions: stringList(m["Versions"]), AEAD: stringList(a["AEAD"]), BaseAsym: stringList(a["BaseAsym"]), BaseHash: stringList(a["BaseHash"]),
		CipherSuites: stringList(a["CipherSuites"]), SignatureAlgorithms: stringList(a["SignatureAlgorithms"])}
}

// SPDMPolicy is the SPDM object of a SecurityPolicy.
type SPDMPolicy struct {
	Enabled                 *bool         `json:"enabled,omitempty"`
	SecureSessionEnabled    *bool         `json:"secure_session_enabled,omitempty"`
	VerifyCertificate       *bool         `json:"verify_certificate,omitempty"`
	AllowExtendedAlgorithms *bool         `json:"allow_extended_algorithms,omitempty"`
	Allowed                 *ParameterSet `json:"allowed,omitempty"`
	Denied                  *ParameterSet `json:"denied,omitempty"`
	TrustedCertificatesURI  string        `json:"trusted_certificates_uri,omitempty"`
	RevokedCertificatesURI  string        `json:"revoked_certificates_uri,omitempty"`
}

// TLSPolicy is one side (Client or Server) of the TLS policy.
type TLSPolicy struct {
	VerifyCertificate      *bool         `json:"verify_certificate,omitempty"`
	Allowed                *ParameterSet `json:"allowed,omitempty"`
	Denied                 *ParameterSet `json:"denied,omitempty"`
	TrustedCertificatesURI string        `json:"trusted_certificates_uri,omitempty"`
	RevokedCertificatesURI string        `json:"revoked_certificates_uri,omitempty"`
}

// SecurityPolicy is the SecurityPolicy resource of a manager (1.1.0).
type SecurityPolicy struct {
	URI                   string `json:"uri"`
	ManagerURI            string `json:"manager_uri,omitempty"`
	Name                  string `json:"name,omitempty"`
	State                 string `json:"state,omitempty"`
	Health                string `json:"health,omitempty"`
	OverrideParentManager *bool  `json:"override_parent_manager,omitempty"`
	// SPDM is nil when the policy has no SPDM object; TLSClient and TLSServer
	// are the manager acting as a TLS client or server.
	SPDM      *SPDMPolicy `json:"spdm,omitempty"`
	TLSClient *TLSPolicy  `json:"tls_client,omitempty"`
	TLSServer *TLSPolicy  `json:"tls_server,omitempty"`
}

// ParseSecurityPolicy decodes a SecurityPolicy resource read at uri.
func ParseSecurityPolicy(uri string, data []byte) (SecurityPolicy, error) {
	p, err := decodeProps(data)
	if err != nil {
		return SecurityPolicy{}, fmt.Errorf("%w: %v", ErrMalformed, err)
	}
	return parseSecurityPolicy(uri, p), nil
}

func tlsPolicyOf(m Props) *TLSPolicy {
	if m == nil {
		return nil
	}
	return &TLSPolicy{VerifyCertificate: boolOf(m, "VerifyCertificate"), Allowed: parameterSetOf(sub(m, "Allowed")), Denied: parameterSetOf(sub(m, "Denied")),
		TrustedCertificatesURI: linkOf(m, "TrustedCertificates"), RevokedCertificatesURI: linkOf(m, "RevokedCertificates")}
}

func parseSecurityPolicy(uri string, p Props) SecurityPolicy {
	sp := SecurityPolicy{URI: uri, Name: strSec(p, "Name"), State: str(sub(p, "Status"), "State"), Health: str(sub(p, "Status"), "Health"),
		OverrideParentManager: boolOf(p, "OverrideParentManager")}
	if s := sub(p, "SPDM"); s != nil {
		sp.SPDM = &SPDMPolicy{Enabled: boolOf(s, "Enabled"), SecureSessionEnabled: boolOf(s, "SecureSessionEnabled"), VerifyCertificate: boolOf(s, "VerifyCertificate"),
			AllowExtendedAlgorithms: boolOf(s, "AllowExtendedAlgorithms"), Allowed: parameterSetOf(sub(s, "Allowed")), Denied: parameterSetOf(sub(s, "Denied")),
			TrustedCertificatesURI: linkOf(s, "TrustedCertificates"), RevokedCertificatesURI: linkOf(s, "RevokedCertificates")}
	}
	tls := sub(p, "TLS")
	sp.TLSClient, sp.TLSServer = tlsPolicyOf(sub(tls, "Client")), tlsPolicyOf(sub(tls, "Server"))
	return sp
}

// PolicyCertificates are the certificates a SecurityPolicy lists, by collection.
type PolicyCertificates struct {
	// Collection names it: spdm.trusted, spdm.revoked, tls_client.trusted...
	Collection   string        `json:"collection"`
	URI          string        `json:"uri"`
	Certificates []Certificate `json:"certificates"`
}

// SecurityPolicy reads the SecurityPolicy of a manager (the one named by id, the
// only one when id is empty). ErrUnsupported when the manager links none.
func (c *Client) SecurityPolicy(ctx context.Context, managerID string) (SecurityPolicy, error) {
	mgr, err := c.ResolveManager(ctx, managerID)
	if err != nil {
		return SecurityPolicy{}, err
	}
	m, _, err := c.getProps(ctx, mgr)
	if err != nil {
		return SecurityPolicy{}, err
	}
	u := linkOf(m, "SecurityPolicy")
	if u == "" {
		return SecurityPolicy{}, fmt.Errorf("%w: the manager %s links no SecurityPolicy", ErrUnsupported, mgr)
	}
	p, _, err := c.getProps(ctx, u)
	if err != nil {
		return SecurityPolicy{}, err
	}
	sp := parseSecurityPolicy(u, p)
	sp.ManagerURI = mgr
	return sp, nil
}

// PolicyCertificates reads the trusted and revoked certificates the policy
// lists (SPDM, TLS client, TLS server), each decoded with ParseX509. A
// collection the policy does not link is left out.
func (c *Client) PolicyCertificates(ctx context.Context, sp SecurityPolicy) ([]PolicyCertificates, error) {
	var out []PolicyCertificates
	add := func(name, uri string) error {
		if uri == "" {
			return nil
		}
		certs, err := c.CertificatesOf(ctx, uri)
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		out = append(out, PolicyCertificates{Collection: name, URI: uri, Certificates: certs})
		return nil
	}
	if s := sp.SPDM; s != nil {
		if err := add("spdm.trusted", s.TrustedCertificatesURI); err != nil {
			return nil, err
		}
		if err := add("spdm.revoked", s.RevokedCertificatesURI); err != nil {
			return nil, err
		}
	}
	for _, side := range []struct {
		name string
		p    *TLSPolicy
	}{{"tls_client", sp.TLSClient}, {"tls_server", sp.TLSServer}} {
		if side.p == nil {
			continue
		}
		if err := add(side.name+".trusted", side.p.TrustedCertificatesURI); err != nil {
			return nil, err
		}
		if err := add(side.name+".revoked", side.p.RevokedCertificatesURI); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// ---- KeyService ----

// KeyService is the KeyService resource (1.0.1).
type KeyService struct {
	URI string `json:"uri"`
	// NVMeoFSecretsURI and NVMeoFKeyPoliciesURI are its two collections.
	NVMeoFSecretsURI     string `json:"nvmeof_secrets_uri,omitempty"`
	NVMeoFKeyPoliciesURI string `json:"nvmeof_key_policies_uri,omitempty"`
}

// NVMeoFSecret is a Key (1.4.1) of KeyService.NVMeoFSecrets, WITHOUT its key:
// KeyString is the key material. It is read from the answer to know it is there
// (KeyPresent) and then dropped: it is not a field, is never printed and is not
// in any error.
type NVMeoFSecret struct {
	URI             string `json:"uri"`
	ID              string `json:"id"`
	Name            string `json:"name,omitempty"`
	KeyType         string `json:"key_type,omitempty"`
	UserDescription string `json:"user_description,omitempty"`
	// KeyPresent is true when the service returned KeyString (the schema has no
	// way to withhold it; the key is not kept).
	KeyPresent bool `json:"key_present"`
	// NQN is the NVMe Qualified Name of the host or target subsystem.
	NQN                  string   `json:"nqn,omitempty"`
	HostKeyID            string   `json:"host_key_id,omitempty"`
	SecurityProtocolType string   `json:"security_protocol_type,omitempty"`
	OEMSecurityProtocol  string   `json:"oem_security_protocol_type,omitempty"`
	SecureHashAllowList  []string `json:"secure_hash_allow_list,omitempty"`
}

// ParseNVMeoFSecret decodes a Key resource read at uri.
func ParseNVMeoFSecret(uri string, data []byte) (NVMeoFSecret, error) {
	p, err := decodeProps(data)
	if err != nil {
		return NVMeoFSecret{}, fmt.Errorf("%w: %v", ErrMalformed, err)
	}
	return parseNVMeoFSecret(uri, p), nil
}

func parseNVMeoFSecret(uri string, p Props) NVMeoFSecret {
	n := sub(p, "NVMeoF")
	k := NVMeoFSecret{URI: uri, ID: strSec(p, "Id"), Name: strSec(p, "Name"), KeyType: strSec(p, "KeyType"), UserDescription: strSec(p, "UserDescription"),
		KeyPresent: str(p, "KeyString") != "", NQN: strSec(n, "NQN"), HostKeyID: strSec(n, "HostKeyId"),
		SecurityProtocolType: strSec(n, "SecurityProtocolType"), OEMSecurityProtocol: strSec(n, "OEMSecurityProtocolType"), SecureHashAllowList: stringList(n["SecureHashAllowList"])}
	if k.ID == "" {
		k.ID = path.Base(uri)
	}
	return k
}

// KeyPolicy is a KeyPolicy resource (1.0.1), NVMe-oF type.
type KeyPolicy struct {
	URI       string `json:"uri"`
	ID        string `json:"id"`
	Name      string `json:"name,omitempty"`
	Type      string `json:"type,omitempty"`
	IsDefault *bool  `json:"is_default,omitempty"`
	// The allow lists of the NVMeoF object, as the service wrote them.
	CipherSuites       []string `json:"cipher_suite_allow_list,omitempty"`
	DHGroups           []string `json:"dh_group_allow_list,omitempty"`
	SecureHashes       []string `json:"secure_hash_allow_list,omitempty"`
	SecurityProtocols  []string `json:"security_protocol_allow_list,omitempty"`
	OEMProtocols       []string `json:"oem_security_protocol_allow_list,omitempty"`
	SecurityTransports []string `json:"security_transport_allow_list,omitempty"`
}

// ParseKeyPolicy decodes a KeyPolicy resource read at uri.
func ParseKeyPolicy(uri string, data []byte) (KeyPolicy, error) {
	p, err := decodeProps(data)
	if err != nil {
		return KeyPolicy{}, fmt.Errorf("%w: %v", ErrMalformed, err)
	}
	return parseKeyPolicy(uri, p), nil
}

func parseKeyPolicy(uri string, p Props) KeyPolicy {
	n := sub(p, "NVMeoF")
	k := KeyPolicy{URI: uri, ID: strSec(p, "Id"), Name: strSec(p, "Name"), Type: strSec(p, "KeyPolicyType"), IsDefault: boolOf(p, "IsDefault"),
		CipherSuites: stringList(n["CipherSuiteAllowList"]), DHGroups: stringList(n["DHGroupAllowList"]), SecureHashes: stringList(n["SecureHashAllowList"]),
		SecurityProtocols: stringList(n["SecurityProtocolAllowList"]), OEMProtocols: stringList(n["OEMSecurityProtocolAllowList"]),
		SecurityTransports: stringList(n["SecurityTransportAllowList"])}
	if k.ID == "" {
		k.ID = path.Base(uri)
	}
	return k
}

// KeyService reads the KeyService of the service (ErrUnsupported when the service
// root links none).
func (c *Client) KeyService(ctx context.Context) (KeyService, error) {
	u, err := c.rootLink(ctx, "KeyService")
	if errors.Is(err, ErrNotFound) {
		return KeyService{}, fmt.Errorf("%w: the service root links no KeyService", ErrUnsupported)
	}
	if err != nil {
		return KeyService{}, err
	}
	p, _, err := c.getProps(ctx, u)
	if err != nil {
		return KeyService{}, err
	}
	return KeyService{URI: u, NVMeoFSecretsURI: linkOf(p, "NVMeoFSecrets"), NVMeoFKeyPoliciesURI: linkOf(p, "NVMeoFKeyPolicies")}, nil
}

func (c *Client) keyCollection(ctx context.Context, name string, pick func(KeyService) string) (string, error) {
	ks, err := c.KeyService(ctx)
	if err != nil {
		return "", err
	}
	if u := pick(ks); u != "" {
		return u, nil
	}
	return "", fmt.Errorf("%w: the KeyService links no %s collection", ErrUnsupported, name)
}

// NVMeoFSecrets lists the NVMe-oF secrets of the KeyService (at most
// MaxSecurityMembers). Their key material is not returned: see NVMeoFSecret.
func (c *Client) NVMeoFSecrets(ctx context.Context) ([]NVMeoFSecret, error) {
	coll, err := c.keyCollection(ctx, "NVMeoFSecrets", func(k KeyService) string { return k.NVMeoFSecretsURI })
	if err != nil {
		return nil, err
	}
	ms, err := c.securityMembers(ctx, coll)
	if err != nil {
		return nil, err
	}
	out := make([]NVMeoFSecret, 0, len(ms))
	for _, m := range ms {
		out = append(out, parseNVMeoFSecret(m.URI, m.Props))
	}
	return out, nil
}

// NVMeoFKeyPolicies lists the NVMe-oF key policies of the KeyService (at most
// MaxSecurityMembers).
func (c *Client) NVMeoFKeyPolicies(ctx context.Context) ([]KeyPolicy, error) {
	coll, err := c.keyCollection(ctx, "NVMeoFKeyPolicies", func(k KeyService) string { return k.NVMeoFKeyPoliciesURI })
	if err != nil {
		return nil, err
	}
	ms, err := c.securityMembers(ctx, coll)
	if err != nil {
		return nil, err
	}
	out := make([]KeyPolicy, 0, len(ms))
	for _, m := range ms {
		out = append(out, parseKeyPolicy(m.URI, m.Props))
	}
	return out, nil
}
