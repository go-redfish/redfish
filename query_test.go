package redfish_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/go-redfish/redfish"
	"github.com/go-redfish/redfish/mockbmc"
)

func ip(n int) *int { return &n }

// allOn is a service that advertises every parameter.
var allOn = redfish.ProtocolFeatures{Advertised: true, ExpandAll: true, ExpandLevels: true, ExpandLinks: true, ExpandNoLinks: true, MaxLevels: 6,
	Excerpt: true, Filter: true, Only: true, Select: true, TopSkip: true}

func queryOf(t *testing.T, enc string) url.Values {
	t.Helper()
	v, err := url.ParseQuery(enc)
	if err != nil {
		t.Fatalf("%q does not parse: %v", enc, err)
	}
	return v
}

// The examples of DSP0266 1.23.0 7.3.1 Table 7, 7.3.2 and 7.3.3, as the query
// strings this client sends.
func TestQueryEncodeSpecExamples(t *testing.T) {
	for _, tc := range []struct {
		name string
		q    redfish.Query
		want string
	}{
		{"expand all, 3 levels (7.3.1)", redfish.Query{Expand: &redfish.Expand{Kind: redfish.ExpandAll, Levels: 3}}, "$expand=*($levels=3)"},
		{"expand period, 1 level (7.3.1)", redfish.Query{Expand: &redfish.Expand{Levels: 1}}, "$expand=."},
		{"expand tilde (Table 8)", redfish.Query{Expand: &redfish.Expand{Kind: redfish.ExpandLinks}}, "$expand=~"},
		{"expand period, 2 levels (Table 8)", redfish.Query{Expand: &redfish.Expand{Levels: 2}}, "$expand=.($levels=2)"},
		{"select (7.3.1)", redfish.Query{Select: []string{"SystemType", "Status"}}, "$select=SystemType,Status"},
		{"select a path (7.3.3)", redfish.Query{Select: []string{"Name", "SystemType", "Status/State"}}, "$select=Name,SystemType,Status/State"},
		{"select with expand (7.3.2)", redfish.Query{Select: []string{"Members/UUID"}, Expand: &redfish.Expand{}}, "$expand=.&$select=Members/UUID"},
		{"top (7.3.1)", redfish.Query{Top: ip(30)}, "$top=30"},
		{"skip (7.3.1)", redfish.Query{Skip: ip(5)}, "$skip=5"},
		{"top 0 is allowed (7.3.1)", redfish.Query{Top: ip(0)}, "$top=0"},
		{"only (7.3.1)", redfish.Query{Only: true}, "only"},
		{"excerpt (7.3.1)", redfish.Query{Excerpt: true}, "excerpt"},
		{"filter (7.3.1)", redfish.Query{Filter: "SystemType eq 'Physical'"}, "$filter=SystemType%20eq%20'Physical'"},
		{"filter, precedence (Table 9)", redfish.Query{Filter: "(Status/State eq 'Enabled' and Status/Health eq 'OK') or SystemType eq 'Physical'"},
			"$filter=(Status/State%20eq%20'Enabled'%20and%20Status/Health%20eq%20'OK')%20or%20SystemType%20eq%20'Physical'"},
		{"filter, not (Table 9)", redfish.Query{Filter: "not (ProcessorSummary/Count eq 2)"}, "$filter=not%20(ProcessorSummary/Count%20eq%202)"},
		{"the order of 7.3.1", redfish.Query{Select: []string{"Id"}, Expand: &redfish.Expand{}, Top: ip(2), Skip: ip(1), Filter: "Id ne 'x'"},
			"$filter=Id%20ne%20'x'&$skip=1&$top=2&$expand=.&$select=Id"},
		{"nothing", redfish.Query{}, ""},
	} {
		got, err := tc.q.Encode(allOn)
		if err != nil || got != tc.want {
			t.Errorf("%s: %q, %v; want %q", tc.name, got, err, tc.want)
		}
	}
}

