package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/go-redfish/redfish/mockbmc"
)

func TestTelemetryReadCommands(t *testing.T) {
	forEachTree(t, func(t *testing.T, e *env) {
		out, _, err := e.run("telemetry", "status")
		if err != nil {
			t.Fatal(err)
		}
		var st map[string]any
		if err := json.Unmarshal([]byte(out), &st); err != nil || st["max_reports"] != float64(8) || st["min_collection_interval"] != "PT1S" || st["submit_test_metric_report"] != true {
			t.Fatalf("status = %v (%v)", out, err)
		}
		for cmd, want := range map[string]int{"definitions": 3, "report-definitions": 2, "reports": 2, "triggers": 1} {
			out, _, err := e.run("telemetry", cmd)
			if err != nil {
				t.Fatalf("%s: %v", cmd, err)
			}
			if l := jsonList(t, out); len(l) != want {
				t.Errorf("%s: %d items, want %d: %s", cmd, len(l), want, out)
			}
		}
		out, _, err = e.run("telemetry", "definitions", "TotalPower")
		if err != nil || !strings.Contains(out, `"units": "W"`) {
			t.Fatalf("definitions TotalPower: %v %s", err, out)
		}
		out, _, err = e.run("telemetry", "report-definitions", "PowerMetrics")
		if err != nil || !strings.Contains(out, `"type": "Periodic"`) || !strings.Contains(out, `"recurrence_interval": "PT10S"`) {
			t.Fatalf("report-definitions PowerMetrics: %v %s", err, out)
		}
		out, _, err = e.run("telemetry", "report", "PowerMetrics")
		if err != nil || !strings.Contains(out, `"raw": "374"`) || !strings.Contains(out, `"number": 374`) {
			t.Fatalf("report PowerMetrics: %v %s", err, out)
		}
		if _, _, err := e.run("telemetry", "report", "Nope"); err == nil {
			t.Error("a report that does not exist")
		}
		if _, _, err := e.run("telemetry", "report", "../Nope"); err == nil {
			t.Error("a report named by a path outside the collection")
		}
	})
}

func TestTelemetryReportTable(t *testing.T) {
	e := newEnvOpts(t, mockbmc.Options{Now: tick()})
	for _, r := range []any{410, 455.5} {
		body, _ := json.Marshal(map[string]any{"Reading": r})
		if _, _, err := e.run("raw", "patch", "/redfish/v1/Chassis/1U/Sensors/TotalPower", "--data", string(body)); err != nil {
			t.Fatal(err)
		}
		e.bmc.TickTelemetry()
	}
	out, _, err := e.run("-o", "table", "telemetry", "report", "PowerMetrics")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 1+6 || !strings.HasPrefix(lines[0], "METRIC") || !strings.Contains(lines[0], "TIME") || !strings.Contains(lines[0], "VALUE") {
		t.Fatalf("table:\n%s", out)
	}
	var power []string
	for _, l := range lines[1:] {
		f := strings.Fields(l)
		if f[0] == "TotalPower" {
			power = append(power, f[len(f)-1])
		}
	}
	if strings.Join(power, ",") != "374,410,455.5" {
		t.Errorf("power column = %v\n%s", power, out)
	}
	// A time series: sorted by time, the times are the mock's clock.
	if !strings.Contains(lines[1], "2026-11-08T12:") {
		t.Errorf("first row: %s", lines[1])
	}
	// The list in a table is a row per report.
	out, _, err = e.run("-o", "table", "telemetry", "reports")
	if err != nil || !strings.Contains(out, "PowerMetrics") || !strings.Contains(out, "CPUTemperature") {
		t.Fatalf("reports table: %v\n%s", err, out)
	}
}

