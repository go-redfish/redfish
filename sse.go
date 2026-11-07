package redfish

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"strconv"
	"time"
)

// Server-sent events. The wire format is the one of the WHATWG HTML Living
// Standard 9.2 (9.2.5 Parsing an event stream, 9.2.6 Interpreting an event
// stream) that DSP0266 1.23.0 12.5.1 and 12.5.2 name as the format of the
// Redfish SSE streams; the parser below follows 9.2.6: UTF-8 with one leading BOM
// dropped, lines ended by CRLF, LF or CR, a line that starts with a colon is a
// comment, the field is what precedes the first colon and the value what follows
// it without one leading space, the fields event, data, id and retry are
// interpreted and any other is ignored, a blank line dispatches the event, an
// event whose data buffer is empty is not dispatched, the trailing LF of the data
// is removed, an id that holds a NUL is ignored, a retry that is not only ASCII
// digits is ignored, and what is pending at the end of the stream is discarded.
//
// Unlike a browser, the parser is bounded: a line and an event larger than the
// limits are refused (ErrEventTooLarge), never buffered. DSP0266 12.1.2 says a
// service shall not send a payload larger than 1 MiB, which is the default of
// both limits.

// Limits of a stream.
const (
	// MaxEventBytes bounds the data of one event (DSP0266 12.1.2: 1 MiB).
	MaxEventBytes = 1 << 20
	// MaxSSELine bounds one line of the stream.
	MaxSSELine = 1 << 20
)

// ErrEventTooLarge: a line or an event of the stream is larger than the limit.
var ErrEventTooLarge = errors.New("redfish: an event is larger than the limit")

// SSEMessage is one dispatched event of a stream.
type SSEMessage struct {
	// ID is the last event ID of the stream when the event was dispatched (the
	// id field: HTML 9.2.6 keeps it from an event to the next).
	ID string
	// Type is the event field, "message" when the event has none.
	Type string
	// Data is the data field, its lines joined by LF.
	Data []byte
}

// SSEReader parses an event stream.
type SSEReader struct {
	br       *bufio.Reader
	maxLine  int
	maxEvent int
	first    bool
	skipLF   bool
	lastID   string
	retry    time.Duration
	buf      []byte
}

// NewSSEReader reads the events of r. A limit that is not above 0 is the default
// (MaxSSELine, MaxEventBytes).
func NewSSEReader(r io.Reader, maxLine, maxEvent int) *SSEReader {
	if maxLine <= 0 {
		maxLine = MaxSSELine
	}
	if maxEvent <= 0 {
		maxEvent = MaxEventBytes
	}
	return &SSEReader{br: bufio.NewReaderSize(r, 4096), maxLine: maxLine, maxEvent: maxEvent, first: true}
}

// LastID is the last event ID seen: what a reconnection sends in Last-Event-ID.
func (s *SSEReader) LastID() string { return s.lastID }

// Retry is the reconnection time the stream asked for (the retry field), 0 if it
// asked for none.
func (s *SSEReader) Retry() time.Duration { return s.retry }

// line reads one line, without its terminator. io.EOF at the end of the stream,
// ErrEventTooLarge when the line is longer than the limit.
func (s *SSEReader) line() ([]byte, error) {
	buf := s.buf[:0]
	for {
		b, err := s.br.ReadByte()
		if err != nil {
			return nil, err
		}
		if s.skipLF {
			s.skipLF = false
			if b == '\n' {
				continue
			}
		}
		switch b {
		case '\n':
			s.buf = buf
			return buf, nil
		case '\r':
			// CRLF is one terminator; the byte after a CR is looked at only if it
			// has arrived, so that a stream of CR terminators is not held back.
			if s.br.Buffered() > 0 {
				if nb, _ := s.br.Peek(1); nb[0] == '\n' {
					_, _ = s.br.Discard(1)
				}
			} else {
				s.skipLF = true
			}
			s.buf = buf
			return buf, nil
		}
		if len(buf) >= s.maxLine {
			return nil, ErrEventTooLarge
		}
		buf = append(buf, b)
	}
}

// Next returns the next event. At the end of the stream it returns io.EOF (an
// event that a blank line did not complete is dropped); a read error is returned
// as it is.
func (s *SSEReader) Next() (SSEMessage, error) {
	var data []byte
	var typ string
	for {
		ln, err := s.line()
		if err != nil {
			return SSEMessage{}, err
		}
		if s.first {
			s.first = false
			ln = bytes.TrimPrefix(ln, []byte("\xef\xbb\xbf"))
		}
		if len(ln) == 0 { // dispatch
			if len(data) == 0 {
				typ = ""
				continue
			}
			data = bytes.TrimSuffix(data, []byte("\n"))
			if typ == "" {
				typ = "message"
			}
			return SSEMessage{ID: s.lastID, Type: typ, Data: data}, nil
		}
		if ln[0] == ':' {
			continue
		}
		name, value := ln, []byte(nil)
		if i := bytes.IndexByte(ln, ':'); i >= 0 {
			name, value = ln[:i], ln[i+1:]
			value = bytes.TrimPrefix(value, []byte(" "))
		}
		switch string(name) {
		case "event":
			typ = string(value)
		case "data":
			if len(data)+len(value)+1 > s.maxEvent {
				return SSEMessage{}, ErrEventTooLarge
			}
			data = append(data, value...)
			data = append(data, '\n')
		case "id":
			if !bytes.ContainsRune(value, 0) {
				s.lastID = string(value)
			}
		case "retry":
			if ms, ok := digits(value); ok {
				s.retry = time.Duration(ms) * time.Millisecond
			}
		}
	}
}

// digits parses a value made only of ASCII digits (at most 9, so that the delay
// cannot overflow).
func digits(v []byte) (int64, bool) {
	if len(v) == 0 || len(v) > 9 {
		return 0, false
	}
	for _, c := range v {
		if c < '0' || c > '9' {
			return 0, false
		}
	}
	n, err := strconv.ParseInt(string(v), 10, 64)
	return n, err == nil
}
