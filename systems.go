package redfish

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
)

// Systems: the commands of community.general redfish_command, redfish_config and
// redfish_info that act on ComputerSystem resources (oracle:
// plugins/module_utils/_redfish_utils.py of community.general, each function is
// named after the method it ports). The schemas are DSP8010 ComputerSystem
// 1.22 (Reset, Boot, PowerRestorePolicy, IndicatorLED), Bios 1.2 (Attributes,
// ResetBios, @Redfish.Settings), SecureBoot 1.1, BootOption 1.0.4, ActionInfo
// 1.3 and AttributeRegistry 1.3; the protocol rules are DSP0266 1.23.0: Actions
// 9.9.1 and AllowableValues 9.9.2, ActionInfo 9.9.6, Settings resources
// 9.9.7/9.10 and their ApplyTime 7.12/9.9.8, ETag and If-Match 6.5 and 7.6.

// Props is the properties of a resource.
type Props = map[string]any

var (
	// ErrUnsupported: the resource does not have the property or action asked for.
	ErrUnsupported = errors.New("redfish: not supported by the resource")
	// ErrAmbiguous: several resources match and none was named.
	ErrAmbiguous = errors.New("redfish: several resources match: name one")
	// ErrMismatch: values differ from the ones expected.
	ErrMismatch = errors.New("redfish: the values do not match")
)

// Outcome is what a command that changes something did.
type Outcome struct {
	// Changed is false when the resource already had the requested state.
	Changed bool
	Message string
	// Warning is a non-fatal remark (attributes the BIOS does not have).
	Warning string
	// Monitor is the task monitor when the service answered 202 (see WaitTask).
	Monitor string
}

func outcomeOf(r *Response, o Outcome) Outcome {
	if r != nil && r.IsTask() {
		o.Monitor = r.Location()
	}
	return o
}

// getProps reads a resource as a property set (numbers kept exact).
func (c *Client) getProps(ctx context.Context, p string) (Props, *Response, error) {
	r, err := c.Get(ctx, p)
	if err != nil {
		return nil, nil, err
	}
	m, err := decodeProps(r.Body)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %s: %v", ErrMalformed, p, err)
	}
	return m, r, nil
}

func decodeProps(data []byte) (Props, error) {
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	var m Props
	if err := d.Decode(&m); err != nil {
		return nil, err
	}
	if m == nil {
		return nil, errors.New("not a JSON object")
	}
	return m, nil
}

// linkOf is the path an @odata.id link property of p holds ("" when absent or
// not a path of the service).
func linkOf(p Props, key string) string {
	m, _ := p[key].(map[string]any)
	s, _ := m["@odata.id"].(string)
	return servicePath(s)
}

func str(p Props, key string) string { s, _ := p[key].(string); return s }

func sub(p Props, key string) Props { m, _ := p[key].(map[string]any); return m }

// pick copies the named properties of a resource that it has.
func pick(p Props, keys ...string) Props {
	out := Props{}
	for _, k := range keys {
		if v, ok := p[k]; ok {
			out[k] = v
		}
	}
	return out
}

// Pick decodes a resource and keeps the named properties it has (the inventory
// commands' "does not fail if not found" lists).
func Pick(data []byte, keys ...string) (Props, error) {
	p, err := decodeProps(data)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrMalformed, err)
	}
	return pick(p, keys...), nil
}

// members lists the members of a collection: Members, paged by
// Members@odata.nextLink (DSP0266 7.2.2), else the pre-1.0 Links.Members some
// services still send. At most MaxMembers.
func (c *Client) members(ctx context.Context, coll string) ([]string, error) {
	var out []string
	next := coll
	for page := 0; next != "" && page < 8 && len(out) < MaxMembers; page++ {
		p, _, err := c.getProps(ctx, next)
		if err != nil {
			return nil, err
		}
		list, _ := p["Members"].([]any)
		if list == nil {
			list, _ = sub(p, "Links")["Members"].([]any)
		}
		for _, x := range list {
			m, _ := x.(map[string]any)
			if id, _ := m["@odata.id"].(string); servicePath(id) != "" && len(out) < MaxMembers {
				out = append(out, servicePath(id))
			}
		}
		next = servicePath(str(p, "Members@odata.nextLink"))
	}
	return out, nil
}

// rootLink is the path the service root gives to a top-level resource.
func (c *Client) rootLink(ctx context.Context, name string) (string, error) {
	root, _, err := c.getProps(ctx, c.root()+"/")
	if err != nil {
		return "", err
	}
	if l := linkOf(root, name); l != "" {
		return l, nil
	}
	return "", fmt.Errorf("%w: the service root links no %s", ErrNotFound, name)
}

// resolve picks a resource among uris: the one named id (the last segment of its
// path), else the only one; with several and no id, ErrAmbiguous.
func resolve(uris []string, id, kind string) (string, error) {
	if id != "" {
		for _, u := range uris {
			if path := strings.TrimRight(u, "/"); path[strings.LastIndexByte(path, '/')+1:] == id {
				return u, nil
			}
		}
		return "", fmt.Errorf("%w: %s %q", ErrNotFound, kind, id)
	}
	if len(uris) == 1 {
		return uris[0], nil
	}
	return "", fmt.Errorf("%w: %d %s resources (%s)", ErrAmbiguous, len(uris), kind, strings.Join(uris, ", "))
}

