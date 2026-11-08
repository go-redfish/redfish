package redfish_test

import (
	"bytes"
	"context"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-redfish/redfish"
	"github.com/go-redfish/redfish/mockbmc"
)

const telBase = "/redfish/v1/TelemetryService"

// clock is a mock clock that moves ten seconds at each reading.
func clock() mockbmc.Clock {
	t := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	return mockbmc.ClockFunc(func() time.Time {
		t = t.Add(10 * time.Second)
		return t
	})
}

func telMock(t *testing.T, tree string) (*mockbmc.BMC, *redfish.Client) {
	t.Helper()
	b, srv := newMock(t, mockbmc.Options{Tree: tree, Clock: clock()})
	return b, newClient(t, srv, redfish.Config{})
}

func forEachTelemetryTree(t *testing.T, fn func(t *testing.T, b *mockbmc.BMC, c *redfish.Client)) {
	t.Helper()
	for _, tree := range []string{mockbmc.TreeRackmount, mockbmc.TreeEmulator} {
		t.Run(tree, func(t *testing.T) {
			b, c := telMock(t, tree)
			fn(t, b, c)
		})
	}
}

func TestParseMetricNumber(t *testing.T) {
	good := map[string]float64{"0": 0, "-0": 0, "7": 7, "-12": -12, "100.5": 100.5, "1e3": 1000, "1E-2": 0.01, "2.5e+2": 250, "325675": 325675, "0.000001": 1e-6}
	for s, want := range good {
		if got, err := redfish.ParseMetricNumber(s); err != nil || got != want {
			t.Errorf("%q = %v, %v; want %v", s, got, err, want)
		}
	}
	for _, s := range []string{"", " 1", "1 ", "+1", "01", "00", "1.", ".5", "-", "1e", "1e+", "0x10", "NaN", "Inf", "-Infinity", "1_000", "1,5", "١٢", "true",
		"1e999", "-1e999", strings.Repeat("9", 65), "1\x00", "--1", "1.2.3"} {
		got, err := redfish.ParseMetricNumber(s)
		if err == nil || !errors.Is(err, redfish.ErrNotNumeric) || got != 0 {
			t.Errorf("%q = %v, %v; want ErrNotNumeric", s, got, err)
		}
	}
}

func TestParseRedfishDuration(t *testing.T) {
	good := map[string]time.Duration{"PT1S": time.Second, "PT0.25S": 250 * time.Millisecond, "PT10M": 10 * time.Minute, "P1D": 24 * time.Hour,
		"P1DT2H3M4.5S": 24*time.Hour + 2*time.Hour + 3*time.Minute + 4*time.Second + 500*time.Millisecond, "PT0S": 0, "PT1H": time.Hour, "PT0.000000001S": 1}
	for s, want := range good {
		if got, err := redfish.ParseRedfishDuration(s); err != nil || got != want {
			t.Errorf("%q = %v, %v; want %v", s, got, err, want)
		}
	}
	for _, s := range []string{"", "P", "PT", "1S", "PT1", "PTS", "P1H", "PT1D", "PT1S1M", "P1DP1D", "PT1.S", "PT.5S", "PT1.5M", "pt1s", "PT-1S", "PT1S ",
		"PT1.0000000001S", "P99999999999D", "P40000D", "PT1S1S", "PTT1S"} {
		if got, err := redfish.ParseRedfishDuration(s); err == nil {
			t.Errorf("%q accepted as %v", s, got)
		}
	}
}

