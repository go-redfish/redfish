package redfish

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// Limits.
const (
	// MaxBody bounds every answer a BMC gives.
	MaxBody = 1 << 20
	// MaxMembers bounds the members read of a collection.
	MaxMembers = 64
	// DefaultTimeout bounds a request when none is set.
	DefaultTimeout = 10 * time.Second
)

// Typed errors, for errors.Is and errors.As.
var (
	// ErrAuth: the BMC refused the credentials (401 or 403).
	ErrAuth = errors.New("redfish: authentication refused")
	// ErrNotFound: the resource does not exist (404), or the service does not
	// link it.
	ErrNotFound = errors.New("redfish: resource not found")
	// ErrTooLarge: an answer is larger than MaxBody.
	ErrTooLarge = errors.New("redfish: the answer is larger than the limit")
	// ErrMalformed: an answer is not the JSON that was expected.
	ErrMalformed = errors.New("redfish: malformed answer")
	// ErrNoReading: the resource carries no energy and no power reading.
	ErrNoReading = errors.New("redfish: no energy or power reading")
	// ErrInsecure: the URL is not an https URL, or carries credentials.
	ErrInsecure = errors.New("redfish: the URL is not acceptable")
)

// StatusError is an answer with another status than 200, 201, 202 or 204. It
// carries what the service said: the error object's code and message and its
// @Message.ExtendedInfo (DSP0266 9.5), decoded.
type StatusError struct {
	Method, Path string
	Code         int
	// ErrorCode and Message are the error object's "code" and "message".
	ErrorCode, Message string
	// Info is the @Message.ExtendedInfo of the error, in order.
	Info []Message
}

func (e *StatusError) Error() string {
	s := fmt.Sprintf("redfish: %s %s answered %d", e.Method, e.Path, e.Code)
	switch {
	case len(e.Info) > 0 && e.Info[0].Message != "":
		s += ": " + clip(e.Info[0].Message)
	case e.Message != "":
		s += ": " + clip(e.Message)
	}
	return s
}

func clip(s string) string {
	if len(s) > 200 {
		return s[:200] + "..."
	}
	return s
}

// Credentials gives the user and password to authenticate with, when they are
// needed: the caller resolves its secret reference there, so a password lives
// as long as a request.
type Credentials interface {
	Get(ctx context.Context) (user, password string, err error)
}

// Static is Credentials held in memory.
type Static struct{ User, Password string }

func (s Static) Get(context.Context) (string, string, error) { return s.User, s.Password, nil }

// Config configures a Client.
type Config struct {
	// BaseURL is the BMC's address, https://host[:port]; a path, if any, is
	// the service root (default /redfish/v1).
	BaseURL string
	// MetricsPath, when set, is the path of the EnvironmentMetrics resource to
	// read, skipping discovery (/redfish/v1/Chassis/1/EnvironmentMetrics).
	MetricsPath string
	// Chassis names the chassis to read when the service has several (its Id,
	// the last element of its path); empty: the first that has a reading.
	Chassis string
	// Credentials authenticate every request.
	Credentials Credentials
	// Token resumes a session opened earlier (the X-Auth-Token it returned);
	// SessionURI is its Session resource, for Close. With a Token, Credentials
	// may be nil: nothing but the token is sent.
	Token, SessionURI string
	// Session, when true, logs in once (POST to the SessionService) and sends
	// X-Auth-Token; otherwise HTTP basic authentication.
	Session bool
	// CAFile is a PEM file of the certificate authorities that sign the BMC's
	// certificate; empty: the system roots.
	CAFile string
	// Timeout bounds each request.
	Timeout time.Duration
	// HTTP overrides the transport (tests); its redirects are still refused.
	HTTP *http.Client
	// NoExpand forces the inventory and listing functions that read the members
	// of a collection (CPUInventory, SoftwareInventory, ListUsers...) to read them
	// one by one. By default they send $expand=. (DSP0266 7.3.2) when the service
	// root advertises it in ProtocolFeaturesSupported, and fall back to the
	// per-member reads when it does not or when the expanded answer fails.
	NoExpand bool
}

