package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/go-redfish/redfish"
	"github.com/spf13/cobra"
)

// userConfigDir is os.UserConfigDir; tests replace it.
var userConfigDir = os.UserConfigDir

// globals are the connection flags.
type globals struct {
	endpoint, user, passwordFile, caFile string
	insecure, session, noExpand          bool
	timeout                              time.Duration
	output                               string
}

// maxPasswordFile bounds a password file.
const maxPasswordFile = 4096

// filePassword reads the password from a file each time it is needed, so it
// lives as long as a request. The file must not be readable by group or others.
type filePassword struct{ user, path string }

func (f filePassword) Get(context.Context) (string, string, error) {
	st, err := os.Stat(f.path)
	if err != nil {
		return "", "", fmt.Errorf("password file: %w", err)
	}
	if !st.Mode().IsRegular() {
		return "", "", fmt.Errorf("password file %s is not a regular file", f.path)
	}
	if st.Mode().Perm()&0o077 != 0 {
		return "", "", fmt.Errorf("password file %s is readable by group or others (mode %04o): chmod 600 it", f.path, st.Mode().Perm())
	}
	if st.Size() > maxPasswordFile {
		return "", "", fmt.Errorf("password file %s is larger than %d bytes", f.path, maxPasswordFile)
	}
	b, err := os.ReadFile(f.path)
	if err != nil {
		return "", "", fmt.Errorf("password file: %w", err)
	}
	pw := strings.TrimRight(string(b), "\r\n")
	if pw == "" {
		return "", "", fmt.Errorf("password file %s is empty", f.path)
	}
	return f.user, pw, nil
}

func (g *globals) checkOutput() error {
	if g.output != "json" && g.output != "table" {
		return fmt.Errorf("--output: want json or table, not %q", g.output)
	}
	return nil
}

// httpClient builds the HTTP client of an insecure run (nil: the library's
// own, which verifies certificates).
func (g *globals) httpClient(cmd *cobra.Command) (*http.Client, error) {
	if !g.insecure {
		return nil, nil
	}
	if g.caFile != "" {
		return nil, errors.New("--insecure and --ca-file are exclusive")
	}
	fmt.Fprintln(cmd.ErrOrStderr(), "warning: --insecure: the BMC's certificate is NOT verified; the password or session token can be read by anyone on the path")
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: true}
	return &http.Client{Transport: tr}, nil
}

// sessionFile is where the session of an endpoint is kept.
func (g *globals) sessionFile() (string, error) {
	dir, err := userConfigDir()
	if err != nil {
		return "", fmt.Errorf("no user config directory: %w", err)
	}
	sum := sha256.Sum256([]byte(g.endpoint))
	return filepath.Join(dir, "redfish", "sessions", hex.EncodeToString(sum[:8])+".json"), nil
}

type savedSession struct {
	Endpoint string `json:"endpoint"`
	Token    string `json:"token"`
	URI      string `json:"uri"`
}

func (g *globals) loadSession() (savedSession, error) {
	p, err := g.sessionFile()
	if err != nil {
		return savedSession{}, err
	}
	st, err := os.Stat(p)
	if err != nil {
		return savedSession{}, fmt.Errorf("no saved session for %s: run `redfish session login` first", g.endpoint)
	}
	if st.Mode().Perm()&0o077 != 0 {
		return savedSession{}, fmt.Errorf("session file %s is readable by group or others (mode %04o): remove it and log in again", p, st.Mode().Perm())
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return savedSession{}, err
	}
	var s savedSession
	if json.Unmarshal(b, &s) != nil || s.Token == "" || s.Endpoint != g.endpoint {
		return savedSession{}, fmt.Errorf("session file %s is not the session of %s: remove it and log in again", p, g.endpoint)
	}
	return s, nil
}

// client builds the library client from the flags.
func (g *globals) client(cmd *cobra.Command, forSession bool) (*redfish.Client, error) {
	if err := g.checkOutput(); err != nil {
		return nil, err
	}
	if g.endpoint == "" {
		return nil, errors.New("--endpoint is required")
	}
	hc, err := g.httpClient(cmd)
	if err != nil {
		return nil, err
	}
	cfg := redfish.Config{BaseURL: g.endpoint, CAFile: g.caFile, Timeout: g.timeout, HTTP: hc, NoExpand: g.noExpand}
	switch {
	case g.session && !forSession:
		s, err := g.loadSession()
		if err != nil {
			return nil, err
		}
		cfg.Token, cfg.SessionURI = s.Token, s.URI
	default:
		if g.user == "" || g.passwordFile == "" {
			return nil, errors.New("--user and --password-file are required (or --session after `redfish session login`)")
		}
		cfg.Credentials = filePassword{g.user, g.passwordFile}
		cfg.Session = forSession
	}
	return redfish.New(cfg)
}

