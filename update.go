package redfish

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
)

// UpdateService: the Update commands of community.general redfish_command
// (SimpleUpdate, MultipartHTTPPushUpdate, PerformRequestedOperations) and
// redfish_info (GetFirmwareInventory, GetSoftwareInventory,
// GetFirmwareUpdateCapabilities, GetUpdateStatus); oracle: plugins/module_utils/
// redfish_utils.py of community.general, each function named after the method it
// ports (simple_update, multipath_http_push_update, get_update_status,
// perform_requested_update_operations, _operation_results,
// _software_inventory). The schemas are DSP8010 UpdateService 1.13 (SimpleUpdate,
// MultipartHttpPushUri, FirmwareInventory, SoftwareInventory), SoftwareInventory
// 1.10, Task 1.7 and the Base and Update message registries; the protocol rules
// are DSP0266 1.23.0: Actions 9.9.1 and AllowableValues 9.9.2, asynchronous
// operations 12.2, the OperationApplyTime annotation 9.9.8, and the multipart
// HTTP push update of the UpdateService (UpdateParameters then UpdateFile parts,
// multipart/form-data, RFC 7578).

// Limits of the Update area.
const (
	// DefaultMaxImage is the largest image a push sends unless PushOptions.MaxSize
	// says otherwise (4 GiB).
	DefaultMaxImage int64 = 4 << 30
	// DefaultUploadTimeout bounds the upload of an image (a request's own timeout
	// is meant for a small JSON document).
	DefaultUploadTimeout = 30 * time.Minute
	// maxInventory bounds the members read of a firmware or software inventory.
	maxInventory = 256
	maxImageURI  = 2048
)

// ErrImageTooLarge: the image is larger than the limit.
var ErrImageTooLarge = errors.New("redfish: the image is larger than the limit")

// updateService reads the UpdateService resource the service root links
// (_find_updateservice_resource).
func (c *Client) updateService(ctx context.Context) (Props, error) {
	uri, err := c.rootLink(ctx, "UpdateService")
	if err != nil {
		return nil, fmt.Errorf("%w: UpdateService resource not found", ErrNotFound)
	}
	p, _, err := c.getProps(ctx, uri)
	return p, err
}

// ---- Capabilities and inventories ----

// UpdateCapabilities is what GetFirmwareUpdateCapabilities reports.
type UpdateCapabilities struct {
	// MultipartSupported: the UpdateService has MultipartHttpPushUri.
	MultipartSupported bool `json:"multipart_supported"`
	// Entries maps each action (its title, else its key) to the transfer protocols
	// it allows.
	Entries map[string][]string `json:"entries"`
}

// NoProtocolList is what the oracle reports for an action that lists no
// TransferProtocol allowable values.
const NoProtocolList = "Key TransferProtocol@Redfish.AllowableValues not found"

// ParseUpdateCapabilities decodes an UpdateService resource
// (get_firmware_update_capabilities): ErrUnsupported when it has no Actions or
// an empty one.
func ParseUpdateCapabilities(data []byte) (UpdateCapabilities, error) {
	p, err := decodeProps(data)
	if err != nil {
		return UpdateCapabilities{}, fmt.Errorf("%w: %v", ErrMalformed, err)
	}
	_, multipart := p["MultipartHttpPushUri"]
	out := UpdateCapabilities{MultipartSupported: multipart, Entries: map[string][]string{}}
	actions, ok := p["Actions"].(map[string]any)
	switch {
	case !ok:
		return out, fmt.Errorf("%w: Key Actions not found", ErrUnsupported)
	case len(actions) == 0:
		return out, fmt.Errorf("%w: Actions list is empty", ErrUnsupported)
	}
	for _, key := range sortedKeys(actions) {
		a, _ := actions[key].(map[string]any)
		if a == nil || len(out.Entries) >= maxParams {
			continue
		}
		title := key
		if t, ok := a["title"].(string); ok {
			title = clipText(t)
		}
		vals, ok := a["TransferProtocol@Redfish.AllowableValues"]
		if !ok {
			out.Entries[title] = []string{NoProtocolList}
			continue
		}
		out.Entries[title] = stringList(vals)
	}
	return out, nil
}