// Client reads one BMC. It is safe for concurrent use.
type Client struct {
	cfg  Config
	base *url.URL
	http *http.Client

	mu       sync.Mutex
	token    string // session token
	location string // session resource, for logout
	metrics  []string
	power    []string
	features *ProtocolFeatures // the service root's ProtocolFeaturesSupported, once read
}

// ValidateURL applies the rules every BMC URL must meet: https, a host, no
// credentials, no query or fragment.
func ValidateURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInsecure, err)
	}
	switch {
	case u.Scheme != "https":
		return fmt.Errorf("%w: %q: the scheme must be https (a password is sent with the requests)", ErrInsecure, raw)
	case u.Host == "":
		return fmt.Errorf("%w: %q: a host is required", ErrInsecure, raw)
	case u.User != nil:
		return fmt.Errorf("%w: %q: credentials in a URL are refused", ErrInsecure, u.Redacted())
	case u.RawQuery != "" || u.Fragment != "" || u.ForceQuery:
		return fmt.Errorf("%w: %q: query and fragment are not allowed", ErrInsecure, raw)
	}
	return nil
}

// New returns a Client; it reads nothing yet.
func New(cfg Config) (*Client, error) {
	if err := ValidateURL(cfg.BaseURL); err != nil {
		return nil, err
	}
	base, _ := url.Parse(cfg.BaseURL)
	if cfg.MetricsPath != "" && !strings.HasPrefix(cfg.MetricsPath, "/redfish/") {
		return nil, fmt.Errorf("%w: the metrics path %q is not under /redfish/", ErrInsecure, cfg.MetricsPath)
	}
	if cfg.Credentials == nil && cfg.Token == "" {
		return nil, errors.New("redfish: credentials are required: a BMC is never read without authentication")
	}
	if cfg.Credentials == nil {
		cfg.Credentials = Static{}
	}
	if cfg.SessionURI != "" && sessionPath(cfg.SessionURI) == "" {
		return nil, fmt.Errorf("%w: the session URI %q is not under /redfish/", ErrInsecure, cfg.SessionURI)
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultTimeout
	}
	c := &Client{cfg: cfg, base: base, token: cfg.Token}
	if cfg.Token != "" {
		c.location = sessionPath(cfg.SessionURI)
		c.cfg.Session = true
	}
	hc := cfg.HTTP
	if hc == nil {
		tr := http.DefaultTransport.(*http.Transport).Clone()
		tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12}
		if cfg.CAFile != "" {
			pem, err := os.ReadFile(cfg.CAFile)
			if err != nil {
				return nil, fmt.Errorf("redfish: the CA file cannot be read: %w", err)
			}
			pool := x509.NewCertPool()
			if !pool.AppendCertsFromPEM(pem) {
				return nil, errors.New("redfish: the CA file holds no certificate")
			}
			tlsCfg.RootCAs = pool
		}
		tr.TLSClientConfig = tlsCfg
		hc = &http.Client{Transport: tr}
	}
	cp := *hc
	cp.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	c.http = &cp
	return c, nil
}

// root is the service root path.
func (c *Client) root() string {
	if p := strings.TrimRight(c.base.Path, "/"); p != "" {
		return p
	}
	return "/redfish/v1"
}

// request sends one request and returns its bounded body.
func (c *Client) request(ctx context.Context, method, path string, body []byte, hdr map[string]string) ([]byte, http.Header, error) {
	return c.requestStatus(ctx, method, path, body, hdr, nil)
}

// requestStatus is request, and stores the status of a successful answer.
func (c *Client) requestStatus(ctx context.Context, method, path string, body []byte, hdr map[string]string, status *int) ([]byte, http.Header, error) {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	return c.requestReader(ctx, method, path, rd, int64(len(body)), c.cfg.Timeout, hdr, status)
}

