package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/go-redfish/redfish"
	"github.com/spf13/cobra"
)

func securityCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "security",
		Short: "Security, read only: component integrity, measurements, trusted components, security policy, keys",
		Long: `security reads the security resources of the service (DSP8010 2025.4:
ComponentIntegrity 1.5.0, TrustedComponent 1.5.0, SecurityPolicy 1.1.0, KeyService
1.0.1, KeyPolicy 1.0.1, Key 1.4.1): integrity and integrity-show list and show the
ComponentIntegrity resources (SPDM and TPM identity, measurements as Base64 and
hex), measurements runs SPDMGetSignedMeasurements, trusted lists the trusted
components of the chassis, policy shows the SPDM and TLS policy with its trusted
and revoked certificates, keys and key-policies list the NVMe-oF secrets and key
policies of the KeyService.

Nothing here changes the service: there is no command to revoke a certificate,
change a policy or create a key (a wrong write can lock an operator out of the
BMC). measurements asks the BMC to run SPDM GET_MEASUREMENTS exchanges with the
device; its answer is a signed statement that this client does NOT verify: the
output says "not verified". A key a service returns is never shown.`,
		Args: cobra.NoArgs,
	}
	cmd.AddCommand(securityIntegrityCmd(g), securityIntegrityShowCmd(g), securityMeasurementsCmd(g), securityTrustedCmd(g),
		securityPolicyCmd(g), securityKeysCmd(g), securityKeyPoliciesCmd(g))
	return cmd
}

// integritySummary is one ComponentIntegrity of a list, without its measurements.
type integritySummary struct {
	ID           string `json:"id"`
	Type         string `json:"type,omitempty"`
	Enabled      string `json:"enabled"`
	State        string `json:"state,omitempty"`
	Health       string `json:"health,omitempty"`
	Target       string `json:"target,omitempty"`
	Measurements int    `json:"measurements"`
	LastUpdated  string `json:"last_updated,omitempty"`
}

func integrityRow(ci redfish.ComponentIntegrity) integritySummary {
	s := integritySummary{ID: ci.ID, Type: ci.Type, Enabled: "-", State: ci.State, Health: ci.Health, Target: ci.TargetComponentURI, LastUpdated: ci.LastUpdated}
	if ci.Enabled != nil {
		s.Enabled = fmt.Sprint(*ci.Enabled)
	}
	if ci.SPDM != nil {
		s.Measurements += len(ci.SPDM.Measurements)
	}
	if ci.TPM != nil {
		s.Measurements += len(ci.TPM.Measurements)
	}
	return s
}

func securityIntegrityCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "integrity",
		Short: "List the ComponentIntegrity resources (-o json: with their measurements)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := g.client(cmd, false)
			if err != nil {
				return err
			}
			l, err := c.ComponentIntegrities(cmd.Context())
			if err != nil {
				return err
			}
			if g.output != "table" {
				return g.show(cmd, l)
			}
			rows := make([]integritySummary, 0, len(l))
			for _, ci := range l {
				rows = append(rows, integrityRow(ci))
			}
			return g.show(cmd, rows)
		},
	}
}

// integrityView is a ComponentIntegrity with the certificates it links.
type integrityView struct {
	redfish.ComponentIntegrity
	Certificates []redfish.LinkedCertificate `json:"certificates,omitempty"`
}

func securityIntegrityShowCmd(g *globals) *cobra.Command {
	var certs bool
	cmd := &cobra.Command{
		Use:   "integrity-show ID",
		Short: "Show a ComponentIntegrity: identity, measurements (Base64 and hex), sessions, certificates",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := g.client(cmd, false)
			if err != nil {
				return err
			}
			ci, err := c.ComponentIntegrity(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			v := integrityView{ComponentIntegrity: ci}
			if certs {
				v.Certificates = c.LinkedCertificates(cmd.Context(), ci.CertificateURIs())
			}
			if g.output == "table" {
				return printIntegrity(cmd.OutOrStdout(), v)
			}
			return g.show(cmd, v)
		},
	}
	cmd.Flags().BoolVar(&certs, "certs", false, "also read the certificates the resource links (decoded with ParseX509)")
	return cmd
}

