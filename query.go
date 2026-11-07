package redfish

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// The query parameters of DSP0266 1.23.0 (the section numbers below are its):
//
//   - 7.3.1 Query parameter overview: a service may support $expand, $filter and
//     $select and should support $top, $skip, only and excerpt; it shall put a
//     ProtocolFeaturesSupported object in the service root when it supports query
//     parameters, "which parameters and options have been implemented"; it shall
//     answer 501 for an unsupported parameter that starts with $ and ignores an
//     unsupported one that does not (only, excerpt); a value it cannot use is 400;
//     only is not to be combined with another parameter (Table 7). A client that
//     sends a parameter the service does not advertise gets either an ignored
//     parameter (a full answer it believes is a subset) or an error: this client
//     never sends one the root does not advertise (ErrUnsupported, naming the
//     ProtocolFeaturesSupported property).
//   - 7.3.2 $expand: the options are the asterisk (all hyperlinks), the period
//     (hyperlinks outside Links), the tilde (hyperlinks in Links) and $levels
//     (default 1); an expanded answer "may exceed" what one response holds (507),
//     and a member the service cannot expand stays a link (200, @odata.id only).
//   - 7.3.3 $select: a comma-separated list of properties; a path into an object
//     or an array of objects is the names joined by a slash, never an index.
//   - 7.3.4 $filter: property paths, literals (a string in single quotes, a number,
//     a boolean) and the operators of Table 9: (), and, or, not, eq, ne, gt, ge,
//     lt, le.
//   - 9.6.10 Count: Members@odata.count is the total, $top and $skip do not
//     change it.
//
// ProtocolFeaturesSupported is the ServiceRoot 1.22.0 type of the DSP8010 2025.4
// schemas: ExpandQuery (ExpandAll, Levels, Links, MaxLevels, NoLinks), ExcerptQuery,
// FilterQuery (and, from 1.17.0, FilterQueryComparisonOperations and
// FilterQueryCompoundOperations), OnlyMemberQuery, SelectQuery and TopSkipQuery
// (both $top and $skip).

// Limits of a query.
const (
	// MaxExpandedBody bounds the answer to a request that carries $expand. An
	// expanded answer holds many resources (DSP0266 7.3.2: "the payload may
	// increase beyond what can be sent in a single response"), so it may exceed
	// MaxBody, but it is bounded all the same: a larger one is ErrTooLarge, and a
	// converted read then falls back to one request per member.
	MaxExpandedBody = 8 << 20
	// maxFilter, maxSelect, maxSelectName, maxLevels and maxFilterTokens bound what
	// a caller may ask for.
	maxFilter       = 1024
	maxSelect       = 64
	maxSelectName   = 256
	maxLevels       = 64
	maxFilterTokens = 256
	maxFilterDepth  = 32
)

// ErrQuery: a query parameter has a value, or a combination, the grammar of
// DSP0266 7.3 refuses. Nothing was sent.
var ErrQuery = errors.New("redfish: invalid query parameter")

// ProtocolFeatures is what the service root says it implements (the
// ProtocolFeaturesSupported object, DSP0266 7.3.1; ServiceRoot 1.22.0). The zero
// value is a service that advertises nothing: no parameter is then sent.
type ProtocolFeatures struct {
	// Advertised: the root has a ProtocolFeaturesSupported object.
	Advertised bool
	// ExpandQuery: ExpandAll is the asterisk, ExpandLinks the tilde, ExpandNoLinks
	// the period, ExpandLevels $levels (to MaxLevels, 0 when the root gives none).
	ExpandAll, ExpandLevels, ExpandLinks, ExpandNoLinks bool
	MaxLevels                                           int
	// ExcerptQuery, FilterQuery, OnlyMemberQuery, SelectQuery and TopSkipQuery
	// (both $top and $skip) are the booleans of the same names.
	Excerpt, Filter, Only, Select, TopSkip bool
	// FilterComparison and FilterCompound are FilterQueryComparisonOperations and
	// FilterQueryCompoundOperations; nil when the root does not say (a service
	// older than ServiceRoot 1.17.0), in which case FilterQuery alone decides.
	FilterComparison, FilterCompound *bool
}

