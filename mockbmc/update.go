package mockbmc

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// The UpdateService of the mock (DSP8010 UpdateService 1.13, DSP0266 1.23.0
// 12.2): on the rackmount tree the DMTF mockup carries the service with its
// FirmwareInventory and SimpleUpdate; augmentUpdate adds what the Update commands
// need and the mockup does not carry:
//
//   - MultipartHttpPushUri, a path that takes a multipart/form-data upload: an
//     UpdateParameters part (JSON) first, then optional extra parts, then the
//     UpdateFile part, whose bytes are counted and hashed and never kept. The
//     upload is bounded (Options.MaxUpload, default 8 MiB): a larger one is 413;
//   - a SoftwareInventory collection with two members;
//   - the Targets parameter of SimpleUpdate (an ActionInfo allowable list) and
//     the OperationApplyTimeSupport annotation of the push;
//   - Options.ResetRequired: the task an update starts carries a
//     Base.ResetRequired message that names the first system and GracefulRestart.
//
// The emulator tree has no UpdateService, as its mockup has none: the service
// root does not link one.

// Upload is a multipart upload the mock accepted.
type Upload struct {
	// Filename is the filename of the UpdateFile part.
	Filename string
	// Size and SHA256 describe the bytes of the UpdateFile part.
	Size   int64
	SHA256 string
	// Params is the decoded UpdateParameters part.
	Params map[string]any
	// Parts are the names of the parts, in order.
	Parts []string
	// Extra holds the content of the parts that are neither UpdateParameters nor
	// UpdateFile, by name.
	Extra map[string]string
	// ContentLength is the length the request declared (-1: chunked).
	ContentLength int64
	// Auth is the kind of authentication the request carried: "basic" or "token".
	Auth string
}

// Uploads returns the uploads accepted so far.
func (b *BMC) Uploads() []Upload {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(b.uploads)
}

// SimpleUpdates returns the bodies of the SimpleUpdate actions accepted so far.
func (b *BMC) SimpleUpdates() []map[string]any {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(b.simple)
}

// Resets returns the bodies of the Reset actions accepted so far, by the path
// they were posted to ("path {json}").
func (b *BMC) Resets() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(b.resets)
}

const (
	pushPath      = root + "/UpdateService/MultipartUpload"
	defaultUpload = 8 << 20
)

func (b *BMC) augmentUpdate(tree string) {
	us := b.res[root+"/UpdateService"]
	if us == nil {
		return
	}
	us["MultipartHttpPushUri"] = pushPath
	us["MultipartHttpPushUri@Redfish.OperationApplyTimeSupport"] = map[string]any{
		"@odata.type": "#Settings.v1_3_5.OperationApplyTimeSupport", "SupportedValues": []any{"Immediate", "OnReset"}}
	if acts, _ := us["Actions"].(map[string]any); acts != nil {
		if su, _ := acts["#UpdateService.SimpleUpdate"].(map[string]any); su != nil {
			su["title"] = "Simple Update"
		}
	}
	if ai := b.res[root+"/UpdateService/SimpleUpdateActionInfo"]; ai != nil {
		params, _ := ai["Parameters"].([]any)
		ai["Parameters"] = append(params, map[string]any{"Name": "Targets", "Required": false, "DataType": "StringArray",
			"AllowableValues": []any{root + "/UpdateService/FirmwareInventory/BIOS", root + "/UpdateService/FirmwareInventory/BMC"}})
	}
	const sw = root + "/UpdateService/SoftwareInventory"
	us["SoftwareInventory"] = map[string]any{"@odata.id": sw}
	b.res[sw] = map[string]any{"@odata.type": "#SoftwareInventoryCollection.SoftwareInventoryCollection", "@odata.id": sw,
		"Name": "Software Inventory Collection", "Members": []any{}, "Members@odata.count": 0}
	b.addMember(sw, sw+"/Agent", map[string]any{"@odata.type": "#SoftwareInventory.v1_10_1.SoftwareInventory", "Id": "Agent", "Name": "Contoso Management Agent",
		"Version": "2.4.1", "Updateable": false, "Status": map[string]any{"State": "Enabled", "Health": "OK"}, "Manufacturer": "Contoso"})
	b.addMember(sw, sw+"/Drivers", map[string]any{"@odata.type": "#SoftwareInventory.v1_10_1.SoftwareInventory", "Id": "Drivers", "Name": "Contoso Drivers",
		"Version": "7.0", "Updateable": true, "SoftwareId": "DRV-7", "Status": map[string]any{"State": "Enabled", "Health": "OK"}})
}