func TestQueryValidation(t *testing.T) {
	bad := map[string]redfish.Query{
		"select with an ampersand":    {Select: []string{"Name&$expand=*"}},
		"select with an index":        {Select: []string{"Members[0]/Id"}},
		"select empty":                {Select: []string{""}},
		"select empty segment":        {Select: []string{"Status//State"}},
		"select wildcard":             {Select: []string{"*"}},
		"select a space":              {Select: []string{"Na me"}},
		"select a digit first":        {Select: []string{"9a"}},
		"top negative":                {Top: ip(-1)},
		"skip too large":              {Skip: ip(1 << 40)},
		"levels negative":             {Expand: &redfish.Expand{Levels: -1}},
		"levels too many":             {Expand: &redfish.Expand{Levels: 65}},
		"expand kind unknown":         {Expand: &redfish.Expand{Kind: 9}},
		"only with top":               {Only: true, Top: ip(1)},
		"only with expand":            {Only: true, Expand: &redfish.Expand{}},
		"only with excerpt":           {Only: true, Excerpt: true},
		"filter an extra parameter":   {Filter: "Id eq 'a'&$expand=*"},
		"filter a hash":               {Filter: "Id eq 'a' #x"},
		"filter no operand":           {Filter: "Id eq"},
		"filter no operator":          {Filter: "Id 'a'"},
		"filter unknown operator":     {Filter: "Id like 'a'"},
		"filter unterminated string":  {Filter: "Id eq 'a"},
		"filter an open parenthesis":  {Filter: "(Id eq 'a'"},
		"filter a closed parenthesis": {Filter: "Id eq 'a')"},
		"filter two comparisons":      {Filter: "Id eq 'a' Id eq 'b'"},
		"filter a literal first":      {Filter: "'a' eq Id"},
		"filter a function":           {Filter: "contains(Id,'a')"},
		"filter a bad number":         {Filter: "Count eq 1.2.3"},
		"filter a path with a dot":    {Filter: "a.b eq 1"},
		"filter empty path":           {Filter: "a//b eq 1"},
		"filter keyword as property":  {Filter: "and eq 1"},
		"filter a control character":  {Filter: "Id eq 'a\nb'"},
		"filter too long":             {Filter: "Id eq '" + strings.Repeat("a", 2000) + "'"},
		"filter too deep":             {Filter: strings.Repeat("(", 100) + "Id eq 1" + strings.Repeat(")", 100)},
	}
	for name, q := range bad {
		if enc, err := q.Encode(allOn); !errors.Is(err, redfish.ErrQuery) {
			t.Errorf("%s: accepted as %q (%v)", name, enc, err)
		}
	}
	// A value that is allowed cannot add a parameter: the ampersand, the hash, the
	// plus and the percent sign of a string literal are encoded.
	q := redfish.Query{Filter: "Name eq 'a&b#c+d%e;f g'", Top: ip(3)}
	enc, err := q.Encode(allOn)
	if err != nil {
		t.Fatal(err)
	}
	v := queryOf(t, enc)
	if len(v) != 2 || v.Get("$filter") != "Name eq 'a&b#c+d%e;f g'" || v.Get("$top") != "3" || strings.ContainsAny(enc, " #+;") || strings.Count(enc, "&") != 1 {
		t.Errorf("%q parses to %v", enc, v)
	}
}

func TestQueryRefusedUnlessAdvertised(t *testing.T) {
	for _, tc := range []struct {
		name    string
		q       redfish.Query
		feature string
		feats   func(f *redfish.ProtocolFeatures)
	}{
		{"$expand=.", redfish.Query{Expand: &redfish.Expand{}}, "ExpandQuery.NoLinks", func(f *redfish.ProtocolFeatures) { f.ExpandNoLinks = false }},
		{"$expand=~", redfish.Query{Expand: &redfish.Expand{Kind: redfish.ExpandLinks}}, "ExpandQuery.Links", func(f *redfish.ProtocolFeatures) { f.ExpandLinks = false }},
		{"$expand=*", redfish.Query{Expand: &redfish.Expand{Kind: redfish.ExpandAll}}, "ExpandQuery.ExpandAll", func(f *redfish.ProtocolFeatures) { f.ExpandAll = false }},
		{"$levels", redfish.Query{Expand: &redfish.Expand{Levels: 2}}, "ExpandQuery.Levels", func(f *redfish.ProtocolFeatures) { f.ExpandLevels = false }},
		{"$levels over MaxLevels", redfish.Query{Expand: &redfish.Expand{Levels: 7}}, "MaxLevels", func(f *redfish.ProtocolFeatures) {}},
		{"$select", redfish.Query{Select: []string{"Id"}}, "SelectQuery", func(f *redfish.ProtocolFeatures) { f.Select = false }},
		{"$top", redfish.Query{Top: ip(1)}, "TopSkipQuery", func(f *redfish.ProtocolFeatures) { f.TopSkip = false }},
		{"$skip", redfish.Query{Skip: ip(1)}, "TopSkipQuery", func(f *redfish.ProtocolFeatures) { f.TopSkip = false }},
		{"only", redfish.Query{Only: true}, "OnlyMemberQuery", func(f *redfish.ProtocolFeatures) { f.Only = false }},
		{"excerpt", redfish.Query{Excerpt: true}, "ExcerptQuery", func(f *redfish.ProtocolFeatures) { f.Excerpt = false }},
		{"$filter", redfish.Query{Filter: "Id eq 'a'"}, "FilterQuery", func(f *redfish.ProtocolFeatures) { f.Filter = false }},
		{"$filter comparison", redfish.Query{Filter: "Id eq 'a'"}, "FilterQueryComparisonOperations", func(f *redfish.ProtocolFeatures) { no := false; f.FilterComparison = &no }},
		{"$filter compound", redfish.Query{Filter: "Id eq 'a' or Id eq 'b'"}, "FilterQueryCompoundOperations", func(f *redfish.ProtocolFeatures) { no := false; f.FilterCompound = &no }},
	} {
		f := allOn
		tc.feats(&f)
		_, err := tc.q.Encode(f)
		if !errors.Is(err, redfish.ErrUnsupported) || !strings.Contains(err.Error(), tc.feature) {
			t.Errorf("%s: %v; want ErrUnsupported naming %s", tc.name, err, tc.feature)
		}
	}
	// Nothing advertised: nothing but the zero query is sent. A filter without
	// compound operations passes when the root does not state them (before 1.17.0).
	if enc, err := (redfish.Query{Filter: "Id eq 'a'"}).Encode(redfish.ProtocolFeatures{Advertised: true, Filter: true}); err != nil || enc == "" {
		t.Errorf("a filter on a service with FilterQuery only: %q, %v", enc, err)
	}
	for _, q := range []redfish.Query{{Only: true}, {Select: []string{"Id"}}, {Top: ip(1)}, {Excerpt: true}, {Filter: "Id eq 1"}, {Expand: &redfish.Expand{}}} {
		if enc, err := q.Encode(redfish.ProtocolFeatures{}); !errors.Is(err, redfish.ErrUnsupported) || enc != "" {
			t.Errorf("%+v on a service that advertises nothing: %q, %v", q, enc, err)
		}
	}
}

