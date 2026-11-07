package redfish

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Events: the EventService, its subscriptions and the test event. There is no
// Ansible oracle for this area: the references are DSP0266 1.23.0 (read as
// published 2025-09-05) 12.1 Eventing (12.1.1 overview, 12.1.2 POST to
// subscription collection, 12.1.3 Open an SSE connection, 12.1.5 Subscribing to
// events, 12.1.6 Event formats), 12.5 Server-sent events and 13.7 Event
// subscriptions, and the schemas of DSP8010 2025.4: EventService 1.12.0,
// EventDestination 1.17.1 and Event 1.14.0. SubmitTestEvent is an action of the
// EventService schema (v1_12_0 SubmitTestEvent), its parameters are checked
// against the ActionInfo or the AllowableValues of the service like every
// action (DSP0266 9.9.2, 9.9.6, see ActionValues).

// MaxSubscriptions bounds the subscriptions read of a service.
const MaxSubscriptions = MaxMembers

// EventService is the EventService resource (EventService 1.12.0), what a client
// reads to know the control properties a subscription may use (DSP0266 12.1.5:
// "Clients can read the EventService resource of a service in order to
// determine what control properties ... are supported").
type EventService struct {
	URI string `json:"uri"`
	// ServiceEnabled is nil when the service does not say. When false, events are no
	// longer published and an SSE connection cannot be opened.
	ServiceEnabled *bool  `json:"service_enabled,omitempty"`
	State          string `json:"state,omitempty"`
	Health         string `json:"health,omitempty"`
	// SubscriptionsURI is the EventDestinationCollection.
	SubscriptionsURI string `json:"subscriptions_uri,omitempty"`
	// ServerSentEventURI is the SSE endpoint (ServerSentEventUri, DSP0266 12.1.3);
	// empty when the service has none.
	ServerSentEventURI string `json:"server_sent_event_uri,omitempty"`
	// EventFormatTypes are the formats a subscription may ask for (Event,
	// MetricReport); empty: Event only (the schema's default).
	EventFormatTypes []string `json:"event_format_types,omitempty"`
	// RegistryPrefixes, ResourceTypes and Severities are the values a subscription
	// may use; empty means the service does not support that kind of filter.
	RegistryPrefixes []string `json:"registry_prefixes,omitempty"`
	ResourceTypes    []string `json:"resource_types,omitempty"`
	Severities       []string `json:"severities,omitempty"`
	// SSEFilterProperties are the properties of SSEFilterPropertiesSupported: which
	// $filter criteria the SSE URI accepts (EventFormatType, MessageId,
	// OriginResource, RegistryPrefix, ResourceType, SubordinateResources...).
	SSEFilterProperties map[string]bool `json:"sse_filter_properties,omitempty"`
	// Capability flags; nil when the service does not say.
	SSEIncludeOriginOfCondition       *bool  `json:"sse_include_origin_of_condition,omitempty"`
	IncludeOriginOfConditionSupported *bool  `json:"include_origin_of_condition_supported,omitempty"`
	SubordinateResourcesSupported     *bool  `json:"subordinate_resources_supported,omitempty"`
	OriginResourcesSupported          *bool  `json:"origin_resources_supported,omitempty"`
	ExcludeMessageID                  *bool  `json:"exclude_message_id,omitempty"`
	ExcludeRegistryPrefix             *bool  `json:"exclude_registry_prefix,omitempty"`
	DeliveryRetryAttempts             *int64 `json:"delivery_retry_attempts,omitempty"`
	DeliveryRetryIntervalSeconds      *int64 `json:"delivery_retry_interval_seconds,omitempty"`
	// SubmitTestEvent is true when the service has the action.
	SubmitTestEvent bool `json:"submit_test_event"`
}

func boolOf(p Props, key string) *bool {
	if b, ok := p[key].(bool); ok {
		return &b
	}
	return nil
}

func intOf(p Props, key string) *int64 {
	if n, ok := portOf(p[key]); ok {
		return &n
	}
	return nil
}

