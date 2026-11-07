package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/go-redfish/redfish"
	"github.com/spf13/cobra"
)

// maxHeadersFile bounds a headers file.
const maxHeadersFile = 4096

// readHeaders reads the HTTP headers a subscription asks the service to send
// with every event (EventDestination.HttpHeaders), one `Name: value` per line;
// blank lines and lines that start with # are skipped. They are credentials (an
// Authorization for the receiver): a Secret each, and an error names a line
// number, never its text.
func readHeaders(r io.Reader) (map[string]redfish.Secret, error) {
	b, err := io.ReadAll(io.LimitReader(r, maxHeadersFile+1))
	if err != nil {
		return nil, errors.New("reading the headers failed")
	}
	defer clear(b)
	if len(b) > maxHeadersFile {
		return nil, fmt.Errorf("the headers are larger than %d bytes", maxHeadersFile)
	}
	out := map[string]redfish.Secret{}
	for i, ln := range bytes.Split(b, []byte("\n")) {
		ln = bytes.TrimSuffix(ln, []byte("\r"))
		if len(bytes.TrimSpace(ln)) == 0 || ln[0] == '#' {
			continue
		}
		name, value, ok := bytes.Cut(ln, []byte(":"))
		name, value = bytes.TrimSpace(name), bytes.TrimSpace(value)
		if !ok || len(name) == 0 || len(value) == 0 {
			return nil, fmt.Errorf("line %d: want `Name: value`", i+1)
		}
		for _, c := range append(append([]byte{}, name...), value...) {
			if (c < 0x20 && c != '\t') || c == 0x7f {
				return nil, fmt.Errorf("line %d: a control character", i+1)
			}
		}
		if _, dup := out[string(name)]; dup {
			return nil, fmt.Errorf("line %d: a header given twice", i+1)
		}
		if len(out) >= 16 {
			return nil, errors.New("at most 16 headers")
		}
		out[string(name)] = redfish.Secret(value)
	}
	if len(out) == 0 {
		return nil, errors.New("the headers are empty")
	}
	return out, nil
}

func eventsCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "events",
		Short: "EventService: status, subscriptions, subscribe, unsubscribe, test, listen",
		Long: `events drives the EventService (DSP0266 1.23.0 12.1 Eventing and 12.5
Server-sent events): status reads the service and what a subscription may use,
subscriptions lists them, subscribe and unsubscribe create and delete one (a POST
to the Subscriptions collection and a DELETE of the member), test submits a test
event (SubmitTestEvent) and listen opens the SSE stream and prints the events as
they arrive until it is interrupted (Ctrl-C), or --count events were read.`,
		Args: cobra.NoArgs,
	}
	cmd.AddCommand(eventsStatusCmd(g), eventsSubscriptionsCmd(g), eventsSubscribeCmd(g), eventsUnsubscribeCmd(g), eventsTestCmd(g), eventsListenCmd(g))
	return cmd
}

func eventsStatusCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "The EventService: enabled, SSE URI, supported formats, registries, resource types and SSE filters",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := g.client(cmd, false)
			if err != nil {
				return err
			}
			es, err := c.EventService(cmd.Context())
			if err != nil {
				return err
			}
			return g.show(cmd, es)
		},
	}
}

func eventsSubscriptionsCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "subscriptions [ID]",
		Short: "List the subscriptions, or show one (the HTTP headers are never returned by a service)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := g.client(cmd, false)
			if err != nil {
				return err
			}
			if len(args) == 1 {
				s, err := c.Subscription(cmd.Context(), args[0])
				if err != nil {
					return err
				}
				return g.show(cmd, s)
			}
			subs, err := c.Subscriptions(cmd.Context())
			if err != nil {
				return err
			}
			return g.show(cmd, subs)
		},
	}
}

