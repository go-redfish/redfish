package redfish

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"path"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Telemetry: the TelemetryService, its metric definitions, metric report
// definitions, metric reports and triggers. There is no Ansible oracle for this
// area: the reference is the specification, read as published. DSP0266 1.23.0
// (2025-09-05): 12.1.6 Event formats (Table 38: the metric report message
// object), 12.5.2 Event service (the metric report SSE stream), 12.5.2.2 Metric
// report SSE stream. DSP8010 2025.4 schemas (redfish.dmtf.org/schemas/v1):
// TelemetryService 1.4.1, MetricDefinition 1.3.6, MetricReportDefinition 1.4.7,
// MetricReport 1.5.2, Triggers 1.4.0 and Schedule 1.2.5.
//
// What the schemas say, and the code follows:
//
//   - MetricValue.MetricValue is a string, or null (MetricReport 1.5.2): "For
//     numeric metrics, the service shall convert the number to a string
//     representation of the number. For array metrics, the service shall convert
//     the array to an RFC8259-defined JSON string. For boolean metrics, this
//     property shall contain the strings true or false. If the metric value is
//     null, this property shall contain null." A value is therefore kept as the
//     string the service sent (Raw), a null is Null, and a number is decoded only
//     when the string is a JSON number (RFC 8259 6) that fits a float64: a value
//     that is not one has no Number and is never read as 0.
//   - A metric report is a time series by its timestamps: "A management
//     application can establish a time series of metric data by retrieving the
//     instances of metric value and sorting them according to their timestamp"
//     (MetricValue.Timestamp); see MetricReport.TimeSeries.
//   - OnRequest (MetricReportDefinitionType): "The metric report is generated when
//     an HTTP GET is performed on the specified metric report". The schema has no
//     action that generates a report on demand: a GET of the MetricReport is it.
//   - SubmitTestMetricReport is an action of TelemetryService 1.4.1; its
//     parameters (MetricReportName, GeneratedMetricReportValues) are checked
//     against the ActionInfo or the AllowableValues of the service like every
//     action (DSP0266 9.9.2, 9.9.6, see ActionValues).

// Bounds of the telemetry reads.
const (
	// MaxTelemetryMembers bounds the members read of a telemetry collection.
	MaxTelemetryMembers = 256
	// MaxMetricValues bounds the MetricValues of one metric report, and the
	// values of a test report.
	MaxMetricValues = 4096
	// MaxMetricReportBytes bounds a metric report document.
	MaxMetricReportBytes = MaxEventBytes
	// maxMetricNumber bounds the length of a string read as a number.
	maxMetricNumber = 64
	// maxMetricRaw bounds the length of a metric value kept.
	maxMetricRaw = 4096
)

// ErrNotNumeric: a metric value is null, or its string is not a number.
var ErrNotNumeric = errors.New("redfish: the metric value is not a number")

// ParseMetricNumber reads a metric value string as a number, strictly: the
// grammar of a JSON number (RFC 8259 6: an optional minus, an integer part
// without a leading zero, an optional fraction, an optional exponent), at most 64
// bytes, and a finite float64. A space, a plus sign, a hexadecimal, "NaN" or
// "Inf", an empty string, or a number out of range is an error wrapping
// ErrNotNumeric: nothing is guessed and nothing becomes 0.
func ParseMetricNumber(s string) (float64, error) {
	if !jsonNumber(s) {
		return 0, fmt.Errorf("%w: %q", ErrNotNumeric, clip(s))
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, fmt.Errorf("%w: %q is out of range", ErrNotNumeric, clip(s))
	}
	return v, nil
}

// jsonNumber reports whether s is a number of the JSON grammar.
func jsonNumber(s string) bool {
	if len(s) == 0 || len(s) > maxMetricNumber {
		return false
	}
	i := 0
	if s[i] == '-' {
		i++
	}
	digits := func() int {
		n := 0
		for i < len(s) && s[i] >= '0' && s[i] <= '9' {
			i++
			n++
		}
		return n
	}
	switch {
	case i < len(s) && s[i] == '0':
		i++
	case i < len(s) && s[i] >= '1' && s[i] <= '9':
		digits()
	default:
		return false
	}
	if i < len(s) && s[i] == '.' {
		i++
		if digits() == 0 {
			return false
		}
	}
	if i < len(s) && (s[i] == 'e' || s[i] == 'E') {
		i++
		if i < len(s) && (s[i] == '+' || s[i] == '-') {
			i++
		}
		if digits() == 0 {
			return false
		}
	}
	return i == len(s)
}

var durationPart = regexp.MustCompile(`^(\d{1,9})([DHMS])`)

// maxDuration is the longest duration read: 100 years.
const maxDuration = 100 * 365 * 24 * time.Hour

// ParseRedfishDuration reads a Redfish duration, the ISO 8601 form the schemas
// pattern as ^P(\d+D)?(T(\d+H)?(\d+M)?(\d+(\.\d+)?S)?)?$ (MinCollectionInterval,
// RecurrenceInterval, SensingInterval...). At least one component is required
// (the pattern also matches "P" and "PT", which name no duration), each is of at
// most 9 digits, the seconds may have up to 9 fraction digits, and the total is
// at most 100 years.
func ParseRedfishDuration(s string) (time.Duration, error) {
	bad := func() (time.Duration, error) {
		return 0, fmt.Errorf("redfish: %q is not a duration (PnDTnHnMnS)", clip(s))
	}
	if len(s) < 3 || len(s) > 64 || s[0] != 'P' {
		return bad()
	}
	rest := s[1:]
	var secs float64 // exact enough: each part is below 1e9, the total is bounded below
	var nanos int64
	seen := 0 // 0 date part, 1 after T
	last := map[byte]bool{}
	order := map[byte]int{'D': 0, 'H': 1, 'M': 2, 'S': 3}
	prev := -1
	components := 0
	for rest != "" {
		if rest[0] == 'T' {
			if seen == 1 {
				return bad()
			}
			seen = 1
			rest = rest[1:]
			if rest == "" {
				return bad()
			}
			continue
		}
		m := durationPart.FindStringSubmatch(rest)
		if m == nil {
			// seconds with a fraction: n.nS
			dot := strings.IndexByte(rest, '.')
			end := strings.IndexByte(rest, 'S')
			if seen != 1 || dot < 1 || end < dot+2 || end-dot-1 > 9 || last['S'] || prev >= order['S'] {
				return bad()
			}
			whole, err1 := strconv.ParseUint(rest[:dot], 10, 32)
			frac, err2 := strconv.ParseUint(rest[dot+1:end], 10, 32)
			if err1 != nil || err2 != nil || dot > 9 {
				return bad()
			}
			secs += float64(whole)
			nanos += int64(frac) * int64(math.Pow10(9-(end-dot-1)))
			last['S'], prev = true, order['S']
			components++
			rest = rest[end+1:]
			continue
		}
		unit := m[2][0]
		if (unit == 'D') != (seen == 0) || last[unit] || order[unit] <= prev {
			return bad()
		}
		n, _ := strconv.ParseUint(m[1], 10, 32)
		switch unit {
		case 'D':
			secs += float64(n) * 86400
		case 'H':
			secs += float64(n) * 3600
		case 'M':
			secs += float64(n) * 60
		case 'S':
			secs += float64(n)
		}
		last[unit], prev = true, order[unit]
		components++
		rest = rest[len(m[0]):]
	}
	if components == 0 || secs > maxDuration.Seconds() {
		return bad()
	}
	return time.Duration(secs)*time.Second + time.Duration(nanos), nil
}

// ---- Resources ----

// Wildcard is a wildcard of a MetricProperties URI and the values that replace
// it (`*` stands for all the possible values).
type Wildcard struct {
	Name   string   `json:"name"`
	Values []string `json:"values,omitempty"`
}