// eventServiceURI is the EventService the service root links.
func (c *Client) eventServiceURI(ctx context.Context) (string, error) {
	u, err := c.rootLink(ctx, "EventService")
	if errors.Is(err, ErrNotFound) {
		return "", fmt.Errorf("%w: the service root links no EventService", ErrUnsupported)
	}
	return u, err
}

// EventService reads the EventService of the service (ErrUnsupported when the
// service root links none).
func (c *Client) EventService(ctx context.Context) (EventService, error) {
	uri, err := c.eventServiceURI(ctx)
	if err != nil {
		return EventService{}, err
	}
	p, _, err := c.getProps(ctx, uri)
	if err != nil {
		return EventService{}, err
	}
	return parseEventService(uri, p), nil
}

func parseEventService(uri string, p Props) EventService {
	es := EventService{URI: uri, ServiceEnabled: boolOf(p, "ServiceEnabled"),
		State: str(sub(p, "Status"), "State"), Health: str(sub(p, "Status"), "Health"),
		SubscriptionsURI:   linkOf(p, "Subscriptions"),
		ServerSentEventURI: servicePath(str(p, "ServerSentEventUri")),
		EventFormatTypes:   stringList(p["EventFormatTypes"]), RegistryPrefixes: stringList(p["RegistryPrefixes"]),
		ResourceTypes: stringList(p["ResourceTypes"]), Severities: stringList(p["Severities"]),
		SSEIncludeOriginOfCondition:       boolOf(p, "SSEIncludeOriginOfConditionSupported"),
		IncludeOriginOfConditionSupported: boolOf(p, "IncludeOriginOfConditionSupported"),
		SubordinateResourcesSupported:     boolOf(p, "SubordinateResourcesSupported"),
		OriginResourcesSupported:          boolOf(p, "OriginResourcesSupported"),
		ExcludeMessageID:                  boolOf(p, "ExcludeMessageId"), ExcludeRegistryPrefix: boolOf(p, "ExcludeRegistryPrefix"),
		DeliveryRetryAttempts: intOf(p, "DeliveryRetryAttempts"), DeliveryRetryIntervalSeconds: intOf(p, "DeliveryRetryIntervalSeconds"),
	}
	if es.DeliveryRetryIntervalSeconds == nil {
		es.DeliveryRetryIntervalSeconds = intOf(p, "DeliveryRetryIntervalInSeconds") // EventService 1.0
	}
	es.SSEFilterProperties = map[string]bool{}
	for k, v := range sub(p, "SSEFilterPropertiesSupported") {
		if b, ok := v.(bool); ok && !strings.Contains(k, "@") && len(es.SSEFilterProperties) < maxParams {
			es.SSEFilterProperties[k] = b
		}
	}
	_, es.SubmitTestEvent = sub(p, "Actions")["#EventService.SubmitTestEvent"].(map[string]any)
	return es
}

// ---- Subscriptions ----

// Subscription is an EventDestination resource (EventDestination 1.17.1). The
// HttpHeaders of a subscription are never read back: the schema has the service
// answer null or an empty array, as they hold credentials.
type Subscription struct {
	URI                     string   `json:"uri"`
	ID                      string   `json:"id"`
	Destination             string   `json:"destination"`
	Protocol                string   `json:"protocol,omitempty"`
	SubscriptionType        string   `json:"subscription_type,omitempty"`
	EventFormatType         string   `json:"event_format_type,omitempty"`
	Context                 string   `json:"context"`
	State                   string   `json:"state,omitempty"`
	Health                  string   `json:"health,omitempty"`
	RegistryPrefixes        []string `json:"registry_prefixes,omitempty"`
	ResourceTypes           []string `json:"resource_types,omitempty"`
	MessageIDs              []string `json:"message_ids,omitempty"`
	ExcludeMessageIDs       []string `json:"exclude_message_ids,omitempty"`
	ExcludeRegistryPrefixes []string `json:"exclude_registry_prefixes,omitempty"`
	Severities              []string `json:"severities,omitempty"`
	OriginResources         []string `json:"origin_resources,omitempty"`
	SubordinateResources    *bool    `json:"subordinate_resources,omitempty"`
}

