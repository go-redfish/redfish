package mockbmc

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// The query parameters of DSP0266 1.23.0 7.3, as a service that supports them
// does them. The TreeRackmount mock advertises them in the service root's
// ProtocolFeaturesSupported (ServiceRoot 1.22.0); TreeEmulator does not, and
// Options.NoQueries makes TreeRackmount a service that does not either. A service
// that does not advertise a parameter does what 7.3.1 says: an unsupported
// parameter that starts with $ is a 501, one that does not (only, excerpt) is
// ignored.
//
// Implemented: $expand (the period, the tilde, the asterisk and $levels to
// maxLevels, 7.3.2), $select (7.3.3: properties and paths, objects whole, the
// @odata annotations kept), $filter (7.3.4: the operators of Table 9 with their
// precedence), $top and $skip (Members@odata.count is the total after the filter
// and is not changed by them, 9.6.10), only, and excerpt. The mockups define no
// Excerpt annotation, so excerpt returns the whole resource, which is what
// Table 7 says for a resource without one. The parameters are applied in the
// order of 7.3.1: $filter, $skip, $top, $expand, excerpt, $select. Not
// implemented: includeoriginofcondition, $skiptoken, OEM parameters (ignored: they
// do not start with $), server-driven paging.
const (
	maxLevels = 6
	// maxExpansions bounds the links one request expands; one more stays a link
	// (7.3.2: "Return the @odata.id property for the member that it cannot expand").
	maxExpansions = 4096
)

func (b *BMC) queriesOn() bool {
	return !b.opts.NoQueries && b.opts.Tree == TreeRackmount
}

// features is the ProtocolFeaturesSupported of the mock.
func features() map[string]any {
	return map[string]any{
		"ExpandQuery":  map[string]any{"ExpandAll": true, "Levels": true, "Links": true, "MaxLevels": maxLevels, "NoLinks": true},
		"ExcerptQuery": true, "FilterQuery": true,
		"FilterQueryComparisonOperations": true, "FilterQueryCompoundOperations": true,
		"OnlyMemberQuery": true, "SelectQuery": true, "TopSkipQuery": true,
	}
}

// Queries returns the raw query string of every request served so far, in the
// order of Log ("" for a request without one).
func (b *BMC) Queries() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.qlog...)
}

var knownParams = map[string]bool{"$expand": true, "$select": true, "$filter": true, "$top": true, "$skip": true, "only": true, "excerpt": true}

// applyQuery applies the query of a GET to a resource. It answers the request
// itself and returns false when the query is refused.
func (b *BMC) applyQuery(w http.ResponseWriter, r *http.Request, m map[string]any) (map[string]any, bool) {
	params := map[string]string{}
	for _, kv := range strings.Split(r.URL.RawQuery, "&") {
		if kv == "" {
			continue
		}
		name, val, _ := strings.Cut(kv, "=")
		n, err1 := url.QueryUnescape(name)
		v, err2 := url.QueryUnescape(val)
		if err1 != nil || err2 != nil {
			b.fail(w, http.StatusBadRequest, "", "A query parameter is not correctly encoded.", "Base.1.19.GeneralError")
			return nil, false
		}
		n = strings.ToLower(n) // 7.3.1: names are case-insensitive strings
		if strings.HasPrefix(n, "$") && (!knownParams[n] || !b.queriesOn()) {
			b.fail(w, http.StatusNotImplemented, "", "The query parameter "+n+" is not implemented.", "Base.1.19.GeneralError")
			return nil, false
		}
		if !knownParams[n] || !b.queriesOn() {
			continue // 7.3.1: ignored when it does not start with $
		}
		if _, dup := params[n]; dup {
			b.fail(w, http.StatusBadRequest, "", "The query parameter "+n+" is given twice.", "Base.1.19.GeneralError")
			return nil, false
		}
		params[n] = v
	}
	if len(params) == 0 {
		return m, true
	}
	bad := func(msg string) (map[string]any, bool) {
		b.fail(w, http.StatusBadRequest, "", msg, "Base.1.19.GeneralError")
		return nil, false
	}
	_, coll := m["Members"].([]any)
	if _, only := params["only"]; only {
		if len(params) > 1 {
			b.fail(w, http.StatusBadRequest, "", "only cannot be combined with other query parameters.", "Base.1.19.QueryCombinationInvalid")
			return nil, false
		}
		if !coll {
			b.fail(w, http.StatusBadRequest, "", "The query parameter only applies to resource collections.", "Base.1.19.QueryNotSupportedOnResource")
			return nil, false
		}
		if list := m["Members"].([]any); len(list) == 1 {
			if id := idOf(list[0]); b.res[id] != nil {
				return jsonCopy(b.res[id]), true
			}
		}
		return m, true
	}
	out := m
	for _, n := range []string{"$filter", "$skip", "$top"} {
		if _, ok := params[n]; ok && !coll {
			b.fail(w, http.StatusBadRequest, "", "The query parameter "+n+" applies to resource collections.", "Base.1.19.QueryNotSupportedOnResource")
			return nil, false
		}
	}
	if coll {
		list := m["Members"].([]any)
		if f, ok := params["$filter"]; ok {
			keep, err := b.filterMembers(list, f)
			if err != nil {
				return bad("The $filter expression is not valid: " + err.Error())
			}
			list = keep
		}
		total := len(list)
		for _, n := range []string{"$skip", "$top"} {
			v, ok := params[n]
			if !ok {
				continue
			}
			k, err := strconv.Atoi(v)
			if err != nil || k < 0 {
				return bad("The value of " + n + " is not a non-negative integer.")
			}
			if n == "$skip" {
				list = list[min(k, len(list)):]
			} else {
				list = list[:min(k, len(list))]
			}
		}
		out = shallow(m)
		out["Members"] = list
		if _, filtered := params["$filter"]; filtered || m["Members@odata.count"] != nil {
			out["Members@odata.count"] = total
		}
	}
	if e, ok := params["$expand"]; ok {
		kind, levels, err := parseExpand(e)
		if err != nil {
			return bad(err.Error())
		}
		out = jsonCopy(out)
		budget := maxExpansions
		b.expand(out, levels, kind, false, &budget)
	}
	// excerpt: the mockups define no Excerpt annotation: the whole resource.
	if s, ok := params["$select"]; ok {
		sel, err := parseSelect(s)
		if err != nil {
			return bad(err.Error())
		}
		out = selectProps(out, sel)
	}
	return out, true
}

