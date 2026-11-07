package redfish_test

import (
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/go-redfish/redfish"
)

// The query builder takes what a caller names (a filter, properties, numbers)
// and the service root's ProtocolFeaturesSupported takes what a BMC sends. Neither
// may panic; a query that is built holds only the parameters asked for, each
// advertised, and no value can add one (DSP0266 7.3).

func featuresFromBits(bits int) redfish.ProtocolFeatures {
	f := redfish.ProtocolFeatures{Advertised: true}
	f.ExpandAll, f.ExpandLevels, f.ExpandLinks, f.ExpandNoLinks = bits&1 != 0, bits&2 != 0, bits&4 != 0, bits&8 != 0
	f.Excerpt, f.Filter, f.Only, f.Select, f.TopSkip = bits&16 != 0, bits&32 != 0, bits&64 != 0, bits&128 != 0, bits&256 != 0
	if bits&512 != 0 {
		f.MaxLevels = 3
	}
	if bits&1024 != 0 {
		no := false
		f.FilterCompound = &no
	}
	return f
}

func FuzzQueryEncode(f *testing.F) {
	f.Add("Id eq 'a'", "Id,Name", 0, 2, 3, 0, 0x1ff)
	f.Add("(Status/State eq 'Enabled' and Status/Health eq 'OK') or SystemType eq 'Physical'", "Members/UUID", 2, 1, 0, 5, 0x1ff)
	f.Add("Name eq 'a&$expand=*#b+c'", "Name&$expand=*", 1, 70, -1, 1<<40, 0x1ff)
	f.Add("not (Count ge 2)", "", 0, 0, 0, 0, 0)
	f.Add(strings.Repeat("(", 40)+"A eq 1"+strings.Repeat(")", 40), "A/B/C", 3, 3, 1, 1, 0x3ff)
	f.Add("A eq 'x' or B ne true", "@odata.id,Members@odata.count", 2, 4, 2, 2, 0x5ff)
	f.Fuzz(func(t *testing.T, filter, sel string, kind, levels, top, skip, bits int) {
		feat := featuresFromBits(bits)
		q := redfish.Query{Filter: filter}
		if sel != "" {
			q.Select = strings.Split(sel, ",")
		}
		if bits&4096 != 0 {
			q.Expand = &redfish.Expand{Kind: redfish.ExpandKind(kind % 4), Levels: levels}
		}
		if bits&8192 != 0 {
			q.Top = &top
		}
		if bits&16384 != 0 {
			q.Skip = &skip
		}
		q.Only, q.Excerpt = bits&32768 != 0, bits&65536 != 0
		enc, err := q.Encode(feat)
		if err != nil {
			if !errors.Is(err, redfish.ErrQuery) && !errors.Is(err, redfish.ErrUnsupported) {
				t.Fatalf("an error of no kind: %v", err)
			}
			if enc != "" {
				t.Fatalf("an error and a query: %q", enc)
			}
			return
		}
		if strings.ContainsAny(enc, " #+;\r\n\t\"<>\\^`{|}") || len(enc) > 8<<10 {
			t.Fatalf("an unencoded character or an unbounded query: %q", enc)
		}
		v, perr := url.ParseQuery(enc)
		if perr != nil {
			t.Fatalf("%q does not parse: %v", enc, perr)
		}
		if strings.Count(enc, "&")+1 != len(v) && enc != "" {
			t.Fatalf("%q has %d parameters for %d ampersands", enc, len(v), strings.Count(enc, "&"))
		}
		for name, vals := range v {
			if len(vals) != 1 {
				t.Fatalf("%s twice in %q", name, enc)
			}
			ok := false
			switch name {
			case "$filter":
				ok = feat.Filter && vals[0] == filter
			case "$select":
				ok = feat.Select && vals[0] == strings.Join(q.Select, ",")
			case "$top", "$skip":
				ok = feat.TopSkip
			case "only":
				ok = feat.Only && q.Only && len(v) == 1
			case "excerpt":
				ok = feat.Excerpt && q.Excerpt
			case "$expand":
				ok = q.Expand != nil && vals[0] != ""
			}
			if !ok {
				t.Fatalf("the parameter %s of %q is not asked for or not advertised (%+v)", name, enc, feat)
			}
		}
	})
}

func FuzzParseProtocolFeatures(f *testing.F) {
	f.Add([]byte(`{"ProtocolFeaturesSupported":{"ExpandQuery":{"ExpandAll":true,"Levels":true,"Links":false,"MaxLevels":6,"NoLinks":true},"ExcerptQuery":true,"FilterQuery":true,"FilterQueryComparisonOperations":true,"FilterQueryCompoundOperations":false,"OnlyMemberQuery":true,"SelectQuery":true,"TopSkipQuery":true}}`))
	f.Add([]byte(`{"ProtocolFeaturesSupported":{"ExpandQuery":{"MaxLevels":-1},"FilterQuery":false,"FilterQueryComparisonOperations":true}}`))
	f.Add([]byte(`{"ProtocolFeaturesSupported":{"ExpandQuery":[1],"SelectQuery":"yes","TopSkipQuery":null}}`))
	f.Add([]byte(`{"ProtocolFeaturesSupported":{"ExpandQuery":{"MaxLevels":99999999999999999999}}}`))
	f.Add([]byte(`{"ProtocolFeaturesSupported":null}`))
	f.Add([]byte(`[]`))
	f.Fuzz(func(t *testing.T, data []byte) {
		feat, err := redfish.ParseProtocolFeatures(data)
		if err != nil {
			if !errors.Is(err, redfish.ErrMalformed) {
				t.Fatalf("an error of no kind: %v", err)
			}
			return
		}
		if feat.MaxLevels < 0 || feat.MaxLevels > 64 {
			t.Fatalf("MaxLevels %d", feat.MaxLevels)
		}
		if !feat.Advertised && (feat.ExpandNoLinks || feat.Select || feat.Filter || feat.Only || feat.TopSkip || feat.Excerpt) {
			t.Fatalf("a feature without the object: %+v", feat)
		}
		// what the features do not allow is never built
		top := 1
		for _, tc := range []struct {
			q  redfish.Query
			ok bool
		}{
			{redfish.Query{Only: true}, feat.Only}, {redfish.Query{Excerpt: true}, feat.Excerpt},
			{redfish.Query{Select: []string{"Id"}}, feat.Select}, {redfish.Query{Top: &top}, feat.TopSkip},
			{redfish.Query{Filter: "Id eq 1"}, feat.Filter && (feat.FilterComparison == nil || *feat.FilterComparison)}, {redfish.Query{Expand: &redfish.Expand{}}, feat.ExpandNoLinks},
			{redfish.Query{Expand: &redfish.Expand{Kind: redfish.ExpandAll}}, feat.ExpandAll},
			{redfish.Query{Expand: &redfish.Expand{Kind: redfish.ExpandLinks}}, feat.ExpandLinks},
		} {
			if _, err := tc.q.Encode(feat); (err == nil) != tc.ok {
				t.Fatalf("%+v: %v with features %+v", tc.q, err, feat)
			}
		}
	})
}
