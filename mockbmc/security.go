package mockbmc

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"regexp"
	"strings"
)

// The security resources of the DMTF public-rackmount1 tree (ComponentIntegrity,
// TrustedComponents, SecurityPolicy, KeyService with NVMe-oF secrets and key
// policies) are served as the mockup has them; the emulator tree has none, so a
// client gets ErrUnsupported there. The mockup has no action, so augmentSecurity
// gives every SPDM ComponentIntegrity the SPDMGetSignedMeasurements action
// (ComponentIntegrity 1.5.0) with an ActionInfo that allows the slots 0, 1 and
// 15.
//
// The answer is a MOCK: the SignedMeasurements blob is a made-up transcript
// (a label, the indices, the nonce, a SHA-256 of them and 96 zero bytes where an
// SPDM signature would be). It is not an SPDM exchange and its "signature" is
// not one: nothing can verify it, which is what a client that does not verify
// must say too.

const spdmSignedLabel = "mockbmc SPDM transcript (not an SPDM exchange, not signed)"

var spdmNonce = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)

func (b *BMC) augmentSecurity() {
	for p, m := range b.res {
		if !strings.HasPrefix(p, root+"/ComponentIntegrity/") || strings.Count(p, "/") != 4 || m["ComponentIntegrityType"] != "SPDM" {
			continue
		}
		ai := p + "/SPDMGetSignedMeasurementsActionInfo"
		acts, _ := m["Actions"].(map[string]any)
		if acts == nil {
			acts = map[string]any{}
			m["Actions"] = acts
		}
		acts["#ComponentIntegrity.SPDMGetSignedMeasurements"] = map[string]any{
			"target": p + "/Actions/ComponentIntegrity.SPDMGetSignedMeasurements", "@Redfish.ActionInfo": ai}
		b.res[ai] = map[string]any{"@odata.type": "#ActionInfo.v1_3_0.ActionInfo", "@odata.id": ai, "Id": "SPDMGetSignedMeasurementsActionInfo",
			"Name": "SPDMGetSignedMeasurements Action Info", "Parameters": []any{
				map[string]any{"Name": "MeasurementIndices", "Required": false, "DataType": "NumberArray"},
				map[string]any{"Name": "Nonce", "Required": false, "DataType": "String"},
				map[string]any{"Name": "SlotId", "Required": false, "DataType": "Number", "AllowableValues": []any{0, 1, 15}},
				map[string]any{"Name": "RawBitstream", "Required": false, "DataType": "Boolean"}}}
	}
}

// spdmGetSignedMeasurements serves the action: 200 with the response object.
func (b *BMC) spdmGetSignedMeasurements(w http.ResponseWriter, in map[string]any, resPath string) {
	nonce := strings.Repeat("00", 32)
	if s, ok := in["Nonce"]; ok {
		n, _ := s.(string)
		if !spdmNonce.MatchString(n) {
			b.fail(w, http.StatusBadRequest, "", "The Nonce is 32 bytes as 64 hexadecimal digits.", "Base.1.19.ActionParameterValueFormatError")
			return
		}
		nonce = strings.ToLower(n)
	}
	indices := "255"
	if l, ok := in["MeasurementIndices"].([]any); ok {
		parts := make([]string, 0, len(l))
		for _, x := range l {
			f, isNum := x.(float64)
			if !isNum || f < 0 || f > 255 || f != float64(int(f)) {
				b.fail(w, http.StatusBadRequest, "", "A measurement index is an integer from 0 to 255.", "Base.1.19.ActionParameterValueTypeError")
				return
			}
			parts = append(parts, fmt.Sprint(int(f)))
		}
		indices = strings.Join(parts, ",")
	}
	nb, _ := hex.DecodeString(nonce)
	head := []byte(spdmSignedLabel + "\nindices=" + indices + "\nresource=" + resPath + "\nnonce=")
	blob := append(append(head, nb...), make([]byte, 96)...)
	sum := sha256.Sum256(blob)
	blob = append(blob, sum[:]...)
	slot := 0
	if f, ok := in["SlotId"].(float64); ok {
		slot = int(f)
	}
	resp := map[string]any{"Version": "1.1", "SigningAlgorithm": "TPM_ALG_ECDSA_ECC_NIST_P384", "HashingAlgorithm": "TPM_ALG_SHA_384",
		"SignedMeasurements": base64.StdEncoding.EncodeToString(blob)}
	if slot == 15 {
		resp["PublicKey"] = "-----BEGIN PUBLIC KEY-----\nbW9jayBrZXkgbm90IGEga2V5\n-----END PUBLIC KEY-----\n"
	} else if cert := b.res[resPath]; cert != nil {
		if auth, _ := cert["SPDM"].(map[string]any); auth != nil {
			if ia, _ := auth["IdentityAuthentication"].(map[string]any); ia != nil {
				if ra, _ := ia["ResponderAuthentication"].(map[string]any); ra != nil {
					resp["Certificate"] = ra["ComponentCertificate"]
				}
			}
		}
	}
	writeJSON(w, http.StatusOK, resp, "")
}