func wildcardsOf(p Props) []Wildcard {
	l, _ := p["Wildcards"].([]any)
	var out []Wildcard
	for _, x := range l {
		m, _ := x.(map[string]any)
		if m == nil || len(out) >= maxParams {
			continue
		}
		w := Wildcard{Name: str(m, "Name"), Values: stringList(m["Values"])}
		if len(w.Values) == 0 {
			w.Values = stringList(m["Keys"]) // deprecated since MetricReportDefinition 1.1
		}
		out = append(out, w)
	}
	return out
}

func numOf(p Props, key string) *float64 {
	switch v := p[key].(type) {
	case json.Number:
		if f, err := v.Float64(); err == nil && !math.IsInf(f, 0) {
			return &f
		}
	case float64:
		if !math.IsInf(v, 0) && !math.IsNaN(v) {
			return &v
		}
	}
	return nil
}

// TelemetryService is the TelemetryService resource (TelemetryService 1.4.1).
type TelemetryService struct {
	URI string `json:"uri"`
	// ServiceEnabled is nil when the service does not say (TelemetryService 1.2).
	ServiceEnabled *bool  `json:"service_enabled,omitempty"`
	State          string `json:"state,omitempty"`
	Health         string `json:"health,omitempty"`
	// SupportedCollectionFunctions are the functions a metric of a report
	// definition may apply (Average, Maximum, Minimum, Summation).
	SupportedCollectionFunctions []string `json:"supported_collection_functions,omitempty"`
	// MinCollectionInterval is the shortest interval between two gatherings the
	// service allows, as the service wrote it; MinInterval is it as a duration, nil
	// when absent or not a duration.
	MinCollectionInterval string         `json:"min_collection_interval,omitempty"`
	MinInterval           *time.Duration `json:"-"`
	// MaxReports is the number of metric reports the service supports.
	MaxReports *int64 `json:"max_reports,omitempty"`
	// The collections and the log service the resource links; empty when absent.
	MetricDefinitionsURI       string `json:"metric_definitions_uri,omitempty"`
	MetricReportDefinitionsURI string `json:"metric_report_definitions_uri,omitempty"`
	MetricReportsURI           string `json:"metric_reports_uri,omitempty"`
	TriggersURI                string `json:"triggers_uri,omitempty"`
	LogServiceURI              string `json:"log_service_uri,omitempty"`
	// SubmitTestMetricReport is true when the service has the action.
	SubmitTestMetricReport bool `json:"submit_test_metric_report"`
}

// ParseTelemetryService decodes a TelemetryService resource read at uri.
func ParseTelemetryService(uri string, data []byte) (TelemetryService, error) {
	p, err := decodeProps(data)
	if err != nil {
		return TelemetryService{}, fmt.Errorf("%w: %v", ErrMalformed, err)
	}
	return parseTelemetryService(uri, p), nil
}

func parseTelemetryService(uri string, p Props) TelemetryService {
	t := TelemetryService{URI: uri, ServiceEnabled: boolOf(p, "ServiceEnabled"),
		State: str(sub(p, "Status"), "State"), Health: str(sub(p, "Status"), "Health"),
		SupportedCollectionFunctions: stringList(p["SupportedCollectionFunctions"]),
		MinCollectionInterval:        str(p, "MinCollectionInterval"), MaxReports: intOf(p, "MaxReports"),
		MetricDefinitionsURI: linkOf(p, "MetricDefinitions"), MetricReportDefinitionsURI: linkOf(p, "MetricReportDefinitions"),
		MetricReportsURI: linkOf(p, "MetricReports"), TriggersURI: linkOf(p, "Triggers"), LogServiceURI: linkOf(p, "LogService")}
	if d, err := ParseRedfishDuration(t.MinCollectionInterval); err == nil {
		t.MinInterval = &d
	}
	_, t.SubmitTestMetricReport = sub(p, "Actions")["#TelemetryService.SubmitTestMetricReport"].(map[string]any)
	return t
}

// MetricDefinition is a MetricDefinition resource (MetricDefinition 1.3.6): the
// metadata of a metric.
type MetricDefinition struct {
	URI  string `json:"uri"`
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
	// MetricType is Numeric, Discrete, Gauge, Counter, Countdown or String;
	// MetricDataType Boolean, DateTime, Decimal, Integer, String or Enumeration;
	// Implementation PhysicalSensor, Calculated, Synthesized or DigitalMeter.
	MetricType     string `json:"metric_type,omitempty"`
	MetricDataType string `json:"metric_data_type,omitempty"`
	Implementation string `json:"implementation,omitempty"`
	// Units follow the Unified Code for Units of Measure (MetricDefinition.Units).
	Units            string     `json:"units,omitempty"`
	PhysicalContext  string     `json:"physical_context,omitempty"`
	MetricProperties []string   `json:"metric_properties,omitempty"`
	Wildcards        []Wildcard `json:"wildcards,omitempty"`
	SensingInterval  string     `json:"sensing_interval,omitempty"`
	DiscreteValues   []string   `json:"discrete_values,omitempty"`
	MinReadingRange  *float64   `json:"min_reading_range,omitempty"`
	MaxReadingRange  *float64   `json:"max_reading_range,omitempty"`
	// CalculationAlgorithm is set for a Calculated or Synthesized metric.
	CalculationAlgorithm string `json:"calculation_algorithm,omitempty"`
}

// ParseMetricDefinition decodes a MetricDefinition resource read at uri.
func ParseMetricDefinition(uri string, data []byte) (MetricDefinition, error) {
	p, err := decodeProps(data)
	if err != nil {
		return MetricDefinition{}, fmt.Errorf("%w: %v", ErrMalformed, err)
	}
	return parseMetricDefinition(uri, p), nil
}

func parseMetricDefinition(uri string, p Props) MetricDefinition {
	d := MetricDefinition{URI: uri, ID: str(p, "Id"), Name: str(p, "Name"), MetricType: str(p, "MetricType"),
		MetricDataType: str(p, "MetricDataType"), Implementation: str(p, "Implementation"), Units: str(p, "Units"),
		PhysicalContext: str(p, "PhysicalContext"), MetricProperties: stringList(p["MetricProperties"]), Wildcards: wildcardsOf(p),
		SensingInterval: str(p, "SensingInterval"), DiscreteValues: stringList(p["DiscreteValues"]),
		MinReadingRange: numOf(p, "MinReadingRange"), MaxReadingRange: numOf(p, "MaxReadingRange"),
		CalculationAlgorithm: str(p, "CalculationAlgorithm")}
	if d.ID == "" {
		d.ID = path.Base(uri)
	}
	return d
}

// ReportSchedule is the Schedule of a metric report definition (Schedule 1.2.5).
type ReportSchedule struct {
	// RecurrenceInterval is the duration between two reports (PT10S).
	RecurrenceInterval string `json:"recurrence_interval,omitempty"`
	InitialStartTime   string `json:"initial_start_time,omitempty"`
	MaxOccurrences     *int64 `json:"max_occurrences,omitempty"`
	Lifetime           string `json:"lifetime,omitempty"`
	Name               string `json:"name,omitempty"`
}

// MetricSpec is one entry of the Metrics of a metric report definition
// (MetricReportDefinition 1.4.7 Metric): the metrics of a MetricDefinition
// (MetricID) or properties named by URI (MetricProperties), the one or the
// other ("should not be present" with the other), and the calculation applied.
type MetricSpec struct {
	MetricID         string   `json:"metric_id,omitempty"`
	MetricProperties []string `json:"metric_properties,omitempty"`
	// CollectionFunction is Average, Maximum, Minimum or Summation; empty: no
	// calculation. CollectionTimeScope is Point, Interval or StartupInterval and
	// CollectionDuration the duration the function covers.
	CollectionFunction  string `json:"collection_function,omitempty"`
	CollectionTimeScope string `json:"collection_time_scope,omitempty"`
	CollectionDuration  string `json:"collection_duration,omitempty"`
}