// SystemURIs lists the ComputerSystem resources (_find_systems_resource).
func (c *Client) SystemURIs(ctx context.Context) ([]string, error) {
	coll, err := c.rootLink(ctx, "Systems")
	if err != nil {
		return nil, err
	}
	u, err := c.members(ctx, coll)
	if err != nil {
		return nil, err
	}
	if len(u) == 0 {
		return nil, fmt.Errorf("%w: the Systems collection has no members", ErrNotFound)
	}
	return u, nil
}

// ResolveSystem is the system named id, the only one when id is empty, else
// ErrAmbiguous.
func (c *Client) ResolveSystem(ctx context.Context, id string) (string, error) {
	u, err := c.SystemURIs(ctx)
	if err != nil {
		return "", err
	}
	return resolve(u, id, "ComputerSystem")
}

// ResolveChassis is ResolveSystem for the chassis.
func (c *Client) ResolveChassis(ctx context.Context, id string) (string, error) {
	u, err := c.ChassisURIs(ctx)
	if err != nil {
		return "", err
	}
	return resolve(u, id, "Chassis")
}

// CheckPayload compares the properties to apply with the ones the resource
// holds (_check_request_payload): changes is true when one differs; unsupported
// names the first property the resource does not have (nested objects are
// compared member by member; arrays and scalars by value).
func CheckPayload(req, cur Props) (changes bool, unsupported string) {
	for _, k := range sortedKeys(req) {
		cv, ok := cur[k]
		if !ok {
			return false, k
		}
		rm, rok := req[k].(map[string]any)
		cm, cok := cv.(map[string]any)
		if rok && cok {
			ch, bad := CheckPayload(rm, cm)
			if bad != "" {
				return false, bad
			}
			changes = changes || ch
			continue
		}
		if !sameJSON(req[k], cv) {
			changes = true
		}
	}
	return changes, ""
}

func sortedKeys(m map[string]any) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

func sameJSON(a, b any) bool {
	x, e1 := json.Marshal(a)
	y, e2 := json.Marshal(b)
	return e1 == nil && e2 == nil && bytes.Equal(x, y)
}

// PatchChecked changes properties the way the modules do (patch_request with
// check_pyld): the resource is read, the ETag it gave goes back in If-Match, a
// property it does not have is ErrUnsupported, and nothing is sent when it
// already holds every value.
func (c *Client) PatchChecked(ctx context.Context, uri string, payload Props) (Outcome, error) {
	return c.patchChecked(ctx, uri, payload, nil)
}

// patchChecked is PatchChecked with write-only properties (a password): they are
// sent with the payload, are never compared with the resource, and make the
// change certain.
func (c *Client) patchChecked(ctx context.Context, uri string, payload, writeOnly Props) (Outcome, error) {
	cur, resp, err := c.getProps(ctx, uri)
	if err != nil {
		return Outcome{}, err
	}
	changes, bad := CheckPayload(payload, cur)
	if bad != "" {
		return Outcome{}, fmt.Errorf("%w: %s does not support the property %s", ErrUnsupported, uri, bad)
	}
	if !changes && len(writeOnly) == 0 {
		return Outcome{Message: "Properties in " + uri + " are already set"}, nil
	}
	tag := resp.ETag()
	if tag == "" {
		tag = str(cur, "@odata.etag")
	}
	if len(writeOnly) > 0 {
		merged := Props{}
		for k, v := range payload {
			merged[k] = v
		}
		for k, v := range writeOnly {
			merged[k] = v
		}
		payload = merged
	}
	r, err := c.Patch(ctx, uri, payload, tag)
	if err != nil {
		return Outcome{}, err
	}
	return outcomeOf(r, Outcome{Changed: true, Message: "Changed the properties of " + uri}), nil
}

const (
	maxParams = 64
	maxValues = 256
)

// ParseActionInfo decodes an ActionInfo resource (DSP0266 9.9.6) into the
// allowable values of each named parameter (nil when it lists none).
func ParseActionInfo(data []byte) (map[string][]string, error) {
	p, err := decodeProps(data)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrMalformed, err)
	}
	out := map[string][]string{}
	params, _ := p["Parameters"].([]any)
	for _, x := range params {
		m, _ := x.(map[string]any)
		name, _ := m["Name"].(string)
		if name == "" || len(out) >= maxParams {
			continue
		}
		out[name] = stringList(m["AllowableValues"])
	}
	return out, nil
}

func stringList(v any) []string {
	l, _ := v.([]any)
	var out []string
	for _, x := range l {
		if len(out) >= maxValues {
			break
		}
		out = append(out, fmt.Sprint(x))
	}
	return out
}

// annotationValues reads the "<Param>@Redfish.AllowableValues" annotations of
// an action (DSP0266 9.9.2).
func annotationValues(action Props) map[string][]string {
	out := map[string][]string{}
	for k, v := range action {
		if name, ok := strings.CutSuffix(k, "@Redfish.AllowableValues"); ok && name != "" && len(out) < maxParams {
			out[name] = stringList(v)
		}
	}
	return out
}

// ActionValues is the allowable values of an action's parameters: from its
// ActionInfo resource when it names one, else from the annotations
// (_get_all_action_info_values).
func (c *Client) ActionValues(ctx context.Context, action Props) map[string][]string {
	var ai map[string][]string
	if p := servicePath(str(action, "@Redfish.ActionInfo")); p != "" {
		if r, err := c.Get(ctx, p); err == nil {
			ai, _ = ParseActionInfo(r.Body)
		}
	}
	if len(ai) == 0 {
		ai = annotationValues(action)
	}
	return ai
}

