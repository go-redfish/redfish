package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/go-redfish/redfish"
	"github.com/spf13/cobra"
)

// readKey reads a PEM private key from a stream: at most redfish.MaxKeyPEM bytes,
// never empty. The error never holds what was read.
func readKey(r io.Reader) (redfish.Secret, error) {
	b, err := io.ReadAll(io.LimitReader(r, redfish.MaxKeyPEM+1))
	if err != nil {
		return "", errors.New("reading the private key failed")
	}
	defer clear(b)
	if len(b) > redfish.MaxKeyPEM {
		return "", fmt.Errorf("the private key is larger than %d bytes", redfish.MaxKeyPEM)
	}
	if len(bytes.TrimSpace(b)) == 0 {
		return "", errors.New("the private key is empty")
	}
	return redfish.Secret(b), nil
}

// secretFlags are where a secret of a certificate command comes from: a file
// (mode 0600) or the standard input, never a flag or the environment.
type secretFlags struct {
	keyFile, p12File, challengeFile string
	keyStdin                        bool
}

func (s *secretFlags) addKey(cmd *cobra.Command) {
	f := cmd.Flags()
	f.StringVar(&s.keyFile, "key-file", "", "file holding the PEM private key of the certificate, for a service that does not know it (mode 0600)")
	f.BoolVar(&s.keyStdin, "key-stdin", false, "read that private key from the standard input")
	f.StringVar(&s.p12File, "p12-password-file", "", "file holding the password of a PKCS12 certificate (mode 0600)")
}

func (s *secretFlags) addChallenge(cmd *cobra.Command) {
	cmd.Flags().StringVar(&s.challengeFile, "challenge-password-file", "", "file holding the challenge password of the request (mode 0600)")
}

// key reads the private key, "" when none is given.
func (s *secretFlags) key(cmd *cobra.Command) (redfish.Secret, error) {
	switch {
	case s.keyFile != "" && s.keyStdin:
		return "", errors.New("--key-file and --key-stdin are exclusive")
	case s.keyFile != "":
		f, err := openSecretFile(s.keyFile)
		if err != nil {
			return "", err
		}
		defer f.Close()
		k, err := readKey(f)
		if err != nil {
			return "", fmt.Errorf("%s: %w", s.keyFile, err)
		}
		return k, nil
	case s.keyStdin:
		return readKey(cmd.InOrStdin())
	}
	return "", nil
}

// fileSecret reads a one-line secret from a file of mode 0600; "" when no file is named.
func fileSecret(path string) (redfish.Secret, error) {
	if path == "" {
		return "", nil
	}
	f, err := openSecretFile(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	pw, err := readPassword(f)
	if err != nil {
		return "", fmt.Errorf("%s: %w", path, err)
	}
	return pw, nil
}

// readCertFile reads a certificate file (public data: any mode), bounded.
func readCertFile(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", fmt.Errorf("certificate file: %w", err)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		return "", fmt.Errorf("certificate file %s is not a regular file", p)
	}
	b, err := io.ReadAll(io.LimitReader(f, redfish.MaxCertificateString+1))
	if err != nil {
		return "", fmt.Errorf("certificate file: %w", err)
	}
	if len(b) > redfish.MaxCertificateString {
		return "", fmt.Errorf("certificate file %s is larger than %d bytes", p, redfish.MaxCertificateString)
	}
	return string(b), nil
}

// certInput builds the CertificateInput of replace and install.
func certInput(cmd *cobra.Command, s *secretFlags, certFile, typ string) (redfish.CertificateInput, error) {
	cs, err := readCertFile(certFile)
	if err != nil {
		return redfish.CertificateInput{}, err
	}
	key, err := s.key(cmd)
	if err != nil {
		return redfish.CertificateInput{}, err
	}
	pw, err := fileSecret(s.p12File)
	if err != nil {
		return redfish.CertificateInput{}, err
	}
	return redfish.CertificateInput{CertificateString: cs, CertificateType: typ, PrivateKey: key, Password: pw}, nil
}