func parseSubscription(uri string, p Props) Subscription {
	s := Subscription{URI: uri, ID: str(p, "Id"), Destination: str(p, "Destination"), Protocol: str(p, "Protocol"),
		SubscriptionType: str(p, "SubscriptionType"), EventFormatType: str(p, "EventFormatType"), Context: str(p, "Context"),
		State: str(sub(p, "Status"), "State"), Health: str(sub(p, "Status"), "Health"),
		RegistryPrefixes: stringList(p["RegistryPrefixes"]), ResourceTypes: stringList(p["ResourceTypes"]),
		MessageIDs: stringList(p["MessageIds"]), ExcludeMessageIDs: stringList(p["ExcludeMessageIds"]),
		ExcludeRegistryPrefixes: stringList(p["ExcludeRegistryPrefixes"]), Severities: stringList(p["Severities"]),
		SubordinateResources: boolOf(p, "SubordinateResources")}
	if s.ID == "" {
		s.ID = path.Base(uri)
	}
	l, _ := p["OriginResources"].([]any)
	for _, x := range l {
		if m, _ := x.(map[string]any); m != nil && len(s.OriginResources) < maxValues {
			if id, _ := m["@odata.id"].(string); id != "" {
				s.OriginResources = append(s.OriginResources, id)
			}
		}
	}
	return s
}

// subscriptionsURI is the Subscriptions collection of the EventService.
func (c *Client) subscriptionsURI(ctx context.Context) (EventService, error) {
	es, err := c.EventService(ctx)
	if err != nil {
		return es, err
	}
	if es.SubscriptionsURI == "" {
		return es, fmt.Errorf("%w: the EventService links no Subscriptions collection", ErrUnsupported)
	}
	return es, nil
}

// Subscriptions lists the subscriptions of the service (DSP0266 12.1.2: the
// EventDestination resources of the Subscriptions collection; at most
// MaxSubscriptions). Among them are the ones the service made for the SSE
// connections that are open (12.5.2).
func (c *Client) Subscriptions(ctx context.Context) ([]Subscription, error) {
	es, err := c.subscriptionsURI(ctx)
	if err != nil {
		return nil, err
	}
	members, err := c.memberResources(ctx, es.SubscriptionsURI, MaxSubscriptions, 8, func(err error) bool {
		return errors.Is(err, ErrNotFound) // closed since the listing
	})
	if err != nil {
		return nil, err
	}
	var out []Subscription
	for _, m := range members {
		uri, err := resolveSubscription(es.SubscriptionsURI, m.URI)
		if err != nil {
			return nil, err
		}
		out = append(out, parseSubscription(uri, m.Props))
	}
	return out, nil
}

var subID = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)

// resolveSubscription turns an Id or a path into the path of a member of the
// Subscriptions collection; anything else is refused.
func resolveSubscription(coll, idOrURI string) (string, error) {
	if subID.MatchString(idOrURI) {
		return coll + "/" + idOrURI, nil
	}
	if p := servicePath(idOrURI); p != "" && path.Dir(p) == coll && subID.MatchString(path.Base(p)) {
		return p, nil
	}
	return "", fmt.Errorf("redfish: %q is not a subscription of %s", idOrURI, coll)
}

// Subscription reads one subscription, named by its Id or its path.
func (c *Client) Subscription(ctx context.Context, idOrURI string) (Subscription, error) {
	es, err := c.subscriptionsURI(ctx)
	if err != nil {
		return Subscription{}, err
	}
	uri, err := resolveSubscription(es.SubscriptionsURI, idOrURI)
	if err != nil {
		return Subscription{}, err
	}
	p, _, err := c.getProps(ctx, uri)
	if err != nil {
		return Subscription{}, err
	}
	return parseSubscription(uri, p), nil
}

// MaxDestination bounds the length of a Destination.
const MaxDestination = 2048