func idOf(v any) string {
	m, _ := v.(map[string]any)
	s, _ := m["@odata.id"].(string)
	return s
}

func shallow(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func jsonCopy(m map[string]any) map[string]any {
	data, _ := json.Marshal(m)
	var out map[string]any
	_ = json.Unmarshal(data, &out)
	return out
}

// ---- $expand ----

// parseExpand reads . | ~ | * with an optional ($levels=N) (7.3.2 Table 8).
func parseExpand(s string) (kind byte, levels int, err error) {
	levels = 1
	if s == "" {
		return 0, 0, fmt.Errorf("$expand has no value")
	}
	kind = s[0]
	if kind != '.' && kind != '~' && kind != '*' {
		return 0, 0, fmt.Errorf("the $expand option %q is not one of . ~ *", s)
	}
	rest := s[1:]
	if rest == "" {
		return kind, levels, nil
	}
	n, ok := strings.CutPrefix(rest, "($levels=")
	if !ok || !strings.HasSuffix(n, ")") {
		return 0, 0, fmt.Errorf("the $expand value %q is not an option with ($levels=N)", s)
	}
	levels, err = strconv.Atoi(strings.TrimSuffix(n, ")"))
	if err != nil || levels < 1 || levels > maxLevels {
		return 0, 0, fmt.Errorf("$levels must be between 1 and %d", maxLevels)
	}
	return kind, levels, nil
}

func isRef(v any) (string, bool) {
	m, ok := v.(map[string]any)
	if !ok || len(m) != 1 {
		return "", false
	}
	id, ok := m["@odata.id"].(string)
	return id, ok
}

// expand replaces, in res, the references the option selects by the resources
// they name, remaining levels deep.
func (b *BMC) expand(v any, remaining int, kind byte, inLinks bool, budget *int) {
	switch x := v.(type) {
	case map[string]any:
		for k, child := range x {
			links := inLinks || k == "Links"
			if id, ok := isRef(child); ok && remaining > 0 && *budget > 0 && (kind == '*' || kind == '.' && !links || kind == '~' && links) && b.res[id] != nil {
				*budget--
				exp := jsonCopy(b.res[id])
				b.expand(exp, remaining-1, kind, false, budget)
				x[k] = exp
				continue
			}
			b.expand(child, remaining, kind, links, budget)
		}
	case []any:
		for i, child := range x {
			if id, ok := isRef(child); ok && remaining > 0 && *budget > 0 && (kind == '*' || kind == '.' && !inLinks || kind == '~' && inLinks) && b.res[id] != nil {
				*budget--
				exp := jsonCopy(b.res[id])
				b.expand(exp, remaining-1, kind, false, budget)
				x[i] = exp
				continue
			}
			b.expand(child, remaining, kind, inLinks, budget)
		}
	}
}

// ---- $select ----

type selNode map[string]selNode

func parseSelect(s string) (selNode, error) {
	root := selNode{}
	for _, p := range strings.Split(s, ",") {
		if p == "" {
			return nil, fmt.Errorf("$select has an empty property")
		}
		n := root
		for _, seg := range strings.Split(p, "/") {
			if seg == "" {
				return nil, fmt.Errorf("$select has an empty name in %q", p)
			}
			if n[seg] == nil {
				n[seg] = selNode{}
			}
			n = n[seg]
		}
	}
	return root, nil
}

// selectProps keeps the selected properties of an object, whole when the
// property is an object and nothing deeper is selected (7.3.3), and the @odata
// annotations: @odata.id, @odata.type, @odata.context, @odata.etag.
func selectProps(res map[string]any, sel selNode) map[string]any {
	out := map[string]any{}
	for k, v := range res {
		name, _, annotation := strings.Cut(k, "@")
		switch {
		case strings.HasPrefix(k, "@odata."):
			out[k] = v
		case annotation && sel[name] != nil: // an annotation of a selected property
			out[k] = v
		case !annotation && sel[k] != nil:
			out[k] = selectValue(v, sel[k])
		}
	}
	return out
}

func selectValue(v any, sel selNode) any {
	if len(sel) == 0 {
		return v
	}
	switch x := v.(type) {
	case map[string]any:
		return selectProps(x, sel)
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = selectValue(e, sel)
		}
		return out
	}
	return v
}