// A value that is null or not a number is shown as it is, never as 0.
func TestTelemetryReportTableNeverShowsNonNumbersAsZero(t *testing.T) {
	e := newEnvOpts(t, mockbmc.Options{Now: tick()})
	for _, body := range []string{`{"Reading":"N/A"}`, `{"Reading":null}`} {
		if _, _, err := e.run("raw", "patch", "/redfish/v1/Chassis/1U/Sensors/TotalPower", "--data", body); err != nil {
			t.Fatal(err)
		}
		e.bmc.TickTelemetry()
	}
	out, _, err := e.run("-o", "table", "telemetry", "report", "PowerMetrics")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"N/A" (not a number)`) || !strings.Contains(out, "null") {
		t.Fatalf("table:\n%s", out)
	}
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, "TotalPower") && strings.HasSuffix(strings.TrimSpace(l), " 0") {
			t.Errorf("a non-number shown as 0: %s", l)
		}
	}
	js, _, err := e.run("telemetry", "report", "PowerMetrics")
	if err != nil || !strings.Contains(js, `"raw": "N/A"`) || !strings.Contains(js, `"null": true`) {
		t.Fatalf("json: %v\n%s", err, js)
	}
}

func tick() func() time.Time {
	t := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	return func() time.Time {
		t = t.Add(10 * time.Second)
		return t
	}
}

func TestTelemetryCreateAndDeleteCommands(t *testing.T) {
	forEachTree(t, func(t *testing.T, e *env) {
		out, _, err := e.run("telemetry", "create-definition", "--id", "Temps", "--name", "Temperatures", "--type", "Periodic", "--interval", "PT5S",
			"--metric", "CPU1Temp", "--function", "Maximum", "--action", "LogToMetricReportsCollection", "--updates", "AppendWrapsWhenFull", "--append-limit", "4")
		if err != nil {
			t.Fatal(err)
		}
		var r struct {
			Changed    bool
			Definition struct {
				URI, ID, Type string
				Metrics       []struct {
					Metric_id, Collection_function string
				}
				Append_limit int
			}
		}
		if err := json.Unmarshal([]byte(out), &r); err != nil || !r.Changed || r.Definition.ID != "Temps" || r.Definition.Type != "Periodic" || r.Definition.Append_limit != 4 ||
			len(r.Definition.Metrics) != 1 || r.Definition.Metrics[0].Metric_id != "CPU1Temp" || r.Definition.Metrics[0].Collection_function != "Maximum" {
			t.Fatalf("create = %s (%v)", out, err)
		}
		e.bmc.TickTelemetry()
		if out, _, err := e.run("telemetry", "report", "Temps"); err != nil || !strings.Contains(out, "CPU1Temp") {
			t.Fatalf("report of the new definition: %v %s", err, out)
		}
		out, _, err = e.run("telemetry", "create-definition", "--type", "OnRequest", "--property", "/redfish/v1/Chassis/1U/Sensors/TotalPower#/Reading", "--id", "ByProperty")
		if err != nil || !strings.Contains(out, `"changed": true`) {
			t.Fatalf("by property: %v %s", err, out)
		}
		out, _, err = e.run("telemetry", "delete-definition", "Temps")
		if err != nil || !strings.Contains(out, `"changed": true`) {
			t.Fatalf("delete: %v %s", err, out)
		}
		out, _, err = e.run("telemetry", "delete-definition", "Temps")
		if err != nil || !strings.Contains(out, `"changed": false`) {
			t.Fatalf("second delete: %v %s", err, out)
		}
	})
}

func TestTelemetryCreateRefusals(t *testing.T) {
	e := newEnv(t)
	for name, args := range map[string][]string{
		"no type":          {"create-definition", "--metric", "TotalPower"},
		"no metrics":       {"create-definition", "--type", "OnRequest"},
		"periodic":         {"create-definition", "--type", "Periodic", "--metric", "TotalPower"},
		"too fast":         {"create-definition", "--type", "Periodic", "--interval", "PT0.1S", "--metric", "TotalPower"},
		"unknown metric":   {"create-definition", "--type", "OnRequest", "--metric", "Nope"},
		"bad function":     {"create-definition", "--type", "OnRequest", "--metric", "TotalPower", "--function", "Median"},
		"id and property":  {"create-definition", "--type", "OnRequest", "--metric", "TotalPower", "--property", "/redfish/v1/x#/y", "--id", "x"},
		"append no limit":  {"create-definition", "--type", "OnRequest", "--metric", "TotalPower", "--updates", "AppendStopsWhenFull"},
		"test without it":  {"test", "--name", "x"},
		"test bad time":    {"test", "--name", "x", "--value", "1", "--timestamp", "soon"},
		"test no name":     {"test", "--value", "1"},
		"delete a non-id":  {"delete-definition", "a/b"},
		"report with path": {"report", "/redfish/v1/Chassis"},
	} {
		n := len(e.bmc.Log())
		if _, _, err := e.run(append([]string{"telemetry"}, args...)...); err == nil {
			t.Errorf("%s: accepted", name)
		}
		for _, l := range e.bmc.Log()[n:] {
			if strings.HasPrefix(l, "POST ") || strings.HasPrefix(l, "DELETE ") {
				t.Errorf("%s: sent %s", name, l)
			}
		}
	}
}

func TestTelemetryTestCommand(t *testing.T) {
	e := newEnv(t)
	out, _, err := e.run("telemetry", "test", "--name", "Probe", "--value", "12.5", "--value", "13", "--metric-id", "TotalPower", "--timestamp", "2026-11-08T12:00:00Z")
	if err != nil || !strings.Contains(out, `"changed": true`) {
		t.Fatalf("test: %v %s", err, out)
	}
	got := e.bmc.TestMetricReports()
	if len(got) != 1 || got[0]["MetricReportName"] != "Probe" || len(got[0]["GeneratedMetricReportValues"].([]any)) != 2 {
		t.Fatalf("the service received %v", got)
	}
}

func TestTelemetryWatch(t *testing.T) {
	e := newEnvOpts(t, mockbmc.Options{Now: tick()})
	var out syncBuffer
	cmd := RootCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(append(append([]string{}, e.baseArgs...), "-o", "table", "telemetry", "watch", "--count", "2"))
	done := make(chan error, 1)
	go func() { done <- cmd.Execute() }()
	waitFor(t, func() bool { return sseUp(e) })
	if _, _, err := e.run("events", "test", "--message-id", "Contoso.1.0.Skipped"); err != nil { // an Event: not shown
		t.Fatal(err)
	}
	e.bmc.TickTelemetry()
	e.bmc.TickTelemetry()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("watch did not end after --count reports")
	}
	s := out.String()
	if strings.Count(s, "METRIC") != 2 || !strings.Contains(s, "# ") || !strings.Contains(s, "TotalPower") || strings.Contains(s, "Skipped") {
		t.Fatalf("output:\n%s", s)
	}
	// events listen prints the same time series for a metric report.
	var out2 syncBuffer
	cmd2 := RootCmd()
	cmd2.SetOut(&out2)
	cmd2.SetErr(&out2)
	cmd2.SetArgs(append(append([]string{}, e.baseArgs...), "events", "listen", "--count", "1", "--filter", "EventFormatType eq 'MetricReport'"))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	done2 := make(chan error, 1)
	go func() { done2 <- cmd2.ExecuteContext(ctx) }()
	waitFor(t, func() bool {
		return strings.Contains(string(e.bmc.Resource("/redfish/v1/EventService/Subscriptions/6")), `"SSE"`) || strings.Contains(string(e.bmc.Resource("/redfish/v1/EventService/Subscriptions/5")), `"SSE"`)
	})
	e.bmc.TickTelemetry()
	if err := <-done2; err != nil {
		t.Fatal(err)
	}
	var l struct {
		Event struct {
			ODataType string `json:"@odata.type"`
		} `json:"event"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out2.String())), &l); err != nil || !strings.HasPrefix(l.Event.ODataType, "#MetricReport.") {
		t.Fatalf("events listen printed %q (%v)", out2.String(), err)
	}
}

func TestTelemetryUnsupportedOnAServiceWithoutIt(t *testing.T) {
	e := newEnv(t)
	if _, _, err := e.run("raw", "patch", "/redfish/v1/", "--data", `{"TelemetryService":null}`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.run("telemetry", "status"); err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Errorf("status: %v", err)
	}
}