// CheckDestination applies the rules a subscription Destination must meet
// before it is sent: an https URL with a host, no credentials in it (userinfo),
// no query and no fragment (a token in a URL ends up in logs: a receiver that
// needs authorization takes it from HttpHeaders), and no control character.
// DSP0266 13.1 requires TLS 1.2 or later of the interface, and 13.7 has the
// service verify the destination's identity: a plain http destination would send
// the events in clear, which this client refuses. redfish-sse:// destinations are
// made by the service for an SSE connection (EventDestination.Destination) and
// are not created by a client.
func CheckDestination(dest string) error {
	if dest == "" || len(dest) > MaxDestination {
		return fmt.Errorf("%w: the destination is empty or longer than %d bytes", ErrInsecure, MaxDestination)
	}
	for _, r := range dest {
		if r < 0x21 || r == 0x7f {
			return fmt.Errorf("%w: the destination holds a space or a control character", ErrInsecure)
		}
	}
	u, err := url.Parse(dest)
	if err != nil {
		return fmt.Errorf("%w: the destination is not a URL", ErrInsecure)
	}
	switch {
	case u.Scheme != "https":
		return fmt.Errorf("%w: the destination scheme must be https (events carry the state of the machine)", ErrInsecure)
	case u.Hostname() == "":
		return fmt.Errorf("%w: the destination has no host", ErrInsecure)
	case u.User != nil:
		return fmt.Errorf("%w: credentials in the destination are refused (use HttpHeaders)", ErrInsecure)
	case u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(dest, "#"):
		return fmt.Errorf("%w: a query or a fragment in the destination is refused", ErrInsecure)
	}
	if port := u.Port(); port != "" {
		if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
			return fmt.Errorf("%w: the destination port is not valid", ErrInsecure)
		}
	}
	return nil
}

// SubscribeOptions describe a subscription (EventDestination, DSP0266 12.1.2 and
// Table 37 in 12.1.5). Only the Redfish protocol and the RedfishEvent
// subscription type are created.
type SubscribeOptions struct {
	// Destination is where the service posts the events (required; CheckDestination).
	Destination string
	// Context is a client-supplied string the service stores and returns in every
	// event for this subscription. It is not a secret: it is echoed.
	Context string
	// EventFormatType is Event or MetricReport; empty: omitted, the service
	// assumes Event.
	EventFormatType string
	// The filters of Table 37. Each is checked against what the EventService
	// supports before anything is sent.
	RegistryPrefixes        []string
	ResourceTypes           []string
	MessageIDs              []string
	ExcludeMessageIDs       []string
	ExcludeRegistryPrefixes []string
	OriginResources         []string
	SubordinateResources    *bool
	// HTTPHeaders are the headers the service sends with every event (an
	// Authorization for the receiver, for instance): credentials, so Secrets. They
	// are sent, never printed and never returned: an error that carries one is
	// redacted.
	HTTPHeaders map[string]Secret
}

// isServicePath: a path of the service as given, not an absolute URL (servicePath
// would keep the path of one).
func isServicePath(p string) bool { return strings.HasPrefix(p, "/redfish/") && servicePath(p) == p }

var headerName = regexp.MustCompile("^[!#$%&'*+.^_`|~0-9A-Za-z-]{1,128}$")

// maxHeaders bounds HTTPHeaders.
const maxHeaders = 16

func checkList(name string, l []string) error {
	if len(l) > maxValues {
		return fmt.Errorf("redfish: %s: at most %d values", name, maxValues)
	}
	for _, v := range l {
		if v == "" || len(v) > 512 || strings.ContainsAny(v, "\r\n\x00") {
			return fmt.Errorf("redfish: %s: %q is not a valid value", name, clip(v))
		}
	}
	return nil
}

// unsupported refuses a filter the EventService does not support.
func unsupported(what string, list []string, supports bool) error {
	if len(list) == 0 || supports {
		return nil
	}
	return fmt.Errorf("%w: the EventService does not support %s", ErrUnsupported, what)
}

func notFalse(b *bool) bool { return b == nil || *b }

