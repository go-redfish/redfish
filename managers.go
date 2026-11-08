package redfish

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Managers: the commands of community.general redfish_command (category
// Manager), redfish_config (Manager) and redfish_info (Manager) and the
// VirtualMedia and LogServices commands they share with the Systems category
// (oracle: plugins/module_utils/_redfish_utils.py at 33e521d6; each function is
// named after the method it ports). The resources are the ones the two mock trees
// serve: Manager (v1_19_0 and v1_1_0 mockups), ManagerNetworkProtocol,
// EthernetInterface, HostInterface, LogService (ClearLog), VirtualMedia
// (InsertMedia, EjectMedia, or a PATCH of Image and Inserted). The protocol rules
// are the ones of systems.go.

// ManagerURIs lists the Manager resources (_find_managers_resource).
func (c *Client) ManagerURIs(ctx context.Context) ([]string, error) {
	coll, err := c.rootLink(ctx, "Managers")
	if err != nil {
		return nil, err
	}
	u, err := c.members(ctx, coll)
	if err != nil {
		return nil, err
	}
	if len(u) == 0 {
		return nil, fmt.Errorf("%w: the Managers collection has no members", ErrNotFound)
	}
	return u, nil
}

// ResolveManager is the manager named id, the only one when id is empty, else
// ErrAmbiguous.
func (c *Client) ResolveManager(ctx context.Context, id string) (string, error) {
	u, err := c.ManagerURIs(ctx)
	if err != nil {
		return "", err
	}
	return resolve(u, id, "Manager")
}

// ManagerEntry is what a read returned for one manager (the oracle's
// aggregate_managers: a manager_uri and its entries).
type ManagerEntry struct {
	ManagerURI string `json:"manager_uri"`
	Entries    any    `json:"entries"`
}

// EachManager runs a read on the managers uris (every manager of the service
// when none is given) and collects the answers; the first error stops it.
func (c *Client) EachManager(ctx context.Context, uris []string, fn func(ctx context.Context, managerURI string) (any, error)) ([]ManagerEntry, error) {
	if len(uris) == 0 {
		var err error
		if uris, err = c.ManagerURIs(ctx); err != nil {
			return nil, err
		}
	}
	out := []ManagerEntry{}
	for _, u := range uris {
		v, err := fn(ctx, u)
		if err != nil {
			return nil, err
		}
		out = append(out, ManagerEntry{ManagerURI: u, Entries: v})
	}
	return out, nil
}

// linked is the resource res and the collection or resource path it links under
// key; ErrUnsupported when it does not link one.
func (c *Client) linked(ctx context.Context, resURI, key string) (Props, string, error) {
	res, _, err := c.getProps(ctx, resURI)
	if err != nil {
		return nil, "", err
	}
	l := linkOf(res, key)
	if l == "" {
		return nil, "", fmt.Errorf("%w: %s has no %s", ErrUnsupported, resURI, key)
	}
	return res, l, nil
}

// ManagerInventory is the inventory properties of a manager (get_manager_inventory).
func (c *Client) ManagerInventory(ctx context.Context, managerURI string) (Props, error) {
	m, _, err := c.getProps(ctx, managerURI)
	if err != nil {
		return nil, err
	}
	return pick(m, "Id", "FirmwareVersion", "ManagerType", "Manufacturer", "Model", "PartNumber", "PowerState", "SerialNumber", "ServiceIdentification", "Status", "UUID"), nil
}

// ManagerHealthReport is the health of a manager (get_manager_health_report:
// a manager has no subsystem).
func (c *Client) ManagerHealthReport(ctx context.Context, managerURI string) (Props, error) {
	return c.HealthReport(ctx, "Manager", managerURI, nil)
}

// ManagerNICInventory lists the EthernetInterfaces of a manager
// (get_nic_inventory on a manager: GetManagerNicInventory).
func (c *Client) ManagerNICInventory(ctx context.Context, managerURI string) ([]Props, error) {
	return c.NICInventory(ctx, managerURI)
}

// ---- Power and reset ----

// ManagerPower runs a power command on a manager (manage_manager_power):
// PowerOn, PowerForceOff, PowerForceRestart, PowerGracefulRestart,
// PowerGracefulShutdown, PowerReboot..., and GracefulRestart, which the module
// maps to PowerGracefulRestart. The ResetType is the one the service allows
// (ActionInfo or AllowableValues).
func (c *Client) ManagerPower(ctx context.Context, managerURI, command string) (Outcome, error) {
	if command == "GracefulRestart" {
		command = "PowerGracefulRestart"
	}
	return c.ManagePower(ctx, managerURI, "#Manager.Reset", command)
}

// WaitAvailable blocks until the service answers (check_service_availability:
// the service root is read), as manage_power does with wait: it sleeps delay
// first (a service may answer while it shuts down), then asks every poll until
// timeout. The oracle's values are 30s, 5s and 120s.
func (c *Client) WaitAvailable(ctx context.Context, delay, poll, timeout time.Duration) error {
	sleep := func(d time.Duration) error {
		t := time.NewTimer(d)
		defer t.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			return nil
		}
	}
	start := time.Now()
	if err := sleep(delay); err != nil {
		return err
	}
	for time.Since(start) <= timeout {
		if _, _, err := c.getProps(ctx, c.root()+"/"); err == nil {
			return nil
		}
		if err := sleep(poll); err != nil {
			return err
		}
	}
	return fmt.Errorf("redfish: the service did not become available after %d seconds", int(timeout.Seconds()))
}