// emitCSR writes the request: the PEM to --out (a new file: an existing one is
// never overwritten) or to the standard output, what it says on stderr.
func emitCSR(cmd *cobra.Command, csr redfish.CSR, out string) error {
	if i := csr.Info; i != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "csr: subject %s, %s", clean(i.Subject), i.PublicKeyAlgorithm)
		if i.KeyCurve != "" {
			fmt.Fprintf(cmd.ErrOrStderr(), " %s", clean(i.KeyCurve))
		} else if i.KeyBits > 0 {
			fmt.Fprintf(cmd.ErrOrStderr(), " %d bits", i.KeyBits)
		}
		fmt.Fprintf(cmd.ErrOrStderr(), ", signature valid: %v\n", i.SignatureValid)
	} else if csr.InfoError != "" {
		fmt.Fprintf(cmd.ErrOrStderr(), "warning: the request does not decode: %s\n", clean(csr.InfoError))
	}
	if csr.CollectionURI != "" {
		fmt.Fprintf(cmd.ErrOrStderr(), "csr: install the signed certificate in %s\n", clean(csr.CollectionURI))
	}
	if csr.CertificateURI != "" {
		fmt.Fprintf(cmd.ErrOrStderr(), "csr: the signed certificate replaces %s (certs replace)\n", clean(csr.CertificateURI))
	}
	if out == "" {
		_, err := io.WriteString(cmd.OutOrStdout(), csr.PEM)
		return err
	}
	f, err := os.OpenFile(out, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return fmt.Errorf("--out: %w", err)
	}
	if _, err := io.WriteString(f, csr.PEM); err != nil {
		f.Close()
		return fmt.Errorf("--out: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("--out: %w", err)
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "csr: wrote %d bytes to %s\n", len(csr.PEM), clean(out))
	return nil
}

func certsCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "certs",
		Short: "CertificateService: status, list, show, csr, replace, install, delete, rekey, renew",
		Long: `certs drives the CertificateService (DSP0266 1.23.0 9.9.13 and 13.1.3; the
schemas CertificateService 1.2.2, Certificate 1.11.0 and CertificateLocations
1.0.4): status reads the service, list and show read the certificates it locates
(show decodes the certificate with crypto/x509 and prints its subject, issuer,
validity, key and usages), csr makes a certificate signing request, install adds
a certificate to a collection, replace puts a certificate in place of another,
delete removes one, rekey and renew make a CSR from an existing certificate.

A certificate made from a CSR of the service needs no private key. For one that
was not, the key is read from --key-file (mode 0600) or --key-stdin, checked
against the certificate, sent as the schema asks (as part of the certificate
string) and never printed: an error that holds it is redacted. Passwords come from
files of mode 0600, never from a flag or the environment. A certificate is named by
its Id (one of the CertificateLocations) or by its path.`,
		Args: cobra.NoArgs,
	}
	cmd.AddCommand(certsStatusCmd(g), certsListCmd(g), certsShowCmd(g), certsCSRCmd(g), certsInstallCmd(g), certsReplaceCmd(g), certsDeleteCmd(g), certsRekeyCmd(g), certsRenewCmd(g))
	return cmd
}

func certsStatusCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "The CertificateService: its locations resource and the actions it has",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := g.client(cmd, false)
			if err != nil {
				return err
			}
			s, err := c.CertificateService(cmd.Context())
			if err != nil {
				return err
			}
			return g.show(cmd, s)
		},
	}
}

func subjectOf(c redfish.Certificate) string {
	switch {
	case c.X509 != nil:
		return c.X509.Subject
	case c.Subject != nil && c.Subject.CommonName != "":
		return "CN=" + c.Subject.CommonName
	}
	return ""
}

func certsListCmd(g *globals) *cobra.Command {
	var uris bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List every certificate the service holds (CertificateLocations)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := g.client(cmd, false)
			if err != nil {
				return err
			}
			if uris {
				u, err := c.CertificateLocations(cmd.Context())
				if err != nil {
					return err
				}
				return g.show(cmd, u)
			}
			certs, err := c.Certificates(cmd.Context())
			if err != nil {
				return err
			}
			rows := make([]map[string]any, 0, len(certs))
			for _, ct := range certs {
				row := map[string]any{"uri": ct.URI, "type": ct.Type, "subject": subjectOf(ct), "rekey": ct.Rekey, "renew": ct.Renew}
				if x := ct.X509; x != nil {
					row["issuer"], row["not_after"], row["serial"] = x.Issuer, x.NotAfter, x.SerialNumber
				} else {
					row["issuer"], row["not_after"], row["serial"] = "", ct.ValidNotAfter, ct.SerialNumber
				}
				rows = append(rows, row)
			}
			return g.show(cmd, rows)
		},
	}
	cmd.Flags().BoolVar(&uris, "uris", false, "list only the paths the CertificateLocations gives")
	return cmd
}

