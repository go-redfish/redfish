package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/go-redfish/redfish/mockbmc"
	"github.com/spf13/cobra"
)

func mockCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{Use: "mock", Short: "A mock BMC to try the client against", Args: cobra.NoArgs}
	var listen, caOut string
	var o mockbmc.Options
	var pwFile string
	serve := &cobra.Command{
		Use:   "serve",
		Short: "Serve a DMTF mockup (--tree) over HTTPS (a test double: it keeps state in memory)",
		Long: `serve starts a Redfish service on a self-signed certificate valid for 24 hours,
writes the certificate to --ca-out (use it as --ca-file) and prints the endpoint.
The user is admin; the password is in --mock-password-file, else "password" (it is a
test double, and says so). It stops on interrupt.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			o.User = "admin"
			if pwFile != "" {
				_, pw, err := filePassword{"admin", pwFile}.Get(cmd.Context())
				if err != nil {
					return err
				}
				o.Password = pw
			} else {
				o.Password = "password"
				fmt.Fprintln(cmd.ErrOrStderr(), `warning: no --mock-password-file: the mock BMC accepts admin / password`)
			}
			b, err := mockbmc.New(o)
			if err != nil {
				return err
			}
			if caOut == "" {
				dir, err := userConfigDir()
				if err != nil {
					return fmt.Errorf("no user config directory: pass --ca-out: %w", err)
				}
				caOut = filepath.Join(dir, "redfish", "mock-ca.pem")
			}
			cert, pemBytes, err := selfSigned()
			if err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(caOut), 0o700); err != nil {
				return err
			}
			if err := os.WriteFile(caOut, pemBytes, 0o644); err != nil { // a certificate, not a secret
				return err
			}
			ln, err := net.Listen("tcp", listen)
			if err != nil {
				return err
			}
			srv := &http.Server{Handler: b.Handler(), TLSConfig: &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12},
				ReadHeaderTimeout: 10 * time.Second}
			fmt.Fprintf(cmd.OutOrStdout(), "endpoint: https://%s\nca-file: %s\nuser: admin\n", ln.Addr(), caOut)
			ctx := cmd.Context()
			errc := make(chan error, 1)
			go func() { errc <- srv.ServeTLS(ln, "", "") }()
			select {
			case err := <-errc:
				return err
			case <-ctx.Done():
				sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if err := srv.Shutdown(sctx); err != nil {
					return err
				}
				if err := <-errc; err != nil && !errors.Is(err, http.ErrServerClosed) {
					return err
				}
				return nil
			}
		},
	}
	serve.Flags().StringVar(&listen, "listen", "127.0.0.1:0", "address to listen on (port 0: a free loopback port)")
	serve.Flags().StringVar(&caOut, "ca-out", "", "where to write the certificate (default: under the user config directory)")
	serve.Flags().StringVar(&pwFile, "mock-password-file", "", "file holding admin's password (mode 0600)")
	serve.Flags().StringVar(&o.Tree, "tree", mockbmc.TreeRackmount, "mockup to serve: "+mockbmc.TreeRackmount+" or "+mockbmc.TreeEmulator)
	serve.Flags().IntVar(&o.TaskPolls, "task-polls", 2, "how many times a task monitor answers 202 before the task completes")
	cmd.AddCommand(serve)
	return cmd
}

// selfSigned makes a certificate for the loopback names, valid for a day.
func selfSigned() (tls.Certificate, []byte, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 62))
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	tpl := &x509.Certificate{
		SerialNumber: serial, Subject: pkix.Name{CommonName: "redfish mock BMC"},
		NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(24 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames: []string{"localhost"}, IPAddresses: []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback},
		BasicConstraintsValid: true, IsCA: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, certPEM, nil
}
