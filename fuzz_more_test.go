package redfish_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-redfish/redfish"
	"github.com/go-redfish/redfish/mockbmc"
)

// Whatever the string, ValidateURL accepts only https with a host, no
// credentials, no query and no fragment (not even an empty one).
func FuzzValidateURL(f *testing.F) {
	for _, s := range []string{
		"https://bmc.example", "https://bmc.example:8443/", "http://bmc.example", "https://u:p@bmc.example",
		"https://@bmc.example", "https://bmc.example/?", "https://bmc.example/#", "https://bmc.example/#x",
		"https://bmc.example?a=b", "https:///x", "https://", "HTTPS://BMC", "https://[::1]:443/", "javascript:alert(1)",
		"https://bmc.example/\x00", "https://bmc .example", "//bmc.example", "", "https://bmc.example%00",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		if redfish.ValidateURL(raw) != nil {
			return
		}
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatalf("accepted %q that does not parse: %v", raw, err)
		}
		if u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.ForceQuery {
			t.Fatalf("accepted %q: %+v", raw, u)
		}
		if strings.ContainsAny(raw, "?#") || strings.Contains(u.Host, "@") {
			t.Fatalf("accepted %q with a query, a fragment or credentials", raw)
		}
	})
}

// Whatever Retry-After says, the delay is never negative and never above an hour.
func FuzzRetryAfter(f *testing.F) {
	for _, s := range []string{
		"", "0", "1", "120", "-1", "-0", "3600", "3601", "99999999999999999999", "9223372036854775807", "1e9", " 5 ",
		"Wed, 21 Oct 2015 07:28:00 GMT", "Mon, 01 Jan 2999 00:00:00 GMT", "Thu, 01 Jan 1970 00:00:00 GMT", "soon", "\x00",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, v string) {
		r := &redfish.Response{Header: http.Header{"Retry-After": {v}}}
		if d := r.RetryAfter(); d < 0 || d > time.Hour {
			t.Fatalf("Retry-After %q gives %v", v, d)
		}
	})
}

func newCSR(tb testing.TB, cn string) (csrPEM, keyPEM string) {
	tb.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		tb.Fatal(err)
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: cn}, DNSNames: []string{cn}}, k)
	if err != nil {
		tb.Fatal(err)
	}
	kd, err := x509.MarshalPKCS8PrivateKey(k)
	if err != nil {
		tb.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der})),
		string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: kd}))
}

// ParseCSR reads a request only: it refuses what is not a CERTIFICATE REQUEST,
// is bounded, and never returns (or echoes) the bytes of a private key block in
// the string it was given.
func FuzzParseCSR(f *testing.F) {
	c, k := newCSR(f, "seed.example.org")
	f.Add(c)
	f.Add(k)
	f.Add(k + c)
	f.Add(c + k)
	f.Add(strings.Replace(c, "CERTIFICATE REQUEST", "NEW CERTIFICATE REQUEST", 2))
	f.Add(strings.Replace(c, "CERTIFICATE REQUEST", "CERTIFICATE", 2))
	f.Add("-----BEGIN CERTIFICATE REQUEST-----\nAAAA\n-----END CERTIFICATE REQUEST-----\n")
	f.Add("")
	f.Fuzz(func(t *testing.T, s string) {
		info, err := redfish.ParseCSR(s)
		if err != nil {
			if info.Subject != "" || info.SignatureAlgorithm != "" || info.DNSNames != nil {
				t.Fatalf("an error with data: %+v", info)
			}
			return
		}
		if len(s) > redfish.MaxCSRString {
			t.Fatalf("accepted %d bytes", len(s))
		}
		b, _ := pem.Decode([]byte(s))
		if b == nil || (b.Type != "CERTIFICATE REQUEST" && b.Type != "NEW CERTIFICATE REQUEST") {
			t.Fatalf("accepted a string whose first block is %v", b)
		}
		if info.PublicKeyAlgorithm == "" {
			t.Fatalf("no public key algorithm: %+v", info)
		}
		out, _ := json.Marshal(info)
		rest := []byte(s)
		for {
			var blk *pem.Block
			blk, rest = pem.Decode(rest)
			if blk == nil {
				break
			}
			if strings.Contains(blk.Type, "PRIVATE KEY") && len(blk.Bytes) > 0 && strings.Contains(string(out), string(blk.Bytes)) {
				t.Fatalf("the result holds the bytes of a %s block", blk.Type)
			}
		}
	})
}

func taskStuck(t *testing.T, cfg redfish.Config) (*redfish.Client, string) {
	t.Helper()
	_, srv := newMock(t, mockbmc.Options{TaskPolls: 1 << 30}) // a task that never ends
	c := newClient(t, srv, cfg)
	r, err := c.Post(context.Background(), "/redfish/v1/UpdateService/Actions/SimpleUpdate", map[string]any{"ImageURI": "https://fw.example/bmc.bin"})
	if err != nil || !r.IsTask() || r.Location() == "" {
		t.Fatalf("simple update: %v %+v", err, r)
	}
	return c, r.Location()
}

// A context with no deadline no longer waits forever: Config.TaskTimeout bounds
// the wait, and the error says it is that bound.
func TestWaitTaskBoundsAContextWithoutDeadline(t *testing.T) {
	c, mon := taskStuck(t, redfish.Config{TaskTimeout: 300 * time.Millisecond})
	start := time.Now()
	_, err := c.WaitTask(context.Background(), mon, 0, nil)
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "did not end within 300ms") {
		t.Fatalf("WaitTask on a task that never ends = %v", err)
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Fatalf("took %v", d)
	}
}

// A deadline or a cancellation of the caller keeps its own error and is not
// replaced by (or extended to) the default bound.
func TestWaitTaskKeepsTheCallersContext(t *testing.T) {
	c, mon := taskStuck(t, redfish.Config{TaskTimeout: time.Hour})
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	_, err := c.WaitTask(ctx, mon, 0, nil)
	if !errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), "did not end") {
		t.Fatalf("caller deadline: %v", err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	time.AfterFunc(300*time.Millisecond, cancel)
	_, err = c.WaitTask(ctx, mon, 0, nil)
	if !errors.Is(err, context.Canceled) || strings.Contains(err.Error(), "did not end") {
		t.Fatalf("caller cancel: %v", err)
	}
}

// A negative TaskTimeout removes the library's bound: only the context ends the wait.
func TestWaitTaskUnboundedByChoice(t *testing.T) {
	c, mon := taskStuck(t, redfish.Config{TaskTimeout: -1})
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(500*time.Millisecond, cancel)
	_, err := c.WaitTask(ctx, mon, 0, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("unbounded wait ended with %v", err)
	}
}

func TestDefaultTaskTimeoutIsTheCLIsWatchTimeout(t *testing.T) {
	if redfish.DefaultTaskTimeout != time.Hour {
		t.Fatalf("DefaultTaskTimeout = %v; the CLI's --watch-timeout default is 1h", redfish.DefaultTaskTimeout)
	}
}
