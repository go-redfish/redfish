package redfish_test

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	"github.com/go-redfish/redfish"
)

// readAll parses a whole stream.
func readAll(stream string, maxLine, maxEvent int) ([]redfish.SSEMessage, error) {
	r := redfish.NewSSEReader(strings.NewReader(stream), maxLine, maxEvent)
	var out []redfish.SSEMessage
	for {
		m, err := r.Next()
		if err != nil {
			if errors.Is(err, io.EOF) {
				err = nil
			}
			return out, err
		}
		out = append(out, m)
	}
}

// The cases are the rules of WHATWG HTML 9.2.5 and 9.2.6.
func TestSSEReaderRules(t *testing.T) {
	type msg struct{ id, typ, data string }
	cases := []struct {
		name, in string
		want     []msg
	}{
		{"one event", "data: x\n\n", []msg{{"", "message", "x"}}},
		{"multi-line data joined by LF", "data: a\ndata: b\n\n", []msg{{"", "message", "a\nb"}}},
		{"no space after the colon", "data:x\n\n", []msg{{"", "message", "x"}}},
		{"only one space is dropped", "data:  x\n\n", []msg{{"", "message", " x"}}},
		{"no colon: empty value", "data\ndata\n\n", []msg{{"", "message", "\n"}}},
		{"CRLF", "id: 1\r\ndata: x\r\n\r\n", []msg{{"1", "message", "x"}}},
		{"CR", "id: 1\rdata: x\r\r", []msg{{"1", "message", "x"}}},
		{"mixed terminators", "data: a\r\ndata: b\rdata: c\n\n", []msg{{"", "message", "a\nb\nc"}}},
		{"comment ignored", ": keep-alive\ndata: x\n: another\n\n", []msg{{"", "message", "x"}}},
		{"BOM dropped", "\xef\xbb\xbfdata: x\n\n", []msg{{"", "message", "x"}}},
		{"event type", "event: ping\ndata: x\n\n", []msg{{"", "ping", "x"}}},
		{"event type not kept", "event: ping\ndata: x\n\ndata: y\n\n", []msg{{"", "ping", "x"}, {"", "message", "y"}}},
		{"id persists", "id: 5\ndata: a\n\ndata: b\n\nid: 6\ndata: c\n\n", []msg{{"5", "message", "a"}, {"5", "message", "b"}, {"6", "message", "c"}}},
		{"id with NUL ignored", "id: 5\ndata: a\n\nid: 6\x007\ndata: b\n\n", []msg{{"5", "message", "a"}, {"5", "message", "b"}}},
		{"empty id resets", "id: 5\ndata: a\n\nid\ndata: b\n\n", []msg{{"5", "message", "a"}, {"", "message", "b"}}},
		{"no data: not dispatched", "event: x\n\nid: 3\n\ndata: y\n\n", []msg{{"3", "message", "y"}}},
		{"unknown field ignored", "foo: bar\ndata: x\n\n", []msg{{"", "message", "x"}}},
		{"field names are case sensitive", "DATA: x\n\n", nil},
		{"incomplete event dropped at the end", "data: x\n\ndata: y", []msg{{"", "message", "x"}}},
		{"blank lines alone", "\n\n\n", nil},
		{"data empty value", "data:\n\n", []msg{{"", "message", ""}}},
	}
	for _, tc := range cases {
		got, err := readAll(tc.in, 0, 0)
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		if len(got) != len(tc.want) {
			t.Errorf("%s: %d events, want %d (%+v)", tc.name, len(got), len(tc.want), got)
			continue
		}
		for i, w := range tc.want {
			if got[i].ID != w.id || got[i].Type != w.typ || string(got[i].Data) != w.data {
				t.Errorf("%s: event %d = %+v, want %+v", tc.name, i, got[i], w)
			}
		}
	}
}