// MetricReportDefinition is a MetricReportDefinition resource
// (MetricReportDefinition 1.4.7): which metrics are collected into a report, and
// when.
type MetricReportDefinition struct {
	URI  string `json:"uri"`
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
	// Type is Periodic, OnChange or OnRequest (MetricReportDefinitionType); a
	// Periodic definition has a Schedule.
	Type string `json:"type,omitempty"`
	// Enabled is nil when the service does not say (MetricReportDefinition 1.2).
	Enabled  *bool           `json:"enabled,omitempty"`
	State    string          `json:"state,omitempty"`
	Health   string          `json:"health,omitempty"`
	Schedule *ReportSchedule `json:"schedule,omitempty"`
	// Interval is the Schedule's RecurrenceInterval as a duration, nil when there
	// is none or it is not a duration.
	Interval *time.Duration `json:"-"`
	Metrics  []MetricSpec   `json:"metrics,omitempty"`
	// MetricProperties and Wildcards are the definition-level properties of the
	// schema, alongside Metrics.
	MetricProperties []string   `json:"metric_properties,omitempty"`
	Wildcards        []Wildcard `json:"wildcards,omitempty"`
	// ReportActions are LogToMetricReportsCollection and RedfishEvent: what is done
	// with a report when it is generated.
	ReportActions []string `json:"report_actions,omitempty"`
	// ReportUpdates is Overwrite, AppendWrapsWhenFull, AppendStopsWhenFull or
	// NewReport; AppendLimit is the entries an appended report holds.
	ReportUpdates               string `json:"report_updates,omitempty"`
	AppendLimit                 *int64 `json:"append_limit,omitempty"`
	ReportTimespan              string `json:"report_timespan,omitempty"`
	SuppressRepeatedMetricValue *bool  `json:"suppress_repeated_metric_value,omitempty"`
	HeartbeatInterval           string `json:"heartbeat_interval,omitempty"`
	// MetricReportURI is the most recent report the definition produced.
	MetricReportURI string `json:"metric_report_uri,omitempty"`
	// TriggerURIs are Links.Triggers.
	TriggerURIs []string `json:"trigger_uris,omitempty"`
}

// ParseMetricReportDefinition decodes a MetricReportDefinition resource read at
// uri.
func ParseMetricReportDefinition(uri string, data []byte) (MetricReportDefinition, error) {
	p, err := decodeProps(data)
	if err != nil {
		return MetricReportDefinition{}, fmt.Errorf("%w: %v", ErrMalformed, err)
	}
	return parseMetricReportDefinition(uri, p), nil
}

func linksOf(v any) []string {
	l, _ := v.([]any)
	var out []string
	for _, x := range l {
		if m, _ := x.(map[string]any); m != nil && len(out) < maxValues {
			if id := servicePath(str(m, "@odata.id")); id != "" {
				out = append(out, id)
			}
		}
	}
	return out
}

func parseMetricReportDefinition(uri string, p Props) MetricReportDefinition {
	d := MetricReportDefinition{URI: uri, ID: str(p, "Id"), Name: str(p, "Name"), Type: str(p, "MetricReportDefinitionType"),
		Enabled: boolOf(p, "MetricReportDefinitionEnabled"), State: str(sub(p, "Status"), "State"), Health: str(sub(p, "Status"), "Health"),
		MetricProperties: stringList(p["MetricProperties"]), Wildcards: wildcardsOf(p), ReportActions: stringList(p["ReportActions"]),
		ReportUpdates: str(p, "ReportUpdates"), AppendLimit: intOf(p, "AppendLimit"), ReportTimespan: str(p, "ReportTimespan"),
		SuppressRepeatedMetricValue: boolOf(p, "SuppressRepeatedMetricValue"), HeartbeatInterval: str(p, "MetricReportHeartbeatInterval"),
		MetricReportURI: linkOf(p, "MetricReport"), TriggerURIs: linksOf(sub(p, "Links")["Triggers"])}
	if d.ID == "" {
		d.ID = path.Base(uri)
	}
	if s := sub(p, "Schedule"); s != nil {
		d.Schedule = &ReportSchedule{RecurrenceInterval: str(s, "RecurrenceInterval"), InitialStartTime: str(s, "InitialStartTime"),
			MaxOccurrences: intOf(s, "MaxOccurrences"), Lifetime: str(s, "Lifetime"), Name: str(s, "Name")}
		if iv, err := ParseRedfishDuration(d.Schedule.RecurrenceInterval); err == nil {
			d.Interval = &iv
		}
	}
	ms, _ := p["Metrics"].([]any)
	for _, x := range ms {
		m, _ := x.(map[string]any)
		if m == nil || len(d.Metrics) >= maxValues {
			continue
		}
		d.Metrics = append(d.Metrics, MetricSpec{MetricID: str(m, "MetricId"), MetricProperties: stringList(m["MetricProperties"]),
			CollectionFunction: str(m, "CollectionFunction"), CollectionTimeScope: str(m, "CollectionTimeScope"), CollectionDuration: str(m, "CollectionDuration")})
	}
	return d
}

// Threshold is a numeric threshold of a trigger (Triggers 1.4.0 Threshold).
type Threshold struct {
	// Activation is Increasing, Decreasing, Either or Disabled.
	Activation string   `json:"activation,omitempty"`
	DwellTime  string   `json:"dwell_time,omitempty"`
	Reading    *float64 `json:"reading,omitempty"`
}

// Trigger is a Triggers resource (Triggers 1.4.0): a condition that applies to
// metrics. It is read, not written.
type Trigger struct {
	URI              string   `json:"uri"`
	ID               string   `json:"id"`
	Name             string   `json:"name,omitempty"`
	MetricType       string   `json:"metric_type,omitempty"`
	MetricIDs        []string `json:"metric_ids,omitempty"`
	MetricProperties []string `json:"metric_properties,omitempty"`
	// NumericThresholds by name: LowerCritical, LowerWarning, UpperCritical,
	// UpperWarning.
	NumericThresholds map[string]Threshold `json:"numeric_thresholds,omitempty"`
	// DiscreteTriggerCondition and the number of DiscreteTriggers.
	DiscreteTriggerCondition string   `json:"discrete_trigger_condition,omitempty"`
	DiscreteTriggers         int      `json:"discrete_triggers,omitempty"`
	EventTriggers            []string `json:"event_triggers,omitempty"`
	// TriggerActions are LogToLogService, RedfishEvent and RedfishMetricReport.
	TriggerActions []string `json:"trigger_actions,omitempty"`
	TriggerEnabled *bool    `json:"trigger_enabled,omitempty"`
	State          string   `json:"state,omitempty"`
	Health         string   `json:"health,omitempty"`
}

// ParseTrigger decodes a Triggers resource read at uri.
func ParseTrigger(uri string, data []byte) (Trigger, error) {
	p, err := decodeProps(data)
	if err != nil {
		return Trigger{}, fmt.Errorf("%w: %v", ErrMalformed, err)
	}
	return parseTrigger(uri, p), nil
}

func parseTrigger(uri string, p Props) Trigger {
	t := Trigger{URI: uri, ID: str(p, "Id"), Name: str(p, "Name"), MetricType: str(p, "MetricType"), MetricIDs: stringList(p["MetricIds"]),
		MetricProperties: stringList(p["MetricProperties"]), DiscreteTriggerCondition: str(p, "DiscreteTriggerCondition"),
		EventTriggers: stringList(p["EventTriggers"]), TriggerActions: stringList(p["TriggerActions"]), TriggerEnabled: boolOf(p, "TriggerEnabled"),
		State: str(sub(p, "Status"), "State"), Health: str(sub(p, "Status"), "Health")}
	if t.ID == "" {
		t.ID = path.Base(uri)
	}
	if l, _ := p["DiscreteTriggers"].([]any); l != nil {
		t.DiscreteTriggers = min(len(l), maxValues)
	}
	for _, name := range []string{"LowerCritical", "LowerWarning", "UpperCritical", "UpperWarning"} {
		if m := sub(sub(p, "NumericThresholds"), name); m != nil {
			if t.NumericThresholds == nil {
				t.NumericThresholds = map[string]Threshold{}
			}
			t.NumericThresholds[name] = Threshold{Activation: str(m, "Activation"), DwellTime: str(m, "DwellTime"), Reading: numOf(m, "Reading")}
		}
	}
	return t
}

