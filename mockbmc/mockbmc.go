// Package mockbmc is a Redfish service for tests and for `redfish mock serve`:
// the DMTF "public-rackmount1" mockup (DSP2043, BSD-3-Clause, vendored under
// testdata with its licence) served by an http.Handler that is stateful enough
// to be driven the way a real BMC is.
//
// What it does, each rule from DSP0266 1.23.0 (section numbers are its):
//
//   - the service root and /redfish are open; every other request needs HTTP
//     Basic or an X-Auth-Token (13.3), else 401 with a Redfish error;
//   - POST to SessionService/Sessions logs in (13.3.4.2): 201, a Location header
//     naming the Session, an X-Auth-Token header; DELETE of the Session logs out;
//   - every resource answers with an ETag (the entity tag of its JSON), a PATCH
//     with If-Match that does not match is 412 (6.5, 7.6; RFC 9110 13.1.1);
//   - PATCH merges the properties sent into the resource and answers with it;
//   - POST to a collection creates a member (201 and Location), DELETE removes it;
//   - POST to an action target (Actions/<Namespace.Action>) is checked against
//     the AllowableValues of its parameters, and ComputerSystem.Reset changes
//     PowerState; UpdateService.SimpleUpdate answers 202 with a Task monitor in
//     Location and Retry-After (12.2), which answers 202 until it
//     completes and then 200 with the Task;
//   - an error is the Redfish error object with @Message.ExtendedInfo (8.6,
//     9.5.11).
//
// It is a test double: it checks what the tests need and nothing is secret.
package mockbmc

import (
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"path"
	"slices"
	"strconv"
	"strings"
	"sync"
)

//go:embed all:testdata/public-rackmount1 all:testdata/emulator-1u
var mockup embed.FS

const (
	root    = "/redfish/v1"
	maxBody = 1 << 20
)

// Trees are the mockups New can serve: the DMTF public-rackmount1 (DSP2043)
// and the static tree of the DMTF Redfish Interface Emulator, a second shape
// (see testdata/README-emulator-1u.txt).
const (
	TreeRackmount = "public-rackmount1"
	TreeEmulator  = "emulator-1u"
)

// Options configure a mock BMC.
type Options struct {
	// Tree is the mockup to serve (default TreeRackmount).
	Tree string
	// User and Password are the administrator (default admin / password).
	User, Password string
	// TaskPolls is how many times a Task monitor answers 202 before the task
	// completes (default 2).
	TaskPolls int
}

// BMC is the mock service. Handler serves it; it is safe for concurrent use.
type BMC struct {
	mu        sync.Mutex
	res       map[string]map[string]any // resource path -> JSON object
	accounts  map[string]string         // user name -> password
	sessions  map[string]string         // token -> session path
	tasks     map[string]*task          // task path -> state
	opts      Options
	nextTask  int
	nextToken int
	metadata  []byte
	tree      string
	// Log records "METHOD path status" of every request, for tests.
	log []string
}

type task struct {
	polls, done int
}

// New loads the mockup.
func New(o Options) (*BMC, error) {
	if o.User == "" {
		o.User, o.Password = "admin", "password"
	}
	if o.TaskPolls <= 0 {
		o.TaskPolls = 2
	}
	if o.Tree == "" {
		o.Tree = TreeRackmount
	}
	if o.Tree != TreeRackmount && o.Tree != TreeEmulator {
		return nil, fmt.Errorf("mockbmc: unknown tree %q", o.Tree)
	}
	mockupRoot := "testdata/" + o.Tree
	b := &BMC{res: map[string]map[string]any{}, accounts: map[string]string{o.User: o.Password},
		sessions: map[string]string{}, tasks: map[string]*task{}, opts: o}
	err := fs.WalkDir(mockup, mockupRoot, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel := strings.TrimPrefix(strings.TrimPrefix(p, mockupRoot), "/")
		switch {
		case rel == "$metadata/index.xml":
			b.metadata, err = mockup.ReadFile(p)
			return err
		case path.Base(rel) != "index.json" || strings.HasPrefix(rel, "$metadata") || strings.HasPrefix(rel, "odata/"):
			return nil
		}
		data, err := mockup.ReadFile(p)
		if err != nil {
			return err
		}
		var m map[string]any
		if err := json.Unmarshal(data, &m); err != nil {
			return fmt.Errorf("mockbmc: %s: %w", p, err)
		}
		dir := strings.TrimSuffix(path.Dir(rel), "/")
		key := root
		if dir != "." {
			key = root + "/" + dir
		}
		b.res[key] = m
		return nil
	})
	if err != nil {
		return nil, err
	}
	b.tree = mockupRoot
	b.augment(o.Tree)
	// A tree without a Sessions collection gets one, so that a session can be
	// opened (DSP0266 13.3.4.2).
	sp := root + "/SessionService/Sessions"
	if b.res[sp] == nil {
		b.res[sp] = map[string]any{"@odata.type": "#SessionCollection.SessionCollection", "@odata.id": sp,
			"Name": "Session Collection", "Members": []any{}, "Members@odata.count": 0}
	}
	return b, nil
}