// printJSON writes a JSON document, indented, or as a table.
func (g *globals) printDoc(w io.Writer, body []byte) error {
	if len(bytes.TrimSpace(body)) == 0 {
		return nil
	}
	var v any
	if err := json.Unmarshal(body, &v); err != nil {
		_, err = w.Write(body) // not JSON: as it came
		return err
	}
	if g.output == "table" {
		return renderTable(w, v)
	}
	var out bytes.Buffer
	if err := json.Indent(&out, body, "", "  "); err != nil {
		return err
	}
	out.WriteByte('\n')
	_, err := w.Write(out.Bytes())
	return err
}

// renderTable shows a resource as property/value rows, or a collection as its
// members.
func renderTable(w io.Writer, v any) error {
	tw := tabwriter.NewWriter(w, 0, 8, 2, ' ', 0)
	if l, ok := v.([]any); ok {
		return renderList(tw, l)
	}
	m, ok := v.(map[string]any)
	if !ok {
		fmt.Fprintf(tw, "%s\n", scalar(v))
		return tw.Flush()
	}
	if ms, ok := m["Members"].([]any); ok {
		fmt.Fprintln(tw, "MEMBER\tNAME")
		for _, x := range ms {
			if mm, ok := x.(map[string]any); ok {
				fmt.Fprintf(tw, "%s\t%s\n", scalar(mm["@odata.id"]), scalar(mm["Name"]))
			}
		}
		return tw.Flush()
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	fmt.Fprintln(tw, "PROPERTY\tVALUE")
	for _, k := range keys {
		switch x := m[k].(type) {
		case map[string]any:
			fmt.Fprintf(tw, "%s\t{%d properties}\n", k, len(x))
		case []any:
			fmt.Fprintf(tw, "%s\t[%d items]\n", k, len(x))
		default:
			fmt.Fprintf(tw, "%s\t%s\n", k, scalar(x))
		}
	}
	return tw.Flush()
}

// maxColumns bounds the columns of a table of objects.
const maxColumns = 12

// renderList shows an array: one VALUE column for scalars, else one row per
// element and one column per property (the first maxColumns, sorted), a nested
// value summarised as renderTable does.
func renderList(tw *tabwriter.Writer, l []any) error {
	objects := len(l) > 0
	cols := map[string]bool{}
	for _, x := range l {
		m, ok := x.(map[string]any)
		if !ok {
			objects = false
			break
		}
		for k := range m {
			cols[k] = true
		}
	}
	if !objects {
		fmt.Fprintln(tw, "VALUE")
		for _, x := range l {
			switch x.(type) {
			case map[string]any, []any:
				fmt.Fprintln(tw, summary(x))
			default:
				fmt.Fprintln(tw, scalar(x))
			}
		}
		return tw.Flush()
	}
	keys := make([]string, 0, len(cols))
	for k := range cols {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if len(keys) > maxColumns {
		keys = keys[:maxColumns]
	}
	head := make([]string, len(keys))
	for i, k := range keys {
		head[i] = clean(k)
	}
	fmt.Fprintln(tw, strings.Join(head, "\t"))
	for _, x := range l {
		m := x.(map[string]any)
		cells := make([]string, len(keys))
		for i, k := range keys {
			cells[i] = summary(m[k])
		}
		fmt.Fprintln(tw, strings.Join(cells, "\t"))
	}
	return tw.Flush()
}

func summary(v any) string {
	switch x := v.(type) {
	case map[string]any:
		return fmt.Sprintf("{%d properties}", len(x))
	case []any:
		return fmt.Sprintf("[%d items]", len(x))
	}
	return scalar(v)
}

func scalar(v any) string {
	switch x := v.(type) {
	case nil:
		return "null"
	case string:
		return clean(x)
	default:
		return clean(fmt.Sprint(x))
	}
}

// clean keeps a control character a BMC sent out of the terminal.
func clean(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return '?'
		}
		return r
	}, s)
}
