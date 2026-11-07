package main

import (
	"fmt"

	"github.com/go-redfish/redfish"
	"github.com/spf13/cobra"
)

// queryFlags are the query parameters of `raw get` (DSP0266 1.23.0 7.3). Each is
// sent only when the service root advertises it in ProtocolFeaturesSupported;
// otherwise the command fails with ErrUnsupported naming the feature and sends
// nothing.
type queryFlags struct {
	expand       int
	expandKind   string
	selectProps  []string
	top, skip    int
	only, excerp bool
	filter       string
}

func (q *queryFlags) bind(cmd *cobra.Command) {
	f := cmd.Flags()
	f.IntVar(&q.expand, "expand", 0, "$expand with N levels (7.3.2; 1 is the default depth); needs ExpandQuery")
	f.StringVar(&q.expandKind, "expand-kind", "nolinks", "which hyperlinks $expand expands: nolinks (.), links (~) or all (*)")
	f.StringSliceVar(&q.selectProps, "select", nil, "$select: comma-separated properties or paths such as Status/State (7.3.3)")
	f.IntVar(&q.top, "top", 0, "$top: the number of members of a collection to return (7.3.1)")
	f.IntVar(&q.skip, "skip", 0, "$skip: the number of members of a collection to skip (7.3.1)")
	f.BoolVar(&q.only, "only", false, "only: a collection of exactly one member answers with that member; alone (7.3.1)")
	f.BoolVar(&q.excerp, "excerpt", false, "excerpt: the properties of the schema's Excerpt annotation (7.3.1)")
	f.StringVar(&q.filter, "filter", "", "$filter expression on a collection's members, e.g. \"Status/State eq 'Enabled'\" (7.3.4)")
}

// query builds the Query of the flags that were given.
func (q *queryFlags) query(cmd *cobra.Command) (redfish.Query, error) {
	var out redfish.Query
	f := cmd.Flags()
	if f.Lookup("expand") == nil { // not a get
		return out, nil
	}
	if f.Changed("expand") || f.Changed("expand-kind") {
		var kind redfish.ExpandKind
		switch q.expandKind {
		case "nolinks":
			kind = redfish.ExpandNoLinks
		case "links":
			kind = redfish.ExpandLinks
		case "all":
			kind = redfish.ExpandAll
		default:
			return out, fmt.Errorf("--expand-kind: want nolinks, links or all, not %q", q.expandKind)
		}
		out.Expand = &redfish.Expand{Kind: kind, Levels: q.expand}
	}
	out.Select = q.selectProps
	if f.Changed("top") {
		out.Top = &q.top
	}
	if f.Changed("skip") {
		out.Skip = &q.skip
	}
	out.Only, out.Excerpt, out.Filter = q.only, q.excerp, q.filter
	return out, nil
}