// ParseProtocolFeatures reads ProtocolFeaturesSupported out of a service root. A
// root without one is not an error: nothing is advertised. A property of another
// type than the schema's is taken as not advertised.
func ParseProtocolFeatures(root []byte) (ProtocolFeatures, error) {
	p, err := decodeProps(root)
	if err != nil {
		return ProtocolFeatures{}, fmt.Errorf("%w: service root: %v", ErrMalformed, err)
	}
	return featuresOf(p), nil
}

func featuresOf(root Props) ProtocolFeatures {
	pf, ok := root["ProtocolFeaturesSupported"].(map[string]any)
	if !ok {
		return ProtocolFeatures{}
	}
	flag := func(m map[string]any, k string) bool { b, _ := m[k].(bool); return b }
	opt := func(m map[string]any, k string) *bool {
		if b, ok := m[k].(bool); ok {
			return &b
		}
		return nil
	}
	f := ProtocolFeatures{Advertised: true,
		Excerpt: flag(pf, "ExcerptQuery"), Filter: flag(pf, "FilterQuery"), Only: flag(pf, "OnlyMemberQuery"),
		Select: flag(pf, "SelectQuery"), TopSkip: flag(pf, "TopSkipQuery"),
		FilterComparison: opt(pf, "FilterQueryComparisonOperations"), FilterCompound: opt(pf, "FilterQueryCompoundOperations")}
	if !f.Filter {
		f.FilterComparison, f.FilterCompound = nil, nil // "shall not be present if FilterQuery is false"
	}
	if e, ok := pf["ExpandQuery"].(map[string]any); ok {
		f.ExpandAll, f.ExpandLevels, f.ExpandLinks, f.ExpandNoLinks = flag(e, "ExpandAll"), flag(e, "Levels"), flag(e, "Links"), flag(e, "NoLinks")
		if n, ok := e["MaxLevels"].(json.Number); ok {
			if v, err := n.Int64(); err == nil && v >= 1 {
				f.MaxLevels = int(min(v, maxLevels))
			}
		}
	}
	return f
}

// ProtocolFeatures reads the ProtocolFeaturesSupported of the service root, once:
// the answer is kept for the life of the Client.
func (c *Client) ProtocolFeatures(ctx context.Context) (ProtocolFeatures, error) {
	c.mu.Lock()
	f := c.features
	c.mu.Unlock()
	if f != nil {
		return *f, nil
	}
	if err := c.login(ctx); err != nil {
		return ProtocolFeatures{}, err
	}
	root, _, err := c.getProps(ctx, c.root()+"/")
	if err != nil {
		return ProtocolFeatures{}, err
	}
	return c.noteFeatures(root), nil
}

// noteFeatures keeps the features of a service root a read already made.
func (c *Client) noteFeatures(root Props) ProtocolFeatures {
	f := featuresOf(root)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.features == nil {
		c.features = &f
	}
	return *c.features
}

// ExpandKind is the option of $expand that says which hyperlinks are expanded
// (DSP0266 7.3.2 Table 8). The zero value is the period.
type ExpandKind int

// The options of $expand.
const (
	// ExpandNoLinks is the period: the hyperlinks that are not in a Links property.
	ExpandNoLinks ExpandKind = iota
	// ExpandLinks is the tilde: the hyperlinks of the Links properties.
	ExpandLinks
	// ExpandAll is the asterisk: every hyperlink.
	ExpandAll
)

func (k ExpandKind) String() string {
	switch k {
	case ExpandNoLinks:
		return "."
	case ExpandLinks:
		return "~"
	case ExpandAll:
		return "*"
	}
	return "?"
}

// Expand is the $expand parameter (DSP0266 7.3.2).
type Expand struct {
	// Kind is which hyperlinks to expand; the zero value is the period.
	Kind ExpandKind
	// Levels is $levels; 0 and 1 are the default, one level, and send no $levels.
	Levels int
}