// FirmwareUpdateCapabilities reads what the UpdateService can do
// (GetFirmwareUpdateCapabilities).
func (c *Client) FirmwareUpdateCapabilities(ctx context.Context) (UpdateCapabilities, error) {
	uri, err := c.rootLink(ctx, "UpdateService")
	if err != nil {
		return UpdateCapabilities{}, fmt.Errorf("%w: UpdateService resource not found", ErrNotFound)
	}
	r, err := c.Get(ctx, uri)
	if err != nil {
		return UpdateCapabilities{}, err
	}
	return ParseUpdateCapabilities(r.Body)
}

func jsonOf(v any) ([]byte, error) { return json.Marshal(v) }

var softwareProperties = []string{"Name", "Id", "Status", "Version", "Updateable", "SoftwareId", "LowestSupportedVersion", "Manufacturer", "ReleaseDate"}

// FirmwareInventory lists the members of the UpdateService's FirmwareInventory
// (GetFirmwareInventory); ErrNotFound when the service has none.
func (c *Client) FirmwareInventory(ctx context.Context) ([]Props, error) {
	return c.softwareInventory(ctx, "FirmwareInventory")
}

// SoftwareInventory is FirmwareInventory for the SoftwareInventory collection
// (GetSoftwareInventory).
func (c *Client) SoftwareInventory(ctx context.Context) ([]Props, error) {
	return c.softwareInventory(ctx, "SoftwareInventory")
}