// ResetToDefaultsModes are the ResetType values of Manager.ResetToDefaults the
// module accepts.
var ResetToDefaultsModes = []string{"ResetAll", "PreserveNetworkAndUsers", "PreserveNetwork"}

// ManagerResetToDefaults runs the ResetToDefaults action of a manager
// (manager_reset_to_defaults) with the mode given (ResetAll,
// PreserveNetworkAndUsers or PreserveNetwork); a mode the service does not
// allow is refused before anything is sent.
func (c *Client) ManagerResetToDefaults(ctx context.Context, managerURI, mode string) (Outcome, error) {
	const name = "#Manager.ResetToDefaults"
	if !slices.Contains(ResetToDefaultsModes, mode) {
		return Outcome{}, fmt.Errorf("redfish: invalid reset mode %q (want one of %s)", mode, strings.Join(ResetToDefaultsModes, ", "))
	}
	m, _, err := c.getProps(ctx, managerURI)
	if err != nil {
		return Outcome{}, err
	}
	act := sub(sub(m, "Actions"), name)
	if act == nil {
		return Outcome{}, fmt.Errorf("%w: action %s not found on %s", ErrUnsupported, name, managerURI)
	}
	target := servicePath(str(act, "target"))
	if target == "" {
		return Outcome{}, fmt.Errorf("%w: the target of action %s is missing", ErrMalformed, name)
	}
	if allowed := c.ActionValues(ctx, act)["ResetType"]; len(allowed) > 0 && !slices.Contains(allowed, mode) {
		return Outcome{}, fmt.Errorf("%w: reset type %s is not supported by the service (supported: %s)", ErrUnsupported, mode, strings.Join(allowed, ", "))
	}
	r, err := c.Post(ctx, target, Props{"ResetType": mode})
	if err != nil {
		return Outcome{}, err
	}
	return outcomeOf(r, Outcome{Changed: true, Message: "ResetType " + mode}), nil
}

// ---- Network protocols, date and time, identification ----

// ProtocolServices are the services of the ManagerNetworkProtocol resource the
// oracle reads and sets.
var ProtocolServices = []string{"SNMP", "VirtualMedia", "Telnet", "SSDP", "IPMI", "SSH", "KVMIP", "NTP", "HTTP", "HTTPS", "DHCP", "DHCPv6", "RDP", "RFB"}

// NetworkProtocols is the services of a manager's NetworkProtocol resource that
// it has (get_network_protocols).
func (c *Client) NetworkProtocols(ctx context.Context, managerURI string) (Props, error) {
	_, np, err := c.linked(ctx, managerURI, "NetworkProtocol")
	if err != nil {
		return nil, err
	}
	p, _, err := c.getProps(ctx, np)
	if err != nil {
		return nil, err
	}
	return pick(p, ProtocolServices...), nil
}

// truth reads the on/off words of the module: true, "true", "True", "on", 1 and
// their opposites.
func truth(v any) (on, ok bool) {
	switch x := v.(type) {
	case bool:
		return x, true
	case string:
		switch x {
		case "true", "True", "on":
			return true, true
		case "false", "False", "off":
			return false, true
		}
	case nil, map[string]any, []any:
	default:
		if n, err := strconv.ParseFloat(fmt.Sprint(x), 64); err == nil {
			switch n {
			case 1:
				return true, true
			case 0:
				return false, true
			}
		}
	}
	return false, false
}

// portOf reads a port: an integer, or a string of digits.
func portOf(v any) (int64, bool) {
	switch x := v.(type) {
	case string:
		if x == "" {
			return 0, false
		}
		for _, r := range x {
			if r < '0' || r > '9' {
				return 0, false
			}
		}
		n, err := strconv.ParseInt(x, 10, 64)
		return n, err == nil
	case bool, nil, map[string]any, []any:
		return 0, false
	default:
		n, err := strconv.ParseInt(fmt.Sprint(x), 10, 64)
		return n, err == nil
	}
}