// Query is the set of query parameters of a read (DSP0266 7.3). Each is sent only
// when the service advertises it in ProtocolFeaturesSupported; otherwise the read
// is refused with ErrUnsupported and nothing is sent. The zero value sends none.
type Query struct {
	// Expand is $expand.
	Expand *Expand
	// Select is $select: property names, or paths joined by a slash.
	Select []string
	// Top and Skip are $top and $skip, on a resource collection; both are one
	// feature, TopSkipQuery.
	Top, Skip *int
	// Only is only: a collection of exactly one member answers with that member.
	// It cannot be combined with another parameter (DSP0266 7.3.1 Table 7).
	Only bool
	// Excerpt is excerpt: the properties the schema's Excerpt annotation names.
	Excerpt bool
	// Filter is $filter, on a resource collection; the grammar of 7.3.4 is
	// checked before anything is sent.
	Filter string
}

// IsZero reports whether the query sends nothing.
func (q Query) IsZero() bool {
	return q.Expand == nil && len(q.Select) == 0 && q.Top == nil && q.Skip == nil && !q.Only && !q.Excerpt && q.Filter == ""
}

func notAdvertised(feature, param string) error {
	return fmt.Errorf("%w: the service root does not advertise ProtocolFeaturesSupported.%s: %s is not sent", ErrUnsupported, feature, param)
}

// Encode validates the query against what the service advertises and returns the
// query string (without the question mark), "" for the zero Query. The
// parameters come in the order the service processes them (DSP0266 7.3.1:
// $filter, $skip, $top, $expand, excerpt, $select). The names keep their dollar
// sign and the values are percent-encoded so that nothing but the intended
// parameters can appear (a value cannot hold an ampersand, a hash or a space);
// url.Values would send %24expand and a plus for each space, which services are
// not required to decode.
func (q Query) Encode(f ProtocolFeatures) (string, error) {
	if q.IsZero() {
		return "", nil
	}
	if q.Only && (q.Expand != nil || len(q.Select) > 0 || q.Top != nil || q.Skip != nil || q.Excerpt || q.Filter != "") {
		return "", fmt.Errorf("%w: only cannot be combined with another parameter (DSP0266 7.3.1)", ErrQuery)
	}
	var parts []string
	add := func(name, value string) { parts = append(parts, name+"="+escapeQuery(value)) }
	if q.Filter != "" {
		if !f.Filter {
			return "", notAdvertised("FilterQuery", "$filter")
		}
		compound, err := checkQueryFilter(q.Filter)
		if err != nil {
			return "", err
		}
		if f.FilterComparison != nil && !*f.FilterComparison {
			return "", notAdvertised("FilterQueryComparisonOperations", "$filter")
		}
		if compound && f.FilterCompound != nil && !*f.FilterCompound {
			return "", notAdvertised("FilterQueryCompoundOperations", "$filter with (), and, or, not")
		}
		add("$filter", q.Filter)
	}
	for _, n := range []struct {
		name string
		v    *int
	}{{"$skip", q.Skip}, {"$top", q.Top}} {
		if n.v == nil {
			continue
		}
		if !f.TopSkip {
			return "", notAdvertised("TopSkipQuery", n.name)
		}
		if *n.v < 0 || *n.v > math.MaxInt32 {
			return "", fmt.Errorf("%w: %s %d is not between 0 and %d", ErrQuery, n.name, *n.v, math.MaxInt32)
		}
		add(n.name, strconv.Itoa(*n.v))
	}
	if q.Only {
		if !f.Only {
			return "", notAdvertised("OnlyMemberQuery", "only")
		}
		parts = append(parts, "only")
	}
	if e := q.Expand; e != nil {
		v, err := e.encode(f)
		if err != nil {
			return "", err
		}
		add("$expand", v)
	}
	if q.Excerpt {
		if !f.Excerpt {
			return "", notAdvertised("ExcerptQuery", "excerpt")
		}
		parts = append(parts, "excerpt")
	}
	if len(q.Select) > 0 {
		if !f.Select {
			return "", notAdvertised("SelectQuery", "$select")
		}
		if err := checkSelect(q.Select); err != nil {
			return "", err
		}
		add("$select", strings.Join(q.Select, ","))
	}
	return strings.Join(parts, "&"), nil
}