// Log returns the requests served so far ("METHOD path status").
func (b *BMC) Log() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(b.log)
}

// Resource returns a copy of a resource as JSON (for assertions), or nil.
func (b *BMC) Resource(p string) []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	m, ok := b.res[strings.TrimRight(p, "/")]
	if !ok {
		return nil
	}
	out, _ := json.Marshal(m)
	return out
}

func etagOf(m map[string]any) string {
	cp := map[string]any{}
	for k, v := range m {
		if k != "@odata.etag" {
			cp[k] = v
		}
	}
	data, _ := json.Marshal(cp)
	sum := sha256.Sum256(data)
	return `W/"` + hex.EncodeToString(sum[:8]) + `"`
}

type statusWriter struct {
	http.ResponseWriter
	code int
}

func (w *statusWriter) WriteHeader(c int) { w.code = c; w.ResponseWriter.WriteHeader(c) }

// Handler is the service.
func (b *BMC) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sw := &statusWriter{ResponseWriter: w, code: 200}
		b.serve(sw, r)
		b.mu.Lock()
		b.log = append(b.log, r.Method+" "+r.URL.Path+" "+strconv.Itoa(sw.code))
		b.mu.Unlock()
	})
}

func (b *BMC) fail(w http.ResponseWriter, status int, code, msg, msgID string) {
	body := map[string]any{"error": map[string]any{
		"code": "Base.1.19.GeneralError", "message": msg,
		"@Message.ExtendedInfo": []any{map[string]any{"@odata.type": "#Message.v1_1_2.Message", "MessageId": msgID, "Message": msg, "Severity": "Critical"}},
	}}
	_ = code
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func (b *BMC) authenticated(r *http.Request) bool {
	if tok := r.Header.Get("X-Auth-Token"); tok != "" {
		_, ok := b.sessions[tok]
		return ok
	}
	if u, p, ok := r.BasicAuth(); ok {
		want, have := b.accounts[u]
		return have && subtle.ConstantTimeCompare([]byte(want), []byte(p)) == 1
	}
	return false
}

// creatable are the collections a client may add to and remove from.
var creatable = map[string]bool{
	root + "/SessionService/Sessions": true, root + "/AccountService/Accounts": true,
	root + "/EventService/Subscriptions": true, root + "/TaskService/Tasks": true,
}

func clean(p string) string {
	p = strings.TrimRight(p, "/")
	if p == "" {
		return "/"
	}
	return p
}

func (b *BMC) serve(w http.ResponseWriter, r *http.Request) {
	b.mu.Lock()
	defer b.mu.Unlock()
	p := clean(r.URL.Path)
	open := (r.Method == http.MethodGet && (p == "/redfish" || p == root || p == root+"/$metadata" || p == root+"/odata")) ||
		(r.Method == http.MethodPost && p == root+"/SessionService/Sessions")
	if !open && !b.authenticated(r) {
		w.Header().Set("WWW-Authenticate", `Basic realm="mockbmc"`)
		b.fail(w, http.StatusUnauthorized, "", "Authentication is required.", "Base.1.19.NoValidSession")
		return
	}
	switch {
	case p == "/redfish" && r.Method == http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{"v1": root + "/"}, "")
	case p == root+"/$metadata" && r.Method == http.MethodGet:
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write(b.metadata)
	case p == root+"/odata" && r.Method == http.MethodGet:
		data, err := mockup.ReadFile(b.tree + "/odata/index.json")
		if err != nil {
			b.fail(w, http.StatusNotFound, "", "The requested resource of type odata was not found.", "Base.1.19.ResourceNotFound")
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write(data)
	case strings.Contains(p, "/Actions/") && r.Method == http.MethodPost:
		b.action(w, r, p)
	case r.Method == http.MethodGet || r.Method == http.MethodHead:
		b.get(w, r, p)
	case r.Method == http.MethodPut:
		b.patch(w, r, p) // a PUT replaces; the mock merges, which is enough for what the tests need
	case r.Method == http.MethodPatch:
		b.patch(w, r, p)
	case r.Method == http.MethodPost:
		b.post(w, r, p)
	case r.Method == http.MethodDelete:
		b.del(w, p)
	default:
		w.Header().Set("Allow", "GET, PATCH, POST, DELETE")
		b.fail(w, http.StatusMethodNotAllowed, "", "Method not allowed.", "Base.1.19.ActionNotSupported")
	}
}

func writeJSON(w http.ResponseWriter, status int, m map[string]any, etag string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("OData-Version", "4.0")
	if etag != "" {
		w.Header().Set("ETag", etag)
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(m)
}

func (b *BMC) get(w http.ResponseWriter, r *http.Request, p string) {
	if t, ok := b.tasks[p]; ok { // a Task monitor
		b.monitor(w, p, t)
		return
	}
	m, ok := b.res[p]
	if !ok {
		b.fail(w, http.StatusNotFound, "", "The requested resource of type "+path.Base(p)+" was not found.", "Base.1.19.ResourceNotFound")
		return
	}
	// $select and $expand are not implemented: the service root would say so
	// in ProtocolFeaturesSupported, which this mockup's does not claim.
	tag := etagOf(m)
	if inm := r.Header.Get("If-None-Match"); inm != "" && inm == tag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	writeJSON(w, http.StatusOK, m, tag)
}

func readJSON(r *http.Request) (map[string]any, error) {
	data, err := io.ReadAll(io.LimitReader(r.Body, maxBody+1))
	if err != nil || len(data) > maxBody {
		return nil, fmt.Errorf("body unreadable or too large")
	}
	var m map[string]any
	if len(data) == 0 {
		return map[string]any{}, nil
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return m, nil
}

func merge(dst, src map[string]any) {
	for k, v := range src {
		if sm, ok := v.(map[string]any); ok {
			if dm, ok := dst[k].(map[string]any); ok {
				merge(dm, sm)
				continue
			}
		}
		dst[k] = v
	}
}

func (b *BMC) patch(w http.ResponseWriter, r *http.Request, p string) {
	m, ok := b.res[p]
	if !ok {
		b.fail(w, http.StatusNotFound, "", "The requested resource was not found.", "Base.1.19.ResourceNotFound")
		return
	}
	if im := r.Header.Get("If-Match"); im != "" && im != "*" && im != etagOf(m) {
		b.fail(w, http.StatusPreconditionFailed, "", "The ETag supplied did not match the ETag required to change this resource.", "Base.1.19.PreconditionFailed")
		return
	}
	in, err := readJSON(r)
	if err != nil {
		b.fail(w, http.StatusBadRequest, "", "The request body is not a JSON object.", "Base.1.19.MalformedJSON")
		return
	}
	for _, ro := range []string{"@odata.id", "@odata.type", "Id"} {
		delete(in, ro)
	}
	if pw, ok := in["Password"].(string); ok && strings.HasPrefix(p, root+"/AccountService/Accounts/") {
		if u, _ := m["UserName"].(string); u != "" {
			b.accounts[u] = pw
		}
		delete(in, "Password")
	}
	// A Settings object of a Bios resource with ApplyTime Immediate changes the
	// attributes in force at once (DSP0266 7.12); otherwise they stay pending.
	if at, _ := in["@Redfish.SettingsApplyTime"].(map[string]any); at != nil && at["ApplyTime"] == "Immediate" && strings.HasSuffix(p, "/Bios/Settings") {
		if attrs, _ := in["Attributes"].(map[string]any); attrs != nil {
			if bios := b.res[path.Dir(p)]; bios != nil {
				if cur, _ := bios["Attributes"].(map[string]any); cur != nil {
					merge(cur, attrs)
				}
			}
		}
	}
	// A VirtualMedia resource names the image it holds: setting or clearing the
	// Image sets or clears the ImageName, as a service does.
	if img, present := in["Image"]; present && strings.Contains(p, "/VirtualMedia/") {
		if s, _ := img.(string); s != "" {
			in["ImageName"] = path.Base(s)
		} else {
			in["ImageName"] = nil
		}
	}
	merge(m, in)
	writeJSON(w, http.StatusOK, m, etagOf(m))
}

func (b *BMC) post(w http.ResponseWriter, r *http.Request, p string) {
	in, err := readJSON(r)
	if err != nil {
		b.fail(w, http.StatusBadRequest, "", "The request body is not a JSON object.", "Base.1.19.MalformedJSON")
		return
	}
	if p == root+"/SessionService/Sessions" {
		u, _ := in["UserName"].(string)
		pw, _ := in["Password"].(string)
		want, have := b.accounts[u]
		if !have || subtle.ConstantTimeCompare([]byte(want), []byte(pw)) != 1 {
			b.fail(w, http.StatusUnauthorized, "", "The credentials are not valid.", "Base.1.19.InvalidCredentials")
			return
		}
		b.nextToken++
		id := strconv.Itoa(1000 + b.nextToken)
		token := "mock-token-" + id + "-" + hex.EncodeToString(sumOf(u+id))
		sp := p + "/" + id
		b.sessions[token] = sp
		b.addMember(p, sp, map[string]any{"@odata.type": "#Session.v1_7_0.Session", "Id": id, "Name": "User Session", "UserName": u})
		w.Header().Set("X-Auth-Token", token)
		w.Header().Set("Location", sp)
		writeJSON(w, http.StatusCreated, b.res[sp], etagOf(b.res[sp]))
		return
	}
	coll, ok := b.res[p]
	if !ok {
		b.fail(w, http.StatusNotFound, "", "The requested resource was not found.", "Base.1.19.ResourceNotFound")
		return
	}
	if _, isColl := coll["Members"]; !isColl || !creatable[p] {
		b.fail(w, http.StatusMethodNotAllowed, "", "POST is not supported on this resource.", "Base.1.19.ActionNotSupported")
		return
	}
	n := len(coll["Members"].([]any)) + 1
	id := strconv.Itoa(n)
	for b.res[p+"/"+id] != nil {
		n++
		id = strconv.Itoa(n)
	}
	mp := p + "/" + id
	if strings.HasPrefix(p, root+"/AccountService/Accounts") {
		u, _ := in["UserName"].(string)
		pw, _ := in["Password"].(string)
		if u == "" || pw == "" {
			b.fail(w, http.StatusBadRequest, "", "UserName and Password are required.", "Base.1.19.PropertyMissing")
			return
		}
		if _, dup := b.accounts[u]; dup {
			b.fail(w, http.StatusConflict, "", "The account already exists.", "Base.1.19.ResourceAlreadyExists")
			return
		}
		b.accounts[u] = pw
		delete(in, "Password")
		in["Enabled"] = true
	}
	in["Id"] = id
	b.addMember(p, mp, in)
	w.Header().Set("Location", mp)
	writeJSON(w, http.StatusCreated, b.res[mp], etagOf(b.res[mp]))
}

func (b *BMC) addMember(coll, member string, m map[string]any) {
	m["@odata.id"] = member
	b.res[member] = m
	c := b.res[coll]
	ms, _ := c["Members"].([]any)
	c["Members"] = append(ms, map[string]any{"@odata.id": member})
	c["Members@odata.count"] = len(ms) + 1
}

func (b *BMC) del(w http.ResponseWriter, p string) {
	m, ok := b.res[p]
	if !ok {
		b.fail(w, http.StatusNotFound, "", "The requested resource was not found.", "Base.1.19.ResourceNotFound")
		return
	}
	coll := path.Dir(p)
	c, isMember := b.res[coll]
	if _, hasMembers := c["Members"]; !isMember || !hasMembers || !creatable[coll] {
		b.fail(w, http.StatusMethodNotAllowed, "", "DELETE is not supported on this resource.", "Base.1.19.ActionNotSupported")
		return
	}
	var keep []any
	for _, x := range c["Members"].([]any) {
		if x.(map[string]any)["@odata.id"] != p {
			keep = append(keep, x)
		}
	}
	c["Members"], c["Members@odata.count"] = keep, len(keep)
	if u, _ := m["UserName"].(string); u != "" && strings.HasPrefix(p, root+"/AccountService/Accounts/") {
		delete(b.accounts, u)
	}
	for tok, sp := range b.sessions {
		if sp == p {
			delete(b.sessions, tok)
		}
	}
	delete(b.res, p)
	w.WriteHeader(http.StatusNoContent)
}

// action serves POST <resource>/Actions/<Name>.
func (b *BMC) action(w http.ResponseWriter, r *http.Request, p string) {
	resPath, _, _ := strings.Cut(p, "/Actions/")
	m, ok := b.res[resPath]
	var spec map[string]any
	// The action is the one whose target is the path posted to; its name is its
	// key ("#LogService.ClearLog"), not the last segment of the target: the
	// emulator's mockup targets ClearLog at ".../LogService.Reset".
	name := ""
	if ok {
		if acts, _ := m["Actions"].(map[string]any); acts != nil {
			for k, v := range acts {
				if sm, _ := v.(map[string]any); sm != nil && clean(fmt.Sprint(sm["target"])) == p {
					spec, name = sm, strings.TrimPrefix(k, "#")
				}
			}
		}
	}
	if spec == nil {
		b.fail(w, http.StatusNotFound, "", "The action is not supported by this resource.", "Base.1.19.ActionNotSupported")
		return
	}
	in, err := readJSON(r)
	if err != nil {
		b.fail(w, http.StatusBadRequest, "", "The request body is not a JSON object.", "Base.1.19.MalformedJSON")
		return
	}
	// AllowableValues of the parameters (DSP0266 9.9.2, @Redfish.AllowableValues
	// in the action's description).
	for k, v := range spec {
		param, found := strings.CutSuffix(k, "@Redfish.AllowableValues")
		if !found {
			continue
		}
		if got, present := in[param]; present {
			var allowed []string
			for _, a := range v.([]any) {
				allowed = append(allowed, fmt.Sprint(a))
			}
			if !slices.Contains(allowed, fmt.Sprint(got)) {
				b.fail(w, http.StatusBadRequest, "", fmt.Sprintf("The value %v for the parameter %s is not in the list of acceptable values.", got, param), "Base.1.19.ActionParameterValueNotInList")
				return
			}
		}
	}
	// The ActionInfo resource, when the action names one (DSP0266 9.9.6): its
	// Parameters list the values allowed.
	if ai, _ := spec["@Redfish.ActionInfo"].(string); ai != "" {
		if info := b.res[clean(ai)]; info != nil {
			params, _ := info["Parameters"].([]any)
			for _, x := range params {
				pm, _ := x.(map[string]any)
				name, _ := pm["Name"].(string)
				allowed, _ := pm["AllowableValues"].([]any)
				got, present := in[name]
				if !present || len(allowed) == 0 {
					continue
				}
				ok := false
				for _, a := range allowed {
					ok = ok || fmt.Sprint(a) == fmt.Sprint(got)
				}
				if !ok {
					b.fail(w, http.StatusBadRequest, "", fmt.Sprintf("The value %v for the parameter %s is not in the list of acceptable values.", got, name), "Base.1.19.ActionParameterValueNotInList")
					return
				}
			}
		}
	}
	switch {
	case isAction(name, "SetDefaultBootOrder"):
		if boot, _ := m["Boot"].(map[string]any); boot != nil {
			boot["BootOrder"] = []any{"Boot0001", "Boot0002"}
		}
		w.WriteHeader(http.StatusNoContent)
	case isAction(name, "ResetBios"):
		m["ResetBiosToDefaultsPending"] = true
		w.WriteHeader(http.StatusNoContent)
	case isAction(name, "ResetToDefaults"):
		m["ResetToDefaultsType"] = in["ResetType"]
		w.WriteHeader(http.StatusNoContent)
	case isAction(name, "ClearLog"):
		// The log is emptied: its entries are removed (LogService.ClearLog).
		entries, _ := m["Entries"].(map[string]any)
		if ec := b.res[clean(fmt.Sprint(entries["@odata.id"]))]; ec != nil {
			for _, x := range ec["Members"].([]any) {
				delete(b.res, clean(fmt.Sprint(x.(map[string]any)["@odata.id"])))
			}
			ec["Members"], ec["Members@odata.count"] = []any{}, 0
		}
		w.WriteHeader(http.StatusNoContent)
	case isAction(name, "InsertMedia"):
		img, _ := in["Image"].(string)
		if img == "" {
			b.fail(w, http.StatusBadRequest, "", "Image is required.", "Base.1.19.ActionParameterMissing")
			return
		}
		m["Image"], m["ImageName"] = img, path.Base(img)
		m["Inserted"] = true
		if v, ok := in["Inserted"].(bool); ok {
			m["Inserted"] = v
		}
		for _, k := range []string{"WriteProtected", "TransferProtocolType", "TransferMethod", "UserName"} {
			if v, ok := in[k]; ok {
				m[k] = v
			}
		}
		w.WriteHeader(http.StatusNoContent)
	case isAction(name, "EjectMedia"):
		m["Image"], m["ImageName"], m["Inserted"] = nil, nil, false
		w.WriteHeader(http.StatusNoContent)
	case isAction(name, "Reset"):
		rt, _ := in["ResetType"].(string)
		if strings.HasPrefix(resPath, root+"/Managers/") {
			m["LastResetType"] = rt
		}
		switch rt {
		case "On", "ForceOn":
			m["PowerState"] = "On"
		case "ForceOff", "GracefulShutdown", "PushPowerButton":
			m["PowerState"] = "Off"
		}
		w.WriteHeader(http.StatusNoContent)
	case isAction(name, "SimpleUpdate"):
		if u, _ := in["ImageURI"].(string); u == "" {
			b.fail(w, http.StatusBadRequest, "", "ImageURI is required.", "Base.1.19.ActionParameterMissing")
			return
		}
		b.nextTask++
		tp := root + "/TaskService/Tasks/" + strconv.Itoa(b.nextTask)
		b.tasks[tp] = &task{polls: b.opts.TaskPolls}
		tm := map[string]any{"@odata.type": "#Task.v1_7_3.Task", "Id": strconv.Itoa(b.nextTask), "Name": "Update", "TaskState": "Running", "TaskStatus": "OK", "PercentComplete": 0}
		b.addMember(root+"/TaskService/Tasks", tp, tm)
		w.Header().Set("Location", tp)
		w.Header().Set("Retry-After", "1")
		writeJSON(w, http.StatusAccepted, tm, "")
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

// monitor answers a Task monitor: 202 with the Task while it runs, then 200.
func (b *BMC) monitor(w http.ResponseWriter, p string, t *task) {
	m := b.res[p]
	t.done++
	if t.done <= t.polls {
		m["PercentComplete"] = t.done * 100 / (t.polls + 1)
		w.Header().Set("Retry-After", "1")
		w.Header().Set("Location", p)
		writeJSON(w, http.StatusAccepted, m, "")
		return
	}
	m["TaskState"], m["PercentComplete"] = "Completed", 100
	writeJSON(w, http.StatusOK, m, etagOf(m))
}

func sumOf(s string) []byte { h := sha256.Sum256([]byte(s)); return h[:6] }

// isAction: the action's name with or without its namespace (ComputerSystem.Reset, SimpleUpdate).
func isAction(name, want string) bool { return name == want || strings.HasSuffix(name, "."+want) }