// ---- The metric report ----

// MetricValue is one entry of MetricValues (MetricReport 1.5.2 MetricValue).
type MetricValue struct {
	// MetricID is the Id of the MetricDefinition that describes the metric;
	// MetricProperty the URI, in JSON pointer notation, of the property it comes
	// from (MetricReport 1.5.2 MetricProperty: "may contain additional
	// calculations performed on the property").
	MetricID       string `json:"metric_id,omitempty"`
	MetricProperty string `json:"metric_property,omitempty"`
	// Raw is the value as the service sent it: the schema has a string (a number
	// converted to its string, a boolean as true or false, an array as a JSON
	// string); a JSON number or boolean where a string was expected is kept as its
	// text. Empty when Null.
	Raw string `json:"raw"`
	// Null: the service sent null, or no value.
	Null bool `json:"null,omitempty"`
	// Number is set only when Raw is a number (ParseMetricNumber); nil otherwise:
	// a value that is not a number is not 0.
	Number *float64 `json:"number,omitempty"`
	// Timestamp is when the metric was obtained, nil when the service sent none or
	// not an RFC 3339 date-time; TimestampRaw is the text as sent.
	Timestamp    *time.Time `json:"timestamp,omitempty"`
	TimestampRaw string     `json:"timestamp_raw,omitempty"`
}

// Float is the value as a number; an error wrapping ErrNotNumeric when it is
// null or not a number.
func (v MetricValue) Float() (float64, error) {
	if v.Number == nil {
		if v.Null {
			return 0, fmt.Errorf("%w: null", ErrNotNumeric)
		}
		return 0, fmt.Errorf("%w: %q", ErrNotNumeric, clip(v.Raw))
	}
	return *v.Number, nil
}

// MetricReport is a MetricReport (MetricReport 1.5.2): a set of collected
// metrics, read from the MetricReports collection or received over SSE (DSP0266
// 12.5.2.2).
type MetricReport struct {
	// URI is the @odata.id of the report; ID, Name and ODataType are its Id, Name
	// and @odata.type.
	URI       string `json:"uri,omitempty"`
	ID        string `json:"id,omitempty"`
	Name      string `json:"name,omitempty"`
	ODataType string `json:"odata_type,omitempty"`
	// Timestamp is when the report was generated (MetricReport.Timestamp, 1.1),
	// nil when absent or not a date-time; TimestampRaw is the text as sent.
	Timestamp    *time.Time `json:"timestamp,omitempty"`
	TimestampRaw string     `json:"timestamp_raw,omitempty"`
	// DefinitionURI is the MetricReportDefinition of the report.
	DefinitionURI string `json:"definition_uri,omitempty"`
	// Context is the context the subscriber gave (present in an event, 1.4).
	Context string        `json:"context,omitempty"`
	Values  []MetricValue `json:"values,omitempty"`
}

// ParseMetricReport decodes a MetricReport document: a resource of the
// collection, or the data field of a metric report SSE event. It never panics,
// and refuses a document larger than MaxMetricReportBytes, one with more than
// MaxMetricValues values, and a value that is not an object. A value that is a
// string, a number, a boolean or null is read as MetricValue says; a timestamp
// that is not an RFC 3339 date-time is kept as text and has no Timestamp.
func ParseMetricReport(data []byte) (MetricReport, error) {
	if len(data) > MaxMetricReportBytes {
		return MetricReport{}, ErrTooLarge
	}
	if t := bytes.TrimSpace(data); len(t) == 0 || t[0] != '{' {
		return MetricReport{}, fmt.Errorf("%w: the metric report is not a JSON object", ErrMalformed)
	}
	var d struct {
		ODataID    string  `json:"@odata.id"`
		ODataType  string  `json:"@odata.type"`
		ID         any     `json:"Id"`
		Name       string  `json:"Name"`
		Context    string  `json:"Context"`
		Timestamp  *string `json:"Timestamp"`
		Definition *struct {
			ID string `json:"@odata.id"`
		} `json:"MetricReportDefinition"`
		Values []json.RawMessage `json:"MetricValues"`
	}
	if err := json.Unmarshal(data, &d); err != nil {
		return MetricReport{}, fmt.Errorf("%w: the metric report: %v", ErrMalformed, err)
	}
	if len(d.Values) > MaxMetricValues {
		return MetricReport{}, fmt.Errorf("%w: the metric report has more than %d values", ErrMalformed, MaxMetricValues)
	}
	r := MetricReport{URI: servicePath(d.ODataID), ID: fmt.Sprint(zeroNil(d.ID)), Name: d.Name, ODataType: d.ODataType, Context: d.Context}
	if d.Timestamp != nil {
		r.TimestampRaw = clipTo(*d.Timestamp)
		r.Timestamp = parseTimestamp(*d.Timestamp)
	}
	if d.Definition != nil {
		r.DefinitionURI = servicePath(d.Definition.ID)
	}
	for i, raw := range d.Values {
		v, err := parseMetricValue(raw)
		if err != nil {
			return MetricReport{}, fmt.Errorf("%w: MetricValues[%d]: %v", ErrMalformed, i, err)
		}
		r.Values = append(r.Values, v)
	}
	return r, nil
}

// clipTo bounds a text the report keeps.
func clipTo(s string) string {
	if len(s) > maxMetricRaw {
		return s[:maxMetricRaw]
	}
	return s
}

// parseTimestamp reads an RFC 3339 date-time (the schema's format date-time),
// nil when it is not one.
func parseTimestamp(s string) *time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return nil
	}
	return &t
}

func parseMetricValue(raw json.RawMessage) (MetricValue, error) {
	var d struct {
		MetricID       *string         `json:"MetricId"`
		MetricProperty *string         `json:"MetricProperty"`
		MetricValue    json.RawMessage `json:"MetricValue"`
		Timestamp      *string         `json:"Timestamp"`
	}
	t := bytes.TrimSpace(raw)
	if len(t) == 0 || t[0] != '{' {
		return MetricValue{}, errors.New("not an object")
	}
	if err := json.Unmarshal(t, &d); err != nil {
		return MetricValue{}, err
	}
	var v MetricValue
	if d.MetricID != nil {
		v.MetricID = clipTo(*d.MetricID)
	}
	if d.MetricProperty != nil {
		v.MetricProperty = clipTo(*d.MetricProperty)
	}
	if d.Timestamp != nil {
		v.TimestampRaw = clipTo(*d.Timestamp)
		v.Timestamp = parseTimestamp(*d.Timestamp)
	}
	mv := bytes.TrimSpace(d.MetricValue)
	switch {
	case len(mv) == 0 || string(mv) == "null":
		v.Null = true
	case mv[0] == '"':
		var s string
		if err := json.Unmarshal(mv, &s); err != nil {
			return MetricValue{}, err
		}
		v.Raw = s
		if len(s) > maxMetricRaw {
			v.Raw = s[:maxMetricRaw]
		} else if n, err := ParseMetricNumber(s); err == nil {
			v.Number = &n
		}
	default: // a number, a boolean, an object or an array where a string was expected
		var buf bytes.Buffer
		if err := json.Compact(&buf, mv); err != nil {
			return MetricValue{}, err
		}
		v.Raw = clipTo(buf.String())
		if mv[0] != '{' && mv[0] != '[' {
			if n, err := ParseMetricNumber(v.Raw); err == nil {
				v.Number = &n
			}
		}
	}
	return v, nil
}

// Point is one sample of a Series.
type Point struct {
	At    time.Time `json:"at"`
	Value float64   `json:"value"`
}