// NetworkProtocolPayload checks the services to set and builds the payload
// (set_network_protocols): a service must be one of ProtocolServices;
// ProtocolEnabled (or protocolenabled) takes true/false, "true"/"false",
// "True"/"False", "on"/"off" or 1/0; Port (or port) an integer or a string of
// digits (the module also lets a boolean through as an integer: refused here);
// any other property goes as it is.
func NetworkProtocolPayload(services Props) (Props, error) {
	payload := Props{}
	for _, name := range sortedKeys(services) {
		if !slices.Contains(ProtocolServices, name) {
			return nil, fmt.Errorf("redfish: service name %s is invalid", clipText(name))
		}
		props, ok := services[name].(map[string]any)
		if !ok {
			return nil, fmt.Errorf("redfish: the properties of service %s are not an object", name)
		}
		out := Props{}
		for _, k := range sortedKeys(props) {
			v := props[k]
			switch k {
			case "ProtocolEnabled", "protocolenabled":
				on, ok := truth(v)
				if !ok {
					return nil, fmt.Errorf("redfish: value of property %s is invalid", clipText(k))
				}
				out["ProtocolEnabled"] = on
			case "port", "Port":
				n, ok := portOf(v)
				if !ok {
					return nil, fmt.Errorf("redfish: value of property %s is invalid", clipText(k))
				}
				out["Port"] = n
			default:
				out[k] = v
			}
		}
		payload[name] = out
	}
	return payload, nil
}

// SetNetworkProtocols changes the services of a manager's NetworkProtocol
// resource (set_network_protocols; PatchChecked: a service the resource does not
// have is ErrUnsupported).
func (c *Client) SetNetworkProtocols(ctx context.Context, managerURI string, services Props) (Outcome, error) {
	payload, err := NetworkProtocolPayload(services)
	if err != nil {
		return Outcome{}, err
	}
	_, np, err := c.linked(ctx, managerURI, "NetworkProtocol")
	if err != nil {
		return Outcome{}, err
	}
	o, err := c.PatchChecked(ctx, np, payload)
	if err == nil && o.Changed {
		o.Message = "Modified manager network protocol settings"
	}
	return o, err
}

// SetNTP sets the NTP servers and, when enabled is not nil, whether the manager
// uses NTP (the NTP service of SetNetworkProtocols; the redfishtool date and
// time commands have no Ansible row).
func (c *Client) SetNTP(ctx context.Context, managerURI string, servers []string, enabled *bool) (Outcome, error) {
	ntp := Props{}
	if servers != nil {
		l := make([]any, len(servers))
		for i, s := range servers {
			l[i] = s
		}
		ntp["NTPServers"] = l
	}
	if enabled != nil {
		ntp["ProtocolEnabled"] = *enabled
	}
	if len(ntp) == 0 {
		return Outcome{}, fmt.Errorf("redfish: no NTP server and no state given")
	}
	return c.SetNetworkProtocols(ctx, managerURI, Props{"NTP": ntp})
}

// ManagerDateTime is the DateTime and DateTimeLocalOffset of a manager
// (redfishtool "Managers": no Ansible row).
func (c *Client) ManagerDateTime(ctx context.Context, managerURI string) (Props, error) {
	m, _, err := c.getProps(ctx, managerURI)
	if err != nil {
		return nil, err
	}
	return pick(m, "DateTime", "DateTimeLocalOffset"), nil
}

// SetManagerDateTime sets the DateTime (an RFC 3339 date-time) and/or the
// DateTimeLocalOffset (+hh:mm or -hh:mm) of a manager; an empty value is left.
func (c *Client) SetManagerDateTime(ctx context.Context, managerURI, dateTime, offset string) (Outcome, error) {
	p := Props{}
	if dateTime != "" {
		if _, err := time.Parse(time.RFC3339, dateTime); err != nil {
			return Outcome{}, fmt.Errorf("redfish: the date and time %q is not RFC 3339: %v", clipText(dateTime), err)
		}
		p["DateTime"] = dateTime
	}
	if offset != "" {
		if !validOffset(offset) {
			return Outcome{}, fmt.Errorf("redfish: the offset %q is not +hh:mm or -hh:mm", clipText(offset))
		}
		p["DateTimeLocalOffset"] = offset
	}
	if len(p) == 0 {
		return Outcome{}, fmt.Errorf("redfish: no date and time and no offset given")
	}
	return c.PatchChecked(ctx, managerURI, p)
}

var offsetRE = regexp.MustCompile(`^[+-]([01][0-9]|2[0-3]):[0-5][0-9]$`)

func validOffset(s string) bool { return offsetRE.MatchString(s) }