// requestReader is requestStatus with a streamed body of known length (n bytes
// of rd; rd nil: no body) and its own timeout, for an upload.
func (c *Client) requestReader(ctx context.Context, method, path string, rd io.Reader, n int64, timeout time.Duration, hdr map[string]string, status *int) ([]byte, http.Header, error) {
	if !strings.HasPrefix(path, "/") || strings.Contains(path, "://") {
		return nil, nil, fmt.Errorf("%w: %q is not a path of this service", ErrMalformed, path)
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	u := *c.base
	u.Path, u.RawQuery = path, ""
	if i := strings.IndexByte(path, '?'); i >= 0 { // a next link of a paged collection
		u.Path, u.RawQuery = path[:i], path[i+1:]
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), rd)
	if err != nil {
		return nil, nil, err
	}
	if rd != nil {
		req.ContentLength = n
		if n == 0 {
			req.Body = http.NoBody
		}
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("OData-Version", "4.0")
	if rd != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	user, pw, err := c.cfg.Credentials.Get(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("redfish: the credentials are unavailable: %w", err)
	}
	c.mu.Lock()
	token := c.token
	c.mu.Unlock()
	switch {
	case token != "":
		req.Header.Set("X-Auth-Token", token)
	case !c.cfg.Session:
		req.SetBasicAuth(user, pw)
	}
	// With a session, only the login request carries no header: its body holds
	// the credentials.
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, nil, scrub(err, pw, user)
	}
	defer resp.Body.Close()
	limit := bodyLimit(ctx) // MaxBody, or MaxExpandedBody for a request with $expand
	data, err := io.ReadAll(io.LimitReader(resp.Body, int64(limit)+1))
	if err != nil {
		return nil, nil, scrub(err, pw, user)
	}
	if len(data) > limit {
		return nil, nil, ErrTooLarge
	}
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		if token != "" { // an expired session: the next read logs in again
			c.mu.Lock()
			c.token, c.location = "", ""
			c.mu.Unlock()
		}
		ae := &authError{method: method, path: path, code: resp.StatusCode}
		_, _, ae.Info = ParseError(data)
		return nil, nil, ae
	case resp.StatusCode == http.StatusNotFound:
		return nil, nil, fmt.Errorf("%w (%s)", ErrNotFound, path)
	case resp.StatusCode < 200 || resp.StatusCode >= 300:
		se := &StatusError{Method: method, Path: path, Code: resp.StatusCode}
		se.ErrorCode, se.Message, se.Info = ParseError(data)
		return nil, nil, se
	}
	if status != nil {
		*status = resp.StatusCode
	}
	return data, resp.Header, nil
}

// authError is ErrAuth with what the service said (PasswordChangeURI reads its
// extended information); its text is the one ErrAuth always had.
type authError struct {
	method, path string
	code         int
	Info         []Message
}

func (e *authError) Error() string {
	return fmt.Sprintf("%s (%s %s answered %d)", ErrAuth.Error(), e.method, e.path, e.code)
}
func (e *authError) Unwrap() error { return ErrAuth }

// scrub keeps a credential out of an error.
func scrub(err error, secrets ...string) error {
	msg := err.Error()
	for _, s := range secrets {
		if s != "" && strings.Contains(msg, s) {
			return errors.New("redfish: the request failed (the error mentioned a credential and was redacted)")
		}
	}
	return err
}

// login opens a session when the client is configured for one.
func (c *Client) login(ctx context.Context) error {
	if !c.cfg.Session {
		return nil
	}
	c.mu.Lock()
	have := c.token != ""
	c.mu.Unlock()
	if have {
		return nil
	}
	user, pw, err := c.cfg.Credentials.Get(ctx)
	if err != nil {
		return fmt.Errorf("redfish: the credentials are unavailable: %w", err)
	}
	body, _ := json.Marshal(map[string]string{"UserName": user, "Password": pw})
	_, h, err := c.request(ctx, http.MethodPost, c.sessionsPath(ctx), body, nil)
	if err != nil {
		return err
	}
	tok := h.Get("X-Auth-Token")
	if tok == "" {
		return fmt.Errorf("%w: the session answer has no X-Auth-Token", ErrMalformed)
	}
	c.mu.Lock()
	c.token, c.location = tok, sessionPath(h.Get("Location"))
	c.mu.Unlock()
	return nil
}