// softwareInventory is _software_inventory: each member of the collection (paged
// by Members@odata.nextLink) is read and its standard properties kept; a member
// that cannot be read is an error, as in the oracle.
func (c *Client) softwareInventory(ctx context.Context, key string) ([]Props, error) {
	svc, err := c.updateService(ctx)
	if err != nil {
		return nil, err
	}
	coll := linkOf(svc, key)
	if coll == "" {
		return nil, fmt.Errorf("%w: No %s resource found", ErrNotFound, key)
	}
	uris, err := c.membersMax(ctx, coll, maxInventory, 32)
	if err != nil {
		return nil, err
	}
	out := []Props{}
	for _, u := range uris {
		r, err := c.Get(ctx, u)
		if err != nil {
			return nil, err
		}
		item, err := Pick(r.Body, softwareProperties...)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, nil
}

// ---- Operation results ----

// ResetRequest is a reset a Base.ResetRequired message asks for.
type ResetRequest struct {
	URI  string `json:"uri"`
	Type string `json:"type"`
}

// UpdateStatus is the status of an update operation (_operation_results).
type UpdateStatus struct {
	// Status is the TaskState (or JobState) of the task, or the one the status
	// code implies: Completed, New, Exception.
	Status   string    `json:"status"`
	Messages []Message `json:"messages"`
	// Handle is the task monitor to follow; empty once the operation is complete.
	Handle string `json:"handle,omitempty"`
	// ResetsRequested are the resets the update needs to continue.
	ResetsRequested []ResetRequest `json:"resets_requested"`
}

// messagesOf reads a list of Message objects, bounded; the arguments are kept as
// text (a service may send numbers).
func messagesOf(v any) []Message {
	l, _ := v.([]any)
	out := []Message{}
	for _, x := range l {
		m, _ := x.(map[string]any)
		if m == nil || len(out) >= maxInfo {
			continue
		}
		msg := Message{MessageID: clipText(str(m, "MessageId")), Message: clipText(str(m, "Message")),
			Severity: clipText(str(m, "Severity")), Resolution: clipText(str(m, "Resolution"))}
		args, _ := m["MessageArgs"].([]any)
		for _, a := range args {
			if len(msg.MessageArgs) < maxArgs {
				msg.MessageArgs = append(msg.MessageArgs, clipText(fmt.Sprint(a)))
			}
		}
		out = append(out, msg)
	}
	return out
}

// ParseUpdateStatus builds the status of an operation from the answer to it:
// its status code, its Location header, its body, and the handle the caller
// last used (_operation_results). The task monitor, the status and the messages
// come from the body when it is a Task or a Job, else from the error object's
// extended information; an OperationTransitionedToJob message moves the handle
// to the job, and a ResetRequired message adds a reset to ResetsRequested (only
// when it names a resource of the service).
func ParseUpdateStatus(code int, location string, body []byte, handle string) UpdateStatus {
	st := UpdateStatus{Messages: []Message{}, ResetsRequested: []ResetRequest{}}
	if code == http.StatusNoContent {
		st.Status = "Completed"
		return st
	}
	st.Handle = handle
	if code == http.StatusAccepted {
		if l := servicePath(location); l != "" {
			st.Handle = l
		}
	}
	data, _ := decodeProps(body)
	switch typ := str(data, "@odata.type"); {
	case data != nil && (strings.HasPrefix(typ, "#Task.") || strings.HasPrefix(typ, "#Job.")):
		st.Status = str(data, "TaskState")
		if st.Status == "" {
			st.Status = str(data, "JobState")
		}
		st.Status = clipText(st.Status)
		st.Messages = messagesOf(data["Messages"])
	case data != nil:
		// An error object, which is a bit of a misnomer since it is used in
		// successful action responses; the top-level @Message.ExtendedInfo of a
		// success is read too.
		st.Status = "Completed"
		switch {
		case code >= http.StatusBadRequest:
			st.Status = "Exception"
		case code == http.StatusAccepted:
			// The oracle calls this Completed and drops the handle; a 202 says the
			// task goes on (DSP0266 12.2), so it is New and the handle is kept.
			st.Status = "New"
		}
		info := sub(data, "error")["@Message.ExtendedInfo"]
		if info == nil {
			info = data["@Message.ExtendedInfo"]
		}
		st.Messages = messagesOf(info)
	default:
		st.Status = "Completed"
		if code == http.StatusAccepted {
			st.Status = "New"
		} else if code >= http.StatusBadRequest {
			st.Status = "Exception"
		}
	}
	switch st.Status {
	case "Completed", "Cancelled", "Exception", "Killed":
		st.Handle = ""
	}
	for _, m := range st.Messages {
		id := m.MessageID
		switch {
		case id == "":
		case strings.HasPrefix(id, "Update.1.") && strings.HasSuffix(id, ".OperationTransitionedToJob"):
			if len(m.MessageArgs) > 0 {
				if h := servicePath(m.MessageArgs[0]); h != "" {
					st.Status, st.Handle, st.ResetsRequested = "New", h, []ResetRequest{}
					return st
				}
			}
		case strings.HasPrefix(id, "Base.1.") && strings.HasSuffix(id, ".ResetRequired"):
			if len(m.MessageArgs) >= 2 {
				if u := servicePath(m.MessageArgs[0]); u != "" {
					st.ResetsRequested = append(st.ResetsRequested, ResetRequest{URI: u, Type: m.MessageArgs[1]})
				}
			}
		}
	}
	return st
}

// UpdateStatus reads the task or job that tracks an update (GetUpdateStatus).
func (c *Client) UpdateStatus(ctx context.Context, handle string) (UpdateStatus, error) {
	if handle == "" {
		return UpdateStatus{}, errors.New("redfish: a handle tracking the update is required")
	}
	p := servicePath(handle)
	if p == "" {
		return UpdateStatus{}, fmt.Errorf("%w: the handle %q is not a path of this service", ErrMalformed, clipText(handle))
	}
	r, err := c.Get(ctx, p)
	if err != nil {
		return UpdateStatus{}, err
	}
	return ParseUpdateStatus(r.Status, r.Header.Get("Location"), r.Body, p), nil
}

// WaitUpdate follows the task monitor of an update until the task ends (see
// WaitTask) and returns its final status.
func (c *Client) WaitUpdate(ctx context.Context, handle string, poll time.Duration, onProgress func(Task)) (UpdateStatus, error) {
	p := servicePath(handle)
	if p == "" {
		return UpdateStatus{}, fmt.Errorf("%w: the handle %q is not a path of this service", ErrMalformed, clipText(handle))
	}
	r, err := c.WaitTask(ctx, p, poll, onProgress)
	if err != nil {
		return UpdateStatus{}, err
	}
	return ParseUpdateStatus(r.Status, "", r.Body, p), nil
}

// UpdateResult is an update that was requested: what was done, and the status
// of the operation.
type UpdateResult struct {
	Outcome
	Status UpdateStatus
}

func scrubStatus(st *UpdateStatus, pw Secret) {
	if pw == "" {
		return
	}
	for i := range st.Messages {
		m := &st.Messages[i]
		for _, f := range []*string{&m.MessageID, &m.Message, &m.Severity, &m.Resolution} {
			if strings.Contains(*f, pw.Reveal()) {
				*f = "[redacted]"
			}
		}
		for j, a := range m.MessageArgs {
			if strings.Contains(a, pw.Reveal()) {
				m.MessageArgs[j] = "[redacted]"
			}
		}
	}
}

func resultOf(r *Response, msg string, pw Secret) UpdateResult {
	st := ParseUpdateStatus(r.Status, r.Header.Get("Location"), r.Body, "")
	scrubStatus(&st, pw)
	return UpdateResult{Outcome: outcomeOf(r, Outcome{Changed: true, Message: msg}), Status: st}
}

// ---- SimpleUpdate ----

// SimpleUpdateOptions are the options of SimpleUpdate (the module's
// update_image_uri, update_protocol, update_targets, update_creds and
// update_apply_time).
type SimpleUpdateOptions struct {
	// ImageURI is where the service fetches the image from (required): an absolute
	// URI whose scheme is one of the transfer protocols the service allows, and
	// that holds no credentials (Username and Password carry them).
	ImageURI string
	// Protocol is the TransferProtocol; empty: not sent.
	Protocol string
	// Targets are the resources to update; empty: the service decides.
	Targets []string
	// Username and Password give access to the image server. The password is a
	// Secret: it is sent, and never printed, returned in an error or kept in a
	// message of the result.
	Username  string
	Password  Secret
	ApplyTime string
}

// defaultProtocols are the TransferProtocol values assumed when the service lists
// none (the oracle's list, "NSF" and all).
var defaultProtocols = []string{"CIFS", "FTP", "SFTP", "HTTP", "HTTPS", "NSF", "SCP", "TFTP", "OEM", "NFS"}

func canonicalScheme(s string) string {
	s = strings.ToLower(s)
	if s == "smb" {
		return "cifs"
	}
	return s
}

// CheckImageURI validates the ImageURI of a SimpleUpdate against the transfer
// protocols the service allows: bounded, no control character or space, no
// credentials in it, and a scheme that is one of the allowed protocols and
// matches protocol when it is given. The error never repeats the URI.
func CheckImageURI(uri, protocol string, allowed []string) error {
	switch {
	case uri == "":
		return errors.New("redfish: an ImageURI is required for the SimpleUpdate command")
	case len(uri) > maxImageURI:
		return fmt.Errorf("redfish: the ImageURI is longer than %d bytes", maxImageURI)
	}
	for _, r := range uri {
		if r <= 0x20 || r == 0x7f {
			return errors.New("redfish: the ImageURI holds a space or a control character")
		}
	}
	u, err := url.Parse(uri)
	if err != nil {
		return errors.New("redfish: the ImageURI is not a valid URI")
	}
	if u.User != nil {
		// A user name alone (scp://user@host/...) names the account; a password in
		// a URI is a secret in a URL, refused.
		if _, has := u.User.Password(); has || strings.Contains(uri, ":@") {
			return errors.New("redfish: a password in the ImageURI is refused: use the password file or stdin option")
		}
	}
	if u.Scheme == "" {
		if protocol == "" {
			return errors.New("redfish: the ImageURI has no scheme: give the transfer protocol")
		}
		return nil
	}
	scheme := canonicalScheme(u.Scheme)
	ok := false
	for _, a := range allowed {
		ok = ok || canonicalScheme(a) == scheme
	}
	if !ok {
		return fmt.Errorf("%w: the scheme %q of the ImageURI is not a transfer protocol of the service (%s)", ErrUnsupported, clipText(u.Scheme), strings.Join(allowed, ", "))
	}
	if protocol != "" && canonicalScheme(protocol) != scheme {
		return fmt.Errorf("redfish: the scheme %q of the ImageURI differs from the transfer protocol %q", clipText(u.Scheme), clipText(protocol))
	}
	return nil
}

// checkApplyTime refuses an apply time the resource does not support, when it
// says (the OperationApplyTimeSupport annotation, DSP0266 9.9.8).
func checkApplyTime(holder Props, key, applyTime string) error {
	if applyTime == "" {
		return nil
	}
	supported := stringList(sub(holder, key+"@Redfish.OperationApplyTimeSupport")["SupportedValues"])
	if len(supported) > 0 && !slices.Contains(supported, applyTime) {
		return fmt.Errorf("%w: the apply time %q is not supported by the service (%s)", ErrUnsupported, clipText(applyTime), strings.Join(supported, ", "))
	}
	return nil
}

// SimpleUpdate asks the service to fetch an image and update with it
// (simple_update): the UpdateService.SimpleUpdate action, its transfer protocol
// and targets checked against the allowable values (an ActionInfo resource, else
// the annotations). The answer is usually a task: Outcome.Monitor and
// Status.Handle name its monitor (WaitUpdate follows it).
func (c *Client) SimpleUpdate(ctx context.Context, o SimpleUpdateOptions) (UpdateResult, error) {
	if o.ImageURI == "" {
		return UpdateResult{}, errors.New("redfish: Must specify an ImageURI for the SimpleUpdate command")
	}
	svc, err := c.updateService(ctx)
	if err != nil {
		return UpdateResult{}, err
	}
	action := sub(sub(svc, "Actions"), "#UpdateService.SimpleUpdate")
	target := servicePath(str(action, "target"))
	if action == nil || target == "" {
		return UpdateResult{}, fmt.Errorf("%w: Service does not support SimpleUpdate", ErrUnsupported)
	}
	values := c.ActionValues(ctx, action)
	protocols := values["TransferProtocol"]
	if len(protocols) == 0 {
		protocols = defaultProtocols
	}
	if o.Protocol != "" && !slices.Contains(protocols, o.Protocol) {
		return UpdateResult{}, fmt.Errorf("%w: Specified protocol (%s) not supported by service. Supported protocols: %v", ErrUnsupported, clipText(o.Protocol), protocols)
	}
	if err := CheckImageURI(o.ImageURI, o.Protocol, protocols); err != nil {
		return UpdateResult{}, err
	}
	if allowed := values["Targets"]; len(allowed) > 0 {
		for _, t := range o.Targets {
			if !slices.Contains(allowed, t) {
				return UpdateResult{}, fmt.Errorf("%w: Specified target (%s) not supported by service. Supported targets: %v", ErrUnsupported, clipText(t), allowed)
			}
		}
	}
	if err := checkApplyTime(action, "", o.ApplyTime); err != nil {
		return UpdateResult{}, err
	}
	payload := Props{"ImageURI": o.ImageURI}
	if o.Protocol != "" {
		payload["TransferProtocol"] = o.Protocol
	}
	if len(o.Targets) > 0 {
		payload["Targets"] = anyList(o.Targets)
	}
	if o.Username != "" {
		payload["Username"] = o.Username
	}
	if o.Password != "" {
		if err := checkPassword(o.Password); err != nil {
			return UpdateResult{}, err
		}
		payload["Password"] = o.Password.Reveal()
	}
	if o.ApplyTime != "" {
		payload["@Redfish.OperationApplyTime"] = o.ApplyTime
	}
	r, err := c.Post(ctx, target, payload)
	if err != nil {
		return UpdateResult{}, redact(err, o.Password)
	}
	return resultOf(r, "SimpleUpdate requested", o.Password), nil
}

// ---- MultipartHTTPPushUpdate ----

// PushOptions are the options of MultipartHTTPPushUpdate (the module's
// update_image_file, update_targets, update_apply_time, update_oem_params and
// update_custom_oem_*).
type PushOptions struct {
	// ImageFile is the image to send (required): a regular file, streamed, never
	// held whole in memory.
	ImageFile string
	Targets   []string
	ApplyTime string
	// OEMParams is the Oem object of UpdateParameters.
	OEMParams Props
	// CustomHeader names an extra part of the upload (a form-data name of letters,
	// digits, dot, underscore and hyphen, not UpdateParameters or UpdateFile) that
	// holds CustomParams, of media type CustomMIMEType (default text/plain).
	CustomHeader, CustomMIMEType, CustomParams string
	// MaxSize is the largest image accepted, in bytes (default DefaultMaxImage);
	// a larger file is ErrImageTooLarge, and nothing is sent.
	MaxSize int64
	// Timeout bounds the whole upload (default DefaultUploadTimeout).
	Timeout time.Duration
}

var partName = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// partFilename is the name given to the file part: the base name of the path,
// with control characters replaced (the oracle sends the base name too).
func partFilename(p string) string {
	s := filepath.Base(p)
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return '_'
		}
		return r
	}, s)
}