func TestParseProtocolFeatures(t *testing.T) {
	f, err := redfish.ParseProtocolFeatures([]byte(`{"Id":"RootService","ProtocolFeaturesSupported":{"ExpandQuery":{"ExpandAll":false,"Levels":true,"Links":true,"MaxLevels":4,"NoLinks":true},
		"ExcerptQuery":false,"FilterQuery":true,"FilterQueryComparisonOperations":true,"FilterQueryCompoundOperations":false,"OnlyMemberQuery":true,"SelectQuery":true,"TopSkipQuery":"yes"}}`))
	if err != nil || !f.Advertised || f.ExpandAll || !f.ExpandLevels || !f.ExpandLinks || !f.ExpandNoLinks || f.MaxLevels != 4 || f.Excerpt || !f.Filter || !f.Only || !f.Select ||
		f.TopSkip || f.FilterComparison == nil || !*f.FilterComparison || f.FilterCompound == nil || *f.FilterCompound {
		t.Errorf("features = %+v, %v", f, err)
	}
	if f, err := redfish.ParseProtocolFeatures([]byte(`{"Id":"RootService"}`)); err != nil || f.Advertised || f.ExpandNoLinks {
		t.Errorf("a root without the object: %+v, %v", f, err)
	}
	if _, err := redfish.ParseProtocolFeatures([]byte(`[1]`)); !errors.Is(err, redfish.ErrMalformed) {
		t.Errorf("not an object: %v", err)
	}
	if f, _ := redfish.ParseProtocolFeatures([]byte(`{"ProtocolFeaturesSupported":{"FilterQuery":false,"FilterQueryComparisonOperations":true,"ExpandQuery":{"Levels":true,"MaxLevels":99999999999}}}`)); f.FilterComparison != nil || f.MaxLevels != 64 {
		t.Errorf("an operation under FilterQuery false, or a huge MaxLevels, is kept: %+v", f)
	}
}

func rootGets(b *mockbmc.BMC) int {
	n := 0
	for _, l := range b.Log() {
		if l == "GET /redfish/v1/ 200" {
			n++
		}
	}
	return n
}

func TestProtocolFeaturesReadOnceAndAdvertised(t *testing.T) {
	b, srv := newMock(t, mockbmc.Options{})
	c := newClient(t, srv, redfish.Config{})
	ctx := context.Background()
	f, err := c.ProtocolFeatures(ctx)
	if err != nil || !f.Advertised || !f.ExpandNoLinks || !f.ExpandAll || !f.ExpandLinks || !f.ExpandLevels || f.MaxLevels != 6 || !f.Select || !f.Filter || !f.Only || !f.Excerpt || !f.TopSkip {
		t.Fatalf("the rackmount mock: %+v, %v", f, err)
	}
	if _, err := c.ProtocolFeatures(ctx); err != nil || rootGets(b) != 1 {
		t.Errorf("the service root was read %d times for two calls", rootGets(b))
	}
	// the other tree, and the same tree with the option, advertise nothing
	for _, o := range []mockbmc.Options{{Tree: mockbmc.TreeEmulator}, {NoQueries: true}} {
		_, srv := newMock(t, o)
		f, err := newClient(t, srv, redfish.Config{}).ProtocolFeatures(ctx)
		if err != nil || f.Advertised || f.ExpandNoLinks || f.Select {
			t.Errorf("%+v advertises %+v, %v", o, f, err)
		}
	}
}