func sessionPath(loc string) string {
	if u, err := url.Parse(loc); err == nil && strings.HasPrefix(u.Path, "/redfish/") {
		return u.Path
	}
	return ""
}

// Close ends the session, if one was opened.
func (c *Client) Close(ctx context.Context) error {
	c.mu.Lock()
	loc := c.location
	c.mu.Unlock()
	if loc == "" {
		return nil
	}
	_, _, err := c.request(ctx, http.MethodDelete, loc, nil, nil)
	c.mu.Lock()
	c.token, c.location = "", ""
	c.mu.Unlock()
	return err
}

// get reads a resource and decodes it.
func (c *Client) get(ctx context.Context, path string, v any) error {
	data, _, err := c.request(ctx, http.MethodGet, path, nil, nil)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("%w: %s: %v", ErrMalformed, path, err)
	}
	return nil
}

// link is a reference to another resource.
type link struct {
	ID string `json:"@odata.id"`
}

// ServiceRoot is what discovery reads of /redfish/v1/.
type ServiceRoot struct {
	RedfishVersion string
	Chassis        string
	SessionService string
}

// Root reads the service root.
func (c *Client) Root(ctx context.Context) (ServiceRoot, error) {
	if err := c.login(ctx); err != nil {
		return ServiceRoot{}, err
	}
	var d struct {
		RedfishVersion string `json:"RedfishVersion"`
		Chassis        *link  `json:"Chassis"`
		SessionService *link  `json:"SessionService"`
	}
	if err := c.get(ctx, c.root()+"/", &d); err != nil {
		return ServiceRoot{}, err
	}
	r := ServiceRoot{RedfishVersion: d.RedfishVersion}
	if d.Chassis != nil {
		r.Chassis = d.Chassis.ID
	}
	if d.SessionService != nil {
		r.SessionService = d.SessionService.ID
	}
	return r, nil
}

// ChassisURIs lists the chassis of the service (at most MaxMembers).
func (c *Client) ChassisURIs(ctx context.Context) ([]string, error) {
	root, err := c.Root(ctx)
	if err != nil {
		return nil, err
	}
	if root.Chassis == "" {
		return nil, fmt.Errorf("%w: the service root links no Chassis collection", ErrNotFound)
	}
	var out []string
	// A collection may be paged (DSP0266 7.2.2: Members@odata.nextLink): the
	// pages are followed, to MaxMembers members and a few pages.
	next := root.Chassis
	for page := 0; next != "" && page < 8 && len(out) < MaxMembers; page++ {
		var coll struct {
			Members []link `json:"Members"`
			Next    string `json:"Members@odata.nextLink"`
		}
		if err := c.get(ctx, next, &coll); err != nil {
			return nil, err
		}
		for _, m := range coll.Members {
			if m.ID != "" && len(out) < MaxMembers {
				out = append(out, m.ID)
			}
		}
		next = ""
		if u, err := url.Parse(coll.Next); err == nil && strings.HasPrefix(u.Path, "/redfish/") {
			next = u.RequestURI()
		}
	}
	return out, nil
}

// Reading is one read of a node's consumption: the energy counter when the
// BMC has one, else a power. Chassis is the resource it came from.
type Reading struct {
	Chassis string
	At      time.Time
	// EnergyKWh is the chassis' energy counter (EnvironmentMetrics.EnergykWh,
	// or EnergyJoules converted), nil when the BMC has none.
	EnergyKWh *float64
	// PowerWatts is the power consumed, nil when the BMC reports none; read
	// only when there is no energy counter.
	PowerWatts *float64
}