// subscriptionBody builds the POST body and checks the options against the
// service (DSP0266 12.1.2: a conflicting request is a 400; the client says so
// before).
func subscriptionBody(o SubscribeOptions, es EventService) (Props, error) {
	if err := CheckDestination(o.Destination); err != nil {
		return nil, err
	}
	for name, l := range map[string][]string{"RegistryPrefixes": o.RegistryPrefixes, "ResourceTypes": o.ResourceTypes, "MessageIds": o.MessageIDs,
		"ExcludeMessageIds": o.ExcludeMessageIDs, "ExcludeRegistryPrefixes": o.ExcludeRegistryPrefixes, "OriginResources": o.OriginResources} {
		if err := checkList(name, l); err != nil {
			return nil, err
		}
	}
	if len(o.Context) > 1024 || strings.ContainsAny(o.Context, "\x00") {
		return nil, errors.New("redfish: the context is longer than 1024 bytes or holds a NUL")
	}
	if len(o.HTTPHeaders) > maxHeaders {
		return nil, fmt.Errorf("redfish: at most %d HTTP headers", maxHeaders)
	}
	if es.ServiceEnabled != nil && !*es.ServiceEnabled {
		return nil, fmt.Errorf("%w: the EventService is disabled (ServiceEnabled is false)", ErrUnsupported)
	}
	if o.EventFormatType != "" {
		if o.EventFormatType != "Event" && o.EventFormatType != "MetricReport" {
			return nil, fmt.Errorf("redfish: invalid EventFormatType %q (want Event or MetricReport)", o.EventFormatType)
		}
		if allowed := es.EventFormatTypes; (len(allowed) > 0 || o.EventFormatType != "Event") && !slices.Contains(allowed, o.EventFormatType) {
			return nil, fmt.Errorf("%w: EventFormatType %s is not supported by the service (supported: %s)", ErrUnsupported, o.EventFormatType, strings.Join(allowed, ", "))
		}
	}
	for _, c := range []struct {
		what string
		list []string
		have []string
	}{{"RegistryPrefixes", o.RegistryPrefixes, es.RegistryPrefixes}, {"ExcludeRegistryPrefixes", o.ExcludeRegistryPrefixes, es.RegistryPrefixes}, {"ResourceTypes", o.ResourceTypes, es.ResourceTypes}} {
		if err := unsupported(c.what, c.list, len(c.have) > 0); err != nil {
			return nil, err
		}
		for _, v := range c.list {
			if !slices.Contains(c.have, v) {
				return nil, fmt.Errorf("%w: %s %q is not one the service lists (%s)", ErrUnsupported, c.what, v, strings.Join(c.have, ", "))
			}
		}
	}
	if err := unsupported("OriginResources", o.OriginResources, notFalse(es.OriginResourcesSupported)); err != nil {
		return nil, err
	}
	if err := unsupported("ExcludeMessageIds", o.ExcludeMessageIDs, notFalse(es.ExcludeMessageID)); err != nil {
		return nil, err
	}
	if o.SubordinateResources != nil && !notFalse(es.SubordinateResourcesSupported) {
		return nil, fmt.Errorf("%w: the EventService does not support SubordinateResources", ErrUnsupported)
	}
	body := Props{"Destination": o.Destination, "Protocol": "Redfish", "SubscriptionType": "RedfishEvent"}
	set := func(k string, l []string) {
		if len(l) > 0 {
			body[k] = l
		}
	}
	if o.Context != "" {
		body["Context"] = o.Context
	}
	if o.EventFormatType != "" {
		body["EventFormatType"] = o.EventFormatType
	}
	set("RegistryPrefixes", o.RegistryPrefixes)
	set("ResourceTypes", o.ResourceTypes)
	set("MessageIds", o.MessageIDs)
	set("ExcludeMessageIds", o.ExcludeMessageIDs)
	set("ExcludeRegistryPrefixes", o.ExcludeRegistryPrefixes)
	if len(o.OriginResources) > 0 {
		var l []any
		for _, r := range o.OriginResources {
			if !isServicePath(r) {
				return nil, fmt.Errorf("redfish: origin resource %q is not a path of the service (/redfish/...)", clip(r))
			}
			l = append(l, map[string]any{"@odata.id": r})
		}
		body["OriginResources"] = l
	}
	if o.SubordinateResources != nil {
		body["SubordinateResources"] = *o.SubordinateResources
	}
	if len(o.HTTPHeaders) > 0 {
		names := make([]string, 0, len(o.HTTPHeaders))
		for k := range o.HTTPHeaders {
			names = append(names, k)
		}
		slices.Sort(names)
		var l []any
		for _, k := range names {
			v := o.HTTPHeaders[k].Reveal()
			if !headerName.MatchString(k) || strings.ContainsAny(v, "\r\n\x00") {
				return nil, fmt.Errorf("redfish: the HTTP header %q is not valid", clip(k))
			}
			l = append(l, map[string]any{k: v})
		}
		body["HttpHeaders"] = l
	}
	return body, nil
}