// resetTypes are the ResetType values the power commands know (Reset action of
// ComputerSystem and Manager).
var resetTypes = []string{"On", "ForceOff", "GracefulShutdown", "GracefulRestart", "ForceRestart", "Nmi", "ForceOn", "PushPowerButton", "PowerCycle", "FullPowerCycle"}

// ResetTypeOf maps a power command (PowerOn, PowerForceOff, PowerReboot,
// PowerCycle...) to its ResetType, ok false for an unknown command.
func ResetTypeOf(command string) (string, bool) {
	if !strings.HasPrefix(command, "Power") {
		return "", false
	}
	t := command[5:]
	if command == "PowerCycle" {
		t = command
	}
	if t == "Reboot" {
		t = "GracefulRestart"
	}
	return t, slices.Contains(resetTypes, t)
}

// mapResetType swaps a ResetType for its equivalent when the service does not
// allow it and allows the equivalent (_map_reset_type).
func mapResetType(t string, allowed []string) string {
	equiv := map[string]string{"On": "ForceOn", "ForceOn": "On", "ForceOff": "GracefulShutdown", "GracefulShutdown": "ForceOff", "GracefulRestart": "ForceRestart", "ForceRestart": "GracefulRestart"}
	if slices.Contains(allowed, t) {
		return t
	}
	if m, ok := equiv[t]; ok && slices.Contains(allowed, m) {
		return m
	}
	return t
}

// SystemPower runs a power command on a system (manage_system_power).
func (c *Client) SystemPower(ctx context.Context, systemURI, command string) (Outcome, error) {
	return c.ManagePower(ctx, systemURI, "#ComputerSystem.Reset", command)
}

// ManagePower runs a power command (PowerOn, PowerForceOff, PowerGracefulShutdown,
// PowerGracefulRestart, PowerForceRestart, PowerReboot, PowerCycle,
// PowerFullPowerCycle) through the Reset action actionName of a resource, with
// the ResetType the service allows (ActionInfo or AllowableValues); nothing is
// sent when the resource is already in the state asked.
func (c *Client) ManagePower(ctx context.Context, resURI, actionName, command string) (Outcome, error) {
	t, ok := ResetTypeOf(command)
	if !ok {
		return Outcome{}, fmt.Errorf("redfish: invalid power command %q", command)
	}
	p, _, err := c.getProps(ctx, resURI)
	if err != nil {
		return Outcome{}, err
	}
	state := str(p, "PowerState")
	if (state == "On" && (t == "On" || t == "ForceOn")) || (state == "Off" && (t == "GracefulShutdown" || t == "ForceOff")) {
		return Outcome{Message: "already " + state}, nil
	}
	act := sub(sub(p, "Actions"), actionName)
	if act == nil {
		return Outcome{}, fmt.Errorf("%w: action %s not found on %s", ErrUnsupported, actionName, resURI)
	}
	target := servicePath(str(act, "target"))
	if target == "" {
		return Outcome{}, fmt.Errorf("%w: the target of action %s is missing", ErrMalformed, actionName)
	}
	t = mapResetType(t, c.ActionValues(ctx, act)["ResetType"])
	r, err := c.Post(ctx, target, Props{"ResetType": t})
	if err != nil {
		return Outcome{}, err
	}
	return outcomeOf(r, Outcome{Changed: true, Message: "ResetType " + t}), nil
}

// SetIndicatorLED sets the IndicatorLED of a system or a chassis (command
// IndicatorLedOn, IndicatorLedOff or IndicatorLedBlink; manage_indicator_led).
func (c *Client) SetIndicatorLED(ctx context.Context, uri, command string) (Outcome, error) {
	v, ok := map[string]string{"IndicatorLedOn": "Lit", "IndicatorLedOff": "Off", "IndicatorLedBlink": "Blinking"}[command]
	if !ok {
		return Outcome{}, fmt.Errorf("redfish: invalid command %q", command)
	}
	o, err := c.PatchChecked(ctx, uri, Props{"IndicatorLED": v})
	if err == nil && o.Changed {
		o.Message = "Set IndicatorLED to " + v
	}
	return o, err
}

// SetAssetTag sets the AssetTag of a system or a chassis (redfishtool
// "setAssetTag"; community.general has no row for it).
func (c *Client) SetAssetTag(ctx context.Context, uri, tag string) (Outcome, error) {
	return c.PatchChecked(ctx, uri, Props{"AssetTag": tag})
}

// vendor is the service's vendor: the root's Vendor, else the name of its first
// Oem object (HPE for Hpe and Hp), "" when unknown (_get_vendor).
func (c *Client) vendor(ctx context.Context) string {
	root, _, err := c.getProps(ctx, c.root()+"/")
	if err != nil {
		return ""
	}
	if v, ok := root["Vendor"].(string); ok {
		return v
	}
	if oem := sub(root, "Oem"); len(oem) > 0 {
		v := sortedKeys(oem)[0]
		if v == "Hpe" || v == "Hp" {
			v = "HPE"
		}
		return v
	}
	return ""
}

// BootOverride is a boot source override (ComputerSystem Boot).
type BootOverride struct {
	// Device is the BootSourceOverrideTarget (Pxe, Cd, Hdd, UefiTarget, UefiBootNext...).
	Device string
	// UefiTarget (UefiTarget device) and BootNext (UefiBootNext device).
	UefiTarget, BootNext string
	// Enabled is Once (SetOneTimeBoot), Continuous (EnableContinuousBootOverride)
	// or Disabled (DisableBootOverride).
	Enabled string
	// Mode is Legacy or UEFI, "" to leave it.
	Mode string
}