// Read reads the node's consumption: the EnergykWh counter of the chassis'
// EnvironmentMetrics, else (no counter) its PowerWatts, else the Power
// resource's PowerControl PowerConsumedWatts. With Config.MetricsPath only
// that resource is read; otherwise the service is discovered once and the
// links are remembered.
func (c *Client) Read(ctx context.Context) (Reading, error) {
	if err := c.login(ctx); err != nil {
		return Reading{}, err
	}
	if err := c.discover(ctx); err != nil {
		return Reading{}, err
	}
	c.mu.Lock()
	metrics, power := append([]string(nil), c.metrics...), append([]string(nil), c.power...)
	c.mu.Unlock()
	var last error = ErrNoReading
	for _, p := range metrics {
		data, _, err := c.request(ctx, http.MethodGet, p, nil, nil)
		if err != nil {
			last = err
			if errors.Is(err, ErrAuth) {
				return Reading{}, err
			}
			continue
		}
		m, err := ParseEnvironmentMetrics(data)
		if err != nil {
			last = err
			continue
		}
		if m.EnergyKWh != nil || m.PowerWatts != nil {
			return Reading{Chassis: p, At: time.Now(), EnergyKWh: m.EnergyKWh, PowerWatts: m.PowerWatts}, nil
		}
	}
	for _, p := range power {
		data, _, err := c.request(ctx, http.MethodGet, p, nil, nil)
		if err != nil {
			last = err
			if errors.Is(err, ErrAuth) {
				return Reading{}, err
			}
			continue
		}
		w, err := ParsePowerConsumed(data)
		if err != nil {
			last = err
			continue
		}
		return Reading{Chassis: p, At: time.Now(), PowerWatts: &w}, nil
	}
	return Reading{}, last
}

// discover finds the resources to read, once.
func (c *Client) discover(ctx context.Context) error {
	c.mu.Lock()
	done := len(c.metrics) > 0 || len(c.power) > 0
	c.mu.Unlock()
	if done {
		return nil
	}
	if c.cfg.MetricsPath != "" {
		c.mu.Lock()
		c.metrics = []string{c.cfg.MetricsPath}
		c.mu.Unlock()
		return nil
	}
	chassis, err := c.ChassisURIs(ctx)
	if err != nil {
		return err
	}
	var metrics, power []string
	for _, ch := range chassis {
		if c.cfg.Chassis != "" && !strings.HasSuffix(strings.TrimRight(ch, "/"), "/"+c.cfg.Chassis) {
			continue
		}
		var d struct {
			EnvironmentMetrics *link `json:"EnvironmentMetrics"`
			Power              *link `json:"Power"`
		}
		if err := c.get(ctx, ch, &d); err != nil {
			if errors.Is(err, ErrAuth) {
				return err
			}
			continue
		}
		if d.EnvironmentMetrics != nil && d.EnvironmentMetrics.ID != "" {
			metrics = append(metrics, d.EnvironmentMetrics.ID)
		}
		if d.Power != nil && d.Power.ID != "" {
			power = append(power, d.Power.ID)
		}
	}
	if len(metrics) == 0 && len(power) == 0 {
		return fmt.Errorf("%w: no chassis links an EnvironmentMetrics or a Power resource", ErrNoReading)
	}
	c.mu.Lock()
	c.metrics, c.power = metrics, power
	c.mu.Unlock()
	return nil
}

// sessionsPath is the Sessions collection: the service root's Links.Sessions
// (DSP0266 13.3.4.1), else the default /redfish/v1/SessionService/Sessions.
func (c *Client) sessionsPath(ctx context.Context) string {
	def := c.root() + "/SessionService/Sessions"
	var d struct {
		Links *struct {
			Sessions *link `json:"Sessions"`
		} `json:"Links"`
	}
	if err := c.get(ctx, c.root()+"/", &d); err != nil || d.Links == nil || d.Links.Sessions == nil {
		return def
	}
	if p := d.Links.Sessions.ID; strings.HasPrefix(p, "/redfish/") && !strings.Contains(p, "..") {
		return p
	}
	return def
}