func (e Expand) encode(f ProtocolFeatures) (string, error) {
	var ok bool
	var feature string
	switch e.Kind {
	case ExpandNoLinks:
		ok, feature = f.ExpandNoLinks, "ExpandQuery.NoLinks"
	case ExpandLinks:
		ok, feature = f.ExpandLinks, "ExpandQuery.Links"
	case ExpandAll:
		ok, feature = f.ExpandAll, "ExpandQuery.ExpandAll"
	default:
		return "", fmt.Errorf("%w: unknown $expand option %d", ErrQuery, int(e.Kind))
	}
	if !ok {
		return "", notAdvertised(feature, "$expand="+e.Kind.String())
	}
	if e.Levels < 0 || e.Levels > maxLevels {
		return "", fmt.Errorf("%w: $levels %d is not between 1 and %d", ErrQuery, e.Levels, maxLevels)
	}
	v := e.Kind.String()
	if e.Levels > 1 {
		if !f.ExpandLevels {
			return "", notAdvertised("ExpandQuery.Levels", "$levels")
		}
		if f.MaxLevels > 0 && e.Levels > f.MaxLevels {
			return "", fmt.Errorf("%w: $levels %d is over ExpandQuery.MaxLevels (%d) the service advertises", ErrUnsupported, e.Levels, f.MaxLevels)
		}
		v += "($levels=" + strconv.Itoa(e.Levels) + ")"
	}
	return v, nil
}

// escapeQuery percent-encodes a query value: letters, digits and the characters
// that cannot end a parameter or a value (- _ . ~ $ ' ( ) * , / : = @ !) stay;
// every other byte, the space, &, #, %, + and ; among them, is %XX.
func escapeQuery(s string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch {
		case ch >= 'a' && ch <= 'z', ch >= 'A' && ch <= 'Z', ch >= '0' && ch <= '9', strings.IndexByte("-_.~$'()*,/:=@!", ch) >= 0:
			b.WriteByte(ch)
		default:
			b.WriteByte('%')
			b.WriteByte(hex[ch>>4])
			b.WriteByte(hex[ch&15])
		}
	}
	return b.String()
}

// checkSelect applies DSP0266 7.3.3: a list of properties, each the names of a
// path joined by a slash (no index, no empty name, no wildcard).
func checkSelect(props []string) error {
	if len(props) > maxSelect {
		return fmt.Errorf("%w: %d properties in $select, at most %d", ErrQuery, len(props), maxSelect)
	}
	for _, p := range props {
		if p == "" || len(p) > maxSelectName {
			return fmt.Errorf("%w: a $select property must have 1 to %d characters", ErrQuery, maxSelectName)
		}
		for _, seg := range strings.Split(p, "/") {
			if !validName(seg) {
				return fmt.Errorf("%w: %q is not a property path of 7.3.3 (names joined by a slash)", ErrQuery, clip(p))
			}
		}
	}
	return nil
}