func bootPayload(bo BootOverride, curMode string, allowed []string, haveAllowed bool) (Props, error) {
	if bo.Enabled != "Disabled" && haveAllowed && !slices.Contains(allowed, bo.Device) {
		return nil, fmt.Errorf("%w: boot device %s not in the allowable values %v", ErrUnsupported, bo.Device, allowed)
	}
	switch {
	case bo.Enabled == "Disabled":
		return Props{"Boot": Props{"BootSourceOverrideEnabled": bo.Enabled, "BootSourceOverrideTarget": "None"}}, nil
	case bo.Device == "UefiTarget":
		if bo.UefiTarget == "" {
			return nil, errors.New("redfish: UefiTarget needs a uefi target")
		}
		b := Props{"BootSourceOverrideEnabled": bo.Enabled, "BootSourceOverrideTarget": bo.Device, "UefiTargetBootSourceOverride": bo.UefiTarget}
		if curMode == "Legacy" {
			b["BootSourceOverrideMode"] = "UEFI"
		}
		return Props{"Boot": b}, nil
	case bo.Device == "UefiBootNext":
		if bo.BootNext == "" {
			return nil, errors.New("redfish: UefiBootNext needs a boot_next")
		}
		b := Props{"BootSourceOverrideEnabled": bo.Enabled, "BootSourceOverrideTarget": bo.Device, "BootNext": bo.BootNext}
		if curMode == "Legacy" {
			b["BootSourceOverrideMode"] = "UEFI"
		}
		return Props{"Boot": b}, nil
	}
	b := Props{"BootSourceOverrideEnabled": bo.Enabled, "BootSourceOverrideTarget": bo.Device}
	if bo.Mode != "" {
		b["BootSourceOverrideMode"] = bo.Mode
	}
	return Props{"Boot": b}, nil
}

// SetBootOverride sets the boot source override of a system (set_boot_override:
// SetOneTimeBoot, EnableContinuousBootOverride, DisableBootOverride). The device
// must be in BootSourceOverrideTarget@Redfish.AllowableValues when the system
// lists them. Dell services get the oracle's two workarounds: the Settings
// object when the system cannot be patched, and no Enabled with UefiTarget.
func (c *Client) SetBootOverride(ctx context.Context, systemURI string, bo BootOverride) (Outcome, error) {
	if bo.Device == "" && bo.Enabled != "Disabled" {
		return Outcome{}, errors.New("redfish: a boot device is required for a boot override")
	}
	sys, _, err := c.getProps(ctx, systemURI)
	if err != nil {
		return Outcome{}, err
	}
	boot := sub(sys, "Boot")
	if boot == nil {
		return Outcome{}, fmt.Errorf("%w: Boot property not found", ErrUnsupported)
	}
	curMode := str(boot, "BootSourceOverrideMode")
	allowed, have := boot["BootSourceOverrideTarget@Redfish.AllowableValues"].([]any)
	payload, err := bootPayload(bo, curMode, stringList(allowed), have)
	if err != nil {
		return Outcome{}, err
	}
	o, err := c.PatchChecked(ctx, systemURI, payload)
	if err != nil && c.vendor(ctx) == "Dell" {
		if settings := servicePath(str(sub(sub(sys, "@Redfish.Settings"), "SettingsObject"), "@odata.id")); settings != "" {
			return c.setBootOverrideSettings(ctx, settings, bo, curMode)
		}
		if bo.Device == "UefiTarget" && bo.Enabled != "Disabled" {
			delete(payload["Boot"].(Props), "BootSourceOverrideEnabled")
			o, err = c.PatchChecked(ctx, systemURI, payload)
		}
	}
	if err == nil && o.Changed {
		o.Message = "Updated the boot override settings"
	}
	return o, err
}

func (c *Client) setBootOverrideSettings(ctx context.Context, settings string, bo BootOverride, curMode string) (Outcome, error) {
	fut, _, err := c.getProps(ctx, settings)
	if err != nil {
		return Outcome{}, err
	}
	allowed, have := sub(fut, "Boot")["BootSourceOverrideTarget@Redfish.AllowableValues"].([]any)
	payload, err := bootPayload(bo, curMode, stringList(allowed), have)
	if err != nil {
		return Outcome{}, err
	}
	o, err := c.PatchChecked(ctx, settings, payload)
	if err != nil && bo.Device == "UefiTarget" && bo.Enabled != "Disabled" {
		delete(payload["Boot"].(Props), "BootSourceOverrideEnabled")
		o, err = c.PatchChecked(ctx, settings, payload)
	}
	if err == nil && o.Changed {
		o.Message = "Updated the boot override settings"
	}
	return o, err
}

// BootOverrideOf is what GetBootOverride reports: the Boot override properties
// the system has and that are not null; an error when the override is not
// enabled-able at all (no BootSourceOverrideEnabled).
func (c *Client) BootOverrideOf(ctx context.Context, systemURI string) (Props, error) {
	sys, _, err := c.getProps(ctx, systemURI)
	if err != nil {
		return nil, err
	}
	boot := sub(sys, "Boot")
	if boot == nil {
		return nil, fmt.Errorf("%w: Key Boot not found", ErrUnsupported)
	}
	out := Props{}
	en, ok := boot["BootSourceOverrideEnabled"]
	if !ok {
		return nil, fmt.Errorf("%w: no boot override is enabled", ErrUnsupported)
	}
	if b, isBool := en.(bool); !isBool || b {
		for _, k := range []string{"BootSourceOverrideEnabled", "BootSourceOverrideTarget", "BootSourceOverrideMode", "UefiTargetBootSourceOverride", "BootSourceOverrideTarget@Redfish.AllowableValues"} {
			if v, ok := boot[k]; ok && v != nil {
				out[k] = v
			}
		}
	}
	return out, nil
}