func eventsSubscribeCmd(g *globals) *cobra.Command {
	var o redfish.SubscribeOptions
	var format, headersFile string
	var headersStdin, subordinate bool
	cmd := &cobra.Command{
		Use:   "subscribe --destination URL",
		Short: "Create a subscription: the service will POST events to the destination",
		Long: `subscribe creates an EventDestination (a POST to the Subscriptions collection,
DSP0266 12.1.2). The destination must be an https URL without credentials, query
or fragment. The filters (--registry-prefix, --resource-type, --message-id,
--origin...) are checked against what the EventService supports before anything
is sent.

The HTTP headers the service sends with every event (an Authorization for the
receiver) are credentials: they come from --headers-file (mode 0600) or
--headers-stdin, one "Name: value" per line, never from a flag, and are never
printed. --context is stored by the service and returned in every event: it is
not a secret.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			opt := o
			opt.EventFormatType = format
			if cmd.Flags().Changed("subordinate") {
				opt.SubordinateResources = &subordinate
			}
			hdrs, err := headersFrom(cmd, headersFile, headersStdin)
			if err != nil {
				return err
			}
			opt.HTTPHeaders = hdrs
			c, err := g.client(cmd, false)
			if err != nil {
				return err
			}
			sub, out, err := c.Subscribe(cmd.Context(), opt)
			if err != nil {
				return err
			}
			return g.show(cmd, map[string]any{"changed": out.Changed, "message": out.Message, "subscription": sub})
		},
	}
	f := cmd.Flags()
	f.StringVar(&o.Destination, "destination", "", "the https URL the events are posted to (required)")
	f.StringVar(&o.Context, "context", "", "a string the service returns in every event (not a secret)")
	f.StringVar(&format, "format", "", "the event format: Event (default) or MetricReport")
	f.StringArrayVar(&o.RegistryPrefixes, "registry-prefix", nil, "send only events of this message registry, e.g. Base (repeatable)")
	f.StringArrayVar(&o.ExcludeRegistryPrefixes, "exclude-registry-prefix", nil, "do not send events of this message registry (repeatable)")
	f.StringArrayVar(&o.ResourceTypes, "resource-type", nil, "send only events about this resource type, e.g. ComputerSystem (repeatable)")
	f.StringArrayVar(&o.MessageIDs, "message-id", nil, "send only events with this MessageId, e.g. Base.ResourceCreated (repeatable)")
	f.StringArrayVar(&o.ExcludeMessageIDs, "exclude-message-id", nil, "do not send events with this MessageId (repeatable)")
	f.StringArrayVar(&o.OriginResources, "origin", nil, "send only events about this resource, a path (repeatable)")
	f.BoolVar(&subordinate, "subordinate", false, "with --origin: include the resources below it")
	f.StringVar(&headersFile, "headers-file", "", "file of the HTTP headers the service sends with every event, Name: value per line (mode 0600)")
	f.BoolVar(&headersStdin, "headers-stdin", false, "read those headers from the standard input")
	_ = cmd.MarkFlagRequired("destination")
	return cmd
}

// headersFrom reads the headers from the file or the standard input; none given:
// nil.
func headersFrom(cmd *cobra.Command, file string, stdin bool) (map[string]redfish.Secret, error) {
	switch {
	case file != "" && stdin:
		return nil, errors.New("--headers-file and --headers-stdin are exclusive")
	case file != "":
		f, err := openSecretFile(file)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		h, err := readHeaders(f)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", file, err)
		}
		return h, nil
	case stdin:
		return readHeaders(cmd.InOrStdin())
	}
	return nil, nil
}

func eventsUnsubscribeCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "unsubscribe ID|URI",
		Short: "Delete a subscription",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := g.client(cmd, false)
			if err != nil {
				return err
			}
			out, err := c.Unsubscribe(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			return g.showOutcome(cmd, out)
		},
	}
}

func eventsTestCmd(g *globals) *cobra.Command {
	var t redfish.TestEvent
	var group int64
	cmd := &cobra.Command{
		Use:   "test --message-id ID",
		Short: "Submit a test event (SubmitTestEvent)",
		Long: `test posts the EventService.SubmitTestEvent action: the service adds an event to
its stream and sends it to the matching destinations. Each parameter is checked
against the service's ActionInfo before the POST. A service without
SubmitTestEvent answers "not supported".`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ev := t
			if cmd.Flags().Changed("group-id") {
				ev.EventGroupID = &group
			}
			c, err := g.client(cmd, false)
			if err != nil {
				return err
			}
			out, err := c.SubmitTestEvent(cmd.Context(), ev)
			if err != nil {
				return err
			}
			return g.showOutcome(cmd, out)
		},
	}
	f := cmd.Flags()
	f.StringVar(&t.MessageID, "message-id", "", "the MessageId, Registry.Major.Minor.Key (required)")
	f.StringVar(&t.Message, "message", "", "the human-readable message")
	f.StringArrayVar(&t.MessageArgs, "arg", nil, "a message argument (repeatable)")
	f.StringVar(&t.Severity, "severity", "", "the severity")
	f.StringVar(&t.MessageSeverity, "message-severity", "", "the message severity: OK, Warning or Critical")
	f.StringVar(&t.OriginOfCondition, "origin", "", "the OriginOfCondition, a path of the service")
	f.StringVar(&t.EventID, "event-id", "", "the EventId")
	f.Int64Var(&group, "group-id", 0, "the EventGroupId")
	f.StringVar(&t.EventTimestamp, "timestamp", "", "the EventTimestamp, a date-time")
	f.StringVar(&t.EventType, "event-type", "", "the EventType (deprecated since EventService 1.3)")
	_ = cmd.MarkFlagRequired("message-id")
	return cmd
}

func eventsListenCmd(g *globals) *cobra.Command {
	var o redfish.EventsOptions
	var count int
	cmd := &cobra.Command{
		Use:   "listen",
		Short: "Open the SSE stream and print the events as they arrive (Ctrl-C ends it)",
		Long: `listen opens the ServerSentEventUri of the EventService (DSP0266 12.5) with the
session or the password of the connection flags, over https, and prints each
event: with -o json one JSON document per line, {"id": ..., "event": <the Event>},
with -o table one line per record. The stream is bounded: a line or an event
larger than --max-line or --max-event-bytes ends it with an error. After a
broken connection it reconnects up to --reconnects times with Last-Event-ID (the
service "should accept" it, DSP0266 12.5.2); an end by the service, a Ctrl-C or
--count events end it cleanly.

--filter is the $filter of the SSE URI, e.g.
  --filter "MessageId eq 'Contoso.1.0.TempAssert'"
  --filter "(RegistryPrefix eq 'Resource') or (RegistryPrefix eq 'Task')"
The service applies it; a property it does not support is refused.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := g.client(cmd, false)
			if err != nil {
				return err
			}
			s, err := c.EventsWith(cmd.Context(), o)
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
	f.StringVar(&o.Filter, "filter", "", "the $filter of the SSE URI")
	f.IntVar(&o.MaxReconnects, "reconnects", 3, "how many times a broken connection is reopened with Last-Event-ID (0: never)")
	f.IntVar(&o.MaxLine, "max-line", 0, "the longest line accepted, in bytes (default 1 MiB)")
	f.IntVar(&o.MaxEvent, "max-event-bytes", 0, "the largest event accepted, in bytes (default 1 MiB, DSP0266 12.1.2)")
	f.IntVar(&count, "count", 0, "stop after this many events (default: until interrupted)")
	return cmd
}

// printEvent writes one event: a JSON line, or the records as table lines.
func (g *globals) printEvent(w io.Writer, ev redfish.Event) error {
	if g.output != "table" {
		line := map[string]any{"id": ev.ID}
		if ev.Err != nil {
			line["error"] = clean(ev.Err.Error())
		} else {
			var buf bytes.Buffer
			if err := json.Compact(&buf, ev.Data); err != nil {
				line["error"] = "the event is not JSON"
			} else {
				line["event"] = json.RawMessage(buf.Bytes())
			}
		}
		b, err := json.Marshal(line)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(w, "%s\n", b)
		return err
	}
	if ev.Err != nil {
		_, err := fmt.Fprintf(w, "%s\t(undecodable event: %s)\n", clean(ev.ID), clean(ev.Err.Error()))
		return err
	}
	if len(ev.Records) == 0 {
		_, err := fmt.Fprintf(w, "%s\t%s\n", clean(ev.ID), clean(strings.TrimPrefix(ev.ODataType, "#")))
		return err
	}
	for _, r := range ev.Records {
		sev := r.MessageSeverity
		if sev == "" {
			sev = r.Severity
		}
		if _, err := fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", clean(ev.ID), clean(r.EventTimestamp), clean(sev), clean(r.MessageID), clean(r.Message)); err != nil {
			return err
		}
	}
	return nil
}