// redactHeaders keeps the values of the headers out of an error.
func redactHeaders(err error, h map[string]Secret) error {
	for _, v := range h {
		err = redact(err, v)
	}
	return err
}

// Subscribe creates a subscription: a POST to the Subscriptions collection
// (DSP0266 12.1.2). The service shall answer 201 Created with the Location of
// the new subscription, and should return its representation; when it does not
// the Location is read. The Outcome says what was created.
func (c *Client) Subscribe(ctx context.Context, o SubscribeOptions) (Subscription, Outcome, error) {
	es, err := c.subscriptionsURI(ctx)
	if err != nil {
		return Subscription{}, Outcome{}, err
	}
	body, err := subscriptionBody(o, es)
	if err != nil {
		return Subscription{}, Outcome{}, err
	}
	r, err := c.Post(ctx, es.SubscriptionsURI, body)
	if err != nil {
		return Subscription{}, Outcome{}, redactHeaders(err, o.HTTPHeaders)
	}
	loc := r.Location()
	if r.Status != 201 || loc == "" || path.Dir(loc) != es.SubscriptionsURI {
		return Subscription{}, Outcome{}, fmt.Errorf("%w: the answer to the subscription is not a 201 with the Location of a member of %s", ErrMalformed, es.SubscriptionsURI)
	}
	p, derr := decodeProps(r.Body)
	if derr != nil || str(p, "Destination") == "" {
		if p, _, err = c.getProps(ctx, loc); err != nil {
			return Subscription{}, Outcome{}, err
		}
	}
	return parseSubscription(loc, p), Outcome{Changed: true, Message: "Created subscription " + loc}, nil
}

// Unsubscribe deletes a subscription, named by its Id or its path (DSP0266
// 12.1.2: an HTTP DELETE of the subscription's URI). A subscription that is
// already gone (404) is not an error: nothing changed.
func (c *Client) Unsubscribe(ctx context.Context, idOrURI string) (Outcome, error) {
	es, err := c.subscriptionsURI(ctx)
	if err != nil {
		return Outcome{}, err
	}
	uri, err := resolveSubscription(es.SubscriptionsURI, idOrURI)
	if err != nil {
		return Outcome{}, err
	}
	if _, err := c.Delete(ctx, uri, ""); err != nil {
		if errors.Is(err, ErrNotFound) {
			return Outcome{Message: uri + " does not exist"}, nil
		}
		return Outcome{}, err
	}
	return Outcome{Changed: true, Message: "Deleted subscription " + uri}, nil
}

// ---- SubmitTestEvent ----

// TestEvent is the parameters of the EventService.SubmitTestEvent action
// (EventService 1.12.0). Only MessageId is required; what is not set is not sent
// and the service omits it from the event.
type TestEvent struct {
	// MessageID is required, of the form Registry.Major.Minor.Key
	// (^\w+\.\d+\.\d+\.\w+$).
	MessageID         string
	Message           string
	MessageArgs       []string
	MessageSeverity   string
	Severity          string
	OriginOfCondition string
	EventID           string
	EventGroupID      *int64
	EventTimestamp    string
	// EventType is deprecated since EventService 1.3 (subscriptions are based on
	// RegistryPrefix and ResourceType); sent only when set.
	EventType string
}

var messageIDPattern = regexp.MustCompile(`^\w+\.\d+\.\d+\.\w+$`)