// MultipartFrame builds the multipart/form-data body of a push but for the
// file's bytes: prefix (every part up to the UpdateFile part's headers),
// suffix (the closing boundary) and the Content-Type; the body is prefix, the
// file, suffix, so its length is known and the file can be streamed
// (_prepare_multipart). params is the JSON of the UpdateParameters part.
func MultipartFrame(params []byte, filename, customName, customMIME, customContent string) (prefix, suffix []byte, contentType string, err error) {
	filename = partFilename(filename)
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	part := func(name string, file bool, mimeType string) (io.Writer, error) {
		args := map[string]string{"name": name}
		if file {
			args["filename"] = filename
		}
		cd := mime.FormatMediaType("form-data", args)
		if cd == "" {
			return nil, fmt.Errorf("redfish: the part %q cannot be named", name)
		}
		h := textproto.MIMEHeader{}
		h.Set("Content-Disposition", cd)
		h.Set("Content-Type", mimeType)
		return w.CreatePart(h)
	}
	pw, err := part("UpdateParameters", false, "application/json")
	if err != nil {
		return nil, nil, "", err
	}
	if _, err = pw.Write(params); err != nil {
		return nil, nil, "", err
	}
	if customName != "" {
		switch {
		case !partName.MatchString(customName) || customName == "UpdateParameters" || customName == "UpdateFile":
			return nil, nil, "", fmt.Errorf("redfish: %q is not a usable name for an extra part", clipText(customName))
		case customMIME == "":
			customMIME = "text/plain"
		}
		if _, _, err := mime.ParseMediaType(customMIME); err != nil || strings.ContainsAny(customMIME, "\r\n") {
			return nil, nil, "", fmt.Errorf("redfish: %q is not a media type", clipText(customMIME))
		}
		cw, err := part(customName, false, customMIME)
		if err != nil {
			return nil, nil, "", err
		}
		if _, err = io.WriteString(cw, customContent); err != nil {
			return nil, nil, "", err
		}
	}
	if _, err = part("UpdateFile", true, "application/octet-stream"); err != nil {
		return nil, nil, "", err
	}
	prefix = bytes.Clone(buf.Bytes())
	buf.Reset()
	if err = w.Close(); err != nil {
		return nil, nil, "", err
	}
	return prefix, bytes.Clone(buf.Bytes()), w.FormDataContentType(), nil
}

