package main

import (
	"errors"
	"fmt"
	"io"
	"strconv"
	"text/tabwriter"
	"time"

	"github.com/go-redfish/redfish"
	"github.com/spf13/cobra"
)

func telemetryCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "telemetry",
		Short: "TelemetryService: status, definitions, report definitions, reports, triggers, watch",
		Long: `telemetry drives the TelemetryService (DSP8010 2025.4: TelemetryService 1.4.1,
MetricDefinition 1.3.6, MetricReportDefinition 1.4.7, MetricReport 1.5.2, Triggers
1.4.0): status reads the service and its limits, definitions and report-definitions
list the metric definitions and the metric report definitions, reports and report
read the metric reports (-o table shows a report as a time series), triggers lists
the triggers, create-definition and delete-definition write a metric report
definition, test submits a test metric report (SubmitTestMetricReport) and watch
prints the metric reports the service sends over the event stream.

A metric value is a string in the schema: the JSON output keeps it as sent ("raw")
and gives a "number" only for a value that is one; the table never shows a value
that is not a number as 0. For a definition of type OnRequest, reading its report
is what generates it.`,
		Args: cobra.NoArgs,
	}
	cmd.AddCommand(telemetryStatusCmd(g), telemetryDefinitionsCmd(g), telemetryReportDefinitionsCmd(g), telemetryReportsCmd(g), telemetryReportCmd(g),
		telemetryTriggersCmd(g), telemetryCreateCmd(g), telemetryDeleteCmd(g), telemetryTestCmd(g), telemetryWatchCmd(g))
	return cmd
}

func telemetryStatusCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "The TelemetryService: enabled, collection functions, minimum interval, maximum reports, links",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := g.client(cmd, false)
			if err != nil {
				return err
			}
			ts, err := c.TelemetryService(cmd.Context())
			if err != nil {
				return err
			}
			return g.show(cmd, ts)
		},
	}
}

func telemetryDefinitionsCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "definitions [ID]",
		Short: "List the metric definitions, or show one",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := g.client(cmd, false)
			if err != nil {
				return err
			}
			if len(args) == 1 {
				d, err := c.MetricDefinition(cmd.Context(), args[0])
				if err != nil {
					return err
				}
				return g.show(cmd, d)
			}
			ds, err := c.MetricDefinitions(cmd.Context())
			if err != nil {
				return err
			}
			return g.show(cmd, ds)
		},
	}
}

func telemetryReportDefinitionsCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "report-definitions [ID]",
		Short: "List the metric report definitions (type, schedule, metrics, report actions), or show one",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := g.client(cmd, false)
			if err != nil {
				return err
			}
			if len(args) == 1 {
				d, err := c.MetricReportDefinition(cmd.Context(), args[0])
				if err != nil {
					return err
				}
				return g.show(cmd, d)
			}
			ds, err := c.MetricReportDefinitions(cmd.Context())
			if err != nil {
				return err
			}
			return g.show(cmd, ds)
		},
	}
}

// reportSummary is one report of a list, without its values.
type reportSummary struct {
	ID         string `json:"id"`
	Timestamp  string `json:"timestamp,omitempty"`
	Values     int    `json:"values"`
	Definition string `json:"definition,omitempty"`
}

func telemetryReportsCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "reports",
		Short: "List the metric reports (-o json: with their values)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := g.client(cmd, false)
			if err != nil {
				return err
			}
			rs, err := c.MetricReports(cmd.Context())
			if err != nil {
				return err
			}
			if g.output != "table" {
				return g.show(cmd, rs)
			}
			sum := make([]reportSummary, 0, len(rs))
			for _, r := range rs {
				sum = append(sum, reportSummary{ID: r.ID, Timestamp: r.TimestampRaw, Values: len(r.Values), Definition: r.DefinitionURI})
			}
			return g.show(cmd, sum)
		},
	}
}

func telemetryReportCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "report ID",
		Short: "Read a metric report; -o table shows it as a time series (reading an OnRequest report generates it)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := g.client(cmd, false)
			if err != nil {
				return err
			}
			r, err := c.MetricReport(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if g.output == "table" {
				return printSeries(cmd.OutOrStdout(), r)
			}
			return g.show(cmd, r)
		},
	}
}