// Series is the samples of one metric property of a report, sorted by time.
type Series struct {
	MetricID       string  `json:"metric_id,omitempty"`
	MetricProperty string  `json:"metric_property,omitempty"`
	Points         []Point `json:"points"`
}

// TimeSeries turns the values of a report into time series, one per (MetricID,
// MetricProperty) in the order they first appear, each sorted by timestamp (the
// way MetricValue.Timestamp says a time series is established; equal timestamps
// keep the report's order). A value with no timestamp or no number is not in a
// series: it is returned in skipped, never made into a 0 or given the report's
// time.
func (r MetricReport) TimeSeries() (series []Series, skipped []MetricValue) {
	idx := map[[2]string]int{}
	for _, v := range r.Values {
		if v.Number == nil || v.Timestamp == nil {
			skipped = append(skipped, v)
			continue
		}
		k := [2]string{v.MetricID, v.MetricProperty}
		i, ok := idx[k]
		if !ok {
			i = len(series)
			idx[k] = i
			series = append(series, Series{MetricID: v.MetricID, MetricProperty: v.MetricProperty})
		}
		series[i].Points = append(series[i].Points, Point{At: *v.Timestamp, Value: *v.Number})
	}
	for i := range series {
		pts := series[i].Points
		sort.SliceStable(pts, func(a, b int) bool { return pts[a].At.Before(pts[b].At) })
	}
	return series, skipped
}

// ---- Reads ----

// telemetryServiceURI is the TelemetryService the service root links.
func (c *Client) telemetryServiceURI(ctx context.Context) (string, error) {
	u, err := c.rootLink(ctx, "TelemetryService")
	if errors.Is(err, ErrNotFound) {
		return "", fmt.Errorf("%w: the service root links no TelemetryService", ErrUnsupported)
	}
	return u, err
}

// TelemetryService reads the TelemetryService of the service (ErrUnsupported
// when the service root links none).
func (c *Client) TelemetryService(ctx context.Context) (TelemetryService, error) {
	uri, err := c.telemetryServiceURI(ctx)
	if err != nil {
		return TelemetryService{}, err
	}
	p, _, err := c.getProps(ctx, uri)
	if err != nil {
		return TelemetryService{}, err
	}
	return parseTelemetryService(uri, p), nil
}

// telemetryCollection returns the TelemetryService and the collection of it that
// pick names, or ErrUnsupported when it links none.
func (c *Client) telemetryCollection(ctx context.Context, name string, pick func(TelemetryService) string) (TelemetryService, string, error) {
	t, err := c.TelemetryService(ctx)
	if err != nil {
		return t, "", err
	}
	coll := pick(t)
	if coll == "" {
		return t, "", fmt.Errorf("%w: the TelemetryService links no %s collection", ErrUnsupported, name)
	}
	return t, coll, nil
}

// resolveMember turns an Id or a path into the path of a member of coll; anything
// else is refused.
func resolveMember(coll, idOrURI, kind string) (string, error) {
	if subID.MatchString(idOrURI) {
		return coll + "/" + idOrURI, nil
	}
	if p := servicePath(idOrURI); p != "" && path.Dir(p) == coll && subID.MatchString(path.Base(p)) {
		return p, nil
	}
	return "", fmt.Errorf("redfish: %q is not a %s of %s", clip(idOrURI), kind, coll)
}

// telemetryMembers reads the members of a telemetry collection (at most
// MaxTelemetryMembers, $expand where the service advertises it, v0.12.0).
func (c *Client) telemetryMembers(ctx context.Context, coll string) ([]member, error) {
	return c.memberResources(ctx, coll, MaxTelemetryMembers, 8, func(err error) bool {
		return errors.Is(err, ErrNotFound) // removed since the listing
	})
}

// MetricDefinitions lists the MetricDefinitions of the service (at most
// MaxTelemetryMembers).
func (c *Client) MetricDefinitions(ctx context.Context) ([]MetricDefinition, error) {
	_, coll, err := c.telemetryCollection(ctx, "MetricDefinitions", func(t TelemetryService) string { return t.MetricDefinitionsURI })
	if err != nil {
		return nil, err
	}
	ms, err := c.telemetryMembers(ctx, coll)
	if err != nil {
		return nil, err
	}
	out := make([]MetricDefinition, 0, len(ms))
	for _, m := range ms {
		out = append(out, parseMetricDefinition(m.URI, m.Props))
	}
	return out, nil
}

// MetricDefinition reads one metric definition, named by its Id or its path.
func (c *Client) MetricDefinition(ctx context.Context, idOrURI string) (MetricDefinition, error) {
	_, coll, err := c.telemetryCollection(ctx, "MetricDefinitions", func(t TelemetryService) string { return t.MetricDefinitionsURI })
	if err != nil {
		return MetricDefinition{}, err
	}
	uri, err := resolveMember(coll, idOrURI, "metric definition")
	if err != nil {
		return MetricDefinition{}, err
	}
	p, _, err := c.getProps(ctx, uri)
	if err != nil {
		return MetricDefinition{}, err
	}
	return parseMetricDefinition(uri, p), nil
}

// MetricReportDefinitions lists the MetricReportDefinitions of the service (at
// most MaxTelemetryMembers).
func (c *Client) MetricReportDefinitions(ctx context.Context) ([]MetricReportDefinition, error) {
	_, coll, err := c.telemetryCollection(ctx, "MetricReportDefinitions", func(t TelemetryService) string { return t.MetricReportDefinitionsURI })
	if err != nil {
		return nil, err
	}
	ms, err := c.telemetryMembers(ctx, coll)
	if err != nil {
		return nil, err
	}
	out := make([]MetricReportDefinition, 0, len(ms))
	for _, m := range ms {
		out = append(out, parseMetricReportDefinition(m.URI, m.Props))
	}
	return out, nil
}

// MetricReportDefinition reads one metric report definition, named by its Id or
// its path.
func (c *Client) MetricReportDefinition(ctx context.Context, idOrURI string) (MetricReportDefinition, error) {
	_, coll, err := c.telemetryCollection(ctx, "MetricReportDefinitions", func(t TelemetryService) string { return t.MetricReportDefinitionsURI })
	if err != nil {
		return MetricReportDefinition{}, err
	}
	uri, err := resolveMember(coll, idOrURI, "metric report definition")
	if err != nil {
		return MetricReportDefinition{}, err
	}
	p, _, err := c.getProps(ctx, uri)
	if err != nil {
		return MetricReportDefinition{}, err
	}
	return parseMetricReportDefinition(uri, p), nil
}

// readReport reads one report document and decodes it with ParseMetricReport
// (bounded by MaxMetricReportBytes), keeping the path it was read at.
func (c *Client) readReport(ctx context.Context, uri string) (MetricReport, error) {
	r, err := c.Get(ctx, uri)
	if err != nil {
		return MetricReport{}, err
	}
	rep, err := ParseMetricReport(r.Body)
	if err != nil {
		return MetricReport{}, fmt.Errorf("%s: %w", uri, err)
	}
	rep.URI = uri
	if rep.ID == "" {
		rep.ID = path.Base(uri)
	}
	return rep, nil
}

// MetricReports lists the MetricReports of the service with their values (at most
// MaxTelemetryMembers reports, each of at most MaxMetricValues values; $expand
// where the service advertises it). A report that does not decode is an error.
func (c *Client) MetricReports(ctx context.Context) ([]MetricReport, error) {
	_, coll, err := c.telemetryCollection(ctx, "MetricReports", func(t TelemetryService) string { return t.MetricReportsURI })
	if err != nil {
		return nil, err
	}
	ms, err := c.telemetryMembers(ctx, coll)
	if err != nil {
		return nil, err
	}
	out := make([]MetricReport, 0, len(ms))
	for _, m := range ms {
		data, err := json.Marshal(m.Props)
		if err != nil {
			return nil, fmt.Errorf("%w: %s: %v", ErrMalformed, m.URI, err)
		}
		rep, err := ParseMetricReport(data)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", m.URI, err)
		}
		rep.URI = m.URI
		if rep.ID == "" {
			rep.ID = path.Base(m.URI)
		}
		out = append(out, rep)
	}
	return out, nil
}