// BootOrder is the boot order of a system as devices: each BootOption's
// DisplayName and BootOptionReference when the system has BootOptions, else the
// reference alone (get_boot_order).
func (c *Client) BootOrder(ctx context.Context, systemURI string) ([]Props, error) {
	sys, _, err := c.getProps(ctx, systemURI)
	if err != nil {
		return nil, err
	}
	boot := sub(sys, "Boot")
	order, ok := boot["BootOrder"].([]any)
	if !ok {
		return nil, fmt.Errorf("%w: Key BootOrder not found", ErrUnsupported)
	}
	opts := c.bootOptions(ctx, boot)
	var out []Props
	for _, x := range order {
		ref := fmt.Sprint(x)
		if o, ok := opts[ref]; ok {
			out = append(out, o)
		} else {
			out = append(out, Props{"BootOptionReference": ref})
		}
	}
	return out, nil
}

// bootOptions maps BootOptionReference to its DisplayName and reference; empty
// when the BootOptions collection is absent or unreadable (_get_boot_options_dict).
func (c *Client) bootOptions(ctx context.Context, boot Props) map[string]Props {
	coll := linkOf(boot, "BootOptions")
	if coll == "" {
		return map[string]Props{}
	}
	uris, err := c.members(ctx, coll)
	if err != nil {
		return map[string]Props{}
	}
	out := map[string]Props{}
	for _, u := range uris {
		o, _, err := c.getProps(ctx, u)
		if err != nil {
			return map[string]Props{}
		}
		ref, ok := o["BootOptionReference"].(string)
		if !ok {
			return map[string]Props{}
		}
		out[ref] = pick(o, "DisplayName", "BootOptionReference")
	}
	return out
}

// SetBootOrder sets the boot order of a system; each reference must be one of
// the system's BootOptions when it has them (set_boot_order).
func (c *Client) SetBootOrder(ctx context.Context, systemURI string, order []string) (Outcome, error) {
	if len(order) == 0 {
		return Outcome{}, errors.New("redfish: a boot order list is required")
	}
	sys, _, err := c.getProps(ctx, systemURI)
	if err != nil {
		return Outcome{}, err
	}
	boot := sub(sys, "Boot")
	if _, ok := boot["BootOrder"]; !ok {
		return Outcome{}, fmt.Errorf("%w: Key BootOrder not found", ErrUnsupported)
	}
	if opts := c.bootOptions(ctx, boot); len(opts) > 0 {
		for _, ref := range order {
			if _, ok := opts[ref]; !ok {
				return Outcome{}, fmt.Errorf("%w: BootOptionReference %s not found in BootOptions", ErrUnsupported, ref)
			}
		}
	}
	o, err := c.PatchChecked(ctx, systemURI, Props{"Boot": Props{"BootOrder": order}})
	if err == nil && o.Changed {
		o.Message = "Modified the boot order"
	}
	return o, err
}

// SetDefaultBootOrder runs the system's SetDefaultBootOrder action.
func (c *Client) SetDefaultBootOrder(ctx context.Context, systemURI string) (Outcome, error) {
	sys, _, err := c.getProps(ctx, systemURI)
	if err != nil {
		return Outcome{}, err
	}
	const action = "#ComputerSystem.SetDefaultBootOrder"
	act := sub(sub(sys, "Actions"), action)
	if act == nil {
		return Outcome{}, fmt.Errorf("%w: action %s not found", ErrUnsupported, action)
	}
	target := servicePath(str(act, "target"))
	if target == "" {
		return Outcome{}, fmt.Errorf("%w: the target of action %s is missing", ErrMalformed, action)
	}
	r, err := c.Post(ctx, target, Props{})
	if err != nil {
		return Outcome{}, err
	}
	return outcomeOf(r, Outcome{Changed: true, Message: "BootOrder set to default"}), nil
}

// biosOf reads the Bios resource of a system.
func (c *Client) biosOf(ctx context.Context, systemURI string) (string, Props, error) {
	sys, _, err := c.getProps(ctx, systemURI)
	if err != nil {
		return "", nil, err
	}
	u := linkOf(sys, "Bios")
	if u == "" {
		return "", nil, fmt.Errorf("%w: Bios resource not found", ErrUnsupported)
	}
	b, _, err := c.getProps(ctx, u)
	return u, b, err
}

// BiosAttributes are the current BIOS attributes of a system (get_bios_attributes).
func (c *Client) BiosAttributes(ctx context.Context, systemURI string) (Props, error) {
	_, b, err := c.biosOf(ctx, systemURI)
	if err != nil {
		return nil, err
	}
	a := sub(b, "Attributes")
	if a == nil {
		return nil, fmt.Errorf("%w: the Bios resource has no Attributes", ErrUnsupported)
	}
	return a, nil
}