// ServiceIdentification is the ServiceIdentification of a manager
// (get_service_identification); ErrUnsupported when it has none.
func (c *Client) ServiceIdentification(ctx context.Context, managerURI string) (string, error) {
	m, _, err := c.getProps(ctx, managerURI)
	if err != nil {
		return "", err
	}
	s, ok := m["ServiceIdentification"].(string)
	if !ok {
		return "", fmt.Errorf("%w: service ID not found for manager %s", ErrUnsupported, managerURI)
	}
	return s, nil
}

// ResolveServiceIdentification picks the manager get_service_identification
// reads: the one named id; else the only manager; else, among several, the one
// manager that has a ServiceIdentification (ErrAmbiguous when none or several).
func (c *Client) ResolveServiceIdentification(ctx context.Context, id string) (string, error) {
	uris, err := c.ManagerURIs(ctx)
	if err != nil {
		return "", err
	}
	if id != "" || len(uris) == 1 {
		return resolve(uris, id, "Manager")
	}
	var with []string
	for _, u := range uris {
		if inv, err := c.ManagerInventory(ctx, u); err == nil && str(inv, "ServiceIdentification") != "" {
			with = append(with, u)
		}
	}
	if len(with) == 1 {
		return with[0], nil
	}
	return "", fmt.Errorf("%w: %d managers have a ServiceIdentification (%s): name one", ErrAmbiguous, len(with), strings.Join(with, ", "))
}

// SetServiceIdentification sets the ServiceIdentification of a manager
// (set_service_identification).
func (c *Client) SetServiceIdentification(ctx context.Context, managerURI, serviceID string) (Outcome, error) {
	return c.PatchChecked(ctx, managerURI, Props{"ServiceIdentification": serviceID})
}

// ---- Ethernet and host interfaces ----

// nicProperties are the properties get_nic keeps.
var nicProperties = []string{"Name", "Id", "Description", "FQDN", "IPv4Addresses", "IPv6Addresses", "NameServers", "MACAddress", "PermanentMACAddress", "SpeedMbps", "MTUSize", "AutoNeg", "Status", "LinkStatus"}

// SetManagerNIC changes the EthernetInterface of a manager whose properties
// contain addr (case-insensitive; empty: the host of the client's URL, as the
// module does with its root URI); config is a flat view: a property that is an
// array in the interface and an object in config becomes an array of that one
// object (set_manager_nic, get_manager_ethernet_uri).
func (c *Client) SetManagerNIC(ctx context.Context, managerURI, addr string, config Props) (Outcome, error) {
	_, coll, err := c.linked(ctx, managerURI, "EthernetInterfaces")
	if err != nil {
		return Outcome{}, err
	}
	if addr == "" {
		addr = c.base.Hostname()
	}
	uris, err := c.members(ctx, coll)
	if err != nil {
		return Outcome{}, err
	}
	var target string
	var current Props
	for _, u := range uris {
		p, _, err := c.getProps(ctx, u)
		if err != nil {
			return Outcome{}, err
		}
		dump, _ := json.Marshal(p)
		if strings.Contains(strings.ToLower(string(dump)), strings.ToLower(addr)) {
			target, current = u, p
			break
		}
	}
	if target == "" {
		return Outcome{}, fmt.Errorf("%w: Manager NIC with address %q", ErrNotFound, clipText(addr))
	}
	payload := Props{}
	for k, v := range config {
		if _, isList := current[k].([]any); isList {
			if obj, isObj := v.(map[string]any); isObj {
				payload[k] = []any{obj}
				continue
			}
		}
		payload[k] = v
	}
	o, err := c.PatchChecked(ctx, target, payload)
	if err == nil && o.Changed {
		o.Message = "Modified manager NIC"
	}
	return o, err
}