// printSeries writes a report as a time series: one row per sample, a column
// each for the metric, the property, the time and the value, the series in the
// order the report has them and each sorted by time. A value that is not a
// number (null, text) or has no time is listed after them as it was sent, and
// is never shown as 0.
func printSeries(w io.Writer, r redfish.MetricReport) error {
	tw := tabwriter.NewWriter(w, 0, 8, 2, ' ', 0)
	fmt.Fprintln(tw, "METRIC\tPROPERTY\tTIME\tVALUE")
	series, skipped := r.TimeSeries()
	for _, s := range series {
		for _, p := range s.Points {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", clean(s.MetricID), clean(s.MetricProperty), p.At.Format(time.RFC3339Nano), formatNumber(p.Value))
		}
	}
	for _, v := range skipped {
		ts, val := v.TimestampRaw, v.Raw
		switch {
		case ts == "":
			ts = "-"
		case v.Timestamp == nil:
			ts += " (not a date-time)"
		}
		switch {
		case v.Null:
			val = "null"
		case v.Number == nil:
			val = fmt.Sprintf("%q (not a number)", clip(v.Raw))
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", clean(v.MetricID), clean(v.MetricProperty), clean(ts), clean(val))
	}
	return tw.Flush()
}

func clip(s string) string {
	if len(s) > 80 {
		return s[:80] + "..."
	}
	return s
}

func formatNumber(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

func telemetryTriggersCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "triggers",
		Short: "List the triggers (thresholds and actions); a service may have none",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := g.client(cmd, false)
			if err != nil {
				return err
			}
			ts, err := c.Triggers(cmd.Context())
			if err != nil {
				return err
			}
			return g.show(cmd, ts)
		},
	}
}

func telemetryCreateCmd(g *globals) *cobra.Command {
	var in redfish.MetricReportDefinitionInput
	var interval, function, scope, duration string
	var metrics, props []string
	var appendLimit int64
	var suppress, enabled bool
	cmd := &cobra.Command{
		Use:   "create-definition --type Periodic|OnChange|OnRequest (--metric ID... | --property URI...)",
		Short: "Create a metric report definition",
		Long: `create-definition posts a MetricReportDefinition to the collection of the
TelemetryService. Before anything is sent it is checked against the schema and
the service: the type, the actions and the update mode are the schema's
enumerations; a Periodic definition needs --interval, a duration such as PT10S,
not shorter than the service's MinCollectionInterval; --function must be one of
the service's SupportedCollectionFunctions; each --metric must be the Id of a
MetricDefinition of the service; a definition that records its reports in the
collection is refused when the service holds MaxReports of them.

--metric (repeatable) names the metrics of a MetricDefinition; --property
(repeatable) lists properties by URI, in one entry; they are exclusive.
--function, --time-scope and --duration apply to every --metric (or to the
--property entry).`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			d := in
			f := cmd.Flags()
			if len(metrics) > 0 && len(props) > 0 {
				return errors.New("--metric and --property are exclusive (MetricReportDefinition 1.4.7 Metric: MetricId or MetricProperties)")
			}
			if interval != "" {
				d.Schedule = &redfish.ReportSchedule{RecurrenceInterval: interval}
			}
			for _, id := range metrics {
				d.Metrics = append(d.Metrics, redfish.MetricSpec{MetricID: id, CollectionFunction: function, CollectionTimeScope: scope, CollectionDuration: duration})
			}
			if len(props) > 0 {
				d.Metrics = append(d.Metrics, redfish.MetricSpec{MetricProperties: props, CollectionFunction: function, CollectionTimeScope: scope, CollectionDuration: duration})
			}
			if f.Changed("append-limit") {
				d.AppendLimit = &appendLimit
			}
			if f.Changed("suppress-repeats") {
				d.SuppressRepeatedMetricValue = &suppress
			}
			if f.Changed("enabled") {
				d.Enabled = &enabled
			}
			c, err := g.client(cmd, false)
			if err != nil {
				return err
			}
			def, out, err := c.CreateMetricReportDefinition(cmd.Context(), d)
			if err != nil {
				return err
			}
			return g.show(cmd, map[string]any{"changed": out.Changed, "message": out.Message, "definition": def})
		},
	}
	f := cmd.Flags()
	f.StringVar(&in.ID, "id", "", "the Id asked for (the service may assign another)")
	f.StringVar(&in.Name, "name", "", "the name (default: the Id)")
	f.StringVar(&in.Type, "type", "", "Periodic, OnChange or OnRequest (required)")
	f.StringVar(&interval, "interval", "", "the RecurrenceInterval of a Periodic definition, e.g. PT10S")
	f.StringArrayVar(&metrics, "metric", nil, "the Id of a MetricDefinition to report (repeatable)")
	f.StringArrayVar(&props, "property", nil, "a property URI to report, e.g. /redfish/v1/Chassis/1U/Sensors/TotalPower#/Reading (repeatable)")
	f.StringVar(&function, "function", "", "the CollectionFunction: Average, Maximum, Minimum or Summation")
	f.StringVar(&scope, "time-scope", "", "the CollectionTimeScope: Point, Interval or StartupInterval")
	f.StringVar(&duration, "duration", "", "the CollectionDuration the function covers, e.g. PT1M")
	f.StringArrayVar(&in.ReportActions, "action", nil, "LogToMetricReportsCollection or RedfishEvent (repeatable)")
	f.StringVar(&in.ReportUpdates, "updates", "", "Overwrite, AppendWrapsWhenFull, AppendStopsWhenFull or NewReport")
	f.Int64Var(&appendLimit, "append-limit", 0, "the entries an appended report holds (required with the Append modes)")
	f.StringVar(&in.ReportTimespan, "timespan", "", "the longest timespan a report covers, e.g. PT1H")
	f.BoolVar(&suppress, "suppress-repeats", false, "leave out a value equal to the one of the previous report")
	f.BoolVar(&enabled, "enabled", true, "MetricReportDefinitionEnabled (sent only when given)")
	_ = cmd.MarkFlagRequired("type")
	return cmd
}

func telemetryDeleteCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "delete-definition ID|URI",
		Short: "Delete a metric report definition (a definition that is already gone is not a change)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := g.client(cmd, false)
			if err != nil {
				return err
			}
			out, err := c.DeleteMetricReportDefinition(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			return g.showOutcome(cmd, out)
		},
	}
}

func telemetryTestCmd(g *globals) *cobra.Command {
	var name, metricID, property, timestamp string
	var values []string
	cmd := &cobra.Command{
		Use:   "test --name NAME --value VALUE...",
		Short: "Submit a test metric report (SubmitTestMetricReport)",
		Long: `test posts the TelemetryService.SubmitTestMetricReport action: the service
generates a metric report named NAME from the values and sends it to the event
destinations. Each --value is a string (a number is its string); --metric-id,
--property and --timestamp (an RFC 3339 date-time) apply to every value. Each
parameter is checked against the service's ActionInfo before the POST. A service
without the action answers "not supported".`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			vs := make([]redfish.TestMetricValue, 0, len(values))
			for _, v := range values {
				vs = append(vs, redfish.TestMetricValue{MetricID: metricID, MetricProperty: property, MetricValue: v, Timestamp: timestamp})
			}
			c, err := g.client(cmd, false)
			if err != nil {
				return err
			}
			out, err := c.SubmitTestMetricReport(cmd.Context(), name, vs)
			if err != nil {
				return err
			}
			return g.showOutcome(cmd, out)
		},
	}
	f := cmd.Flags()
	f.StringVar(&name, "name", "", "the MetricReportName (required)")
	f.StringArrayVar(&values, "value", nil, "a metric value, as a string (repeatable; at least one)")
	f.StringVar(&metricID, "metric-id", "", "the MetricId of the values")
	f.StringVar(&property, "property", "", "the MetricProperty of the values")
	f.StringVar(&timestamp, "timestamp", "", "the Timestamp of the values, an RFC 3339 date-time")
	_ = cmd.MarkFlagRequired("name")
	_ = cmd.MarkFlagRequired("value")
	return cmd
}

func telemetryWatchCmd(g *globals) *cobra.Command {
	var o redfish.EventsOptions
	var count int
	cmd := &cobra.Command{
		Use:   "watch",
		Short: "Print the metric reports the service sends over the event stream (Ctrl-C ends it)",
		Long: `watch opens the SSE stream of the EventService with the filter
EventFormatType eq 'MetricReport' (DSP0266 1.23.0 12.5.2, 12.5.2.2) and prints each
report: with -o json one JSON document per line, {"id": ..., "event": <the
MetricReport as sent>}, with -o table the time series of each report. The stream
is the one of events listen (same printer, bounds and reconnection).`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := g.client(cmd, false)
			if err != nil {
				return err
			}
			s, err := c.MetricReportEvents(cmd.Context(), o)
			if err != nil {
				return err
			}
			defer s.Close()
			out := cmd.OutOrStdout()
			n := 0
			for ev := range s.C {
				if err := g.printEvent(out, ev); err != nil {
					return err
				}
				if n++; count > 0 && n >= count {
					s.Close()
					break
				}
			}
			return s.Err()
		},
	}
	f := cmd.Flags()
	f.StringVar(&o.Filter, "filter", "", "a further $filter, joined with `and` to the format criterion")
	f.IntVar(&o.MaxReconnects, "reconnects", 3, "how many times a broken connection is reopened with Last-Event-ID (0: never)")
	f.IntVar(&count, "count", 0, "stop after this many reports (default: until interrupted)")
	return cmd
}