// SubmitTestEvent runs the SubmitTestEvent action. ErrUnsupported when the
// service has no such action (an EventService 1.0 has SendTestEvent, another
// action with other semantics, which is not used). Each parameter is checked
// against the ActionInfo (or AllowableValues annotations) the service gives, and
// refused before the POST when the service does not list it.
func (c *Client) SubmitTestEvent(ctx context.Context, t TestEvent) (Outcome, error) {
	const name = "#EventService.SubmitTestEvent"
	if !messageIDPattern.MatchString(t.MessageID) {
		return Outcome{}, fmt.Errorf("redfish: MessageId %q is not of the form Registry.Major.Minor.Key", clip(t.MessageID))
	}
	if t.OriginOfCondition != "" && !isServicePath(t.OriginOfCondition) {
		return Outcome{}, fmt.Errorf("redfish: OriginOfCondition %q is not a path of the service (/redfish/...)", clip(t.OriginOfCondition))
	}
	for _, v := range append([]string{t.Message, t.MessageSeverity, t.Severity, t.EventID, t.EventTimestamp, t.EventType}, t.MessageArgs...) {
		if len(v) > 2048 || strings.ContainsAny(v, "\x00") {
			return Outcome{}, errors.New("redfish: a test event parameter is longer than 2048 bytes or holds a NUL")
		}
	}
	uri, err := c.eventServiceURI(ctx)
	if err != nil {
		return Outcome{}, err
	}
	p, _, err := c.getProps(ctx, uri)
	if err != nil {
		return Outcome{}, err
	}
	act := sub(sub(p, "Actions"), name)
	if act == nil {
		return Outcome{}, fmt.Errorf("%w: action %s not found on %s", ErrUnsupported, name, uri)
	}
	target := servicePath(str(act, "target"))
	if target == "" {
		return Outcome{}, fmt.Errorf("%w: the target of action %s is missing", ErrMalformed, name)
	}
	body := Props{"MessageId": t.MessageID}
	for k, v := range map[string]string{"Message": t.Message, "MessageSeverity": t.MessageSeverity, "Severity": t.Severity,
		"OriginOfCondition": t.OriginOfCondition, "EventId": t.EventID, "EventTimestamp": t.EventTimestamp, "EventType": t.EventType} {
		if v != "" {
			body[k] = v
		}
	}
	if len(t.MessageArgs) > 0 {
		body["MessageArgs"] = t.MessageArgs
	}
	if t.EventGroupID != nil {
		body["EventGroupId"] = *t.EventGroupID
	}
	values := c.ActionValues(ctx, act)
	for k, v := range body {
		allowed, listed := values[k]
		if len(values) > 0 && !listed {
			return Outcome{}, fmt.Errorf("%w: the service does not list the parameter %s of %s", ErrUnsupported, k, name)
		}
		if s, isStr := v.(string); isStr && len(allowed) > 0 && !slices.Contains(allowed, s) {
			return Outcome{}, fmt.Errorf("%w: %s %q is not supported by the service (supported: %s)", ErrUnsupported, k, s, strings.Join(allowed, ", "))
		}
	}
	r, err := c.Post(ctx, target, body)
	if err != nil {
		return Outcome{}, err
	}
	return outcomeOf(r, Outcome{Changed: true, Message: "Submitted the test event " + t.MessageID}), nil
}

// ---- The Event payload ----

// MaxEventRecords bounds the records of one Event.
const MaxEventRecords = 256

// EventRecord is one entry of the Events array of an Event (Event 1.14.0
// EventRecord).
type EventRecord struct {
	MemberID        string   `json:"member_id,omitempty"`
	EventType       string   `json:"event_type,omitempty"`
	EventID         string   `json:"event_id,omitempty"`
	EventGroupID    *int64   `json:"event_group_id,omitempty"`
	EventTimestamp  string   `json:"event_timestamp,omitempty"`
	Severity        string   `json:"severity,omitempty"`
	MessageSeverity string   `json:"message_severity,omitempty"`
	Message         string   `json:"message,omitempty"`
	MessageID       string   `json:"message_id,omitempty"`
	MessageArgs     []string `json:"message_args,omitempty"`
	// OriginOfCondition is the @odata.id of the origin, when the service sends one.
	OriginOfCondition string `json:"origin_of_condition,omitempty"`
	Context           string `json:"context,omitempty"`
}