// MetricReport reads one metric report, named by its Id or its path. For a
// definition of type OnRequest this GET is what generates the report
// (MetricReportDefinitionType OnRequest); no other call is needed.
func (c *Client) MetricReport(ctx context.Context, idOrURI string) (MetricReport, error) {
	_, coll, err := c.telemetryCollection(ctx, "MetricReports", func(t TelemetryService) string { return t.MetricReportsURI })
	if err != nil {
		return MetricReport{}, err
	}
	uri, err := resolveMember(coll, idOrURI, "metric report")
	if err != nil {
		return MetricReport{}, err
	}
	return c.readReport(ctx, uri)
}

// Triggers lists the Triggers of the service (at most MaxTelemetryMembers);
// ErrUnsupported when the TelemetryService links no Triggers collection (the
// collection is optional).
func (c *Client) Triggers(ctx context.Context) ([]Trigger, error) {
	_, coll, err := c.telemetryCollection(ctx, "Triggers", func(t TelemetryService) string { return t.TriggersURI })
	if err != nil {
		return nil, err
	}
	ms, err := c.telemetryMembers(ctx, coll)
	if err != nil {
		return nil, err
	}
	out := make([]Trigger, 0, len(ms))
	for _, m := range ms {
		out = append(out, parseTrigger(m.URI, m.Props))
	}
	return out, nil
}

// ---- Create and delete a MetricReportDefinition ----

// MetricReportDefinitionInput describes a metric report definition to create
// (MetricReportDefinition 1.4.7). CreateMetricReportDefinition checks it against
// the schema's enumerations and against the TelemetryService before anything is
// sent.
type MetricReportDefinitionInput struct {
	// ID is the Id the definition asks for (the service may assign another); Name
	// defaults to the ID.
	ID, Name string
	// Type is Periodic, OnChange or OnRequest (required). A Periodic definition
	// needs Schedule.RecurrenceInterval ("If the value is Periodic, the Schedule
	// property shall be present").
	Type     string
	Schedule *ReportSchedule
	// Metrics is what the report holds (required); each entry names a MetricID that
	// a MetricDefinition of the service has, or MetricProperties, and may apply a
	// CollectionFunction the service supports.
	Metrics []MetricSpec
	// ReportActions are LogToMetricReportsCollection and RedfishEvent.
	ReportActions []string
	// ReportUpdates is Overwrite, AppendWrapsWhenFull, AppendStopsWhenFull or
	// NewReport; AppendLimit is required for the two Append values.
	ReportUpdates               string
	AppendLimit                 *int64
	ReportTimespan              string
	SuppressRepeatedMetricValue *bool
	// Enabled is MetricReportDefinitionEnabled; nil: not sent.
	Enabled *bool
}

var (
	reportDefinitionTypes = []string{"Periodic", "OnChange", "OnRequest"}
	collectionFunctions   = []string{"Average", "Maximum", "Minimum", "Summation"}
	collectionTimeScopes  = []string{"Point", "Interval", "StartupInterval"}
	reportActionValues    = []string{"LogToMetricReportsCollection", "RedfishEvent"}
	reportUpdateValues    = []string{"Overwrite", "AppendWrapsWhenFull", "AppendStopsWhenFull", "NewReport"}
)

func checkEnum(name, v string, allowed []string) error {
	if !slices.Contains(allowed, v) {
		return fmt.Errorf("redfish: %s %q is not one of %s", name, clip(v), strings.Join(allowed, ", "))
	}
	return nil
}

func checkDuration(name, v string) (time.Duration, error) {
	d, err := ParseRedfishDuration(v)
	if err != nil {
		return 0, fmt.Errorf("redfish: %s: %w", name, err)
	}
	return d, nil
}

// definitionBody checks what does not depend on the service and builds the POST
// body; the interval it returns is the Periodic recurrence (0 otherwise).
func definitionBody(in MetricReportDefinitionInput) (body Props, interval time.Duration, err error) {
	if in.ID != "" && !subID.MatchString(in.ID) {
		return nil, 0, fmt.Errorf("redfish: the definition Id %q is not valid (letters, digits, . _ -; at most 128)", clip(in.ID))
	}
	if len(in.Name) > 256 || strings.ContainsAny(in.Name, "\x00\r\n") {
		return nil, 0, errors.New("redfish: the name is longer than 256 bytes or holds a control character")
	}
	if err := checkEnum("MetricReportDefinitionType", in.Type, reportDefinitionTypes); err != nil {
		return nil, 0, err
	}
	if len(in.Metrics) == 0 || len(in.Metrics) > maxValues {
		return nil, 0, fmt.Errorf("redfish: a definition needs between 1 and %d metrics", maxValues)
	}
	metrics := make([]any, 0, len(in.Metrics))
	for i, m := range in.Metrics {
		mb := Props{}
		switch {
		case m.MetricID != "" && len(m.MetricProperties) > 0:
			return nil, 0, fmt.Errorf("redfish: metric %d names a MetricId and MetricProperties: only one (MetricReportDefinition 1.4.7 Metric)", i)
		case m.MetricID == "" && len(m.MetricProperties) == 0:
			return nil, 0, fmt.Errorf("redfish: metric %d names neither a MetricId nor MetricProperties", i)
		case m.MetricID != "":
			if !subID.MatchString(m.MetricID) {
				return nil, 0, fmt.Errorf("redfish: metric %d: the MetricId %q is not valid", i, clip(m.MetricID))
			}
			mb["MetricId"] = m.MetricID
		default:
			if err := checkList("MetricProperties", m.MetricProperties); err != nil {
				return nil, 0, err
			}
			for _, u := range m.MetricProperties {
				if !strings.HasPrefix(u, "/redfish/") {
					return nil, 0, fmt.Errorf("redfish: metric %d: %q is not a URI of the service (/redfish/...)", i, clip(u))
				}
			}
			mb["MetricProperties"] = m.MetricProperties
		}
		if m.CollectionFunction != "" {
			if err := checkEnum("CollectionFunction", m.CollectionFunction, collectionFunctions); err != nil {
				return nil, 0, err
			}
			mb["CollectionFunction"] = m.CollectionFunction
		}
		if m.CollectionTimeScope != "" {
			if err := checkEnum("CollectionTimeScope", m.CollectionTimeScope, collectionTimeScopes); err != nil {
				return nil, 0, err
			}
			mb["CollectionTimeScope"] = m.CollectionTimeScope
		}
		if m.CollectionDuration != "" {
			if _, err := checkDuration("CollectionDuration", m.CollectionDuration); err != nil {
				return nil, 0, err
			}
			mb["CollectionDuration"] = m.CollectionDuration
		}
		metrics = append(metrics, mb)
	}
	body = Props{"MetricReportDefinitionType": in.Type, "Metrics": metrics}
	name := in.Name
	if name == "" {
		name = in.ID
	}
	if name == "" {
		name = "MetricReportDefinition"
	}
	body["Name"] = name
	if in.ID != "" {
		body["Id"] = in.ID
	}
	switch {
	case in.Type == "Periodic" && (in.Schedule == nil || in.Schedule.RecurrenceInterval == ""):
		return nil, 0, errors.New("redfish: a Periodic definition needs Schedule.RecurrenceInterval")
	case in.Schedule != nil:
		s := Props{}
		if in.Schedule.RecurrenceInterval != "" {
			if interval, err = checkDuration("RecurrenceInterval", in.Schedule.RecurrenceInterval); err != nil {
				return nil, 0, err
			}
			if interval <= 0 {
				return nil, 0, errors.New("redfish: RecurrenceInterval must be above zero")
			}
			s["RecurrenceInterval"] = in.Schedule.RecurrenceInterval
		}
		if in.Schedule.InitialStartTime != "" {
			if parseTimestamp(in.Schedule.InitialStartTime) == nil {
				return nil, 0, fmt.Errorf("redfish: InitialStartTime %q is not an RFC 3339 date-time", clip(in.Schedule.InitialStartTime))
			}
			s["InitialStartTime"] = in.Schedule.InitialStartTime
		}
		if in.Schedule.MaxOccurrences != nil {
			if *in.Schedule.MaxOccurrences < 1 {
				return nil, 0, errors.New("redfish: MaxOccurrences must be at least 1")
			}
			s["MaxOccurrences"] = *in.Schedule.MaxOccurrences
		}
		if in.Schedule.Lifetime != "" {
			if _, err := checkDuration("Lifetime", in.Schedule.Lifetime); err != nil {
				return nil, 0, err
			}
			s["Lifetime"] = in.Schedule.Lifetime
		}
		body["Schedule"] = s
	}
	for _, a := range in.ReportActions {
		if err := checkEnum("ReportActions", a, reportActionValues); err != nil {
			return nil, 0, err
		}
	}
	if len(in.ReportActions) > 0 {
		body["ReportActions"] = in.ReportActions
	}
	if in.ReportUpdates != "" {
		if err := checkEnum("ReportUpdates", in.ReportUpdates, reportUpdateValues); err != nil {
			return nil, 0, err
		}
		body["ReportUpdates"] = in.ReportUpdates
	}
	appends := in.ReportUpdates == "AppendWrapsWhenFull" || in.ReportUpdates == "AppendStopsWhenFull"
	switch {
	case in.AppendLimit != nil && *in.AppendLimit < 0:
		return nil, 0, errors.New("redfish: AppendLimit must not be negative (minimum 0)")
	case appends && in.AppendLimit == nil:
		return nil, 0, fmt.Errorf("redfish: AppendLimit is required with ReportUpdates %s", in.ReportUpdates)
	case in.AppendLimit != nil:
		body["AppendLimit"] = *in.AppendLimit
	}
	if in.ReportTimespan != "" {
		if _, err := checkDuration("ReportTimespan", in.ReportTimespan); err != nil {
			return nil, 0, err
		}
		body["ReportTimespan"] = in.ReportTimespan
	}
	if in.SuppressRepeatedMetricValue != nil {
		body["SuppressRepeatedMetricValue"] = *in.SuppressRepeatedMetricValue
	}
	if in.Enabled != nil {
		body["MetricReportDefinitionEnabled"] = *in.Enabled
	}
	return body, interval, nil
}