func TestTelemetryServiceRead(t *testing.T) {
	forEachTelemetryTree(t, func(t *testing.T, _ *mockbmc.BMC, c *redfish.Client) {
		ts, err := c.TelemetryService(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if ts.URI != telBase || ts.ServiceEnabled == nil || !*ts.ServiceEnabled || ts.State != "Enabled" || ts.Health != "OK" || !ts.SubmitTestMetricReport {
			t.Fatalf("TelemetryService = %+v", ts)
		}
		if !slices.Equal(ts.SupportedCollectionFunctions, []string{"Average", "Maximum", "Minimum", "Summation"}) ||
			ts.MinCollectionInterval != "PT1S" || ts.MinInterval == nil || *ts.MinInterval != time.Second || ts.MaxReports == nil || *ts.MaxReports != 8 {
			t.Fatalf("limits = %+v", ts)
		}
		if ts.MetricDefinitionsURI != telBase+"/MetricDefinitions" || ts.MetricReportDefinitionsURI != telBase+"/MetricReportDefinitions" ||
			ts.MetricReportsURI != telBase+"/MetricReports" || ts.TriggersURI != telBase+"/Triggers" {
			t.Fatalf("links = %+v", ts)
		}
	})
}

// A service whose root links no TelemetryService, or a TelemetryService without
// a collection, is ErrUnsupported on every call.
func TestTelemetryUnsupportedWhenAbsent(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/redfish/v1/":
			_, _ = w.Write([]byte(`{"@odata.id":"/redfish/v1/","Name":"Root","TelemetryService":{"@odata.id":"/redfish/v1/TelemetryService"}}`))
		case "/redfish/v1/TelemetryService":
			_, _ = w.Write([]byte(`{"@odata.id":"/redfish/v1/TelemetryService","Id":"TelemetryService","Name":"T"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	c := newClient(t, srv, redfish.Config{})
	ctx := context.Background()
	if _, err := c.TelemetryService(ctx); err != nil {
		t.Fatal(err)
	}
	calls := map[string]func() error{
		"MetricDefinitions":       func() error { _, err := c.MetricDefinitions(ctx); return err },
		"MetricReportDefinitions": func() error { _, err := c.MetricReportDefinitions(ctx); return err },
		"MetricReports":           func() error { _, err := c.MetricReports(ctx); return err },
		"Triggers":                func() error { _, err := c.Triggers(ctx); return err },
		"Create": func() error {
			_, _, err := c.CreateMetricReportDefinition(ctx, redfish.MetricReportDefinitionInput{Type: "OnRequest", Metrics: []redfish.MetricSpec{{MetricID: "X"}}})
			return err
		},
		"SubmitTest": func() error {
			_, err := c.SubmitTestMetricReport(ctx, "r", []redfish.TestMetricValue{{MetricValue: "1"}})
			return err
		},
	}
	for name, f := range calls {
		if err := f(); !errors.Is(err, redfish.ErrUnsupported) {
			t.Errorf("%s: %v, want ErrUnsupported", name, err)
		}
	}
	// No TelemetryService at all.
	_, srv2 := newMock(t, mockbmc.Options{})
	c2 := newClient(t, srv2, redfish.Config{})
	if _, err := c2.Patch(ctx, "/redfish/v1/", map[string]any{"TelemetryService": nil}, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := c2.TelemetryService(ctx); !errors.Is(err, redfish.ErrUnsupported) {
		t.Errorf("a root with no link: %v", err)
	}
}

func TestTelemetryReads(t *testing.T) {
	forEachTelemetryTree(t, func(t *testing.T, _ *mockbmc.BMC, c *redfish.Client) {
		ctx := context.Background()
		defs, err := c.MetricDefinitions(ctx)
		if err != nil || len(defs) != 3 {
			t.Fatalf("MetricDefinitions = %+v, %v", defs, err)
		}
		d, err := c.MetricDefinition(ctx, "TotalPower")
		if err != nil || d.ID != "TotalPower" || d.MetricType != "Gauge" || d.MetricDataType != "Decimal" || d.Units != "W" || d.Implementation != "PhysicalSensor" ||
			!slices.Equal(d.MetricProperties, []string{"/redfish/v1/Chassis/1U/Sensors/TotalPower#/Reading"}) || d.URI != telBase+"/MetricDefinitions/TotalPower" {
			t.Fatalf("MetricDefinition = %+v, %v", d, err)
		}
		if d2, err := c.MetricDefinition(ctx, d.URI); err != nil || d2.ID != d.ID {
			t.Fatalf("by path: %+v, %v", d2, err)
		}
		if _, err := c.MetricDefinition(ctx, "../../Chassis"); err == nil {
			t.Error("a name with a slash was resolved")
		}
		if _, err := c.MetricDefinition(ctx, "Nope"); !errors.Is(err, redfish.ErrNotFound) {
			t.Errorf("unknown definition: %v", err)
		}
		rds, err := c.MetricReportDefinitions(ctx)
		if err != nil || len(rds) != 2 {
			t.Fatalf("MetricReportDefinitions = %+v, %v", rds, err)
		}
		pm, err := c.MetricReportDefinition(ctx, "PowerMetrics")
		if err != nil {
			t.Fatal(err)
		}
		if pm.Type != "Periodic" || pm.Schedule == nil || pm.Schedule.RecurrenceInterval != "PT10S" || pm.Interval == nil || *pm.Interval != 10*time.Second ||
			len(pm.Metrics) != 2 || pm.Metrics[0].MetricID != "TotalPower" || !slices.Equal(pm.ReportActions, []string{"LogToMetricReportsCollection", "RedfishEvent"}) ||
			pm.ReportUpdates != "AppendWrapsWhenFull" || pm.AppendLimit == nil || *pm.AppendLimit != 16 || pm.Enabled == nil || !*pm.Enabled ||
			pm.MetricReportURI != telBase+"/MetricReports/PowerMetrics" || pm.State != "Enabled" {
			t.Fatalf("PowerMetrics = %+v", pm)
		}
		od, err := c.MetricReportDefinition(ctx, "CPUTemperature")
		if err != nil || od.Type != "OnRequest" || od.Schedule != nil || len(od.Metrics) != 1 || len(od.Metrics[0].MetricProperties) != 1 {
			t.Fatalf("CPUTemperature = %+v, %v", od, err)
		}
		reps, err := c.MetricReports(ctx)
		if err != nil || len(reps) != 2 {
			t.Fatalf("MetricReports = %+v, %v", reps, err)
		}
		r, err := c.MetricReport(ctx, "PowerMetrics")
		if err != nil {
			t.Fatal(err)
		}
		if r.ID != "PowerMetrics" || r.URI != telBase+"/MetricReports/PowerMetrics" || r.DefinitionURI != pm.URI || r.Timestamp == nil || len(r.Values) != 2 ||
			!strings.HasPrefix(r.ODataType, "#MetricReport.v1_5_2") {
			t.Fatalf("PowerMetrics report = %+v", r)
		}
		v := r.Values[0]
		if f, err := v.Float(); v.MetricID != "TotalPower" || v.Raw != "374" || err != nil || f != 374 || v.Timestamp == nil || v.Null ||
			v.MetricProperty != "/redfish/v1/Chassis/1U/Sensors/TotalPower#/Reading" {
			t.Fatalf("value = %+v", v)
		}
		trs, err := c.Triggers(ctx)
		if err != nil || len(trs) != 1 {
			t.Fatalf("Triggers = %+v, %v", trs, err)
		}
		tr := trs[0]
		th := tr.NumericThresholds["UpperCritical"]
		if tr.ID != "PowerCritical" || tr.MetricType != "Numeric" || !slices.Equal(tr.MetricIDs, []string{"TotalPower"}) || th.Reading == nil || *th.Reading != 550 ||
			th.Activation != "Increasing" || th.DwellTime != "PT10S" || !slices.Equal(tr.TriggerActions, []string{"RedfishEvent"}) || tr.TriggerEnabled == nil || !*tr.TriggerEnabled {
			t.Fatalf("Trigger = %+v", tr)
		}
	})
}

// The reads give the same answer with $expand (one request per collection) and
// without it (member by member).
func TestTelemetryReadsWithAndWithoutExpand(t *testing.T) {
	b, srv := newMock(t, mockbmc.Options{Clock: clock()})
	ctx := context.Background()
	plain := newClient(t, srv, redfish.Config{NoExpand: true})
	expanded := newClient(t, srv, redfish.Config{})
	want, err := plain.MetricReports(ctx)
	if err != nil {
		t.Fatal(err)
	}
	n := len(b.Log())
	got, err := expanded.MetricReports(ctx)
	if err != nil {
		t.Fatal(err)
	}
	expandedRequests := 0
	for _, l := range b.Log()[n:] {
		if strings.Contains(l, "MetricReports") {
			expandedRequests++
		}
	}
	if len(got) != len(want) || got[0].ID != want[0].ID || len(got[0].Values) != len(want[0].Values) || got[0].Values[0].Raw != want[0].Values[0].Raw {
		t.Fatalf("expanded %+v, plain %+v", got, want)
	}
	if expandedRequests != 1 {
		t.Errorf("%d requests to MetricReports with $expand, want 1: %v", expandedRequests, b.Log()[n:])
	}
}

// A report is regenerated from the sensors: a change of a Reading shows in the
// next report, and the series of the report is sorted by time.
func TestMetricReportFollowsTheSensors(t *testing.T) {
	forEachTelemetryTree(t, func(t *testing.T, b *mockbmc.BMC, c *redfish.Client) {
		ctx := context.Background()
		for i, w := range []float64{410, 455.5, 600} {
			if _, err := c.Patch(ctx, "/redfish/v1/Chassis/1U/Sensors/TotalPower", map[string]any{"Reading": w}, ""); err != nil {
				t.Fatal(err)
			}
			if n := b.TickTelemetry(); n != 1 {
				t.Fatalf("tick %d generated %d reports, want 1 (the Periodic one)", i, n)
			}
		}
		r, err := c.MetricReport(ctx, "PowerMetrics")
		if err != nil {
			t.Fatal(err)
		}
		series, skipped := r.TimeSeries()
		if len(skipped) != 0 || len(series) != 2 {
			t.Fatalf("series = %+v, skipped %+v", series, skipped)
		}
		pw := series[0]
		if pw.MetricID != "TotalPower" || len(pw.Points) != 4 {
			t.Fatalf("power series = %+v", pw)
		}
		var got []float64
		for _, p := range pw.Points {
			got = append(got, p.Value)
		}
		if !slices.Equal(got, []float64{374, 410, 455.5, 600}) {
			t.Fatalf("power values = %v (the first is the report made at creation)", got)
		}
		for i := 1; i < len(pw.Points); i++ {
			if !pw.Points[i].At.After(pw.Points[i-1].At) {
				t.Fatalf("points not sorted: %+v", pw.Points)
			}
		}
	})
}

func TestMetricReportAppendWrapsAtTheLimit(t *testing.T) {
	b, c := telMock(t, "")
	ctx := context.Background()
	for range 20 {
		b.TickTelemetry()
	}
	r, err := c.MetricReport(ctx, "PowerMetrics")
	if err != nil || len(r.Values) != 16 {
		t.Fatalf("report holds %d values, want the AppendLimit 16: %v", len(r.Values), err)
	}
	// The oldest entries went: the first value is later than the creation.
	series, _ := r.TimeSeries()
	if series[0].Points[0].At.Before(time.Date(2026, 11, 8, 12, 0, 30, 0, time.UTC)) {
		t.Errorf("the oldest entries were not dropped: %v", series[0].Points[0].At)
	}
}

// OnRequest: the report is generated by the GET.
func TestOnRequestReportIsGeneratedByTheGet(t *testing.T) {
	b, c := telMock(t, "")
	ctx := context.Background()
	if _, err := c.Patch(ctx, "/redfish/v1/Chassis/1U/Sensors/CPU1Temp", map[string]any{"Reading": 51}, ""); err != nil {
		t.Fatal(err)
	}
	r1, err := c.MetricReport(ctx, "CPUTemperature")
	if err != nil || len(r1.Values) != 1 || r1.Values[0].Raw != "51" || r1.Timestamp == nil {
		t.Fatalf("report = %+v, %v", r1, err)
	}
	if _, err := c.Patch(ctx, "/redfish/v1/Chassis/1U/Sensors/CPU1Temp", map[string]any{"Reading": 63}, ""); err != nil {
		t.Fatal(err)
	}
	r2, err := c.MetricReport(ctx, "CPUTemperature")
	if err != nil || r2.Values[0].Raw != "63" || !r2.Timestamp.After(*r1.Timestamp) {
		t.Fatalf("second report = %+v, %v (a GET generates the report)", r2, err)
	}
	if n := b.TickTelemetry(); n != 1 { // only the Periodic definition ticks
		t.Errorf("tick generated %d reports, want 1", n)
	}
}

func TestCreateAndDeleteMetricReportDefinition(t *testing.T) {
	forEachTelemetryTree(t, func(t *testing.T, b *mockbmc.BMC, c *redfish.Client) {
		ctx := context.Background()
		limit := int64(4)
		d, out, err := c.CreateMetricReportDefinition(ctx, redfish.MetricReportDefinitionInput{ID: "Temps", Name: "Temperatures", Type: "Periodic",
			Schedule: &redfish.ReportSchedule{RecurrenceInterval: "PT5S"}, Metrics: []redfish.MetricSpec{{MetricID: "CPU1Temp", CollectionFunction: "Maximum"}},
			ReportActions: []string{"LogToMetricReportsCollection"}, ReportUpdates: "AppendWrapsWhenFull", AppendLimit: &limit})
		if err != nil || !out.Changed {
			t.Fatalf("Create = %+v, %+v, %v", d, out, err)
		}
		if d.URI != telBase+"/MetricReportDefinitions/Temps" || d.ID != "Temps" || d.Type != "Periodic" || d.Name != "Temperatures" ||
			d.Interval == nil || *d.Interval != 5*time.Second || d.Metrics[0].CollectionFunction != "Maximum" || d.MetricReportURI != telBase+"/MetricReports/Temps" {
			t.Fatalf("created = %+v", d)
		}
		b.TickTelemetry()
		r, err := c.MetricReport(ctx, "Temps")
		if err != nil || len(r.Values) != 2 || r.Values[0].MetricID != "CPU1Temp" {
			t.Fatalf("report of the new definition = %+v, %v", r, err)
		}
		out, err = c.DeleteMetricReportDefinition(ctx, "Temps")
		if err != nil || !out.Changed {
			t.Fatalf("Delete = %+v, %v", out, err)
		}
		if _, err := c.MetricReport(ctx, "Temps"); !errors.Is(err, redfish.ErrNotFound) {
			t.Errorf("the report of a deleted definition: %v", err)
		}
		out, err = c.DeleteMetricReportDefinition(ctx, "Temps")
		if err != nil || out.Changed {
			t.Errorf("second delete = %+v, %v (already gone is not a change)", out, err)
		}
		if _, err := c.DeleteMetricReportDefinition(ctx, "../Nope"); err == nil {
			t.Error("a path outside the collection was deleted")
		}
	})
}

func TestCreateMetricReportDefinitionRefusals(t *testing.T) {
	b, c := telMock(t, "")
	ctx := context.Background()
	ok := redfish.MetricReportDefinitionInput{Type: "OnRequest", Metrics: []redfish.MetricSpec{{MetricID: "TotalPower"}}}
	neg := int64(-1)
	cases := map[string]struct {
		in   redfish.MetricReportDefinitionInput
		want error // nil: any error, nothing sent
	}{
		"no type":               {redfish.MetricReportDefinitionInput{Metrics: ok.Metrics}, nil},
		"bad type":              {redfish.MetricReportDefinitionInput{Type: "Hourly", Metrics: ok.Metrics}, nil},
		"no metrics":            {redfish.MetricReportDefinitionInput{Type: "OnRequest"}, nil},
		"id and properties":     {redfish.MetricReportDefinitionInput{Type: "OnRequest", Metrics: []redfish.MetricSpec{{MetricID: "TotalPower", MetricProperties: []string{"/redfish/v1/x#/y"}}}}, nil},
		"neither":               {redfish.MetricReportDefinitionInput{Type: "OnRequest", Metrics: []redfish.MetricSpec{{}}}, nil},
		"property not a URI":    {redfish.MetricReportDefinitionInput{Type: "OnRequest", Metrics: []redfish.MetricSpec{{MetricProperties: []string{"https://evil.example/x"}}}}, nil},
		"periodic no schedule":  {redfish.MetricReportDefinitionInput{Type: "Periodic", Metrics: ok.Metrics}, nil},
		"bad interval":          {redfish.MetricReportDefinitionInput{Type: "Periodic", Metrics: ok.Metrics, Schedule: &redfish.ReportSchedule{RecurrenceInterval: "10s"}}, nil},
		"zero interval":         {redfish.MetricReportDefinitionInput{Type: "Periodic", Metrics: ok.Metrics, Schedule: &redfish.ReportSchedule{RecurrenceInterval: "PT0S"}}, nil},
		"below the minimum":     {redfish.MetricReportDefinitionInput{Type: "Periodic", Metrics: ok.Metrics, Schedule: &redfish.ReportSchedule{RecurrenceInterval: "PT0.5S"}}, redfish.ErrUnsupported},
		"bad function":          {redfish.MetricReportDefinitionInput{Type: "OnRequest", Metrics: []redfish.MetricSpec{{MetricID: "TotalPower", CollectionFunction: "Median"}}}, nil},
		"bad action":            {redfish.MetricReportDefinitionInput{Type: "OnRequest", Metrics: ok.Metrics, ReportActions: []string{"Email"}}, nil},
		"bad updates":           {redfish.MetricReportDefinitionInput{Type: "OnRequest", Metrics: ok.Metrics, ReportUpdates: "Forever"}, nil},
		"append without limit":  {redfish.MetricReportDefinitionInput{Type: "OnRequest", Metrics: ok.Metrics, ReportUpdates: "AppendStopsWhenFull"}, nil},
		"negative limit":        {redfish.MetricReportDefinitionInput{Type: "OnRequest", Metrics: ok.Metrics, AppendLimit: &neg}, nil},
		"bad id":                {redfish.MetricReportDefinitionInput{ID: "a/b", Type: "OnRequest", Metrics: ok.Metrics}, nil},
		"unknown metric id":     {redfish.MetricReportDefinitionInput{Type: "OnRequest", Metrics: []redfish.MetricSpec{{MetricID: "Nope"}}}, redfish.ErrNotFound},
		"bad collection time":   {redfish.MetricReportDefinitionInput{Type: "OnRequest", Metrics: []redfish.MetricSpec{{MetricID: "TotalPower", CollectionTimeScope: "Forever"}}}, nil},
		"bad collection length": {redfish.MetricReportDefinitionInput{Type: "OnRequest", Metrics: []redfish.MetricSpec{{MetricID: "TotalPower", CollectionDuration: "soon"}}}, nil},
		"bad timespan":          {redfish.MetricReportDefinitionInput{Type: "OnRequest", Metrics: ok.Metrics, ReportTimespan: "1h"}, nil},
	}
	for name, tc := range cases {
		n := len(b.Log())
		_, _, err := c.CreateMetricReportDefinition(ctx, tc.in)
		if err == nil || (tc.want != nil && !errors.Is(err, tc.want)) {
			t.Errorf("%s: %v (want %v)", name, err, tc.want)
		}
		for _, l := range b.Log()[n:] {
			if strings.HasPrefix(l, "POST ") {
				t.Errorf("%s: a POST was sent: %s", name, l)
			}
		}
	}
	// The functions the service supports are the ones it lists.
	if _, err := c.Patch(ctx, telBase, map[string]any{"SupportedCollectionFunctions": []string{"Average"}}, ""); err != nil {
		t.Fatal(err)
	}
	_, _, err := c.CreateMetricReportDefinition(ctx, redfish.MetricReportDefinitionInput{Type: "OnRequest", Metrics: []redfish.MetricSpec{{MetricID: "TotalPower", CollectionFunction: "Maximum"}}})
	if !errors.Is(err, redfish.ErrUnsupported) {
		t.Errorf("a function the service does not list: %v", err)
	}
	// A disabled service takes no definition.
	if _, err := c.Patch(ctx, telBase, map[string]any{"ServiceEnabled": false}, ""); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.CreateMetricReportDefinition(ctx, ok); !errors.Is(err, redfish.ErrUnsupported) {
		t.Errorf("a disabled service: %v", err)
	}
}

// The service's MaxReports is a limit the client respects before it sends, and
// the service enforces when it is asked anyway.
func TestCreateMetricReportDefinitionMaxReports(t *testing.T) {
	b, c := telMock(t, "")
	ctx := context.Background()
	for i := range 6 { // two reports exist, MaxReports is eight
		_, _, err := c.CreateMetricReportDefinition(ctx, redfish.MetricReportDefinitionInput{ID: "R" + strconv.Itoa(i), Type: "OnRequest", Metrics: []redfish.MetricSpec{{MetricID: "TotalPower"}}})
		if err != nil {
			t.Fatalf("definition %d: %v", i, err)
		}
	}
	n := len(b.Log())
	_, _, err := c.CreateMetricReportDefinition(ctx, redfish.MetricReportDefinitionInput{ID: "Over", Type: "OnRequest", Metrics: []redfish.MetricSpec{{MetricID: "TotalPower"}}})
	if !errors.Is(err, redfish.ErrUnsupported) || !strings.Contains(err.Error(), "MaxReports") {
		t.Fatalf("over the limit: %v", err)
	}
	for _, l := range b.Log()[n:] {
		if strings.HasPrefix(l, "POST ") {
			t.Errorf("a POST was sent over the limit: %s", l)
		}
	}
	// A definition that only sends events makes no report in the collection.
	if _, out, err := c.CreateMetricReportDefinition(ctx, redfish.MetricReportDefinitionInput{ID: "EventsOnly", Type: "OnChange", Metrics: []redfish.MetricSpec{{MetricID: "TotalPower"}},
		ReportActions: []string{"RedfishEvent"}}); err != nil || !out.Changed {
		t.Errorf("an events-only definition over the limit: %+v, %v", out, err)
	}
	// An Id in use is the service's 409.
	var se *redfish.StatusError
	_, _, err = c.CreateMetricReportDefinition(ctx, redfish.MetricReportDefinitionInput{ID: "R1", Type: "OnChange", Metrics: []redfish.MetricSpec{{MetricID: "TotalPower"}}, ReportActions: []string{"RedfishEvent"}})
	if !errors.As(err, &se) || se.Code != http.StatusConflict {
		t.Errorf("an Id in use: %v", err)
	}
}

// NewReport keeps one report per generation and the collection within MaxReports.
func TestNewReportUpdates(t *testing.T) {
	b, c := telMock(t, "")
	ctx := context.Background()
	if _, _, err := c.CreateMetricReportDefinition(ctx, redfish.MetricReportDefinitionInput{ID: "Each", Type: "Periodic", Schedule: &redfish.ReportSchedule{RecurrenceInterval: "PT2S"},
		Metrics: []redfish.MetricSpec{{MetricID: "TotalPower"}}, ReportUpdates: "NewReport"}); err != nil {
		t.Fatal(err)
	}
	for range 12 {
		b.TickTelemetry()
	}
	reps, err := c.MetricReports(ctx)
	if err != nil || len(reps) > 8 {
		t.Fatalf("%d reports, %v (MaxReports is 8)", len(reps), err)
	}
	ids := map[string]bool{}
	for _, r := range reps {
		ids[r.ID] = true
	}
	d, err := c.MetricReportDefinition(ctx, "Each")
	if err != nil || !strings.HasPrefix(d.MetricReportURI, telBase+"/MetricReports/Each_") || !ids[d.MetricReportURI[strings.LastIndex(d.MetricReportURI, "/")+1:]] {
		t.Fatalf("definition = %+v, %v; the report link names the latest report", d, err)
	}
}

func TestScheduleMaxOccurrencesDisablesTheDefinition(t *testing.T) {
	b, c := telMock(t, "")
	ctx := context.Background()
	two := int64(2)
	if _, _, err := c.CreateMetricReportDefinition(ctx, redfish.MetricReportDefinitionInput{ID: "Twice", Type: "Periodic",
		Schedule: &redfish.ReportSchedule{RecurrenceInterval: "PT2S", MaxOccurrences: &two}, Metrics: []redfish.MetricSpec{{MetricID: "TotalPower"}}, ReportUpdates: "AppendWrapsWhenFull",
		AppendLimit: &two}); err != nil {
		t.Fatal(err)
	}
	total := 0
	for range 5 {
		total += b.TickTelemetry()
	}
	d, err := c.MetricReportDefinition(ctx, "Twice")
	if err != nil || d.Enabled == nil || *d.Enabled || d.State != "Disabled" {
		t.Fatalf("definition after its occurrences = %+v, %v", d, err)
	}
	if total != 5+2 { // PowerMetrics ticks five times, Twice twice
		t.Errorf("%d reports generated, want 7", total)
	}
}

func TestSubmitTestMetricReport(t *testing.T) {
	forEachTelemetryTree(t, func(t *testing.T, b *mockbmc.BMC, c *redfish.Client) {
		ctx := context.Background()
		out, err := c.SubmitTestMetricReport(ctx, "Probe", []redfish.TestMetricValue{
			{MetricID: "TotalPower", MetricProperty: "/redfish/v1/Chassis/1U/Sensors/TotalPower#/Reading", MetricValue: "123.5", Timestamp: "2026-11-08T12:00:00+01:00"},
			{MetricValue: "N/A"}})
		if err != nil || !out.Changed {
			t.Fatalf("Submit = %+v, %v", out, err)
		}
		got := b.TestMetricReports()
		if len(got) != 1 || got[0]["MetricReportName"] != "Probe" {
			t.Fatalf("the service received %v", got)
		}
		vals, _ := got[0]["GeneratedMetricReportValues"].([]any)
		first, _ := vals[0].(map[string]any)
		if len(vals) != 2 || first["MetricId"] != "TotalPower" || first["MetricValue"] != "123.5" {
			t.Fatalf("values sent = %v", vals)
		}
		for name, tc := range map[string]struct {
			name   string
			values []redfish.TestMetricValue
		}{
			"no name":      {"", []redfish.TestMetricValue{{MetricValue: "1"}}},
			"no values":    {"x", nil},
			"bad time":     {"x", []redfish.TestMetricValue{{MetricValue: "1", Timestamp: "yesterday"}}},
			"newline name": {"a\nb", []redfish.TestMetricValue{{MetricValue: "1"}}},
			"too many":     {"x", make([]redfish.TestMetricValue, redfish.MaxMetricValues+1)},
		} {
			n := len(b.Log())
			if _, err := c.SubmitTestMetricReport(ctx, tc.name, tc.values); err == nil {
				t.Errorf("%s: accepted", name)
			}
			for _, l := range b.Log()[n:] {
				if strings.HasPrefix(l, "POST ") {
					t.Errorf("%s: a POST was sent: %s", name, l)
				}
			}
		}
		// The action absent: ErrUnsupported.
		if _, err := c.Patch(ctx, telBase, map[string]any{"Actions": map[string]any{"#TelemetryService.SubmitTestMetricReport": nil}}, ""); err != nil {
			t.Fatal(err)
		}
		if _, err := c.SubmitTestMetricReport(ctx, "Probe", []redfish.TestMetricValue{{MetricValue: "1"}}); !errors.Is(err, redfish.ErrUnsupported) {
			t.Errorf("no action: %v", err)
		}
	})
}

// A parameter the service's ActionInfo does not list is refused before the POST.
func TestSubmitTestMetricReportChecksTheActionInfo(t *testing.T) {
	b, c := telMock(t, "")
	ctx := context.Background()
	ai := telBase + "/SubmitTestMetricReportActionInfo"
	if _, err := c.Patch(ctx, ai, map[string]any{"Parameters": []any{map[string]any{"Name": "MetricReportName", "Required": true, "DataType": "String"}}}, ""); err != nil {
		t.Fatal(err)
	}
	n := len(b.Log())
	_, err := c.SubmitTestMetricReport(ctx, "Probe", []redfish.TestMetricValue{{MetricValue: "1"}})
	if !errors.Is(err, redfish.ErrUnsupported) {
		t.Fatalf("err = %v", err)
	}
	for _, l := range b.Log()[n:] {
		if strings.HasPrefix(l, "POST ") {
			t.Errorf("a POST was sent: %s", l)
		}
	}
}

// ---- The stream ----

func TestMetricReportsOverTheEventStream(t *testing.T) {
	b, srv := newMock(t, mockbmc.Options{Clock: clock()})
	c := newClient(t, srv, redfish.Config{Session: true})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s, err := c.MetricReportEvents(ctx, redfish.EventsOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	// An Event does not pass the MetricReport filter; the report does.
	submit(t, c, redfish.TestEvent{MessageID: "Contoso.1.0.A"})
	if _, err := c.Patch(ctx, "/redfish/v1/Chassis/1U/Sensors/TotalPower", map[string]any{"Reading": 480}, ""); err != nil {
		t.Fatal(err)
	}
	b.TickTelemetry()
	ev := recv(t, s, 1)[0]
	if ev.Err != nil || !ev.IsMetricReport() || ev.Report == nil || len(ev.Records) != 0 {
		t.Fatalf("event = %+v", ev)
	}
	r := ev.Report
	if r.ID != "PowerMetrics" || r.DefinitionURI != telBase+"/MetricReportDefinitions/PowerMetrics" || r.Context == "" || r.Context != ev.Context && ev.Context != "" {
		t.Fatalf("report = %+v", r)
	}
	series, skipped := r.TimeSeries()
	if len(skipped) != 0 || len(series) != 2 || series[0].Points[len(series[0].Points)-1].Value != 480 {
		t.Fatalf("series = %+v", series)
	}
	// The same decoder as the collection's: the report of the event is the
	// report of the resource.
	rr, err := c.MetricReport(ctx, "PowerMetrics")
	if err != nil || len(rr.Values) != len(r.Values) || rr.Values[len(rr.Values)-1].Raw != r.Values[len(r.Values)-1].Raw {
		t.Fatalf("resource %+v, %v", rr, err)
	}
	// A definition that sends no event, and an OnRequest one, send none.
	before := len(b.Log())
	_ = before
	b.TickTelemetry()
	if ev := recv(t, s, 1)[0]; ev.Report == nil {
		t.Fatalf("second event = %+v", ev)
	}
	select {
	case ev := <-s.C:
		t.Fatalf("an extra event: %+v", ev)
	case <-time.After(300 * time.Millisecond):
	}
}

func TestEventStreamWithoutFilterCarriesBothFormats(t *testing.T) {
	b, srv := newMock(t, mockbmc.Options{Clock: clock()})
	c := newClient(t, srv, redfish.Config{Session: true})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	all, err := c.Events(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	defer all.Close()
	onlyEvents, err := c.Events(ctx, "EventFormatType eq 'Event'")
	if err != nil {
		t.Fatal(err)
	}
	defer onlyEvents.Close()
	submit(t, c, redfish.TestEvent{MessageID: "Contoso.1.0.A"})
	b.TickTelemetry()
	evs := recv(t, all, 2)
	if evs[0].IsMetricReport() || len(evs[0].Records) != 1 || !evs[1].IsMetricReport() || evs[1].Report == nil || len(evs[1].Report.Values) == 0 {
		t.Fatalf("events = %+v", evs)
	}
	if ev := recv(t, onlyEvents, 1)[0]; ev.IsMetricReport() {
		t.Fatalf("the Event stream got %+v", ev)
	}
	select {
	case ev := <-onlyEvents.C:
		t.Fatalf("a metric report passed the Event filter: %+v", ev)
	case <-time.After(300 * time.Millisecond):
	}
}

func TestSubmitTestMetricReportOverTheStream(t *testing.T) {
	_, srv := newMock(t, mockbmc.Options{Clock: clock()})
	c := newClient(t, srv, redfish.Config{Session: true})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s, err := c.MetricReportEvents(ctx, redfish.EventsOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := c.SubmitTestMetricReport(ctx, "Probe", []redfish.TestMetricValue{{MetricID: "TotalPower", MetricValue: "99", Timestamp: "2026-11-08T12:00:00Z"}, {MetricValue: "oops"}}); err != nil {
		t.Fatal(err)
	}
	ev := recv(t, s, 1)[0]
	if ev.Report == nil || ev.Report.ID != "Probe" || len(ev.Report.Values) != 2 {
		t.Fatalf("event = %+v", ev)
	}
	v0, v1 := ev.Report.Values[0], ev.Report.Values[1]
	if f, err := v0.Float(); err != nil || f != 99 || v0.Timestamp == nil || !v0.Timestamp.Equal(time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)) {
		t.Fatalf("value 0 = %+v", v0)
	}
	if _, err := v1.Float(); !errors.Is(err, redfish.ErrNotNumeric) || v1.Raw != "oops" || v1.Number != nil {
		t.Fatalf("value 1 = %+v: a value that is not a number must not become 0", v1)
	}
}

// The service lists its event formats: a stream of metric reports is refused
// when MetricReport is not one of them.
func TestMetricReportEventsRefusals(t *testing.T) {
	_, srv := newMock(t, mockbmc.Options{})
	c := newClient(t, srv, redfish.Config{Session: true})
	ctx := context.Background()
	if _, err := c.Patch(ctx, "/redfish/v1/EventService", map[string]any{"EventFormatTypes": []string{"Event"}}, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := c.MetricReportEvents(ctx, redfish.EventsOptions{}); !errors.Is(err, redfish.ErrUnsupported) {
		t.Errorf("no MetricReport format: %v", err)
	}
	// The emulator tree has no stream.
	_, srv2 := newMock(t, mockbmc.Options{Tree: mockbmc.TreeEmulator})
	c2 := newClient(t, srv2, redfish.Config{})
	if _, err := c2.MetricReportEvents(ctx, redfish.EventsOptions{}); !errors.Is(err, redfish.ErrUnsupported) {
		t.Errorf("emulator: %v", err)
	}
}

// ---- Decoding ----

// The example of DSP0266 1.23.0 12.5.2.2 (Metric report SSE stream).
const specMetricReport = `{
    "@odata.id": "/redfish/v1/TelemetryService/MetricReports/AvgPlatformPowerUsage",
    "@odata.type": "#MetricReport.v1_3_0.MetricReport",
    "Id": "AvgPlatformPowerUsage",
    "Name": "Average Platform Power Usage metric report",
    "MetricReportDefinition": {
        "@odata.id": "/redfish/v1/TelemetryService/MetricReportDefinitions/AvgPlatformPowerUsage"
    },
    "MetricValues": [
        {"MetricId": "AverageConsumedWatts", "MetricValue": "100", "Timestamp": "2016-11-08T12:25:00-05:00", "MetricProperty": "/redfish/v1/Chassis/Tray_1/Power#/0/PowerConsumedWatts"},
        {"MetricId": "AverageConsumedWatts", "MetricValue": "94", "Timestamp": "2016-11-08T13:25:00-05:00", "MetricProperty": "/redfish/v1/Chassis/Tray_1/Power#/0/PowerConsumedWatts"},
        {"MetricId": "AverageConsumedWatts", "MetricValue": "100", "Timestamp": "2016-11-08T14:25:00-05:00", "MetricProperty": "/redfish/v1/Chassis/Tray_1/Power#/0/PowerConsumedWatts"}
    ]
}`

func TestSpecMetricReportExample(t *testing.T) {
	r, err := redfish.ParseMetricReport([]byte(specMetricReport))
	if err != nil {
		t.Fatal(err)
	}
	if r.ID != "AvgPlatformPowerUsage" || r.URI != "/redfish/v1/TelemetryService/MetricReports/AvgPlatformPowerUsage" || len(r.Values) != 3 ||
		r.DefinitionURI != "/redfish/v1/TelemetryService/MetricReportDefinitions/AvgPlatformPowerUsage" {
		t.Fatalf("report = %+v", r)
	}
	series, skipped := r.TimeSeries()
	if len(skipped) != 0 || len(series) != 1 || series[0].MetricID != "AverageConsumedWatts" || len(series[0].Points) != 3 {
		t.Fatalf("series = %+v, skipped %v", series, skipped)
	}
	want := []float64{100, 94, 100}
	for i, p := range series[0].Points {
		if p.Value != want[i] {
			t.Errorf("point %d = %v", i, p)
		}
	}
	if !series[0].Points[0].At.Equal(time.Date(2016, 11, 8, 17, 25, 0, 0, time.UTC)) {
		t.Errorf("first timestamp = %v", series[0].Points[0].At)
	}
	// Through the event parser too.
	ev, err := redfish.ParseEvent([]byte(specMetricReport))
	if err != nil || !ev.IsMetricReport() || ev.Report == nil || len(ev.Report.Values) != 3 {
		t.Fatalf("ParseEvent = %+v, %v", ev, err)
	}
}

func TestParseMetricReportValues(t *testing.T) {
	doc := func(values string) []byte {
		return []byte(`{"@odata.type":"#MetricReport.v1_5_2.MetricReport","Id":"R","Name":"R","Timestamp":"2026-11-08T12:00:00Z","MetricValues":[` + values + `]}`)
	}
	cases := []struct {
		name, value string
		raw         string
		null        bool
		number      *float64
	}{
		{"string number", `"12.5"`, "12.5", false, f64(12.5)},
		{"string integer", `"7"`, "7", false, f64(7)},
		{"negative exponent", `"-1.5e-3"`, "-1.5e-3", false, f64(-0.0015)},
		{"JSON number where a string is expected", `42`, "42", false, f64(42)},
		{"null", `null`, "", true, nil},
		{"absent", ``, "", true, nil},
		{"text", `"N/A"`, "N/A", false, nil},
		{"empty string", `""`, "", false, nil},
		{"boolean string", `"true"`, "true", false, nil},
		{"JSON boolean", `true`, "true", false, nil},
		{"array as JSON string", `"[1,2,3]"`, "[1,2,3]", false, nil},
		{"array", `[1, 2]`, "[1,2]", false, nil},
		{"object", `{"a": 1}`, `{"a":1}`, false, nil},
		{"NaN", `"NaN"`, "NaN", false, nil},
		{"Inf", `"Infinity"`, "Infinity", false, nil},
		{"hex", `"0x10"`, "0x10", false, nil},
		{"leading space", `" 5"`, " 5", false, nil},
		{"leading plus", `"+5"`, "+5", false, nil},
		{"overflow", `"1e999"`, "1e999", false, nil},
		{"leading zero", `"007"`, "007", false, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mv := `{"MetricId":"M","Timestamp":"2026-11-08T12:00:00Z"`
			if tc.value != "" {
				mv += `,"MetricValue":` + tc.value
			}
			r, err := redfish.ParseMetricReport(doc(mv + `}`))
			if err != nil || len(r.Values) != 1 {
				t.Fatalf("%+v, %v", r, err)
			}
			v := r.Values[0]
			if v.Raw != tc.raw || v.Null != tc.null || (v.Number == nil) != (tc.number == nil) || (v.Number != nil && *v.Number != *tc.number) {
				t.Fatalf("value = %+v", v)
			}
			if _, err := v.Float(); (err == nil) != (tc.number != nil) {
				t.Errorf("Float: %v", err)
			}
		})
	}
	t.Run("timestamps", func(t *testing.T) {
		r, err := redfish.ParseMetricReport(doc(`{"MetricValue":"1","Timestamp":"2026-11-08T12:00:00.123456789+05:30"},{"MetricValue":"2","Timestamp":"yesterday"},{"MetricValue":"3","Timestamp":"2026-11-08 12:00:00"},{"MetricValue":"4","Timestamp":null},{"MetricValue":"5"},{"MetricValue":"6","Timestamp":"2026-13-45T25:61:61Z"}`))
		if err != nil || len(r.Values) != 6 {
			t.Fatal(err)
		}
		if r.Values[0].Timestamp == nil || r.Values[0].Timestamp.Nanosecond() != 123456789 {
			t.Errorf("fractional timestamp = %+v", r.Values[0])
		}
		for i := 1; i < 6; i++ {
			if r.Values[i].Timestamp != nil {
				t.Errorf("value %d got the timestamp %v from %q", i, r.Values[i].Timestamp, r.Values[i].TimestampRaw)
			}
		}
		if r.Values[1].TimestampRaw != "yesterday" || r.Values[4].TimestampRaw != "" {
			t.Errorf("raw timestamps = %q %q", r.Values[1].TimestampRaw, r.Values[4].TimestampRaw)
		}
		series, skipped := r.TimeSeries()
		if len(series) != 1 || len(series[0].Points) != 1 || len(skipped) != 5 {
			t.Errorf("series %+v, skipped %d: a value without a time or a number is not in a series", series, len(skipped))
		}
	})
	t.Run("series order and grouping", func(t *testing.T) {
		r, err := redfish.ParseMetricReport(doc(`{"MetricId":"B","MetricValue":"3","Timestamp":"2026-11-08T12:00:03Z"},{"MetricId":"A","MetricProperty":"/p","MetricValue":"1","Timestamp":"2026-11-08T12:00:02Z"},
			{"MetricId":"B","MetricValue":"1","Timestamp":"2026-11-08T12:00:01Z"},{"MetricId":"A","MetricValue":"9","Timestamp":"2026-11-08T12:00:02Z"},{"MetricId":"B","MetricValue":"2","Timestamp":"2026-11-08T12:00:02Z"}`))
		if err != nil {
			t.Fatal(err)
		}
		series, _ := r.TimeSeries()
		if len(series) != 3 || series[0].MetricID != "B" || series[1].MetricID != "A" || series[1].MetricProperty != "/p" || series[2].MetricID != "A" || series[2].MetricProperty != "" {
			t.Fatalf("series = %+v", series)
		}
		var got []float64
		for _, p := range series[0].Points {
			got = append(got, p.Value)
		}
		if !slices.Equal(got, []float64{1, 2, 3}) {
			t.Errorf("B = %v, want sorted by time", got)
		}
	})
	t.Run("not an object", func(t *testing.T) {
		for _, bad := range []string{`null`, `5`, `"x"`, `[]`} {
			if _, err := redfish.ParseMetricReport(doc(bad)); !errors.Is(err, redfish.ErrMalformed) {
				t.Errorf("%s: %v", bad, err)
			}
		}
	})
}

func f64(v float64) *float64 { return &v }

func TestParseMetricReportBounds(t *testing.T) {
	one := `{"MetricId":"M","MetricValue":"1","Timestamp":"2026-11-08T12:00:00Z"}`
	build := func(n int) []byte {
		return []byte(`{"MetricValues":[` + strings.TrimSuffix(strings.Repeat(one+",", n), ",") + `]}`)
	}
	r, err := redfish.ParseMetricReport(build(redfish.MaxMetricValues))
	if err != nil || len(r.Values) != redfish.MaxMetricValues {
		t.Fatalf("at the bound: %d values, %v", len(r.Values), err)
	}
	if _, err := redfish.ParseMetricReport(build(redfish.MaxMetricValues + 1)); !errors.Is(err, redfish.ErrMalformed) {
		t.Errorf("above the bound: %v", err)
	}
	if _, err := redfish.ParseMetricReport(bytes.Repeat([]byte(" "), redfish.MaxMetricReportBytes+1)); !errors.Is(err, redfish.ErrTooLarge) {
		t.Errorf("a document above the bound: %v", err)
	}
	for _, bad := range []string{``, `[]`, `null`, `"x"`, `{`, `{"MetricValues":5}`, `{"MetricValues":{}}`, `{"Id":{}}`} {
		if _, err := redfish.ParseMetricReport([]byte(bad)); err == nil && bad != `{"Id":{}}` {
			t.Errorf("%q accepted", bad)
		}
	}
	// A long text is clipped, not kept whole.
	long := strings.Repeat("x", 100000)
	r, err = redfish.ParseMetricReport([]byte(`{"MetricValues":[{"MetricId":"` + long + `","MetricValue":"` + long + `","MetricProperty":"` + long + `","Timestamp":"` + long + `"}]}`))
	if err != nil || len(r.Values[0].Raw) > 4096 || len(r.Values[0].MetricID) > 4096 || len(r.Values[0].TimestampRaw) > 4096 {
		t.Errorf("long texts: %v", err)
	}
	// An event with too many values is an undecodable event, not a truncated one.
	if _, err := redfish.ParseEvent(append([]byte(`{"@odata.type":"#MetricReport.v1_5_2.MetricReport",`), build(redfish.MaxMetricValues + 1)[1:]...)); err == nil {
		t.Error("an event above the bound was decoded")
	}
}

// ---- The Meter adapter ----

func TestReadingsFromSeriesFeedsAMeter(t *testing.T) {
	t0 := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	energy := &redfish.Series{MetricID: "TotalEnergy", Points: []redfish.Point{{At: t0.Add(20 * time.Second), Value: 10.5}, {At: t0, Value: 10}, {At: t0.Add(10 * time.Second), Value: 10.25}}}
	power := &redfish.Series{MetricID: "TotalPower", Points: []redfish.Point{{At: t0, Value: 400}, {At: t0.Add(5 * time.Second), Value: 420}}}
	rs, err := redfish.ReadingsFromSeries("1U", energy, redfish.UnitKWh, power)
	if err != nil || len(rs) != 4 {
		t.Fatalf("readings = %+v, %v", rs, err)
	}
	if !rs[0].At.Equal(t0) || rs[0].EnergyKWh == nil || *rs[0].EnergyKWh != 10 || rs[0].PowerWatts == nil || *rs[0].PowerWatts != 400 || rs[0].Chassis != "1U" {
		t.Errorf("first reading = %+v", rs[0])
	}
	if rs[1].EnergyKWh != nil || rs[1].PowerWatts == nil || *rs[1].PowerWatts != 420 || !rs[1].At.Equal(t0.Add(5*time.Second)) {
		t.Errorf("second reading = %+v", rs[1])
	}
	// The Meter does with them what it does with the Readings of Read: the counter
	// difference. Fed the mixed readings it reports what it sees: the power-only
	// sample between two counters changes the source, and no energy is made up.
	var m redfish.Meter
	var total float64
	var reasons []string
	for _, r := range rs {
		if iv, ok := m.Observe(r); ok && iv.Valid {
			total += iv.KWh
		} else if ok {
			reasons = append(reasons, iv.Reason)
		}
	}
	if math.Abs(total-0.25) > 1e-12 || !slices.Contains(reasons, redfish.ReasonSource) {
		t.Errorf("mixed: energy = %v kWh, reasons %v", total, reasons)
	}
	rsE, err := redfish.ReadingsFromSeries("1U", energy, redfish.UnitKWh, nil)
	if err != nil {
		t.Fatal(err)
	}
	var m2 redfish.Meter
	total = 0
	for _, r := range rsE {
		if iv, ok := m2.Observe(r); ok && iv.Valid {
			total += iv.KWh
		}
	}
	if math.Abs(total-0.5) > 1e-12 { // the counters: 10 -> 10.25 -> 10.5
		t.Errorf("energy = %v kWh, want 0.5", total)
	}
	// Units.
	for unit, value := range map[redfish.EnergyUnit]float64{redfish.UnitWh: 10500, redfish.UnitJ: 10.5 * 3_600_000} {
		rs, err := redfish.ReadingsFromSeries("1U", &redfish.Series{Points: []redfish.Point{{At: t0, Value: value}}}, unit, nil)
		if err != nil || len(rs) != 1 || math.Abs(*rs[0].EnergyKWh-10.5) > 1e-9 {
			t.Errorf("%s: %+v, %v", unit, rs, err)
		}
	}
	if _, err := redfish.ReadingsFromSeries("1U", energy, "BTU", nil); err == nil {
		t.Error("an unknown unit was accepted")
	}
	for _, v := range []float64{-1, math.NaN(), math.Inf(1)} {
		if _, err := redfish.ReadingsFromSeries("1U", &redfish.Series{Points: []redfish.Point{{At: t0, Value: v}}}, redfish.UnitKWh, nil); err == nil {
			t.Errorf("%v accepted", v)
		}
		if _, err := redfish.ReadingsFromSeries("1U", nil, "", &redfish.Series{Points: []redfish.Point{{At: t0, Value: v}}}); err == nil {
			t.Errorf("power %v accepted", v)
		}
	}
	if rs, err := redfish.ReadingsFromSeries("1U", nil, "", nil); err != nil || len(rs) != 0 {
		t.Errorf("no series: %+v, %v", rs, err)
	}
}

// From the mock end to end: the series of a report and the Reading of Read come
// from the same sensor, and Read itself is not touched by the telemetry.
func TestMetricReportSeriesAgreeWithRead(t *testing.T) {
	b, c := telMock(t, "")
	ctx := context.Background()
	b.TickTelemetry()
	r, err := c.MetricReport(ctx, "PowerMetrics")
	if err != nil {
		t.Fatal(err)
	}
	series, _ := r.TimeSeries()
	var energy, power *redfish.Series
	for i := range series {
		switch series[i].MetricID {
		case "TotalEnergy":
			energy = &series[i]
		case "TotalPower":
			power = &series[i]
		}
	}
	if energy == nil || power == nil {
		t.Fatalf("series = %+v", series)
	}
	rs, err := redfish.ReadingsFromSeries("1U", energy, redfish.UnitKWh, power)
	if err != nil || len(rs) != 2 || *rs[0].EnergyKWh != 325675 || *rs[0].PowerWatts != 374 {
		t.Fatalf("readings = %+v, %v", rs, err)
	}
	read, err := c.Read(ctx)
	if err != nil || read.PowerWatts == nil || *read.PowerWatts != *rs[0].PowerWatts {
		t.Fatalf("Read = %+v, %v", read, err)
	}
}

// ---- Fuzz ----

func FuzzParseMetricNumber(f *testing.F) {
	for _, s := range []string{"0", "-1.5e3", "1e999", "NaN", "0x1", " 1", "", "007", "1.", strings.Repeat("9", 70), "١"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		v, err := redfish.ParseMetricNumber(s)
		if err != nil {
			if v != 0 || !errors.Is(err, redfish.ErrNotNumeric) {
				t.Fatalf("%q: %v, %v", s, v, err)
			}
			return
		}
		if math.IsNaN(v) || math.IsInf(v, 0) || len(s) > 64 || s[0] == '+' || strings.ContainsAny(s, " xX_,") {
			t.Fatalf("%q accepted as %v", s, v)
		}
	})
}

func FuzzParseRedfishDuration(f *testing.F) {
	for _, s := range []string{"PT1S", "P1DT2H3M4.5S", "PT", "P", "PT0.25S", "P99999999D", "PT1.0000000001S", "1S", "PT1M1S"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		d, err := redfish.ParseRedfishDuration(s)
		if err != nil {
			return
		}
		if d < 0 || d > 100*365*24*time.Hour+time.Second || !strings.HasPrefix(s, "P") {
			t.Fatalf("%q = %v", s, d)
		}
	})
}

func FuzzParseMetricReport(f *testing.F) {
	f.Add([]byte(specMetricReport))
	f.Add([]byte(`{"MetricValues":[{"MetricValue":null},{"MetricValue":1e400},{"MetricValue":"1e400","Timestamp":"0000-00-00T00:00:00Z"},{"MetricValue":[1],"Timestamp":5}]}`))
	f.Add([]byte(`{"Id":1.5,"MetricValues":[{"MetricId":null,"MetricValue":{"a":1},"Timestamp":"2026-11-08T12:00:00.9999999999Z"}]}`))
	f.Add([]byte(`{"MetricValues":[null]}`))
	f.Add([]byte(`{"MetricValues":[` + strings.Repeat(`{"MetricValue":"1","Timestamp":"2026-11-08T12:00:00Z"},`, 300) + `{}]}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		r, err := redfish.ParseMetricReport(data)
		if err != nil {
			return
		}
		if len(r.Values) > redfish.MaxMetricValues {
			t.Fatalf("%d values", len(r.Values))
		}
		series, skipped := r.TimeSeries()
		n := 0
		for _, s := range series {
			for i, p := range s.Points {
				n++
				if math.IsNaN(p.Value) || math.IsInf(p.Value, 0) || (i > 0 && p.At.Before(s.Points[i-1].At)) {
					t.Fatalf("bad series %+v", s)
				}
			}
		}
		if n+len(skipped) != len(r.Values) {
			t.Fatalf("%d points + %d skipped != %d values", n, len(skipped), len(r.Values))
		}
		for _, v := range r.Values {
			if v.Number != nil && (v.Null || math.IsNaN(*v.Number) || math.IsInf(*v.Number, 0)) {
				t.Fatalf("a number on %+v", v)
			}
			if _, err := v.Float(); (err == nil) != (v.Number != nil) {
				t.Fatalf("Float disagrees with Number on %+v", v)
			}
		}
	})
}

func FuzzParseTelemetryResources(f *testing.F) {
	f.Add([]byte(`{"Id":"T","Name":"T","MaxReports":8,"MinCollectionInterval":"PT1S","SupportedCollectionFunctions":["Average",null],"Actions":{"#TelemetryService.SubmitTestMetricReport":{"target":"/x"}},"MetricReports":{"@odata.id":"/redfish/v1/x"}}`))
	f.Add([]byte(`{"Id":"D","MetricReportDefinitionType":"Periodic","Schedule":{"RecurrenceInterval":"PT10S","MaxOccurrences":"x"},"Metrics":[{"MetricId":5,"MetricProperties":[1,null]},null],"Wildcards":[{"Name":"X","Values":[1]}],"Links":{"Triggers":[{"@odata.id":"/redfish/v1/t"},5]}}`))
	f.Add([]byte(`{"Id":"M","MetricProperties":["/a#/b"],"MinReadingRange":1e999,"Wildcards":[null]}`))
	f.Add([]byte(`{"Id":"G","NumericThresholds":{"UpperCritical":{"Reading":"x","Activation":5}},"DiscreteTriggers":[1,2],"MetricIds":[null]}`))
	f.Add([]byte(`[]`))
	f.Fuzz(func(t *testing.T, data []byte) {
		if ts, err := redfish.ParseTelemetryService("/redfish/v1/TelemetryService", data); err == nil && ts.MinInterval != nil && *ts.MinInterval < 0 {
			t.Fatalf("a negative interval: %+v", ts)
		}
		if d, err := redfish.ParseMetricReportDefinition("/redfish/v1/x/D", data); err == nil && (d.Interval != nil && *d.Interval < 0 || len(d.Metrics) > 256) {
			t.Fatalf("definition = %+v", d)
		}
		if d, err := redfish.ParseMetricDefinition("/redfish/v1/x/M", data); err == nil && (d.MinReadingRange != nil && math.IsInf(*d.MinReadingRange, 0)) {
			t.Fatalf("definition = %+v", d)
		}
		if tr, err := redfish.ParseTrigger("/redfish/v1/x/G", data); err == nil {
			for _, th := range tr.NumericThresholds {
				if th.Reading != nil && (math.IsNaN(*th.Reading) || math.IsInf(*th.Reading, 0)) {
					t.Fatalf("trigger = %+v", tr)
				}
			}
		}
	})
}