func certsShowCmd(g *globals) *cobra.Command {
	var pemOnly bool
	cmd := &cobra.Command{
		Use:   "show ID|URI",
		Short: "Show a certificate: what the service says, and what the certificate says (x509)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := g.client(cmd, false)
			if err != nil {
				return err
			}
			ct, err := c.Certificate(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if pemOnly {
				if ct.PEM == "" {
					return errors.New("the certificate has no PEM string to print")
				}
				_, err := io.WriteString(cmd.OutOrStdout(), ct.PEM)
				return err
			}
			return g.show(cmd, ct)
		},
	}
	cmd.Flags().BoolVar(&pemOnly, "pem", false, "print the certificate as PEM, nothing else")
	return cmd
}

func certsCSRCmd(g *globals) *cobra.Command {
	var r redfish.CSRRequest
	var out string
	var s secretFlags
	cmd := &cobra.Command{
		Use:   "csr --collection URI",
		Short: "Make a certificate signing request (GenerateCSR); the PEM goes to stdout or --out",
		Long: `csr runs CertificateService.GenerateCSR: the service makes a key pair, keeps the
private key for the installation of the signed certificate and answers with the
PEM request (PKCS#10, RFC 2986), which is written to the standard output, or to
--out (a new file: an existing one is not overwritten). Each parameter is checked
against the service's ActionInfo before the POST. What the request says is
printed on stderr.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			req := r
			pw, err := fileSecret(s.challengeFile)
			if err != nil {
				return err
			}
			req.ChallengePassword = pw
			c, err := g.client(cmd, false)
			if err != nil {
				return err
			}
			csr, _, err := c.GenerateCSR(cmd.Context(), req)
			if err != nil {
				return err
			}
			return emitCSR(cmd, csr, out)
		},
	}
	f := cmd.Flags()
	f.StringVar(&r.CertificateCollection, "collection", "", "the certificate collection the signed certificate is installed in, a path (required)")
	f.StringVar(&r.CommonName, "common-name", "", "the name of the component to secure")
	f.StringArrayVar(&r.AlternativeNames, "alt-name", nil, "an additional host name (subjectAltName; repeatable)")
	f.StringVar(&r.Organization, "organization", "", "the organization")
	f.StringVar(&r.OrganizationalUnit, "organizational-unit", "", "the unit of the organization")
	f.StringVar(&r.City, "city", "", "the city or locality")
	f.StringVar(&r.State, "state", "", "the state, province or region")
	f.StringVar(&r.Country, "country", "", "the two-letter ISO country code")
	f.StringVar(&r.Email, "email", "", "the contact's email address")
	f.StringVar(&r.GivenName, "given-name", "", "the given name of the requester")
	f.StringVar(&r.Surname, "surname", "", "the surname of the requester")
	f.StringVar(&r.Initials, "initials", "", "the initials of the requester")
	f.StringVar(&r.ContactPerson, "contact-person", "", "the name of the user making the request")
	f.StringVar(&r.UnstructuredName, "unstructured-name", "", "the unstructured name of the subject")
	f.StringVar(&r.KeyPairAlgorithm, "algorithm", "", "the key pair type, e.g. TPM_ALG_RSA or TPM_ALG_ECDSA (the service lists its own)")
	f.IntVar(&r.KeyBitLength, "bits", 0, "the key length in bits, when the algorithm needs one")
	f.StringVar(&r.KeyCurveID, "curve", "", "the curve of the key, e.g. TPM_ECC_NIST_P256")
	f.StringArrayVar(&r.KeyUsage, "key-usage", nil, "a key usage, e.g. DigitalSignature, ServerAuthentication (repeatable)")
	s.addChallenge(cmd)
	f.StringVar(&out, "out", "", "write the CSR to this new file instead of the standard output")
	_ = cmd.MarkFlagRequired("collection")
	return cmd
}

func certsInstallCmd(g *globals) *cobra.Command {
	var coll, certFile, typ string
	var s secretFlags
	cmd := &cobra.Command{
		Use:   "install --collection URI --cert-file FILE",
		Short: "Install a certificate in a certificate collection (POST)",
		Long: `install adds a certificate to a CertificateCollection (a POST with CertificateString
and CertificateType). The formats the collection supports (the
@Redfish.SupportedCertificates annotation, DSP0266 9.9.13) are checked first. A
certificate made from a CSR of the service needs no key; for another, give
--key-file or --key-stdin.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			in, err := certInput(cmd, &s, certFile, typ)
			if err != nil {
				return err
			}
			c, err := g.client(cmd, false)
			if err != nil {
				return err
			}
			cert, out, err := c.InstallCertificate(cmd.Context(), coll, in)
			if err != nil {
				return err
			}
			return g.show(cmd, map[string]any{"changed": out.Changed, "message": out.Message, "certificate": cert})
		},
	}
	f := cmd.Flags()
	f.StringVar(&coll, "collection", "", "the certificate collection, a path (required)")
	f.StringVar(&certFile, "cert-file", "", "the certificate file (required)")
	f.StringVar(&typ, "type", "PEM", "the format: PEM, PEMchain, PKCS7 or PKCS12")
	s.addKey(cmd)
	_ = cmd.MarkFlagRequired("collection")
	_ = cmd.MarkFlagRequired("cert-file")
	return cmd
}