// CreateMetricReportDefinition creates a metric report definition: a POST to the
// MetricReportDefinitions collection (the schema says it is insertable). The
// input is checked first against the schema, and against the service: it must be
// enabled; a Periodic recurrence must not be shorter than MinCollectionInterval;
// a CollectionFunction must be in SupportedCollectionFunctions; a MetricId must
// be the Id of a MetricDefinition; and a definition that records its reports in
// the collection is refused when the service already holds MaxReports of them.
// Nothing is sent when a check fails. The service answers 201 with the Location
// of the new definition.
func (c *Client) CreateMetricReportDefinition(ctx context.Context, in MetricReportDefinitionInput) (MetricReportDefinition, Outcome, error) {
	body, interval, err := definitionBody(in)
	if err != nil {
		return MetricReportDefinition{}, Outcome{}, err
	}
	t, coll, err := c.telemetryCollection(ctx, "MetricReportDefinitions", func(t TelemetryService) string { return t.MetricReportDefinitionsURI })
	if err != nil {
		return MetricReportDefinition{}, Outcome{}, err
	}
	if t.ServiceEnabled != nil && !*t.ServiceEnabled {
		return MetricReportDefinition{}, Outcome{}, fmt.Errorf("%w: the TelemetryService is disabled (ServiceEnabled is false)", ErrUnsupported)
	}
	if interval > 0 && t.MinInterval != nil && interval < *t.MinInterval {
		return MetricReportDefinition{}, Outcome{}, fmt.Errorf("%w: RecurrenceInterval %s is shorter than the MinCollectionInterval %s of the service",
			ErrUnsupported, in.Schedule.RecurrenceInterval, t.MinCollectionInterval)
	}
	needDefinitions := false
	for _, m := range in.Metrics {
		if m.CollectionFunction != "" && !slices.Contains(t.SupportedCollectionFunctions, m.CollectionFunction) {
			return MetricReportDefinition{}, Outcome{}, fmt.Errorf("%w: CollectionFunction %s is not supported by the service (supported: %s)",
				ErrUnsupported, m.CollectionFunction, strings.Join(t.SupportedCollectionFunctions, ", "))
		}
		needDefinitions = needDefinitions || m.MetricID != ""
	}
	if needDefinitions {
		defs, err := c.MetricDefinitions(ctx)
		if err != nil {
			return MetricReportDefinition{}, Outcome{}, err
		}
		have := map[string]bool{}
		for _, d := range defs {
			have[d.ID] = true
		}
		for _, m := range in.Metrics {
			if m.MetricID != "" && !have[m.MetricID] {
				return MetricReportDefinition{}, Outcome{}, fmt.Errorf("%w: the service has no MetricDefinition %q", ErrNotFound, clip(m.MetricID))
			}
		}
	}
	logs := len(in.ReportActions) == 0 || slices.Contains(in.ReportActions, "LogToMetricReportsCollection")
	if logs && t.MaxReports != nil && t.MetricReportsURI != "" {
		uris, err := c.membersMax(ctx, t.MetricReportsURI, MaxTelemetryMembers, 8)
		if err != nil {
			return MetricReportDefinition{}, Outcome{}, err
		}
		if int64(len(uris)) >= *t.MaxReports {
			return MetricReportDefinition{}, Outcome{}, fmt.Errorf("%w: the service holds %d metric reports, its MaxReports", ErrUnsupported, *t.MaxReports)
		}
	}
	r, err := c.Post(ctx, coll, body)
	if err != nil {
		return MetricReportDefinition{}, Outcome{}, err
	}
	loc := r.Location()
	if r.Status != 201 || loc == "" || path.Dir(loc) != coll {
		return MetricReportDefinition{}, Outcome{}, fmt.Errorf("%w: the answer to the creation is not a 201 with the Location of a member of %s", ErrMalformed, coll)
	}
	p, derr := decodeProps(r.Body)
	if derr != nil || str(p, "MetricReportDefinitionType") == "" {
		if p, _, err = c.getProps(ctx, loc); err != nil {
			return MetricReportDefinition{}, Outcome{}, err
		}
	}
	return parseMetricReportDefinition(loc, p), Outcome{Changed: true, Message: "Created metric report definition " + loc}, nil
}

// DeleteMetricReportDefinition deletes a metric report definition, named by its
// Id or its path: the resource is read first so that only a
// MetricReportDefinition is deleted, then removed with a DELETE. A definition
// that is already gone (404) is not an error: nothing changed.
func (c *Client) DeleteMetricReportDefinition(ctx context.Context, idOrURI string) (Outcome, error) {
	_, coll, err := c.telemetryCollection(ctx, "MetricReportDefinitions", func(t TelemetryService) string { return t.MetricReportDefinitionsURI })
	if err != nil {
		return Outcome{}, err
	}
	uri, err := resolveMember(coll, idOrURI, "metric report definition")
	if err != nil {
		return Outcome{}, err
	}
	p, _, err := c.getProps(ctx, uri)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return Outcome{Message: uri + " does not exist"}, nil
		}
		return Outcome{}, err
	}
	if !strings.HasPrefix(str(p, "@odata.type"), "#MetricReportDefinition.") {
		return Outcome{}, fmt.Errorf("redfish: %s is not a MetricReportDefinition (%s)", uri, clip(str(p, "@odata.type")))
	}
	if _, err := c.Delete(ctx, uri, ""); err != nil {
		if errors.Is(err, ErrNotFound) {
			return Outcome{Message: uri + " does not exist"}, nil
		}
		return Outcome{}, err
	}
	return Outcome{Changed: true, Message: "Deleted metric report definition " + uri}, nil
}