func printIntegrity(w io.Writer, v integrityView) error {
	ci := v.ComponentIntegrity
	rows := [][2]string{{"id", ci.ID}, {"type", ci.Type + " " + ci.TypeVersion}, {"state", ci.State}, {"health", ci.Health}, {"target", ci.TargetComponentURI},
		{"last updated", ci.LastUpdated}}
	if ci.Enabled != nil {
		rows = append(rows, [2]string{"enabled", fmt.Sprint(*ci.Enabled)})
	}
	var ms []redfish.Measurement
	if s := ci.SPDM; s != nil {
		rows = append(rows, [2]string{"requester", s.RequesterURI}, [2]string{"measurement specification", s.MeasurementSpecification},
			[2]string{"summary type", s.SummaryType}, [2]string{"summary hash algorithm", s.SummaryHashAlgorithm},
			[2]string{"responder verification", s.ResponderVerification}, [2]string{"responder certificate", s.ResponderCertificateURI})
		if s.Summary != nil {
			rows = append(rows, [2]string{"summary (hex)", s.Summary.Hex})
		}
		ms = s.Measurements
	}
	if t := ci.TPM; t != nil {
		rows = append(rows, [2]string{"verification", t.VerificationStatus}, [2]string{"certificate", t.CertificateURI})
		ms = t.Measurements
	}
	tw := tabwriter.NewWriter(w, 0, 8, 2, ' ', 0)
	fmt.Fprintln(tw, "PROPERTY\tVALUE")
	for _, r := range rows {
		if strings.TrimSpace(r[1]) != "" {
			fmt.Fprintf(tw, "%s\t%s\n", clean(r[0]), clean(r[1]))
		}
	}
	if len(ms) > 0 {
		fmt.Fprintln(tw, "\t")
		fmt.Fprintln(tw, "INDEX/PCR\tTYPE\tALGORITHM\tIN SUMMARY\tUPDATED\tVALUE (hex)")
		for _, m := range ms {
			idx := "-"
			switch {
			case m.Index != nil:
				idx = fmt.Sprint(*m.Index)
			case m.PCR != nil:
				idx = fmt.Sprintf("PCR%d", *m.PCR)
			}
			in := "-"
			if m.PartOfSummary != nil {
				in = fmt.Sprint(*m.PartOfSummary)
			}
			val := "(not decoded: " + clip(m.DecodeError) + ")"
			if m.Measurement != nil {
				val = m.Measurement.Hex
				if m.SizeMatches != nil && !*m.SizeMatches {
					val += " (size does not match the algorithm)"
				}
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", idx, dash(m.Type), dash(m.HashAlgorithm), in, dash(m.LastUpdated), clean(val))
		}
	}
	if len(v.Certificates) > 0 {
		fmt.Fprintln(tw, "\t")
		printCertRows(tw, "", certsOf(v.Certificates))
	}
	return tw.Flush()
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return clean(s)
}

func certsOf(l []redfish.LinkedCertificate) []redfish.Certificate {
	var out []redfish.Certificate
	for _, lc := range l {
		if lc.Certificate != nil {
			out = append(out, *lc.Certificate)
		} else {
			out = append(out, redfish.Certificate{URI: lc.URI, X509Error: lc.Error})
		}
	}
	return out
}

// printCertRows writes certificates as rows: where they come from, their Id,
// subject, expiry and the SHA-256 fingerprint of the DER that this client computed.
func printCertRows(tw *tabwriter.Writer, from string, certs []redfish.Certificate) {
	fmt.Fprintln(tw, "FROM\tCERTIFICATE\tSUBJECT\tNOT AFTER\tSHA-256")
	for _, c := range certs {
		subj, after, fp := "-", "-", "-"
		if c.X509 != nil {
			subj, after, fp = c.X509.Subject, c.X509.NotAfter.Format("2006-01-02"), c.X509.SHA256Fingerprint
		} else if c.X509Error != "" {
			subj = "(not decoded: " + clip(c.X509Error) + ")"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", dash(from), clean(c.ID+" "+c.URI), clean(subj), after, clean(fp))
	}
}

func securityMeasurementsCmd(g *globals) *cobra.Command {
	var (
		indices      []int
		nonce        string
		slot         int
		rawBitstream bool
	)
	cmd := &cobra.Command{
		Use:   "measurements ID",
		Short: "SPDMGetSignedMeasurements: ask the BMC for signed SPDM measurements (the signature is NOT verified)",
		Long: `measurements runs the SPDMGetSignedMeasurements action of a ComponentIntegrity of
type SPDM (ComponentIntegrity 1.5.0). The BMC runs SPDM GET_MEASUREMENTS exchanges
with the device and answers with a signed statement over the nonce and the
measurements; nothing in the service changes. --index names the measurement blocks
(0 to 254, repeated or comma-separated, or 255 for all; default all), --nonce is 32
bytes as 64 hex digits (default: a fresh random one), --slot the SPDM slot of the
signing certificate.

The signature is NOT verified: this client does not check it against the SPDM
certificate chain (DSP0274); the output says so ("signature_verified": false) and
the same line is printed on stderr. signed_measurements.base64 is the blob for a
verifier that does.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := g.client(cmd, false)
			if err != nil {
				return err
			}
			req := redfish.SignedMeasurementsRequest{Indices: indices, Nonce: nonce, RawBitstream: rawBitstream}
			if cmd.Flags().Changed("slot") {
				req.SlotID = &slot
			}
			sm, err := c.GetSignedMeasurements(cmd.Context(), args[0], req)
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.ErrOrStderr(), "note: the signature is not verified by this client")
			return g.show(cmd, sm)
		},
	}
	f := cmd.Flags()
	f.IntSliceVar(&indices, "index", nil, "the measurement indices to sign (0-254, or 255 for all)")
	f.StringVar(&nonce, "nonce", "", "the nonce, 32 bytes as 64 hex digits (default: random)")
	f.IntVar(&slot, "slot", 0, "the SPDM slot of the signing certificate (default: the service's, 0)")
	f.BoolVar(&rawBitstream, "raw-bitstream", false, "ask for raw bitstreams (SPDM 1.2 and later)")
	return cmd
}

// chassisIDs resolves --chassis (an Id or a path) to the chassis it names.
func chassisByID(ctx context.Context, c *redfish.Client, id string) ([]string, error) {
	if id == "" {
		return nil, nil
	}
	all, err := c.ChassisURIs(ctx)
	if err != nil {
		return nil, err
	}
	for _, u := range all {
		if u == id || path.Base(u) == id {
			return []string{u}, nil
		}
	}
	return nil, fmt.Errorf("%w: chassis %q", redfish.ErrNotFound, clip(id))
}

type trustedView struct {
	redfish.TrustedComponent
	Certificates []redfish.Certificate `json:"certificates,omitempty"`
}

func securityTrustedCmd(g *globals) *cobra.Command {
	var chassis string
	var certs bool
	cmd := &cobra.Command{
		Use:   "trusted [ID]",
		Short: "List the trusted components of the chassis (TPM, root of trust), or show one",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := g.client(cmd, false)
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			set, err := chassisByID(ctx, c, chassis)
			if err != nil {
				return err
			}
			if len(args) == 0 {
				l, err := c.TrustedComponents(ctx, set...)
				if err != nil {
					return err
				}
				return g.show(cmd, l)
			}
			t, err := c.TrustedComponent(ctx, args[0], set...)
			if err != nil {
				return err
			}
			v := trustedView{TrustedComponent: t}
			if certs {
				if v.Certificates, err = c.CertificatesOf(ctx, t.CertificatesURI); err != nil {
					return err
				}
			}
			return g.show(cmd, v)
		},
	}
	cmd.Flags().StringVar(&chassis, "chassis", "", "the chassis to read (default: every chassis)")
	cmd.Flags().BoolVar(&certs, "certs", false, "with an ID: also read its certificates (decoded with ParseX509)")
	return cmd
}

type policyView struct {
	redfish.SecurityPolicy
	Certificates []redfish.PolicyCertificates `json:"certificates,omitempty"`
}

func securityPolicyCmd(g *globals) *cobra.Command {
	var manager string
	var noCerts bool
	cmd := &cobra.Command{
		Use:   "policy",
		Short: "The SecurityPolicy of a manager: SPDM and TLS policy, trusted and revoked certificates",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := g.client(cmd, false)
			if err != nil {
				return err
			}
			sp, err := c.SecurityPolicy(cmd.Context(), manager)
			if err != nil {
				return err
			}
			v := policyView{SecurityPolicy: sp}
			if !noCerts {
				if v.Certificates, err = c.PolicyCertificates(cmd.Context(), sp); err != nil {
					return err
				}
			}
			if g.output == "table" {
				return printPolicy(cmd.OutOrStdout(), v)
			}
			return g.show(cmd, v)
		},
	}
	cmd.Flags().StringVar(&manager, "id", "", "the manager to read (default: the only one)")
	cmd.Flags().BoolVar(&noCerts, "no-certs", false, "do not read the trusted and revoked certificates")
	return cmd
}

// printPolicy writes the policy as dotted property rows, then its certificates.
func printPolicy(w io.Writer, v policyView) error {
	b, err := json.Marshal(v.SecurityPolicy)
	if err != nil {
		return err
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return err
	}
	var rows []string
	flatten("", m, &rows)
	sort.Strings(rows)
	tw := tabwriter.NewWriter(w, 0, 8, 2, ' ', 0)
	fmt.Fprintln(tw, "PROPERTY\tVALUE")
	for _, r := range rows {
		fmt.Fprintln(tw, r)
	}
	for _, pc := range v.Certificates {
		fmt.Fprintln(tw, "\t")
		printCertRows(tw, pc.Collection, pc.Certificates)
		if len(pc.Certificates) == 0 {
			fmt.Fprintf(tw, "%s\t(none)\t\t\t\n", clean(pc.Collection))
		}
	}
	return tw.Flush()
}

func flatten(prefix string, v any, out *[]string) {
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			flatten(prefix+k+".", e, out)
		}
	case []any:
		parts := make([]string, 0, len(x))
		for _, e := range x {
			parts = append(parts, fmt.Sprint(e))
		}
		*out = append(*out, clean(strings.TrimSuffix(prefix, "."))+"\t"+clean("["+strings.Join(parts, ", ")+"]"))
	default:
		*out = append(*out, clean(strings.TrimSuffix(prefix, "."))+"\t"+clean(fmt.Sprint(x)))
	}
}

func securityKeysCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "keys [ID]",
		Short: "List the NVMe-oF secrets of the KeyService (never their key), or show one",
		Long: `keys lists the NVMe-oF secrets of the KeyService: NQN, protocol, hash list. The
key itself (KeyString) is never shown, kept or put in an error; key_present says
the service returned it. Reading key material is not a goal of this client.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := g.client(cmd, false)
			if err != nil {
				return err
			}
			l, err := c.NVMeoFSecrets(cmd.Context())
			if err != nil {
				return err
			}
			present := false
			for _, s := range l {
				present = present || s.KeyPresent
			}
			if present {
				fmt.Fprintln(cmd.ErrOrStderr(), "note: the service returns the key material of these secrets; it is not shown or kept by this client")
			}
			if len(args) == 1 {
				for _, s := range l {
					if s.ID == args[0] || s.URI == args[0] {
						return g.show(cmd, s)
					}
				}
				return fmt.Errorf("%w: secret %q", redfish.ErrNotFound, clip(args[0]))
			}
			return g.show(cmd, l)
		},
	}
}

func securityKeyPoliciesCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "key-policies [ID]",
		Short: "List the NVMe-oF key policies of the KeyService, or show one",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := g.client(cmd, false)
			if err != nil {
				return err
			}
			l, err := c.NVMeoFKeyPolicies(cmd.Context())
			if err != nil {
				return err
			}
			if len(args) == 1 {
				for _, p := range l {
					if p.ID == args[0] || p.URI == args[0] {
						return g.show(cmd, p)
					}
				}
				return fmt.Errorf("%w: key policy %q", redfish.ErrNotFound, clip(args[0]))
			}
			return g.show(cmd, l)
		},
	}
}