// SetBiosAttributes sets BIOS attributes through the Settings object of the Bios
// resource (@Redfish.Settings, DSP0266 9.10; set_bios_attributes). Attributes
// the BIOS does not have are left out and named in Outcome.Warning; the ones it
// already holds are not sent. applyTime, when not empty, is sent as
// @Redfish.SettingsApplyTime (DSP0266 7.12: Immediate, OnReset, AtMaintenanceWindowStart...);
// a Dell service gets OnReset when it is empty, as in the oracle. The change
// takes effect as the service applies it (usually a reset).
func (c *Client) SetBiosAttributes(ctx context.Context, systemURI string, attrs Props, applyTime string) (Outcome, error) {
	_, b, err := c.biosOf(ctx, systemURI)
	if err != nil {
		return Outcome{}, err
	}
	cur := sub(b, "Attributes")
	patch, bad := Props{}, Props{}
	for k, v := range attrs {
		cv, ok := cur[k]
		switch {
		case !ok:
			bad[k] = v
		case !sameJSON(cv, v):
			patch[k] = v
		}
	}
	warn := ""
	if len(bad) > 0 {
		j, _ := json.Marshal(bad)
		warn = "Unsupported attributes " + string(j)
	}
	if len(patch) == 0 {
		return Outcome{Message: "BIOS attributes already set", Warning: warn}, nil
	}
	settings := servicePath(str(sub(sub(b, "@Redfish.Settings"), "SettingsObject"), "@odata.id"))
	if settings == "" {
		return Outcome{}, fmt.Errorf("%w: Settings resource for BIOS attributes not found", ErrUnsupported)
	}
	payload := Props{"Attributes": patch}
	if applyTime == "" && c.vendor(ctx) == "Dell" {
		applyTime = "OnReset"
	}
	if applyTime != "" {
		payload["@Redfish.SettingsApplyTime"] = Props{"ApplyTime": applyTime}
	}
	cur2, resp, err := c.getProps(ctx, settings)
	if err != nil {
		return Outcome{}, err
	}
	tag := resp.ETag()
	if tag == "" {
		tag = str(cur2, "@odata.etag")
	}
	r, err := c.Patch(ctx, settings, payload, tag)
	if err != nil {
		return Outcome{}, err
	}
	j, _ := json.Marshal(patch)
	return outcomeOf(r, Outcome{Changed: true, Message: "Modified BIOS attributes " + string(j) + ". A reboot is required", Warning: warn}), nil
}

// ResetBios runs the Bios.ResetBios action: the BIOS goes back to its default
// settings (set_bios_default_settings).
func (c *Client) ResetBios(ctx context.Context, systemURI string) (Outcome, error) {
	_, b, err := c.biosOf(ctx, systemURI)
	if err != nil {
		return Outcome{}, err
	}
	target := servicePath(str(sub(sub(b, "Actions"), "#Bios.ResetBios"), "target"))
	if target == "" {
		return Outcome{}, fmt.Errorf("%w: ResetBios action not found", ErrUnsupported)
	}
	r, err := c.Post(ctx, target, Props{})
	if err != nil {
		return Outcome{}, err
	}
	return outcomeOf(r, Outcome{Changed: true, Message: "BIOS set to default settings"}), nil
}

// VerifyBiosAttributes checks that the BIOS holds the attributes given: an
// attribute it does not have, or holds with another value, is an error
// wrapping ErrUnsupported or ErrMismatch (verify_bios_attributes).
func (c *Client) VerifyBiosAttributes(ctx context.Context, systemURI string, attrs Props) (Outcome, error) {
	cur, err := c.BiosAttributes(ctx, systemURI)
	if err != nil {
		return Outcome{}, err
	}
	wrong, diff := Props{}, Props{}
	for k, v := range attrs {
		cv, ok := cur[k]
		switch {
		case !ok:
			wrong[k] = v
		case !sameJSON(cv, v):
			diff[k] = v
		}
	}
	if len(wrong) > 0 {
		j, _ := json.Marshal(wrong)
		return Outcome{}, fmt.Errorf("%w: wrong parameters are provided: %s", ErrUnsupported, j)
	}
	if len(diff) > 0 {
		j, _ := json.Marshal(diff)
		return Outcome{}, fmt.Errorf("%w: BIOS parameters are not matching: %s", ErrMismatch, j)
	}
	return Outcome{Message: "BIOS verification completed"}, nil
}

var safeName = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)

// BiosRegistry is the attribute registry of the system's BIOS: the document
// named by Bios.AttributeRegistry, found under Registries and read from its
// English Location (get_bios_registries). uri is where the document was read.
func (c *Client) BiosRegistry(ctx context.Context, systemURI string) (registry Props, uri string, err error) {
	_, b, err := c.biosOf(ctx, systemURI)
	if err != nil {
		return nil, "", err
	}
	name := str(b, "AttributeRegistry")
	if !safeName.MatchString(name) { // the name is the service's, and goes into a path
		return nil, "", fmt.Errorf("%w: AttributeRegistry %q", ErrMalformed, name)
	}
	regs, err := c.rootLink(ctx, "Registries")
	if err != nil {
		regs = c.root() + "/Registries"
	}
	reg, _, err := c.getProps(ctx, strings.TrimRight(regs, "/")+"/"+name)
	if err != nil {
		return nil, "", err
	}
	locs, _ := reg["Location"].([]any)
	for _, x := range locs { // the last English one wins, as in the oracle
		l, _ := x.(map[string]any)
		if lang, _ := l["Language"].(string); !strings.HasPrefix(lang, "en") {
			continue
		}
		switch u := l["Uri"].(type) {
		case string:
			uri = u
		case map[string]any: // HPE iLO 4: {"extref": ...}
			uri, _ = u["extref"].(string)
		}
	}
	if servicePath(uri) == "" {
		return nil, "", fmt.Errorf("%w: no English location of the registry %s", ErrNotFound, name)
	}
	uri = servicePath(uri)
	doc, _, err := c.getProps(ctx, uri)
	if err != nil {
		return nil, "", err
	}
	if _, ok := doc["RegistryEntries"]; !ok {
		return nil, "", fmt.Errorf("%w: RegistryEntries not present in %s", ErrMalformed, uri)
	}
	return doc, uri, nil
}