// Event is the payload of an event: the Event resource of the schema, or a
// MetricReport (DSP0266 12.5.2.2) whose records are not decoded.
type Event struct {
	// ID is the SSE id field of the event (DSP0266 12.5.2: unique in the stream).
	ID string `json:"id"`
	// ODataType is the @odata.type of the payload (#Event.v1_6_0.Event).
	ODataType string `json:"odata_type,omitempty"`
	// EventID is the Id of the payload.
	EventID string `json:"event_id,omitempty"`
	Name    string `json:"name,omitempty"`
	// Context is the context of the subscription: for an SSE stream, a string the
	// service generated.
	Context string        `json:"context,omitempty"`
	Records []EventRecord `json:"records,omitempty"`
	// Data is the payload as the service sent it (JSON).
	Data []byte `json:"-"`
	// Err is set, with the other fields empty but ID and Data, when the payload
	// could not be decoded: one bad event does not end the stream.
	Err error `json:"-"`
}

// IsMetricReport reports whether the payload is a MetricReport and not an Event.
func (e Event) IsMetricReport() bool { return strings.HasPrefix(e.ODataType, "#MetricReport.") }

// ParseEvent decodes the data of an event of an SSE stream (DSP0266 12.5.2.1:
// the JSON representation of the Event object; 12.5.2.2: of a MetricReport).
// It never panics and refuses what is larger than MaxEventBytes or holds more
// than MaxEventRecords records.
func ParseEvent(data []byte) (Event, error) {
	if len(data) > MaxEventBytes {
		return Event{}, ErrEventTooLarge
	}
	var d struct {
		ODataType string `json:"@odata.type"`
		ID        any    `json:"Id"`
		Name      string `json:"Name"`
		Context   string `json:"Context"`
		Events    *[]struct {
			MemberID          string `json:"MemberId"`
			EventType         string `json:"EventType"`
			EventID           string `json:"EventId"`
			EventGroupID      *int64 `json:"EventGroupId"`
			EventTimestamp    string `json:"EventTimestamp"`
			Severity          string `json:"Severity"`
			MessageSeverity   string `json:"MessageSeverity"`
			Message           string `json:"Message"`
			MessageID         string `json:"MessageId"`
			MessageArgs       []any  `json:"MessageArgs"`
			OriginOfCondition *struct {
				ID string `json:"@odata.id"`
			} `json:"OriginOfCondition"`
			Context string `json:"Context"`
		} `json:"Events"`
	}
	if err := json.Unmarshal(data, &d); err != nil {
		return Event{}, fmt.Errorf("%w: the event: %v", ErrMalformed, err)
	}
	e := Event{ODataType: d.ODataType, EventID: fmt.Sprint(zeroNil(d.ID)), Name: d.Name, Context: d.Context, Data: data}
	if d.Events == nil {
		if e.IsMetricReport() {
			return e, nil
		}
		return Event{}, fmt.Errorf("%w: the event has no Events array", ErrMalformed)
	}
	if len(*d.Events) > MaxEventRecords {
		return Event{}, fmt.Errorf("%w: the event has more than %d records", ErrMalformed, MaxEventRecords)
	}
	for _, r := range *d.Events {
		rec := EventRecord{MemberID: r.MemberID, EventType: r.EventType, EventID: r.EventID, EventGroupID: r.EventGroupID,
			EventTimestamp: r.EventTimestamp, Severity: r.Severity, MessageSeverity: r.MessageSeverity, Message: r.Message,
			MessageID: r.MessageID, Context: r.Context}
		for _, a := range r.MessageArgs {
			if len(rec.MessageArgs) < maxValues {
				rec.MessageArgs = append(rec.MessageArgs, fmt.Sprint(a))
			}
		}
		if r.OriginOfCondition != nil {
			rec.OriginOfCondition = r.OriginOfCondition.ID
		}
		e.Records = append(e.Records, rec)
	}
	return e, nil
}

func zeroNil(v any) any {
	if v == nil {
		return ""
	}
	return v
}