func certsReplaceCmd(g *globals) *cobra.Command {
	var certFile, typ string
	var s secretFlags
	cmd := &cobra.Command{
		Use:   "replace ID|URI --cert-file FILE",
		Short: "Replace a certificate (ReplaceCertificate); the new one's path is printed",
		Long: `replace runs CertificateService.ReplaceCertificate: the certificate named is
replaced by the one in --cert-file. The service answers with the path of the new
certificate resource, which is printed. The type is checked against the
service's ActionInfo before the POST. For a certificate that was not made from a
CSR of the service, give --key-file or --key-stdin.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			in, err := certInput(cmd, &s, certFile, typ)
			if err != nil {
				return err
			}
			c, err := g.client(cmd, false)
			if err != nil {
				return err
			}
			uri, out, err := c.ReplaceCertificate(cmd.Context(), args[0], in)
			if err != nil {
				return err
			}
			return g.show(cmd, map[string]any{"changed": out.Changed, "message": out.Message, "uri": uri})
		},
	}
	f := cmd.Flags()
	f.StringVar(&certFile, "cert-file", "", "the new certificate file (required)")
	f.StringVar(&typ, "type", "PEM", "the format: PEM, PEMchain, PKCS7 or PKCS12")
	s.addKey(cmd)
	_ = cmd.MarkFlagRequired("cert-file")
	return cmd
}

func certsDeleteCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "delete ID|URI",
		Short: "Delete a certificate",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := g.client(cmd, false)
			if err != nil {
				return err
			}
			out, err := c.DeleteCertificate(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			return g.showOutcome(cmd, out)
		},
	}
}

func certsRekeyCmd(g *globals) *cobra.Command {
	var o redfish.RekeyOptions
	var out string
	var s secretFlags
	cmd := &cobra.Command{
		Use:   "rekey ID|URI",
		Short: "Make a CSR with a new key pair for a certificate (Certificate.Rekey)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opt := o
			pw, err := fileSecret(s.challengeFile)
			if err != nil {
				return err
			}
			opt.ChallengePassword = pw
			c, err := g.client(cmd, false)
			if err != nil {
				return err
			}
			csr, _, err := c.RekeyCertificate(cmd.Context(), args[0], opt)
			if err != nil {
				return err
			}
			return emitCSR(cmd, csr, out)
		},
	}
	f := cmd.Flags()
	f.StringVar(&o.KeyPairAlgorithm, "algorithm", "", "the key pair type, e.g. TPM_ALG_RSA or TPM_ALG_ECDSA")
	f.IntVar(&o.KeyBitLength, "bits", 0, "the key length in bits")
	f.StringVar(&o.KeyCurveID, "curve", "", "the curve of the key, e.g. TPM_ECC_NIST_P256")
	s.addChallenge(cmd)
	f.StringVar(&out, "out", "", "write the CSR to this new file instead of the standard output")
	return cmd
}

func certsRenewCmd(g *globals) *cobra.Command {
	var out string
	var s secretFlags
	cmd := &cobra.Command{
		Use:   "renew ID|URI",
		Short: "Make a CSR from the existing information and key pair of a certificate (Certificate.Renew)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			pw, err := fileSecret(s.challengeFile)
			if err != nil {
				return err
			}
			c, err := g.client(cmd, false)
			if err != nil {
				return err
			}
			csr, _, err := c.RenewCertificate(cmd.Context(), args[0], pw)
			if err != nil {
				return err
			}
			return emitCSR(cmd, csr, out)
		},
	}
	s.addChallenge(cmd)
	cmd.Flags().StringVar(&out, "out", "", "write the CSR to this new file instead of the standard output")
	return cmd
}