// ---- SubmitTestMetricReport ----

// TestMetricValue is one entry of GeneratedMetricReportValues of the
// SubmitTestMetricReport action (TelemetryService 1.4.1 MetricValue): the
// value is a string, as in a report.
type TestMetricValue struct {
	MetricID       string
	MetricProperty string
	MetricValue    string
	// Timestamp is an RFC 3339 date-time; empty: not sent.
	Timestamp string
}

// SubmitTestMetricReport runs the TelemetryService.SubmitTestMetricReport action:
// the service generates a metric report named name from the values and sends it
// to the appropriate event destinations ("immediately generate the metric report
// as an alert event"). Both parameters are required by the schema; the values
// are at most MaxMetricValues, their timestamps RFC 3339 date-times. Each
// parameter is checked against the ActionInfo (or AllowableValues) the service
// gives. ErrUnsupported when the service has no such action.
func (c *Client) SubmitTestMetricReport(ctx context.Context, name string, values []TestMetricValue) (Outcome, error) {
	const action = "#TelemetryService.SubmitTestMetricReport"
	if name == "" || len(name) > 256 || strings.ContainsAny(name, "\x00\r\n") {
		return Outcome{}, errors.New("redfish: the report name is empty, longer than 256 bytes or holds a control character")
	}
	if len(values) == 0 || len(values) > MaxMetricValues {
		return Outcome{}, fmt.Errorf("redfish: a test report needs between 1 and %d values", MaxMetricValues)
	}
	list := make([]any, 0, len(values))
	for i, v := range values {
		for _, s := range []string{v.MetricID, v.MetricProperty, v.MetricValue, v.Timestamp} {
			if len(s) > maxMetricRaw || strings.ContainsAny(s, "\x00") {
				return Outcome{}, fmt.Errorf("redfish: value %d holds a text longer than %d bytes or a NUL", i, maxMetricRaw)
			}
		}
		if v.Timestamp != "" && parseTimestamp(v.Timestamp) == nil {
			return Outcome{}, fmt.Errorf("redfish: value %d: the timestamp %q is not an RFC 3339 date-time", i, clip(v.Timestamp))
		}
		m := Props{"MetricValue": v.MetricValue}
		for k, s := range map[string]string{"MetricId": v.MetricID, "MetricProperty": v.MetricProperty, "Timestamp": v.Timestamp} {
			if s != "" {
				m[k] = s
			}
		}
		list = append(list, m)
	}
	uri, err := c.telemetryServiceURI(ctx)
	if err != nil {
		return Outcome{}, err
	}
	p, _, err := c.getProps(ctx, uri)
	if err != nil {
		return Outcome{}, err
	}
	act := sub(sub(p, "Actions"), action)
	if act == nil {
		return Outcome{}, fmt.Errorf("%w: action %s not found on %s", ErrUnsupported, action, uri)
	}
	target := servicePath(str(act, "target"))
	if target == "" {
		return Outcome{}, fmt.Errorf("%w: the target of action %s is missing", ErrMalformed, action)
	}
	body := Props{"MetricReportName": name, "GeneratedMetricReportValues": list}
	values2 := c.ActionValues(ctx, act)
	for k, v := range body {
		allowed, listed := values2[k]
		if len(values2) > 0 && !listed {
			return Outcome{}, fmt.Errorf("%w: the service does not list the parameter %s of %s", ErrUnsupported, k, action)
		}
		if s, isStr := v.(string); isStr && len(allowed) > 0 && !slices.Contains(allowed, s) {
			return Outcome{}, fmt.Errorf("%w: %s %q is not supported by the service (supported: %s)", ErrUnsupported, k, s, strings.Join(allowed, ", "))
		}
	}
	r, err := c.Post(ctx, target, body)
	if err != nil {
		return Outcome{}, err
	}
	return outcomeOf(r, Outcome{Changed: true, Message: "Submitted the test metric report " + name}), nil
}

// ---- Streaming ----

// MetricReportEvents opens the SSE stream of the service for metric reports:
// EventsWith with the filter `EventFormatType eq 'MetricReport'` (DSP0266 12.5.2:
// "The service sends events of the matching EventFormatType"), joined by `and`
// with o.Filter when there is one. It is the EventStream of Events, not another
// reader: each event arrives with Event.Report set to the decoded MetricReport.
// ErrUnsupported when the EventService lists its EventFormatTypes without
// MetricReport.
func (c *Client) MetricReportEvents(ctx context.Context, o EventsOptions) (*EventStream, error) {
	es, err := c.EventService(ctx)
	if err != nil {
		return nil, err
	}
	if len(es.EventFormatTypes) > 0 && !slices.Contains(es.EventFormatTypes, "MetricReport") {
		return nil, fmt.Errorf("%w: the EventService does not list the EventFormatType MetricReport", ErrUnsupported)
	}
	const f = "EventFormatType eq 'MetricReport'"
	if o.Filter == "" {
		o.Filter = f
	} else {
		o.Filter = "(" + o.Filter + ") and " + f
	}
	return c.EventsWith(ctx, o)
}

// ---- The Meter adapter ----

// EnergyUnit is the unit of an energy series.
type EnergyUnit string

// The units ReadingsFromSeries converts to kWh.
const (
	UnitKWh EnergyUnit = "kWh"
	UnitWh  EnergyUnit = "Wh"
	UnitJ   EnergyUnit = "J"
)

// ReadingsFromSeries turns series of a MetricReport into the Readings a Meter
// observes, for a caller that wants to feed one: it is opt-in, and changes
// neither Read nor the Meter. The caller says which series is an energy counter
// (and its unit: the metric definition's Units says which) and which is a power
// in watts; either may be nil. Points of the two series at the same instant make
// one Reading; the result is sorted by time. The Meter decides what a Reading is
// worth (a reset, a gap, a counter that went down): this only converts and
// orders, and refuses a value that is negative, not finite or a unit it does not
// know.
func ReadingsFromSeries(chassis string, energy *Series, unit EnergyUnit, power *Series) ([]Reading, error) {
	factor := 1.0
	if energy != nil {
		switch unit {
		case UnitKWh:
		case UnitWh:
			factor = 1.0 / 1000
		case UnitJ:
			factor = KWhPerJoule
		default:
			return nil, fmt.Errorf("redfish: unknown energy unit %q (kWh, Wh or J)", clip(string(unit)))
		}
	}
	byTime := map[time.Time]*Reading{}
	var order []time.Time
	add := func(at time.Time, set func(r *Reading)) {
		r := byTime[at]
		if r == nil {
			r = &Reading{Chassis: chassis, At: at}
			byTime[at] = r
			order = append(order, at)
		}
		set(r)
	}
	check := func(what string, v float64) error {
		if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
			return fmt.Errorf("redfish: the %s series holds %v: a counter or a power is finite and not negative", what, v)
		}
		return nil
	}
	if energy != nil {
		for _, p := range energy.Points {
			if err := check("energy", p.Value); err != nil {
				return nil, err
			}
			v := p.Value * factor
			add(p.At, func(r *Reading) { r.EnergyKWh = &v })
		}
	}
	if power != nil {
		for _, p := range power.Points {
			if err := check("power", p.Value); err != nil {
				return nil, err
			}
			v := p.Value
			add(p.At, func(r *Reading) { r.PowerWatts = &v })
		}
	}
	sort.SliceStable(order, func(i, j int) bool { return order[i].Before(order[j]) })
	out := make([]Reading, 0, len(order))
	for _, at := range order {
		out = append(out, *byTime[at])
	}
	return out, nil
}