// newTask starts a task for an update and answers 202 with its monitor in
// Location and Retry-After (DSP0266 12.2).
func (b *BMC) newTask(w http.ResponseWriter) {
	b.nextTask++
	tp := root + "/TaskService/Tasks/" + strconv.Itoa(b.nextTask)
	b.tasks[tp] = &task{polls: b.opts.TaskPolls}
	tm := map[string]any{"@odata.type": "#Task.v1_7_3.Task", "Id": strconv.Itoa(b.nextTask), "Name": "Update", "TaskState": "Running", "TaskStatus": "OK", "PercentComplete": 0}
	if b.opts.ResetRequired {
		var systems []string
		for p := range b.res {
			if strings.HasPrefix(p, root+"/Systems/") && strings.Count(p, "/") == 4 {
				systems = append(systems, p)
			}
		}
		sort.Strings(systems)
		if len(systems) > 0 {
			tm["Messages"] = []any{map[string]any{"@odata.type": "#Message.v1_1_2.Message", "MessageId": "Base.1.19.ResetRequired",
				"Message": "The update needs a reset of " + systems[0] + ".", "Severity": "OK", "MessageArgs": []any{systems[0], "GracefulRestart"}}}
		}
	}
	b.addMember(root+"/TaskService/Tasks", tp, tm)
	w.Header().Set("Location", tp)
	w.Header().Set("Retry-After", "1")
	writeJSON(w, http.StatusAccepted, tm, "")
}

// push serves the multipart upload of the UpdateService.
func (b *BMC) push(w http.ResponseWriter, r *http.Request) {
	limit := int64(b.opts.MaxUpload)
	if limit <= 0 {
		limit = defaultUpload
	}
	mt, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mt != "multipart/form-data" || params["boundary"] == "" {
		b.fail(w, http.StatusUnsupportedMediaType, "", "A multipart/form-data body is required.", "Base.1.19.UnsupportedMediaType")
		return
	}
	if r.ContentLength > limit {
		b.fail(w, http.StatusRequestEntityTooLarge, "", "The upload is larger than the service accepts.", "Base.1.19.PayloadTooLarge")
		return
	}
	body := http.MaxBytesReader(w, r.Body, limit)
	mr := multipart.NewReader(body, params["boundary"])
	up := Upload{ContentLength: r.ContentLength, Extra: map[string]string{}, Auth: "token"}
	if _, _, basic := r.BasicAuth(); basic {
		up.Auth = "basic"
	}
	var sawParams, sawFile bool
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			b.uploadError(w, err)
			return
		}
		name := part.FormName()
		up.Parts = append(up.Parts, name)
		switch {
		case name == "UpdateParameters" && !sawParams && len(up.Parts) == 1:
			data, err := io.ReadAll(io.LimitReader(part, 1<<16))
			if err == nil {
				err = json.Unmarshal(data, &up.Params)
			}
			if err != nil {
				b.fail(w, http.StatusBadRequest, "", "UpdateParameters is not a JSON object.", "Base.1.19.MalformedJSON")
				return
			}
			sawParams = true
		case name == "UpdateFile" && !sawFile:
			h := sha256.New()
			n, err := io.Copy(h, part)
			if err != nil {
				b.uploadError(w, err)
				return
			}
			up.Filename, up.Size, up.SHA256 = part.FileName(), n, hex.EncodeToString(h.Sum(nil))
			sawFile = true
		default:
			data, err := io.ReadAll(io.LimitReader(part, 1<<16))
			if err != nil {
				b.uploadError(w, err)
				return
			}
			up.Extra[name] = string(data)
		}
	}
	if !sawParams || !sawFile {
		b.fail(w, http.StatusBadRequest, "", "The upload needs an UpdateParameters part, then an UpdateFile part.", "Base.1.19.PropertyMissing")
		return
	}
	if at, _ := up.Params["@Redfish.OperationApplyTime"].(string); at != "" && at != "Immediate" && at != "OnReset" {
		b.fail(w, http.StatusBadRequest, "", fmt.Sprintf("The value %v for the parameter @Redfish.OperationApplyTime is not in the list of acceptable values.", at), "Base.1.19.ActionParameterValueNotInList")
		return
	}
	b.uploads = append(b.uploads, up)
	b.newTask(w)
}

func (b *BMC) uploadError(w http.ResponseWriter, err error) {
	var tooBig *http.MaxBytesError
	if errors.As(err, &tooBig) {
		b.fail(w, http.StatusRequestEntityTooLarge, "", "The upload is larger than the service accepts.", "Base.1.19.PayloadTooLarge")
		return
	}
	b.fail(w, http.StatusBadRequest, "", "The multipart body is malformed.", "Base.1.19.MalformedJSON")
}