// validName is a property name: letters, digits and underscores, with an optional
// leading @ (an annotation) and dots inside (@odata.id, Members@odata.count).
func validName(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch {
		case ch >= 'a' && ch <= 'z', ch >= 'A' && ch <= 'Z', ch == '_':
		case ch >= '0' && ch <= '9', ch == '.', ch == '@':
			if i == 0 && ch != '@' {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// ---- $filter ----

type filterToken struct {
	kind byte // 'w' word or path, 's' string, 'n' number, '(' or ')'
	text string
}

// lexFilter splits a $filter expression into words (property paths and
// operators), single-quoted strings (” stands for one quote), numbers and
// parentheses (DSP0266 7.3.4).
func lexFilter(s string) ([]filterToken, error) {
	var out []filterToken
	for i := 0; i < len(s); {
		if len(out) >= maxFilterTokens {
			return nil, fmt.Errorf("%w: $filter has more than %d tokens", ErrQuery, maxFilterTokens)
		}
		ch := s[i]
		switch {
		case ch == ' ':
			i++
		case ch == '(' || ch == ')':
			out = append(out, filterToken{kind: ch})
			i++
		case ch == '\'':
			j := i + 1
			for {
				if j >= len(s) {
					return nil, fmt.Errorf("%w: $filter has an unterminated string", ErrQuery)
				}
				if s[j] == '\'' {
					if j+1 < len(s) && s[j+1] == '\'' {
						j += 2
						continue
					}
					break
				}
				if s[j] < ' ' || s[j] == 0x7f {
					return nil, fmt.Errorf("%w: $filter has a control character in a string", ErrQuery)
				}
				j++
			}
			out = append(out, filterToken{kind: 's', text: s[i+1 : j]})
			i = j + 1
		case ch == '-' || ch >= '0' && ch <= '9':
			j := i + 1
			for j < len(s) && (s[j] >= '0' && s[j] <= '9' || s[j] == '.' || s[j] == 'e' || s[j] == 'E' || s[j] == '+' || s[j] == '-') {
				j++
			}
			if _, err := strconv.ParseFloat(s[i:j], 64); err != nil {
				return nil, fmt.Errorf("%w: $filter has the number %q that is not one", ErrQuery, clip(s[i:j]))
			}
			out = append(out, filterToken{kind: 'n', text: s[i:j]})
			i = j
		case ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch == '_':
			j := i
			for j < len(s) && (s[j] >= 'a' && s[j] <= 'z' || s[j] >= 'A' && s[j] <= 'Z' || s[j] >= '0' && s[j] <= '9' || s[j] == '_' || s[j] == '/') {
				j++
			}
			out = append(out, filterToken{kind: 'w', text: s[i:j]})
			i = j
		default:
			return nil, fmt.Errorf("%w: $filter has the character %q, which the grammar of 7.3.4 does not allow", ErrQuery, string(ch))
		}
	}
	return out, nil
}

type filterParser struct {
	toks     []filterToken
	pos      int
	compound bool
}

func (p *filterParser) peek() (filterToken, bool) {
	if p.pos < len(p.toks) {
		return p.toks[p.pos], true
	}
	return filterToken{}, false
}

func (p *filterParser) isWord(w string) bool {
	t, ok := p.peek()
	return ok && t.kind == 'w' && t.text == w
}

// orExpr is and { or and }; and is unary { and unary }; unary is not unary, a
// parenthesised expression or a comparison (the precedence of 7.3.4).
func (p *filterParser) orExpr(depth int) error {
	if depth > maxFilterDepth {
		return fmt.Errorf("%w: $filter is nested deeper than %d", ErrQuery, maxFilterDepth)
	}
	if err := p.andExpr(depth); err != nil {
		return err
	}
	for p.isWord("or") {
		p.pos++
		p.compound = true
		if err := p.andExpr(depth); err != nil {
			return err
		}
	}
	return nil
}

func (p *filterParser) andExpr(depth int) error {
	if err := p.unary(depth); err != nil {
		return err
	}
	for p.isWord("and") {
		p.pos++
		p.compound = true
		if err := p.unary(depth); err != nil {
			return err
		}
	}
	return nil
}

var filterOps = map[string]bool{"eq": true, "ne": true, "gt": true, "ge": true, "lt": true, "le": true}

func (p *filterParser) unary(depth int) error {
	if depth > maxFilterDepth {
		return fmt.Errorf("%w: $filter is nested deeper than %d", ErrQuery, maxFilterDepth)
	}
	t, ok := p.peek()
	if !ok {
		return fmt.Errorf("%w: $filter ends where an operand is expected", ErrQuery)
	}
	switch {
	case t.kind == 'w' && t.text == "not":
		p.pos++
		p.compound = true
		return p.unary(depth + 1)
	case t.kind == '(':
		p.pos++
		p.compound = true
		if err := p.orExpr(depth + 1); err != nil {
			return err
		}
		if c, ok := p.peek(); !ok || c.kind != ')' {
			return fmt.Errorf("%w: $filter has a parenthesis that is not closed", ErrQuery)
		}
		p.pos++
		return nil
	case t.kind == 'w':
		for _, seg := range strings.Split(t.text, "/") {
			if !validName(seg) || strings.ContainsAny(seg, ".@") {
				return fmt.Errorf("%w: $filter has %q where a property path is expected", ErrQuery, clip(t.text))
			}
		}
		if filterOps[t.text] || t.text == "and" || t.text == "or" || t.text == "true" || t.text == "false" {
			return fmt.Errorf("%w: $filter has the keyword %q where a property is expected", ErrQuery, t.text)
		}
		p.pos++
		op, ok := p.peek()
		if !ok || op.kind != 'w' || !filterOps[op.text] {
			return fmt.Errorf("%w: $filter has no comparison operator (eq, ne, gt, ge, lt, le) after %q", ErrQuery, clip(t.text))
		}
		p.pos++
		lit, ok := p.peek()
		if !ok || !(lit.kind == 's' || lit.kind == 'n' || lit.kind == 'w' && (lit.text == "true" || lit.text == "false")) {
			return fmt.Errorf("%w: $filter has no literal (a string in quotes, a number, true or false) after %s", ErrQuery, op.text)
		}
		p.pos++
		return nil
	}
	return fmt.Errorf("%w: $filter has an operand that is not a property comparison", ErrQuery)
}

// checkQueryFilter validates a $filter expression against the grammar of DSP0266 7.3.4
// and reports whether it uses the compound operations (parentheses, and, or, not).
func checkQueryFilter(s string) (compound bool, err error) {
	if len(s) > maxFilter {
		return false, fmt.Errorf("%w: $filter is longer than %d bytes", ErrQuery, maxFilter)
	}
	toks, err := lexFilter(s)
	if err != nil {
		return false, err
	}
	if len(toks) == 0 {
		return false, fmt.Errorf("%w: $filter is empty", ErrQuery)
	}
	p := &filterParser{toks: toks}
	if err := p.orExpr(0); err != nil {
		return false, err
	}
	if p.pos != len(toks) {
		return false, fmt.Errorf("%w: $filter has %q where and, or or the end is expected", ErrQuery, clip(toks[p.pos].text))
	}
	return p.compound, nil
}

// ---- the read ----

type limitKey struct{}

// withLimit lets a request read up to MaxExpandedBody (and no more).
func withLimit(ctx context.Context) context.Context {
	return context.WithValue(ctx, limitKey{}, MaxExpandedBody)
}

func bodyLimit(ctx context.Context) int {
	if v, ok := ctx.Value(limitKey{}).(int); ok && v > MaxBody && v <= MaxExpandedBody {
		return v
	}
	return MaxBody
}

// GetWith reads a resource with query parameters. p is a path of the service
// without a query string. The ProtocolFeaturesSupported of the service root is
// read first (once per Client); a parameter it does not advertise is refused
// with ErrUnsupported, a value the grammar forbids with ErrQuery, and nothing is
// sent. The answer is bounded by MaxBody, or by MaxExpandedBody when the query
// has $expand.
func (c *Client) GetWith(ctx context.Context, p string, q Query) (*Response, error) {
	if strings.ContainsAny(p, "?#") {
		return nil, fmt.Errorf("%w: %q: give the parameters in the Query, not in the path", ErrQuery, clip(p))
	}
	if q.IsZero() {
		return c.Get(ctx, p)
	}
	f, err := c.ProtocolFeatures(ctx)
	if err != nil {
		return nil, err
	}
	enc, err := q.Encode(f)
	if err != nil {
		return nil, err
	}
	if q.Expand != nil {
		ctx = withLimit(ctx)
	}
	return c.Do(ctx, Request{Method: http.MethodGet, Path: p + "?" + enc})
}

// withExpand adds the $expand of the period to a path that may already carry a
// query (a next link), unless it has one.
func withExpand(p string) string {
	if i := strings.IndexByte(p, '?'); i >= 0 {
		if vals, err := url.ParseQuery(p[i+1:]); err == nil {
			for k := range vals {
				if strings.EqualFold(k, "$expand") {
					return p
				}
			}
		}
		return p + "&$expand=."
	}
	return p + "?$expand=."
}