// HostInterfaces lists the host interfaces of the managers (every manager when
// none is given) with the NICs they use (get_hostinterfaces); an error when the
// service has none.
func (c *Client) HostInterfaces(ctx context.Context, managerURIs ...string) ([]Props, error) {
	if len(managerURIs) == 0 {
		var err error
		if managerURIs, err = c.ManagerURIs(ctx); err != nil {
			return nil, err
		}
	}
	props := []string{"Id", "Name", "Description", "HostInterfaceType", "Status", "InterfaceEnabled", "ExternallyAccessible", "AuthenticationModes", "AuthNoneRoleId", "CredentialBootstrapping"}
	out := []Props{}
	for _, mu := range managerURIs {
		m, _, err := c.getProps(ctx, mu)
		if err != nil {
			return nil, err
		}
		coll := linkOf(m, "HostInterfaces")
		if coll == "" {
			continue
		}
		// The members of both collections come with $expand=. when the service
		// allows it; ManagerEthernetInterface is one link, read on its own.
		hms, err := c.memberResources(ctx, coll, MaxMembers, 8, nil)
		if err != nil {
			return nil, err
		}
		for _, hm := range hms {
			h := hm.Props
			e := Props{}
			for _, k := range props {
				if v, ok := h[k]; ok && v != nil {
					e[k] = v
				}
			}
			if nu := linkOf(h, "ManagerEthernetInterface"); nu != "" {
				n, _, err := c.getProps(ctx, nu)
				if err != nil {
					return nil, err
				}
				e["ManagerEthernetInterface"] = pick(n, nicProperties...)
			}
			if hl := linkOf(h, "HostEthernetInterfaces"); hl != "" {
				nics, err := c.memberResources(ctx, hl, MaxMembers, 8, nil)
				if err != nil {
					return nil, err
				}
				for _, n := range nics {
					l, _ := e["HostEthernetInterfaces"].([]Props)
					e["HostEthernetInterfaces"] = append(l, pick(n.Props, nicProperties...))
				}
			}
			if len(out) < MaxMembers {
				out = append(out, e)
			}
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%w: no HostInterface objects found", ErrNotFound)
	}
	return out, nil
}

// SetHostInterface changes the HostInterface of a manager: the one whose Id is
// id, else the only one (set_hostinterface_attributes; the module matches id as a
// substring of the Id, here it is the whole Id).
func (c *Client) SetHostInterface(ctx context.Context, managerURI, id string, config Props) (Outcome, error) {
	if len(config) == 0 {
		return Outcome{}, fmt.Errorf("redfish: a host interface configuration is required")
	}
	_, coll, err := c.linked(ctx, managerURI, "HostInterfaces")
	if err != nil {
		return Outcome{}, err
	}
	uris, err := c.members(ctx, coll)
	if err != nil {
		return Outcome{}, err
	}
	u, err := resolve(uris, id, "HostInterface")
	if err != nil {
		return Outcome{}, err
	}
	o, err := c.PatchChecked(ctx, u, config)
	if err == nil && o.Changed {
		o.Message = "Modified host interface"
	}
	return o, err
}

// ---- Logs ----

// LogEntries is the entries of one LogService.
type LogEntries struct {
	// Service is the LogService resource.
	Service string
	// Description is the one of the Entries collection ("Collection of log
	// entries" when it has none).
	Description string
	Entries     []Props
}

// MaxLogEntries bounds the entries read of a log.
const MaxLogEntries = 1024

var logProperties = []string{"Severity", "Created", "EntryType", "OemRecordFormat", "Message", "MessageId", "MessageArgs"}

// ParseLogEntries decodes one page of a LogEntry collection: its Description,
// its members (as they are) and the link to the next page.
func ParseLogEntries(data []byte) (description string, members []Props, next string, err error) {
	p, err := decodeProps(data)
	if err != nil {
		return "", nil, "", fmt.Errorf("%w: %v", ErrMalformed, err)
	}
	description = "Collection of log entries"
	if d, ok := p["Description"].(string); ok {
		description = d
	}
	list, _ := p["Members"].([]any)
	for _, x := range list {
		if m, ok := x.(map[string]any); ok && len(members) < MaxLogEntries {
			members = append(members, m)
		}
	}
	return description, members, servicePath(str(p, "Members@odata.nextLink")), nil
}

// logServices lists the LogService resources of a system, a manager or a chassis.
func (c *Client) logServices(ctx context.Context, resURI string) ([]string, error) {
	_, coll, err := c.linked(ctx, resURI, "LogServices")
	if err != nil {
		return nil, err
	}
	return c.members(ctx, coll)
}

// Logs reads the entries of the log services of a manager (get_logs), or of a
// system or a chassis (redfishtool "Logs"), at most max entries per log (0:
// 64). The oracle keeps the entries as the collection expands them; an entry
// the collection lists only by link is read.
func (c *Client) Logs(ctx context.Context, resURI string, max int) ([]LogEntries, error) {
	if max <= 0 {
		max = MaxMembers
	}
	max = min(max, MaxLogEntries)
	_, lsColl, err := c.linked(ctx, resURI, "LogServices")
	if err != nil {
		return nil, err
	}
	// The LogService resources come with $expand=. when the service allows it.
	svcs, err := c.memberResources(ctx, lsColl, MaxMembers, 8, nil)
	if err != nil {
		return nil, err
	}
	// The entries are paged (Members@odata.nextLink) and a page is read whole by
	// the GET that lists it, so $expand=. only matters for a service that lists
	// them as links: a page of such a service costs one request instead of one per
	// entry. A page can be large (MaxExpandedBody bounds it); $top is not used:
	// a service that answers a shorter page need not give a nextLink, and the
	// entries read would then differ from the ones read without it. When the
	// expanded page fails the plain page is read, as for every converted read.
	expand := c.canExpand(ctx)
	out := []LogEntries{}
	for _, sm := range svcs {
		su, svc := sm.URI, sm.Props
		eu := linkOf(svc, "Entries")
		if eu == "" {
			continue
		}
		le := LogEntries{Service: su, Entries: []Props{}}
		for page, next := 0, eu; next != "" && page < 8 && len(le.Entries) < max; page++ {
			r, err := c.pageMaybeExpanded(ctx, next, expand)
			if err != nil {
				return nil, err
			}
			desc, members, nx, err := ParseLogEntries(r.Body)
			if err != nil {
				return nil, err
			}
			if page == 0 {
				le.Description = desc
			}
			for _, m := range members {
				if len(le.Entries) >= max {
					break
				}
				e := pick(m, logProperties...)
				if len(e) == 0 {
					if u := linkOf(Props{"x": m}, "x"); u != "" {
						full, _, err := c.getProps(ctx, u)
						if err != nil {
							return nil, err
						}
						e = pick(full, logProperties...)
					}
				}
				if len(e) > 0 {
					le.Entries = append(le.Entries, e)
				}
			}
			next = nx
		}
		out = append(out, le)
	}
	return out, nil
}

// ClearLogs runs the ClearLog action of every log service of a manager that has
// it (clear_logs), or of a system or a chassis (redfishtool "clearLog").
func (c *Client) ClearLogs(ctx context.Context, resURI string) (Outcome, error) {
	svcs, err := c.logServices(ctx, resURI)
	if err != nil {
		return Outcome{}, err
	}
	n := 0
	var monitor string
	for _, su := range svcs {
		svc, _, err := c.getProps(ctx, su)
		if err != nil {
			return Outcome{}, err
		}
		act := sub(sub(svc, "Actions"), "#LogService.ClearLog")
		if act == nil {
			continue
		}
		target := servicePath(str(act, "target"))
		if target == "" {
			return Outcome{}, fmt.Errorf("%w: the target of the ClearLog action of %s is missing", ErrMalformed, su)
		}
		r, err := c.Post(ctx, target, Props{})
		if err != nil {
			return Outcome{}, err
		}
		n++
		if r.IsTask() && monitor == "" {
			monitor = r.Location()
		}
	}
	return Outcome{Changed: n > 0, Message: fmt.Sprintf("Cleared %d log services", n), Monitor: monitor}, nil
}

// ---- Virtual media ----

var virtualMediaProperties = []string{"Description", "ConnectedVia", "Id", "MediaTypes", "Image", "ImageName", "Name", "WriteProtected", "TransferMethod", "TransferProtocolType"}

// VirtualMedia lists the VirtualMedia resources of a system or a manager
// (get_virtualmedia); ErrUnsupported when it has none.
func (c *Client) VirtualMedia(ctx context.Context, resURI string) ([]Props, error) {
	return c.memberProps(ctx, resURI, "VirtualMedia", virtualMediaProperties, nil, false)
}

// VirtualMediaOptions are the options of VirtualMediaInsert (the module's
// virtual_media dict).
type VirtualMediaOptions struct {
	// ImageURL is the image to insert (required).
	ImageURL string
	// MediaTypes selects a slot that supports one of them (CD, DVD, Floppy,
	// USBStick...); empty: any slot.
	MediaTypes []string
	// Inserted and WriteProtected are sent when not nil (the module's defaults
	// are both true).
	Inserted, WriteProtected *bool
	// Username and Password give access to the image; TransferProtocolType and
	// TransferMethod say how to fetch it.
	Username, Password, TransferProtocolType, TransferMethod string
}

// slot is a VirtualMedia resource read.
type slot struct {
	uri   string
	data  Props
	allow string // the Allow header
}

// readSlots reads each VirtualMedia slot on its own and does not use $expand
// (unlike VirtualMedia, which lists them): the Allow header of each slot's own
// answer decides whether an insert or an eject is permitted, and an expanded
// member carries no header of its own.
func (c *Client) readSlots(ctx context.Context, resURI string) ([]slot, error) {
	_, coll, err := c.linked(ctx, resURI, "VirtualMedia")
	if err != nil {
		return nil, err
	}
	uris, err := c.members(ctx, coll)
	if err != nil {
		return nil, err
	}
	var out []slot
	for _, u := range uris {
		p, r, err := c.getProps(ctx, u)
		if err != nil {
			continue // _read_virt_media_resources skips what it cannot read
		}
		out = append(out, slot{u, p, r.Header.Get("Allow")})
	}
	return out, nil
}

// sameImage compares two image URLs as urlparse does (all their parts).
func sameImage(a, b string) bool {
	x, e1 := url.Parse(a)
	y, e2 := url.Parse(b)
	if e1 != nil || e2 != nil {
		return a == b
	}
	return x.Scheme == y.Scheme && x.Opaque == y.Opaque && x.Host == y.Host && x.User.String() == y.User.String() && x.EscapedPath() == y.EscapedPath() && x.RawQuery == y.RawQuery && x.Fragment == y.Fragment
}

func flag(p Props, key string, def bool) bool {
	if b, ok := p[key].(bool); ok {
		return b
	}
	return def
}

func hasImageName(p Props) bool { s, ok := p["ImageName"].(string); return ok && s != "" }

// findEmptySlot is _find_empty_virt_media_slot.
func findEmptySlot(slots []slot, mediaTypes []string, strict bool, vendor string) (slot, bool) {
	for _, s := range slots {
		types := stringList(s.data["MediaTypes"])
		if _, has := s.data["MediaTypes"]; has && len(mediaTypes) > 0 {
			if !slices.ContainsFunc(mediaTypes, func(t string) bool { return slices.Contains(types, t) }) {
				continue
			}
		} else if strict {
			continue
		}
		if vendor == "Lenovo" && (strings.Contains(s.uri, "RDOC") || strings.Contains(s.uri, "Remote")) {
			continue
		}
		if !flag(s.data, "Inserted", false) && !hasImageName(s.data) {
			return s, true
		}
		// Supermicro does not clear ImageName when media is ejected.
		if vendor == "Supermicro" && s.data["ConnectedVia"] == "NotConnected" {
			return s, true
		}
	}
	return slot{}, false
}

// imageInserted is _virt_media_image_inserted.
func imageInserted(slots []slot, image string) bool {
	for _, s := range slots {
		if img := str(s.data, "Image"); img != "" && sameImage(image, img) && flag(s.data, "Inserted", false) && hasImageName(s.data) {
			return true
		}
	}
	return false
}

// findToEject is _find_virt_media_to_eject: the slot that holds the image and
// whether it is inserted; the last slot with the image when none is.
func findToEject(slots []slot, image string) (s slot, found, eject bool) {
	for _, x := range slots {
		if img := str(x.data, "Image"); img != "" && sameImage(image, img) {
			s, found = x, true
			_, hasName := x.data["ImageName"]
			if flag(x.data, "Inserted", true) && (!hasName || hasImageName(x.data)) {
				return x, true, true
			}
		}
	}
	return s, found, false
}

// insertPayload is _insert_virt_media_payload: allowed(param) gives the values
// the service allows for a parameter.
func insertPayload(o VirtualMediaOptions, data Props, allowed func(string) []string) (Props, error) {
	payload := Props{"Image": o.ImageURL}
	opts := []struct {
		param string
		val   any
		set   bool
	}{
		{"Inserted", o.Inserted, o.Inserted != nil},
		{"WriteProtected", o.WriteProtected, o.WriteProtected != nil},
		{"UserName", o.Username, o.Username != ""},
		{"Password", o.Password, o.Password != ""},
		{"TransferProtocolType", o.TransferProtocolType, o.TransferProtocolType != ""},
		{"TransferMethod", o.TransferMethod, o.TransferMethod != ""},
	}
	for _, x := range opts {
		if !x.set {
			continue
		}
		if _, has := data[x.param]; !has {
			continue
		}
		v := x.val
		if b, ok := v.(*bool); ok {
			v = *b
		}
		if al := allowed(x.param); len(al) > 0 && !slices.Contains(al, fmt.Sprint(v)) {
			return nil, fmt.Errorf("redfish: value %q for %s is not in the list of AllowableValues %v", fmt.Sprint(v), x.param, al)
		}
		payload[x.param] = v
	}
	return payload, nil
}

// patchAllowed: with an Allow header that lacks PATCH, a PATCH is not tried.
func patchAllowed(allow string) bool {
	if allow == "" {
		return true
	}
	for _, m := range strings.Split(allow, ",") {
		if strings.TrimSpace(m) == "PATCH" {
			return true
		}
	}
	return false
}

func quirkyVendor(v string) bool { return v == "HPE" || v == "Supermicro" }

// VirtualMediaInsert inserts an image in a free slot of the VirtualMedia of a
// system or a manager (virtual_media_insert): nothing is done when the image is
// already inserted; the slot is the first free one that supports one of the media
// types (else the first free one); the InsertMedia action is used when the slot
// has it, else a PATCH of Image and Inserted. Older HPE and Supermicro services
// that refuse Inserted and WriteProtected are asked again without them.
func (c *Client) VirtualMediaInsert(ctx context.Context, resURI string, o VirtualMediaOptions) (Outcome, error) {
	if o.ImageURL == "" {
		return Outcome{}, fmt.Errorf("redfish: an image URL is required for VirtualMediaInsert")
	}
	slots, err := c.readSlots(ctx, resURI)
	if err != nil {
		return Outcome{}, err
	}
	if imageInserted(slots, o.ImageURL) {
		return Outcome{Message: fmt.Sprintf("VirtualMedia '%s' already inserted", clipText(o.ImageURL))}, nil
	}
	vendor := c.vendor(ctx)
	s, ok := findEmptySlot(slots, o.MediaTypes, true, vendor)
	if !ok {
		s, ok = findEmptySlot(slots, o.MediaTypes, false, vendor)
	}
	if !ok {
		return Outcome{}, fmt.Errorf("%w: no available VirtualMedia resource supporting %v", ErrNotFound, o.MediaTypes)
	}
	act := sub(sub(s.data, "Actions"), "#VirtualMedia.InsertMedia")
	if act == nil {
		if !patchAllowed(s.allow) {
			return Outcome{}, fmt.Errorf("%w: #VirtualMedia.InsertMedia action not found and PATCH not allowed on %s", ErrUnsupported, s.uri)
		}
		payload, err := insertPayload(o, s.data, func(p string) []string { return annotationValues(s.data)[p] })
		if err != nil {
			return Outcome{}, err
		}
		if _, ok := payload["Inserted"]; !ok {
			payload["Inserted"] = true
		}
		out, err := c.PatchChecked(ctx, s.uri, payload)
		if err != nil && quirkyVendor(vendor) {
			delete(payload, "Inserted")
			delete(payload, "WriteProtected")
			out, err = c.PatchChecked(ctx, s.uri, payload)
		}
		if err == nil && out.Changed {
			out.Message = "VirtualMedia inserted"
		}
		return out, err
	}
	target := servicePath(str(act, "target"))
	if target == "" {
		return Outcome{}, fmt.Errorf("%w: target URI missing from Action #VirtualMedia.InsertMedia", ErrMalformed)
	}
	ai := c.ActionValues(ctx, act)
	payload, err := insertPayload(o, s.data, func(p string) []string { return ai[p] })
	if err != nil {
		return Outcome{}, err
	}
	r, err := c.Post(ctx, target, payload)
	if err != nil && quirkyVendor(vendor) && (payload["Inserted"] != nil || payload["WriteProtected"] != nil) {
		delete(payload, "Inserted")
		delete(payload, "WriteProtected")
		r, err = c.Post(ctx, target, payload)
	}
	if err != nil {
		return Outcome{}, err
	}
	return outcomeOf(r, Outcome{Changed: true, Message: "VirtualMedia inserted"}), nil
}

// VirtualMediaEject ejects the image from the slot of a system or a manager that
// holds it (virtual_media_eject): the EjectMedia action when the slot has it,
// else a PATCH of Inserted and Image; an image already ejected is not an error,
// an image no slot holds is ErrNotFound.
func (c *Client) VirtualMediaEject(ctx context.Context, resURI, imageURL string) (Outcome, error) {
	if imageURL == "" {
		return Outcome{}, fmt.Errorf("redfish: an image URL is required for VirtualMediaEject")
	}
	slots, err := c.readSlots(ctx, resURI)
	if err != nil {
		return Outcome{}, err
	}
	s, found, eject := findToEject(slots, imageURL)
	switch {
	case found && !eject:
		return Outcome{Message: fmt.Sprintf("VirtualMedia image '%s' already ejected", clipText(imageURL))}, nil
	case !found:
		return Outcome{}, fmt.Errorf("%w: no VirtualMedia resource found with image '%s' inserted", ErrNotFound, clipText(imageURL))
	}
	act := sub(sub(s.data, "Actions"), "#VirtualMedia.EjectMedia")
	if act == nil {
		if !patchAllowed(s.allow) {
			return Outcome{}, fmt.Errorf("%w: #VirtualMedia.EjectMedia action not found and PATCH not allowed on %s", ErrUnsupported, s.uri)
		}
		payload := Props{"Inserted": false, "Image": nil}
		out, err := c.PatchChecked(ctx, s.uri, payload)
		if err != nil && quirkyVendor(c.vendor(ctx)) {
			delete(payload, "Inserted")
			out, err = c.PatchChecked(ctx, s.uri, payload)
		}
		if err == nil && out.Changed {
			out.Message = "VirtualMedia ejected"
		}
		return out, err
	}
	target := servicePath(str(act, "target"))
	if target == "" {
		return Outcome{}, fmt.Errorf("%w: target URI property missing from Action #VirtualMedia.EjectMedia", ErrMalformed)
	}
	r, err := c.Post(ctx, target, Props{})
	if err != nil {
		return Outcome{}, err
	}
	return outcomeOf(r, Outcome{Changed: true, Message: "VirtualMedia ejected"}), nil
}