// ---- $filter ----

// filterMembers keeps the members of a collection that satisfy a $filter
// expression (the properties are those of the member resources).
func (b *BMC) filterMembers(list []any, expr string) ([]any, error) {
	toks, err := qlexFilter(expr)
	if err != nil {
		return nil, err
	}
	p := &qfparser{toks: toks}
	ast, err := p.or()
	if err != nil {
		return nil, err
	}
	if p.pos != len(toks) {
		return nil, fmt.Errorf("unexpected %q", toks[p.pos].text)
	}
	out := []any{} // an empty collection is an empty array, never null (DSP0266 9.6.11)
	for _, m := range list {
		res := b.res[idOf(m)]
		if res == nil {
			continue
		}
		ok, err := ast(res)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, m)
		}
	}
	return out, nil
}

type qftok struct {
	kind byte // w s n ( )
	text string
}

func qlexFilter(s string) ([]qftok, error) {
	var out []qftok
	for i := 0; i < len(s); {
		ch := s[i]
		switch {
		case ch == ' ':
			i++
		case ch == '(' || ch == ')':
			out = append(out, qftok{ch, string(ch)})
			i++
		case ch == '\'':
			var sb strings.Builder
			j := i + 1
			for {
				if j >= len(s) {
					return nil, fmt.Errorf("unterminated string")
				}
				if s[j] == '\'' {
					if j+1 < len(s) && s[j+1] == '\'' {
						sb.WriteByte('\'')
						j += 2
						continue
					}
					break
				}
				sb.WriteByte(s[j])
				j++
			}
			out = append(out, qftok{'s', sb.String()})
			i = j + 1
		case ch == '-' || ch >= '0' && ch <= '9':
			j := i + 1
			for j < len(s) && strings.IndexByte("0123456789.eE+-", s[j]) >= 0 {
				j++
			}
			if _, err := strconv.ParseFloat(s[i:j], 64); err != nil {
				return nil, fmt.Errorf("bad number %q", s[i:j])
			}
			out = append(out, qftok{'n', s[i:j]})
			i = j
		case ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch == '_':
			j := i
			for j < len(s) && (s[j] >= 'a' && s[j] <= 'z' || s[j] >= 'A' && s[j] <= 'Z' || s[j] >= '0' && s[j] <= '9' || s[j] == '_' || s[j] == '/') {
				j++
			}
			out = append(out, qftok{'w', s[i:j]})
			i = j
		default:
			return nil, fmt.Errorf("unexpected character %q", string(ch))
		}
	}
	return out, nil
}

type qfnode func(res map[string]any) (bool, error)

type qfparser struct {
	toks []qftok
	pos  int
}

func (p *qfparser) word(w string) bool {
	if p.pos < len(p.toks) && p.toks[p.pos].kind == 'w' && p.toks[p.pos].text == w {
		p.pos++
		return true
	}
	return false
}