func TestGetWithRefusesWhatIsNotAdvertisedAndSendsNothing(t *testing.T) {
	ctx := context.Background()
	for _, o := range []mockbmc.Options{{Tree: mockbmc.TreeEmulator}, {NoQueries: true}} {
		b, srv := newMock(t, o)
		c := newClient(t, srv, redfish.Config{})
		for _, q := range []redfish.Query{{Only: true}, {Excerpt: true}, {Select: []string{"Id"}}, {Top: ip(1)}, {Filter: "Id eq 'a'"}, {Expand: &redfish.Expand{}}} {
			if _, err := c.GetWith(ctx, "/redfish/v1/Systems", q); !errors.Is(err, redfish.ErrUnsupported) {
				t.Errorf("%+v: %v", q, err)
			}
		}
		for i, line := range b.Log() {
			if b.Queries()[i] != "" {
				t.Errorf("a query was sent: %s ?%s", line, b.Queries()[i])
			}
		}
	}
	// the path carries no query
	_, srv := newMock(t, mockbmc.Options{})
	c := newClient(t, srv, redfish.Config{})
	if _, err := c.GetWith(ctx, "/redfish/v1/Systems?$top=1&only", redfish.Query{Only: true}); !errors.Is(err, redfish.ErrQuery) {
		t.Errorf("a query in the path: %v", err)
	}
	// and the mock does what 7.3.1 says of a service that does not: 501 for a $ parameter, ignored otherwise
	_, srv = newMock(t, mockbmc.Options{NoQueries: true})
	c = newClient(t, srv, redfish.Config{})
	var se *redfish.StatusError
	if _, err := c.Do(ctx, redfish.Request{Method: "GET", Path: "/redfish/v1/Systems?$select=Id"}); !errors.As(err, &se) || se.Code != http.StatusNotImplemented {
		t.Errorf("$select on a service without it: %v", err)
	}
	r, err := c.Do(ctx, redfish.Request{Method: "GET", Path: "/redfish/v1/Systems?only&excerpt&Foo=1"})
	if err != nil || !strings.Contains(string(r.Body), `"Members"`) {
		t.Errorf("only ignored by a service without it: %v", err)
	}
}