// SetSecureBoot enables or disables UEFI Secure Boot (SecureBootEnable of the
// system's SecureBoot resource; EnableSecureBoot is SetSecureBoot(true)).
func (c *Client) SetSecureBoot(ctx context.Context, systemURI string, enable bool) (Outcome, error) {
	sys, _, err := c.getProps(ctx, systemURI)
	if err != nil {
		return Outcome{}, err
	}
	u := linkOf(sys, "SecureBoot")
	if u == "" {
		return Outcome{}, fmt.Errorf("%w: SecureBoot resource not found", ErrUnsupported)
	}
	return c.PatchChecked(ctx, u, Props{"SecureBootEnable": enable})
}

// PowerRestorePolicy is the policy of a system (get_power_restore_policy).
func (c *Client) PowerRestorePolicy(ctx context.Context, systemURI string) (any, error) {
	sys, _, err := c.getProps(ctx, systemURI)
	if err != nil {
		return nil, err
	}
	v, ok := sys["PowerRestorePolicy"]
	if !ok {
		return nil, fmt.Errorf("%w: PowerRestorePolicy", ErrUnsupported)
	}
	return v, nil
}

// SetPowerRestorePolicy sets it (AlwaysOn, AlwaysOff, LastState).
func (c *Client) SetPowerRestorePolicy(ctx context.Context, systemURI, policy string) (Outcome, error) {
	return c.PatchChecked(ctx, systemURI, Props{"PowerRestorePolicy": policy})
}

// SystemInventory is the inventory properties of a system (get_system_inventory).
func (c *Client) SystemInventory(ctx context.Context, systemURI string) (Props, error) {
	sys, _, err := c.getProps(ctx, systemURI)
	if err != nil {
		return nil, err
	}
	return pick(sys, "Status", "HostName", "PowerState", "BootProgress", "Model", "Manufacturer", "PartNumber", "SystemType", "AssetTag", "ServiceTag", "SerialNumber", "SKU", "BiosVersion", "MemorySummary", "ProcessorSummary", "TrustedModules", "Name", "Id"), nil
}

// memberProps reads each member of the collection a resource links under key,
// keeping the named properties; keep may drop a member (nil to keep all).
func (c *Client) memberProps(ctx context.Context, resURI, key string, props []string, keep func(Props) bool, skipErrors bool) ([]Props, error) {
	res, _, err := c.getProps(ctx, resURI)
	if err != nil {
		return nil, err
	}
	coll := linkOf(res, key)
	if coll == "" {
		return nil, fmt.Errorf("%w: Key %s not found", ErrUnsupported, key)
	}
	uris, err := c.members(ctx, coll)
	if err != nil {
		return nil, err
	}
	out := []Props{}
	for _, u := range uris {
		m, _, err := c.getProps(ctx, u)
		if err != nil {
			if skipErrors && !errors.Is(err, ErrAuth) {
				continue
			}
			return nil, err
		}
		if keep != nil && !keep(m) {
			continue
		}
		out = append(out, pick(m, props...))
	}
	return out, nil
}

// CPUInventory lists the processors of a system (get_cpu_inventory).
func (c *Client) CPUInventory(ctx context.Context, systemURI string) ([]Props, error) {
	return c.memberProps(ctx, systemURI, "Processors", []string{"Id", "Name", "Manufacturer", "Model", "MaxSpeedMHz", "ProcessorArchitecture", "TotalCores", "TotalThreads", "Status"}, nil, false)
}

// MemoryInventory lists the memory modules of a system that are present: one
// whose Status is missing or Absent is left out (get_memory_inventory).
func (c *Client) MemoryInventory(ctx context.Context, systemURI string) ([]Props, error) {
	return c.memberProps(ctx, systemURI, "Memory", []string{"Id", "SerialNumber", "MemoryDeviceType", "PartNumber", "MemoryLocation", "RankCount", "CapacityMiB", "OperatingMemoryModes", "Status", "Manufacturer", "Name"},
		func(m Props) bool { return m["Status"] != nil && sub(m, "Status")["State"] != "Absent" }, false)
}

// NICInventory lists the EthernetInterfaces of a system or a manager that can
// be read (get_nic_inventory).
func (c *Client) NICInventory(ctx context.Context, resURI string) ([]Props, error) {
	return c.memberProps(ctx, resURI, "EthernetInterfaces", nicProperties, nil, true)
}