func TestSSEReaderRetry(t *testing.T) {
	r := redfish.NewSSEReader(strings.NewReader("retry: 1500\ndata: x\n\nretry: 12x\nretry: 99999999999\ndata: y\n\n"), 0, 0)
	if _, err := r.Next(); err != nil || r.Retry() != 1500*time.Millisecond {
		t.Fatalf("retry = %v, %v", r.Retry(), err)
	}
	if _, err := r.Next(); err != nil || r.Retry() != 1500*time.Millisecond {
		t.Fatalf("a retry that is not digits, or too long, changed it: %v, %v", r.Retry(), err)
	}
}

// A stream that arrives byte by byte and one that arrives whole give the same
// events; a lone CR does not hold an event back.
func TestSSEReaderChunking(t *testing.T) {
	in := "id: 1\r\ndata: {\r\ndata:   \"a\": 1\r\ndata: }\r\n\r\nid: 2\rdata: z\r\r"
	whole, err := readAll(in, 0, 0)
	if err != nil || len(whole) != 2 {
		t.Fatalf("whole: %+v, %v", whole, err)
	}
	r := redfish.NewSSEReader(iotest.OneByteReader(strings.NewReader(in)), 0, 0)
	for i := 0; i < 2; i++ {
		m, err := r.Next()
		if err != nil || !bytes.Equal(m.Data, whole[i].Data) || m.ID != whole[i].ID {
			t.Fatalf("one byte at a time, event %d: %+v, %v", i, m, err)
		}
	}
	// Dispatch must not wait for a byte after the final CR.
	pr, pw := io.Pipe()
	done := make(chan redfish.SSEMessage, 1)
	go func() {
		m, _ := redfish.NewSSEReader(pr, 0, 0).Next()
		done <- m
	}()
	_, _ = pw.Write([]byte("data: x\r\r"))
	select {
	case m := <-done:
		if string(m.Data) != "x" {
			t.Fatalf("data = %q", m.Data)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("an event ended by CR CR was held back")
	}
	_ = pw.Close()
}

func TestSSEReaderBounds(t *testing.T) {
	long := strings.Repeat("x", 5000)
	for name, c := range map[string]struct {
		in                string
		maxLine, maxEvent int
	}{
		"line":                     {"data: " + long + "\n\n", 1000, 0},
		"comment line":             {": " + long + "\n", 1000, 0},
		"line without terminator":  {"data: " + long, 1000, 0},
		"event":                    {strings.Repeat("data: "+strings.Repeat("y", 90)+"\n", 20) + "\n", 0, 1000},
		"event of one line":        {"data: " + long + "\n\n", 0, 1000},
		"unknown field line":       {"foo: " + long + "\n", 1000, 0},
		"many data lines of empty": {strings.Repeat("data\n", 2000) + "\n", 0, 1000},
	} {
		if _, err := readAll(c.in, c.maxLine, c.maxEvent); !errors.Is(err, redfish.ErrEventTooLarge) {
			t.Errorf("%s: %v, want ErrEventTooLarge", name, err)
		}
	}
	// At the limit it passes.
	if got, err := readAll("data: "+strings.Repeat("a", 994)+"\n\n", 1000, 1000); err != nil || len(got) != 1 {
		t.Errorf("at the limit: %v, %d events", err, len(got))
	}
	// A stream of blank lines and comments is not buffered: it ends.
	if got, err := readAll(strings.Repeat(": c\n\n", 100000), 0, 0); err != nil || len(got) != 0 {
		t.Errorf("comments: %v, %d events", err, len(got))
	}
	// A read error is returned, not mistaken for the end.
	r := redfish.NewSSEReader(iotest.ErrReader(errors.New("boom")), 0, 0)
	if _, err := r.Next(); err == nil || errors.Is(err, io.EOF) {
		t.Errorf("read error: %v", err)
	}
}

// The example of DSP0266 1.23.0 12.5.2.1.
const specEventStream = `id: 1
data:{
data:    "@odata.type": "#Event.v1_6_0.Event",
data:    "Id": "1",
data:    "Name": "Event Array",
data:    "Context": "ABCDEFGH",
data:    "Events": [
data:        {
data:            "MemberId": "1",
data:            "EventType": "Alert",
data:            "EventId": "1",
data:            "Severity": "Warning",
data:            "MessageSeverity": "Warning",
data:            "EventTimestamp": "2017-11-23T17:17:42-0600",
data:            "Message": "The LAN has been disconnected",
data:            "MessageId": "Alert.1.0.LanDisconnect",
data:            "MessageArgs": [
data:                "EthernetInterface 1",
data:                "/redfish/v1/Systems/1"
data:            ],
data:            "OriginOfCondition": {
data:                "@odata.id": "/redfish/v1/Systems/1/EthernetInterfaces/1"
data:            },
data:            "Context": "ABCDEFGH"
data:        }
data:    ]
data:}

`

func TestSpecEventExample(t *testing.T) {
	msgs, err := readAll(specEventStream, 0, 0)
	if err != nil || len(msgs) != 1 || msgs[0].ID != "1" {
		t.Fatalf("stream: %+v, %v", msgs, err)
	}
	ev, err := redfish.ParseEvent(msgs[0].Data)
	if err != nil {
		t.Fatal(err)
	}
	if ev.ODataType != "#Event.v1_6_0.Event" || ev.EventID != "1" || ev.Name != "Event Array" || ev.Context != "ABCDEFGH" || len(ev.Records) != 1 {
		t.Fatalf("event = %+v", ev)
	}
	r := ev.Records[0]
	if r.EventType != "Alert" || r.MessageID != "Alert.1.0.LanDisconnect" || len(r.MessageArgs) != 2 || r.MessageArgs[1] != "/redfish/v1/Systems/1" ||
		r.OriginOfCondition != "/redfish/v1/Systems/1/EthernetInterfaces/1" || r.MessageSeverity != "Warning" || r.Context != "ABCDEFGH" {
		t.Fatalf("record = %+v", r)
	}
}

func TestParseEvent(t *testing.T) {
	// A MetricReport (12.5.2.2) is not an Event but is a valid payload.
	mr, err := redfish.ParseEvent([]byte(`{"@odata.id":"/redfish/v1/TelemetryService/MetricReports/R","@odata.type":"#MetricReport.v1_3_0.MetricReport","Id":"R","MetricValues":[]}`))
	if err != nil || !mr.IsMetricReport() || len(mr.Records) != 0 || mr.EventID != "R" {
		t.Fatalf("metric report = %+v, %v", mr, err)
	}
	for name, doc := range map[string]string{
		"not JSON":        `data`,
		"not an object":   `[1]`,
		"null":            `null`,
		"no Events":       `{"@odata.type":"#Event.v1_6_0.Event","Id":"1"}`,
		"Events a string": `{"Events":"x"}`,
		"record a number": `{"Events":[1]}`,
		"MessageId typed": `{"Events":[{"MessageId":5}]}`,
		"too many":        `{"Events":[` + strings.Repeat(`{},`, redfish.MaxEventRecords) + `{}]}`,
	} {
		if _, err := redfish.ParseEvent([]byte(doc)); !errors.Is(err, redfish.ErrMalformed) {
			t.Errorf("%s: %v, want ErrMalformed", name, err)
		}
	}
	if _, err := redfish.ParseEvent(make([]byte, redfish.MaxEventBytes+1)); !errors.Is(err, redfish.ErrEventTooLarge) {
		t.Errorf("too large: %v", err)
	}
	// Tolerated: unknown properties, a numeric Id, null origin.
	ev, err := redfish.ParseEvent([]byte(`{"Id":7,"Events":[{"MessageId":"A.1.0.B","Extra":{"x":1},"OriginOfCondition":null,"EventGroupId":3,"MessageArgs":[1,"a",null]}],"Oem":{}}`))
	if err != nil || ev.EventID != "7" || ev.Records[0].EventGroupID == nil || *ev.Records[0].EventGroupID != 3 || len(ev.Records[0].MessageArgs) != 3 {
		t.Fatalf("lenient decode = %+v, %v", ev, err)
	}
}

// ---- Fuzz ----

// The parser takes what a BMC sends, split anywhere: it never panics, never
// returns more than its limits, and chunking the input does not change the events.
func FuzzSSEReader(f *testing.F) {
	f.Add([]byte(specEventStream), 7)
	f.Add([]byte("retry: 10\r\nid: 4\r\ndata: a\r\ndata: b\r\n\r\n: c\r\rdata: z\r\r"), 1)
	f.Add([]byte("\xef\xbb\xbfid\x00: x\nevent\n\ndata"), 3)
	f.Add([]byte(strings.Repeat("data: "+strings.Repeat("a", 300)+"\n", 8)+"\n"), 1000)
	f.Fuzz(func(t *testing.T, data []byte, chunk int) {
		const maxLine, maxEvent = 512, 1024
		run := func(r io.Reader) ([]redfish.SSEMessage, error) {
			rd := redfish.NewSSEReader(r, maxLine, maxEvent)
			var out []redfish.SSEMessage
			for len(out) < 10000 {
				m, err := rd.Next()
				if err != nil {
					return out, err
				}
				if len(m.Data) > maxEvent {
					t.Fatalf("an event of %d bytes passed the limit %d", len(m.Data), maxEvent)
				}
				m.Data = bytes.Clone(m.Data)
				out = append(out, m)
			}
			return out, nil
		}
		whole, werr := run(bytes.NewReader(data))
		if chunk < 1 {
			chunk = 1
		}
		parts, perr := run(&chunkReader{data: data, n: chunk})
		if (werr == nil || errors.Is(werr, io.EOF)) != (perr == nil || errors.Is(perr, io.EOF)) || len(whole) != len(parts) {
			t.Fatalf("chunking changed the result: %d/%v vs %d/%v", len(whole), werr, len(parts), perr)
		}
		for i := range whole {
			if whole[i].ID != parts[i].ID || whole[i].Type != parts[i].Type || !bytes.Equal(whole[i].Data, parts[i].Data) {
				t.Fatalf("event %d differs", i)
			}
		}
	})
}

// chunkReader gives at most n bytes per Read.
type chunkReader struct {
	data []byte
	n    int
}

func (c *chunkReader) Read(p []byte) (int, error) {
	if len(c.data) == 0 {
		return 0, io.EOF
	}
	k := min(c.n, len(p), len(c.data))
	copy(p, c.data[:k])
	c.data = c.data[k:]
	return k, nil
}

func FuzzParseEvent(f *testing.F) {
	f.Add([]byte(`{"@odata.type":"#Event.v1_6_0.Event","Id":"1","Context":"c","Events":[{"MessageId":"A.1.0.B","MessageArgs":["x",1],"OriginOfCondition":{"@odata.id":"/redfish/v1/x"},"EventGroupId":2}]}`))
	f.Add([]byte(`{"@odata.type":"#MetricReport.v1_3_0.MetricReport","Id":"R"}`))
	f.Add([]byte(`{"Events":[null,{"Severity":5}],"Id":[1]}`))
	f.Add([]byte(`[]`))
	f.Fuzz(func(t *testing.T, data []byte) {
		ev, err := redfish.ParseEvent(data)
		if err != nil {
			return
		}
		if len(ev.Records) > redfish.MaxEventRecords {
			t.Fatalf("%d records", len(ev.Records))
		}
		if !bytes.Equal(ev.Data, data) {
			t.Fatal("Data is not the payload")
		}
	})
}

func FuzzCheckDestination(f *testing.F) {
	for _, s := range []string{"https://h.example/x", "https://u:p@h/x", "http://h/x", "https://h/x?a=b", "https://h:99999/", "https://h/#f", "redfish-sse://1.2.3.4:5", "https://h/a b", ""} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, dest string) {
		if err := redfish.CheckDestination(dest); err != nil {
			return
		}
		// What is accepted is an https URL with a host and nothing that carries a credential.
		for _, bad := range []string{"?", "#", " ", "\r", "\n", "\x00"} {
			if strings.Contains(dest, bad) {
				t.Fatalf("%q accepted with %q in it", dest, bad)
			}
		}
		if !strings.HasPrefix(strings.ToLower(dest), "https://") { // the scheme is case-insensitive (RFC 3986 3.1)
			t.Fatalf("%q accepted without https", dest)
		}
	})
}