// or, and, unary: the lowest precedence is or, then and, then not, then grouping
// (7.3.4); the comparisons are leaves.
func (p *qfparser) or() (qfnode, error) {
	l, err := p.and()
	if err != nil {
		return nil, err
	}
	for p.word("or") {
		r, err := p.and()
		if err != nil {
			return nil, err
		}
		a, b := l, r
		l = func(res map[string]any) (bool, error) {
			x, err := a(res)
			if err != nil || x {
				return x, err
			}
			return b(res)
		}
	}
	return l, nil
}

func (p *qfparser) and() (qfnode, error) {
	l, err := p.unary()
	if err != nil {
		return nil, err
	}
	for p.word("and") {
		r, err := p.unary()
		if err != nil {
			return nil, err
		}
		a, b := l, r
		l = func(res map[string]any) (bool, error) {
			x, err := a(res)
			if err != nil || !x {
				return x, err
			}
			return b(res)
		}
	}
	return l, nil
}

func (p *qfparser) unary() (qfnode, error) {
	if p.pos >= len(p.toks) {
		return nil, fmt.Errorf("an operand is missing")
	}
	if p.word("not") {
		n, err := p.unary()
		if err != nil {
			return nil, err
		}
		return func(res map[string]any) (bool, error) { x, err := n(res); return !x, err }, nil
	}
	if p.toks[p.pos].kind == '(' {
		p.pos++
		n, err := p.or()
		if err != nil {
			return nil, err
		}
		if p.pos >= len(p.toks) || p.toks[p.pos].kind != ')' {
			return nil, fmt.Errorf("a parenthesis is not closed")
		}
		p.pos++
		return n, nil
	}
	if p.toks[p.pos].kind != 'w' {
		return nil, fmt.Errorf("a comparison is expected")
	}
	path := p.toks[p.pos].text
	if p.pos+2 >= len(p.toks) {
		return nil, fmt.Errorf("a comparison is incomplete")
	}
	op, lit := p.toks[p.pos+1], p.toks[p.pos+2]
	if op.kind != 'w' {
		return nil, fmt.Errorf("an operator is expected after %s", path)
	}
	switch op.text {
	case "eq", "ne", "gt", "ge", "lt", "le":
	default:
		return nil, fmt.Errorf("%q is not a comparison operator", op.text)
	}
	var want any
	switch {
	case lit.kind == 's':
		want = lit.text
	case lit.kind == 'n':
		f, _ := strconv.ParseFloat(lit.text, 64)
		want = f
	case lit.kind == 'w' && (lit.text == "true" || lit.text == "false"):
		want = lit.text == "true"
	default:
		return nil, fmt.Errorf("%q is not a literal", lit.text)
	}
	p.pos += 3
	return func(res map[string]any) (bool, error) { return compare(res, path, op.text, want) }, nil
}

func lookup(res map[string]any, path string) (any, bool) {
	var cur any = res
	for _, seg := range strings.Split(path, "/") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		if cur, ok = m[seg]; !ok {
			return nil, false
		}
	}
	return cur, cur != nil
}

func number(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case int:
		return float64(x), true
	case json.Number:
		f, err := x.Float64()
		return f, err == nil
	}
	return 0, false
}

// compare evaluates property op literal; a property the member does not have
// (or null) is neither equal nor ordered, and is not equal to anything (ne is
// true); a literal of another type than the property is an error (400).
func compare(res map[string]any, path, op string, want any) (bool, error) {
	got, ok := lookup(res, path)
	if !ok {
		return op == "ne", nil
	}
	var c int
	switch w := want.(type) {
	case string:
		g, ok := got.(string)
		if !ok {
			return false, fmt.Errorf("%s is not a string", path)
		}
		c = strings.Compare(g, w)
	case float64:
		g, ok := number(got)
		if !ok {
			return false, fmt.Errorf("%s is not a number", path)
		}
		switch {
		case g < w:
			c = -1
		case g > w:
			c = 1
		}
	case bool:
		g, ok := got.(bool)
		if !ok {
			return false, fmt.Errorf("%s is not a boolean", path)
		}
		if op != "eq" && op != "ne" {
			return false, fmt.Errorf("a boolean is not ordered")
		}
		if g != w {
			c = 1
		}
	}
	switch op {
	case "eq":
		return c == 0, nil
	case "ne":
		return c != 0, nil
	case "gt":
		return c > 0, nil
	case "ge":
		return c >= 0, nil
	case "lt":
		return c < 0, nil
	}
	return c <= 0, nil
}