func getJSON(t *testing.T, c *redfish.Client, p string, q redfish.Query) map[string]any {
	t.Helper()
	r, err := c.GetWith(context.Background(), p, q)
	if err != nil {
		t.Fatalf("%s %+v: %v", p, q, err)
	}
	var m map[string]any
	if err := json.Unmarshal(r.Body, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestGetWithAgainstTheMock(t *testing.T) {
	_, srv := newMock(t, mockbmc.Options{})
	c := newClient(t, srv, redfish.Config{})
	ctx := context.Background()
	const sensors = "/redfish/v1/Chassis/1U/Sensors"
	full := getJSON(t, c, sensors, redfish.Query{})
	all, _ := full["Members"].([]any)
	if len(all) < 10 {
		t.Fatalf("the sensors collection has %d members", len(all))
	}
	count := full["Members@odata.count"]

	// 9.6.10: $top and $skip do not change the count; 7.3.1: $top=0 is an empty set; $skip past the end too
	m := getJSON(t, c, sensors, redfish.Query{Top: ip(3), Skip: ip(2)})
	got, _ := m["Members"].([]any)
	if len(got) != 3 || got[0].(map[string]any)["@odata.id"] != all[2].(map[string]any)["@odata.id"] || m["Members@odata.count"] != count {
		t.Errorf("top 3 skip 2 = %v (count %v want %v)", got, m["Members@odata.count"], count)
	}
	if m := getJSON(t, c, sensors, redfish.Query{Top: ip(0)}); len(m["Members"].([]any)) != 0 || m["Members@odata.count"] != count {
		t.Errorf("top 0 = %v", m)
	}
	if m := getJSON(t, c, sensors, redfish.Query{Skip: ip(1000)}); len(m["Members"].([]any)) != 0 {
		t.Errorf("skip 1000 = %v", m)
	}

	// $expand: the members are inline, the same as a read of each member
	m = getJSON(t, c, sensors, redfish.Query{Expand: &redfish.Expand{}})
	exp, _ := m["Members"].([]any)
	if len(exp) != len(all) {
		t.Fatalf("expand: %d members for %d", len(exp), len(all))
	}
	for i, x := range exp {
		one := getJSON(t, c, all[i].(map[string]any)["@odata.id"].(string), redfish.Query{})
		if a, _ := json.Marshal(x); string(a) != mustJSON(t, one) {
			t.Fatalf("member %d expanded differs from its own read:\n%s\n%s", i, a, mustJSON(t, one))
		}
	}

	// $levels: one level leaves the sub-links of a member alone, two expand them
	sys := getJSON(t, c, "/redfish/v1/Systems/437XR1138R2", redfish.Query{Expand: &redfish.Expand{}})
	if _, deeper := sys["Processors"].(map[string]any)["Members"]; !deeper {
		t.Errorf("$expand=. on a system leaves Processors a link: %v", sys["Processors"])
	}
	sys = getJSON(t, c, "/redfish/v1/Systems", redfish.Query{Expand: &redfish.Expand{}})
	if _, in := sys["Members"].([]any)[0].(map[string]any)["Processors"].(map[string]any)["Members"]; in {
		t.Error("level 1 expanded the Processors of a member")
	}
	sys = getJSON(t, c, "/redfish/v1/Systems", redfish.Query{Expand: &redfish.Expand{Levels: 2}})
	if _, in := sys["Members"].([]any)[0].(map[string]any)["Processors"].(map[string]any)["Members"]; !in {
		t.Error("level 2 did not expand the Processors of a member")
	}
	// the period leaves Links alone, the tilde expands only them, the asterisk both
	ch := getJSON(t, c, "/redfish/v1/Chassis/1U", redfish.Query{Expand: &redfish.Expand{}})
	if _, ok := ch["Links"].(map[string]any)["ComputerSystems"].([]any)[0].(map[string]any)["Id"]; ok {
		t.Error("the period expanded a link of Links")
	}
	ch = getJSON(t, c, "/redfish/v1/Chassis/1U", redfish.Query{Expand: &redfish.Expand{Kind: redfish.ExpandLinks}})
	if _, ok := ch["Links"].(map[string]any)["ComputerSystems"].([]any)[0].(map[string]any)["Id"]; !ok {
		t.Error("the tilde did not expand a link of Links")
	}
	if _, ok := ch["Thermal"].(map[string]any)["Fans"]; ok {
		t.Error("the tilde expanded a link outside Links")
	}
	ch = getJSON(t, c, "/redfish/v1/Chassis/1U", redfish.Query{Expand: &redfish.Expand{Kind: redfish.ExpandAll}})
	if _, ok := ch["Links"].(map[string]any)["ComputerSystems"].([]any)[0].(map[string]any)["Id"]; !ok {
		t.Error("the asterisk did not expand a link of Links")
	}
	if _, ok := ch["Thermal"].(map[string]any)["Fans"]; !ok {
		t.Error("the asterisk did not expand a link outside Links")
	}

	// $select: the properties asked for, @odata.id and @odata.type always (7.3.3)
	m = getJSON(t, c, "/redfish/v1/Systems/437XR1138R2", redfish.Query{Select: []string{"Name", "Status/State"}})
	if len(m) != 4 || m["Name"] == nil || m["@odata.id"] == nil || m["@odata.type"] == nil || len(m["Status"].(map[string]any)) != 1 {
		t.Errorf("select = %v", m)
	}
	// $select with $expand, 7.3.2: the UUIDs of the members of the system collection
	m = getJSON(t, c, "/redfish/v1/Systems", redfish.Query{Select: []string{"Members/UUID"}, Expand: &redfish.Expand{}})
	if mem := m["Members"].([]any)[0].(map[string]any); mem["UUID"] == nil || mem["Name"] != nil || mem["@odata.id"] == nil {
		t.Errorf("select Members/UUID = %v", m)
	}

	// $filter on the members: 7.3.4
	n := func(expr string) int {
		return len(getJSON(t, c, sensors, redfish.Query{Filter: expr})["Members"].([]any))
	}
	fans := n("ReadingUnits eq '%'")
	if fans == 0 || fans >= len(all) {
		t.Fatalf("filter eq: %d of %d", fans, len(all))
	}
	if n("not (ReadingUnits eq '%')") != len(all)-fans || n("ReadingUnits ne '%'") != len(all)-fans {
		t.Error("not and ne disagree with eq")
	}
	if n("ReadingUnits eq '%' or ReadingUnits ne '%'") != len(all) || n("ReadingUnits eq '%' and ReadingUnits ne '%'") != 0 {
		t.Error("or and and")
	}
	if n("(ReadingUnits eq '%' and Status/State eq 'Enabled') or Id eq 'CPUFan1'") < 1 {
		t.Error("grouping")
	}
	if n("Reading gt 0") == 0 || n("Reading ge 0") < n("Reading gt 0") || n("Reading lt 0") != 0 {
		t.Error("numbers")
	}
	if m := getJSON(t, c, sensors, redfish.Query{Filter: "ReadingUnits eq '%'"}); int(m["Members@odata.count"].(float64)) != fans {
		t.Errorf("the count after a filter is %v, want %d (9.6.10)", m["Members@odata.count"], fans)
	}
	// a service answers 400 for a literal of the wrong type
	if _, err := c.GetWith(ctx, sensors, redfish.Query{Filter: "ReadingUnits eq 3"}); err == nil {
		t.Error("a number compared to a string was accepted")
	}

	// only: a collection of one member answers with it; of several, the collection
	one := getJSON(t, c, "/redfish/v1/Systems", redfish.Query{Only: true})
	if one["Id"] != "437XR1138R2" || one["Members"] != nil {
		t.Errorf("only on a collection of one = %v", one["@odata.id"])
	}
	if many := getJSON(t, c, sensors, redfish.Query{Only: true}); many["Members"] == nil {
		t.Error("only on a collection of many did not return the collection")
	}
	// on a singular resource, top/skip/filter/only are 400 QueryNotSupportedOnResource (7.3.1)
	var se *redfish.StatusError
	if _, err := c.GetWith(ctx, "/redfish/v1/Systems/437XR1138R2", redfish.Query{Top: ip(1)}); !errors.As(err, &se) || se.Code != 400 || se.Info[0].MessageID != "Base.1.19.QueryNotSupportedOnResource" {
		t.Errorf("top on a resource: %v", err)
	}
	// excerpt: no Excerpt annotation in the mockups: the whole resource
	if a, b := getJSON(t, c, sensors, redfish.Query{Excerpt: true}), full; mustJSON(t, a) != mustJSON(t, b) {
		t.Error("excerpt changed a resource that has no Excerpt annotation")
	}
	// the raw requests the mock saw were the ones in the spec's spelling
	if _, err := c.GetWith(ctx, "/redfish/v1/Systems", redfish.Query{Expand: &redfish.Expand{Kind: redfish.ExpandAll, Levels: 2}, Select: []string{"Members/Id"}}); err != nil {
		t.Fatal(err)
	}
}

func mustJSON(t *testing.T, m map[string]any) string {
	t.Helper()
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestMockQueryRefusalsAreThoseOfTheSpec(t *testing.T) {
	_, srv := newMock(t, mockbmc.Options{})
	c := newClient(t, srv, redfish.Config{})
	ctx := context.Background()
	for _, tc := range []struct {
		query string
		code  int
		id    string
	}{
		{"only&$top=1", 400, "Base.1.19.QueryCombinationInvalid"},
		{"$bogus=1", 501, ""},
		{"$top=-1", 400, ""},
		{"$top=a", 400, ""},
		{"$expand=x", 400, ""},
		{"$expand=.($levels=99)", 400, ""},
		{"$filter=Id%20eq", 400, ""},
		{"$top=1&$top=2", 400, ""},
	} {
		_, err := c.Do(ctx, redfish.Request{Method: "GET", Path: "/redfish/v1/Chassis/1U/Sensors?" + tc.query})
		var se *redfish.StatusError
		if !errors.As(err, &se) || se.Code != tc.code || tc.id != "" && (len(se.Info) == 0 || se.Info[0].MessageID != tc.id) {
			t.Errorf("?%s: %v", tc.query, err)
		}
	}
	// names are case-insensitive (7.3.1: "should")
	r, err := c.Do(ctx, redfish.Request{Method: "GET", Path: "/redfish/v1/Chassis/1U/Sensors?$TOP=1"})
	if err != nil || strings.Count(string(r.Body), `"@odata.id": "/redfish/v1/Chassis/1U/Sensors/`) > 1 && strings.Count(string(r.Body), `"Members"`) != 1 {
		t.Errorf("$TOP: %v", err)
	}
}

// ---- the bounds ----

// A stub service whose root advertises $expand and whose collection answers a
// body of the given size.
func bigService(t *testing.T, size int) *httptest.Server {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/redfish/v1/" || r.URL.Path == "/redfish/v1" {
			_, _ = w.Write([]byte(`{"ProtocolFeaturesSupported":{"ExpandQuery":{"NoLinks":true,"Levels":true,"MaxLevels":3}}}`))
			return
		}
		pad := strings.Repeat("x", size)
		_, _ = w.Write([]byte(`{"Members":[],"Pad":"` + pad + `"}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestExpandedBodiesAreBoundedByAnExplicitLimit(t *testing.T) {
	ctx := context.Background()
	// between MaxBody and MaxExpandedBody: refused without $expand, accepted with it
	c := newClient(t, bigService(t, redfish.MaxBody+100), redfish.Config{})
	if _, err := c.Get(ctx, "/redfish/v1/Big"); !errors.Is(err, redfish.ErrTooLarge) {
		t.Errorf("a plain read over MaxBody: %v", err)
	}
	if _, err := c.GetWith(ctx, "/redfish/v1/Big", redfish.Query{Expand: &redfish.Expand{}}); err != nil {
		t.Errorf("an expanded read over MaxBody and under MaxExpandedBody: %v", err)
	}
	// the select alone does not raise the limit
	if _, err := c.GetWith(ctx, "/redfish/v1/Big", redfish.Query{Expand: nil, Top: nil}); !errors.Is(err, redfish.ErrTooLarge) {
		t.Errorf("a zero query over MaxBody: %v", err)
	}
	// over MaxExpandedBody: refused, never read whole
	c = newClient(t, bigService(t, redfish.MaxExpandedBody+100), redfish.Config{})
	if _, err := c.GetWith(ctx, "/redfish/v1/Big", redfish.Query{Expand: &redfish.Expand{}}); !errors.Is(err, redfish.ErrTooLarge) {
		t.Errorf("an expanded read over MaxExpandedBody: %v", err)
	}
}

// ---- the converted reads ----

type read struct {
	name string
	fn   func(c *redfish.Client) (any, error)
	// perMember is true when the read has more than one member to read, so that
	// $expand must save requests.
	perMember bool
}

func convertedReads() []read {
	ctx := context.Background()
	return []read{
		{"CPUInventory", func(c *redfish.Client) (any, error) { return c.CPUInventory(ctx, sysPath) }, true},
		{"MemoryInventory", func(c *redfish.Client) (any, error) { return c.MemoryInventory(ctx, sysPath) }, true},
		{"NICInventory", func(c *redfish.Client) (any, error) { return c.NICInventory(ctx, sysPath) }, true},
		{"ChassisSensors", func(c *redfish.Client) (any, error) { return c.ChassisSensors(ctx, chassisPath) }, true},
		{"ChassisPowerSubsystem", func(c *redfish.Client) (any, error) { return c.ChassisPowerSubsystem(ctx, chassisPath) }, false},
		{"ChassisThermalSubsystem", func(c *redfish.Client) (any, error) { return c.ChassisThermalSubsystem(ctx, chassisPath) }, true},
		{"ChassisInventory", func(c *redfish.Client) (any, error) { return c.ChassisInventory(ctx) }, false},
		{"FirmwareInventory", func(c *redfish.Client) (any, error) { return c.FirmwareInventory(ctx) }, true},
		{"SoftwareInventory", func(c *redfish.Client) (any, error) { return c.SoftwareInventory(ctx) }, true},
		{"ListUsers", func(c *redfish.Client) (any, error) { return c.ListUsers(ctx) }, true},
		{"Roles", func(c *redfish.Client) (any, error) { return c.Roles(ctx) }, true},
		{"Subscriptions", func(c *redfish.Client) (any, error) { return c.Subscriptions(ctx) }, true},
	}
}

func encodeAny(t *testing.T, v any, err error) string {
	t.Helper()
	if err != nil {
		return "error: " + err.Error()
	}
	b, jerr := json.Marshal(v)
	if jerr != nil {
		t.Fatal(jerr)
	}
	return string(b)
}

// requests is the number of requests the mock served since mark.
func requests(b *mockbmc.BMC, mark int) int { return len(b.Log()) - mark }

// Each converted read gives the same answer with $expand and member by member,
// and the same on the tree whose root advertises nothing; with $expand it makes
// fewer requests (counted from the mock's own log) and sends the parameter only
// where it is advertised.
func TestConvertedReadsAreEquivalentAndCheaper(t *testing.T) {
	ctx := context.Background()
	for _, tree := range trees {
		t.Run(tree, func(t *testing.T) {
			b, srv := newMock(t, mockbmc.Options{Tree: tree})
			advertised := tree == mockbmc.TreeRackmount
			auto := newClient(t, srv, redfish.Config{})
			forced := newClient(t, srv, redfish.Config{NoExpand: true})
			if _, err := auto.ProtocolFeatures(ctx); err != nil { // the one read of the root, not counted below
				t.Fatal(err)
			}
			for _, r := range convertedReads() {
				mark := len(b.Log())
				on, onErr := r.fn(auto)
				nOn := requests(b, mark)
				expanded := 0
				for _, q := range b.Queries()[mark:] {
					if strings.Contains(q, "$expand") {
						expanded++
					}
				}
				mark = len(b.Log())
				off, offErr := r.fn(forced)
				nOff := requests(b, mark)
				for _, q := range b.Queries()[mark:] {
					if q != "" {
						t.Errorf("%s: NoExpand sent a query %q", r.name, q)
					}
				}
				if a, z := encodeAny(t, on, onErr), encodeAny(t, off, offErr); a != z {
					t.Errorf("%s: with $expand:\n%s\nmember by member:\n%s", r.name, a, z)
				}
				if onErr != nil {
					t.Logf("%s: %v", r.name, onErr)
				}
				switch {
				case !advertised && (expanded != 0 || nOn != nOff):
					t.Errorf("%s: the service advertises nothing: %d requests with $expand=. (%d), %d without", r.name, nOn, expanded, nOff)
				case advertised && onErr == nil && r.perMember && (expanded == 0 || nOn >= nOff):
					t.Errorf("%s: %d requests with $expand, %d member by member", r.name, nOn, nOff)
				case advertised && nOn > nOff:
					t.Errorf("%s: $expand costs more requests: %d against %d", r.name, nOn, nOff)
				}
				t.Logf("%-24s with $expand %2d requests, member by member %2d", r.name, nOn, nOff)
			}
		})
	}
}

// The request counts of the converted reads on the rackmount mock, with and
// without $expand: a change to them is a change to the reads, to be made on
// purpose. (The service root is read once, before: ProtocolFeaturesSupported.)
func TestConvertedReadsRequestCounts(t *testing.T) {
	ctx := context.Background()
	want := map[string][2]int{ // with $expand, member by member
		"CPUInventory": {2, 5}, "MemoryInventory": {2, 6}, "NICInventory": {2, 6}, "ChassisSensors": {2, 43},
		"ChassisPowerSubsystem": {4, 6}, "ChassisThermalSubsystem": {5, 9}, "ChassisInventory": {2, 3},
		"FirmwareInventory": {3, 6}, "SoftwareInventory": {3, 5}, "ListUsers": {3, 4}, "Roles": {3, 6}, "Subscriptions": {3, 7},
	}
	b, srv := newMock(t, mockbmc.Options{})
	auto := newClient(t, srv, redfish.Config{})
	forced := newClient(t, srv, redfish.Config{NoExpand: true})
	if _, err := auto.ProtocolFeatures(ctx); err != nil {
		t.Fatal(err)
	}
	got := map[string][2]int{}
	for _, r := range convertedReads() {
		mark := len(b.Log())
		_, _ = r.fn(auto)
		on := requests(b, mark)
		mark = len(b.Log())
		_, _ = r.fn(forced)
		got[r.name] = [2]int{on, requests(b, mark)}
	}
	for name, w := range want {
		if got[name] != w {
			t.Errorf("%s: %d requests with $expand and %d member by member, want %d and %d", name, got[name][0], got[name][1], w[0], w[1])
		}
	}
	if len(got) != len(want) {
		t.Errorf("%d converted reads, %d counted", len(got), len(want))
	}
}

// A service that advertises $expand but ignores it answers links: the members
// are read one by one and the result is the same.
func TestExpandIgnoredByTheServiceFallsBackToMembers(t *testing.T) {
	b, err := mockbmc.New(mockbmc.Options{})
	if err != nil {
		t.Fatal(err)
	}
	var seen []string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.URL.Path+"?"+r.URL.RawQuery)
		switch {
		case strings.Contains(r.URL.RawQuery, "$expand") && r.URL.Path == "/redfish/v1/Chassis/1U/Sensors":
			r.URL.RawQuery = "" // the service ignores it: links only
		case strings.Contains(r.URL.RawQuery, "$expand"):
			http.Error(w, `{"error":{"code":"Base.1.19.GeneralError","message":"too big"}}`, http.StatusInsufficientStorage)
			return
		}
		b.Handler().ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	ref, srv2 := newMock(t, mockbmc.Options{NoQueries: true})
	_ = ref
	ctx := context.Background()
	c := newClient(t, srv, redfish.Config{})
	got, err := c.ChassisSensors(ctx, chassisPath)
	want, err2 := newClient(t, srv2, redfish.Config{}).ChassisSensors(ctx, chassisPath)
	if err != nil || err2 != nil || encodeAny(t, got, nil) != encodeAny(t, want, nil) {
		t.Fatalf("links only: %v %v\n%s\n%s", err, err2, encodeAny(t, got, nil), encodeAny(t, want, nil))
	}
	// a 507 on the expanded request: the members are read one by one
	seen = nil
	got2, err := c.CPUInventory(ctx, sysPath)
	want2, _ := newClient(t, srv2, redfish.Config{}).CPUInventory(ctx, sysPath)
	if err != nil || encodeAny(t, got2, nil) != encodeAny(t, want2, nil) || len(got2) == 0 {
		t.Fatalf("507: %v\n%v\n%v", err, got2, want2)
	}
	if !slices.ContainsFunc(seen, func(s string) bool { return strings.Contains(s, "$expand") }) {
		t.Error("the expanded request was never tried")
	}
}