// MultipartHTTPPushUpdate sends an image to the MultipartHttpPushUri of the
// UpdateService (multipath_http_push_update): a multipart/form-data upload of an
// UpdateParameters part (Targets, apply time, Oem) and the UpdateFile part.
// The file is opened first and must be a regular file within MaxSize; it is
// streamed with a known Content-Length, never read whole. The answer is usually
// a task, as for SimpleUpdate.
func (c *Client) MultipartHTTPPushUpdate(ctx context.Context, o PushOptions) (UpdateResult, error) {
	if o.ImageFile == "" {
		return UpdateResult{}, errors.New("redfish: Must specify an image file for the MultipartHTTPPushUpdate command")
	}
	limit := o.MaxSize
	if limit <= 0 {
		limit = DefaultMaxImage
	}
	f, err := os.Open(o.ImageFile)
	if err != nil {
		return UpdateResult{}, fmt.Errorf("redfish: Could not read file %s: %w", o.ImageFile, errors.Unwrap(err))
	}
	defer f.Close()
	st, err := f.Stat()
	switch {
	case err != nil:
		return UpdateResult{}, fmt.Errorf("redfish: Could not read file %s", o.ImageFile)
	case !st.Mode().IsRegular():
		return UpdateResult{}, fmt.Errorf("redfish: Must specify a valid file for the MultipartHTTPPushUpdate command: %s is not a regular file", o.ImageFile)
	case st.Size() > limit:
		return UpdateResult{}, fmt.Errorf("%w: %s is %d bytes, the limit is %d", ErrImageTooLarge, o.ImageFile, st.Size(), limit)
	}
	svc, err := c.updateService(ctx)
	if err != nil {
		return UpdateResult{}, err
	}
	if _, ok := svc["MultipartHttpPushUri"]; !ok {
		return UpdateResult{}, fmt.Errorf("%w: Service does not support MultipartHttpPushUri", ErrUnsupported)
	}
	target := servicePath(str(svc, "MultipartHttpPushUri"))
	if target == "" {
		return UpdateResult{}, fmt.Errorf("%w: MultipartHttpPushUri is not a path of the service", ErrMalformed)
	}
	if err := checkApplyTime(svc, "MultipartHttpPushUri", o.ApplyTime); err != nil {
		return UpdateResult{}, err
	}
	params := Props{}
	if len(o.Targets) > 0 {
		params["Targets"] = anyList(o.Targets)
	}
	if o.ApplyTime != "" {
		params["@Redfish.OperationApplyTime"] = o.ApplyTime
	}
	if len(o.OEMParams) > 0 {
		params["Oem"] = o.OEMParams
	}
	pj, err := jsonOf(params)
	if err != nil {
		return UpdateResult{}, err
	}
	custom := ""
	if o.CustomParams != "" || o.CustomHeader != "" {
		custom = o.CustomHeader
		if custom == "" {
			return UpdateResult{}, errors.New("redfish: an extra part needs a name (CustomHeader)")
		}
	}
	prefix, suffix, ctype, err := MultipartFrame(pj, partFilename(o.ImageFile), custom, o.CustomMIMEType, o.CustomParams)
	if err != nil {
		return UpdateResult{}, err
	}
	body := io.MultiReader(bytes.NewReader(prefix), io.LimitReader(f, st.Size()), bytes.NewReader(suffix))
	n := int64(len(prefix)) + st.Size() + int64(len(suffix))
	timeout := o.Timeout
	if timeout <= 0 {
		timeout = DefaultUploadTimeout
	}
	if err := c.login(ctx); err != nil {
		return UpdateResult{}, err
	}
	var code int
	data, h, err := c.requestReader(ctx, http.MethodPost, target, body, n, timeout, map[string]string{"Content-Type": ctype}, &code)
	if err != nil {
		return UpdateResult{}, err
	}
	return resultOf(&Response{Status: code, Header: h, Body: data}, "MultipartHTTPPushUpdate requested", ""), nil
}