// PSUInventory lists the power supplies of the chassis that are present, from
// each chassis' Power resource (get_psu_inventory); an error when there is none.
func (c *Client) PSUInventory(ctx context.Context) ([]Props, error) {
	chassis, err := c.ChassisURIs(ctx)
	if err != nil {
		return nil, err
	}
	var out []Props
	for _, ch := range chassis {
		cp, _, err := c.getProps(ctx, ch)
		if err != nil {
			return nil, err
		}
		pu := linkOf(cp, "Power")
		if pu == "" {
			continue
		}
		pw, _, err := c.getProps(ctx, pu)
		if err != nil {
			return nil, err
		}
		list, ok := pw["PowerSupplies"].([]any)
		if !ok {
			return nil, fmt.Errorf("%w: Key PowerSupplies not found", ErrUnsupported)
		}
		for _, x := range list {
			psu, _ := x.(map[string]any)
			if len(out) >= MaxMembers {
				break
			}
			if sub(psu, "Status")["State"] == "Absent" {
				continue
			}
			d := Props{}
			for _, k := range []string{"Name", "Model", "SerialNumber", "PartNumber", "Manufacturer", "FirmwareVersion", "PowerCapacityWatts", "PowerSupplyType", "Status"} {
				if v, ok := psu[k]; ok && v != nil {
					d[k] = v
				}
			}
			out = append(out, d)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%w: no PowerSupply objects found", ErrNotFound)
	}
	return out, nil
}

// maxHealth bounds the entries of a health report.
const maxHealth = 1024

type healthWalk struct {
	c      *Client
	health Props
	n      int
}

// HealthReport is the Status of a resource and of its subsystems (get_health_report):
// a subsystem is a property ("Memory"), "Links.X" or "Prop.X" (the resource
// Prop links, then its X). Each entry names the uri of the member and its
// Status ("Status not available" when it has none).
func (c *Client) HealthReport(ctx context.Context, category, uri string, subsystems []string) (Props, error) {
	data, _, err := c.getProps(ctx, uri)
	if err != nil {
		return nil, err
	}
	w := &healthWalk{c: c, health: Props{}}
	w.health[category] = Props{"Status": statusOf(data)}
	for _, s := range subsystems {
		var d Props
		switch {
		case strings.HasPrefix(s, "Links."):
			s = strings.TrimPrefix(s, "Links.")
			d = sub(data, "Links")
		case strings.Contains(s, "."):
			p, rest, _ := strings.Cut(s, ".")
			s = rest
			if u := linkOf(data, p); u != "" {
				if r, _, err := c.getProps(ctx, u); err == nil {
					d = r
				}
			}
			if len(d) == 0 {
				continue
			}
		default:
			d = data
		}
		w.health[s] = []Props{}
		w.subsystem(ctx, s, d, 0)
		if l, _ := w.health[s].([]Props); len(l) == 0 {
			delete(w.health, s)
		}
	}
	return w.health, nil
}

func statusOf(p Props) any {
	if s, ok := p["Status"]; ok {
		return s
	}
	return "Status not available"
}

func toSingular(s string) string {
	switch {
	case strings.HasSuffix(s, "ies"):
		return s[:len(s)-3] + "y"
	case strings.HasSuffix(s, "s"):
		return s[:len(s)-1]
	}
	return s
}

func (w *healthWalk) add(subsystem string, e Props) {
	if w.n < maxHealth {
		w.n++
		w.health[subsystem] = append(w.health[subsystem].([]Props), e)
	}
}

func (w *healthWalk) subsystem(ctx context.Context, name string, data Props, depth int) {
	if depth > 3 {
		return
	}
	if v, ok := data[name]; ok {
		switch s := v.(type) {
		case []any:
			for _, x := range s {
				r, _ := x.(map[string]any)
				id, ok := r["@odata.id"].(string)
				if !ok {
					continue
				}
				var expanded Props
				if strings.Contains(id, "#") && len(r) > 1 {
					expanded = r
				}
				w.resource(ctx, name, id, expanded)
			}
		case map[string]any:
			if id, ok := s["@odata.id"].(string); ok {
				w.resource(ctx, name, id, nil)
			}
		}
		return
	}
	list, _ := data["Members"].([]any)
	for _, x := range list {
		m, _ := x.(map[string]any)
		u := servicePath(fmt.Sprint(m["@odata.id"]))
		if u == "" || w.n >= maxHealth {
			continue
		}
		if d, _, err := w.c.getProps(ctx, u); err == nil {
			w.subsystem(ctx, name, d, depth+1)
		}
	}
}

func (w *healthWalk) resource(ctx context.Context, subsystem, uri string, expanded Props) {
	key := toSingular(strings.ToLower(subsystem)) + "_uri"
	d := expanded
	if d == nil {
		p := servicePath(uri)
		if p == "" {
			return
		}
		var err error
		if d, _, err = w.c.getProps(ctx, p); err != nil {
			return
		}
	}
	if list, ok := d["Members"].([]any); ok { // a collection
		for _, x := range list {
			m, _ := x.(map[string]any)
			u, _ := m["@odata.id"].(string)
			if p := servicePath(u); p != "" && w.n < maxHealth {
				if r, _, err := w.c.getProps(ctx, p); err == nil {
					w.add(subsystem, Props{key: u, "Status": statusOf(r)})
				}
			}
		}
		return
	}
	w.add(subsystem, Props{key: uri, "Status": statusOf(d)})
}

// SystemHealthReport is the health of a system and of its processors, memory,
// storage and network interfaces (get_system_health_report).
func (c *Client) SystemHealthReport(ctx context.Context, systemURI string) (Props, error) {
	return c.HealthReport(ctx, "System", systemURI, []string{"Processors", "Memory", "SimpleStorage", "Storage", "EthernetInterfaces", "NetworkInterfaces.NetworkPorts", "NetworkInterfaces.NetworkDeviceFunctions"})
}