// ---- PerformRequestedOperations ----

// resetTarget is where a requested reset is posted: the Reset action of the
// resource when it has one, else the resource itself, as the oracle does.
func (c *Client) resetTarget(ctx context.Context, uri string) string {
	p, _, err := c.getProps(ctx, uri)
	if err != nil {
		return uri
	}
	acts := sub(p, "Actions")
	keys := make([]string, 0, len(acts))
	for k := range acts {
		if strings.HasSuffix(k, ".Reset") {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		if t := servicePath(str(sub(acts, k), "target")); t != "" {
			return t
		}
	}
	return uri
}

// PerformRequestedOperations does what the update asks to continue
// (perform_requested_update_operations): the resets its task requests. Changed
// is false when it asks for none. When a reset fails, the Outcome says whether
// the earlier ones were done.
func (c *Client) PerformRequestedOperations(ctx context.Context, handle string) (Outcome, error) {
	st, err := c.UpdateStatus(ctx, handle)
	if err != nil {
		return Outcome{}, err
	}
	changed := false
	for _, rs := range st.ResetsRequested {
		if _, err := c.Post(ctx, c.resetTarget(ctx, rs.URI), Props{"ResetType": rs.Type}); err != nil {
			return Outcome{Changed: changed}, err
		}
		changed = true
	}
	if changed {
		return Outcome{Changed: true, Message: "One or more components reset to continue the update"}, nil
	}
	return Outcome{Message: "No operations required for the update"}, nil
}
