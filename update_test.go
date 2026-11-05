package redfish_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/go-redfish/redfish"
	"github.com/go-redfish/redfish/mockbmc"
)

const secretPW = "Tr0ub4dor&3-image-server"

func TestFirmwareUpdateCapabilities(t *testing.T) {
	_, srv := newMock(t, mockbmc.Options{})
	c := newClient(t, srv, redfish.Config{})
	caps, err := c.FirmwareUpdateCapabilities(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// The oracle reads the TransferProtocol annotation only: the mockup lists the
	// protocols in an ActionInfo, so the oracle's sentinel is the entry.
	got := caps.Entries["Simple Update"]
	if !caps.MultipartSupported || len(got) != 1 || got[0] != redfish.NoProtocolList {
		t.Fatalf("capabilities = %+v", caps)
	}
	// The emulator tree has no UpdateService.
	_, srv2 := newMock(t, mockbmc.Options{Tree: mockbmc.TreeEmulator})
	_, err = newClient(t, srv2, redfish.Config{}).FirmwareUpdateCapabilities(context.Background())
	if !errors.Is(err, redfish.ErrNotFound) {
		t.Fatalf("emulator: %v", err)
	}
	for name, doc := range map[string]string{
		"no actions":    `{"Id":"U"}`,
		"empty actions": `{"Actions":{}}`,
	} {
		if _, err := redfish.ParseUpdateCapabilities([]byte(doc)); !errors.Is(err, redfish.ErrUnsupported) {
			t.Errorf("%s: %v", name, err)
		}
	}
	c2, err := redfish.ParseUpdateCapabilities([]byte(`{"Actions":{"#UpdateService.SimpleUpdate":{"title":"SU","TransferProtocol@Redfish.AllowableValues":["HTTP","TFTP"]},"#X.Y":{}}}`))
	if err != nil || c2.MultipartSupported || !slices.Equal(c2.Entries["SU"], []string{"HTTP", "TFTP"}) || c2.Entries["#X.Y"][0] != redfish.NoProtocolList {
		t.Fatalf("parse = %+v, %v", c2, err)
	}
}

func TestUpdateInventories(t *testing.T) {
	_, srv := newMock(t, mockbmc.Options{})
	c := newClient(t, srv, redfish.Config{})
	ctx := context.Background()
	fw, err := c.FirmwareInventory(ctx)
	if err != nil || len(fw) != 3 {
		t.Fatalf("FirmwareInventory = %v, %v", fw, err)
	}
	found := false
	for _, it := range fw {
		if it["Id"] == "BIOS" {
			found = true
			if it["Version"] != "P79 v1.45" || it["Updateable"] != true || it["SoftwareId"] == nil {
				t.Fatalf("BIOS = %v", it)
			}
			if _, ok := it["RelatedItem"]; ok {
				t.Fatalf("a property that is not on the oracle's list: %v", it)
			}
		}
	}
	if !found {
		t.Fatalf("no BIOS in %v", fw)
	}
	sw, err := c.SoftwareInventory(ctx)
	if err != nil || len(sw) != 2 || sw[0]["Name"] == nil {
		t.Fatalf("SoftwareInventory = %v, %v", sw, err)
	}
	_, srv2 := newMock(t, mockbmc.Options{Tree: mockbmc.TreeEmulator})
	c2 := newClient(t, srv2, redfish.Config{})
	if _, err := c2.FirmwareInventory(ctx); !errors.Is(err, redfish.ErrNotFound) {
		t.Fatalf("emulator firmware: %v", err)
	}
	if _, err := c2.SoftwareInventory(ctx); !errors.Is(err, redfish.ErrNotFound) {
		t.Fatalf("emulator software: %v", err)
	}
}

func TestSimpleUpdate(t *testing.T) {
	b, srv := newMock(t, mockbmc.Options{})
	c := newClient(t, srv, redfish.Config{})
	ctx := context.Background()
	bios := "/redfish/v1/UpdateService/FirmwareInventory/BIOS"
	res, err := c.SimpleUpdate(ctx, redfish.SimpleUpdateOptions{
		ImageURI: "https://images.example/bios.bin", Protocol: "HTTPS", Targets: []string{bios},
		Username: "fetcher", Password: secretPW, ApplyTime: "OnReset"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Changed || res.Message != "SimpleUpdate requested" || res.Status.Status != "Running" || res.Status.Handle == "" || res.Monitor != res.Status.Handle {
		t.Fatalf("result = %+v", res)
	}
	sent := b.SimpleUpdates()
	if len(sent) != 1 || sent[0]["Password"] != secretPW || sent[0]["Username"] != "fetcher" || sent[0]["TransferProtocol"] != "HTTPS" || sent[0]["@Redfish.OperationApplyTime"] != "OnReset" {
		t.Fatalf("the service received %v", sent)
	}
	if strings.Contains(fmt.Sprintf("%v %+v %#v", res, res, res), secretPW) {
		t.Fatal("the password is in the result")
	}
	// WaitUpdate follows the monitor: progress, then Completed and no handle.
	var progress []float64
	st, err := c.WaitUpdate(ctx, res.Status.Handle, 100*time.Millisecond, func(tk redfish.Task) {
		if tk.PercentComplete != nil {
			progress = append(progress, *tk.PercentComplete)
		}
	})
	if err != nil || st.Status != "Completed" || st.Handle != "" || len(progress) != 2 {
		t.Fatalf("WaitUpdate = %+v, %v, progress %v", st, err, progress)
	}
	// GetUpdateStatus of the finished task.
	st, err = c.UpdateStatus(ctx, res.Status.Handle)
	if err != nil || st.Status != "Completed" || st.Handle != "" {
		t.Fatalf("UpdateStatus = %+v, %v", st, err)
	}
}

func TestSimpleUpdateRefusals(t *testing.T) {
	b, srv := newMock(t, mockbmc.Options{})
	c := newClient(t, srv, redfish.Config{})
	ctx := context.Background()
	for name, o := range map[string]redfish.SimpleUpdateOptions{
		"no image":              {},
		"protocol not allowed":  {ImageURI: "https://h/x", Protocol: "NFS", Password: secretPW},
		"scheme not allowed":    {ImageURI: "file:///etc/passwd", Password: secretPW},
		"scheme vs protocol":    {ImageURI: "http://h/x", Protocol: "HTTPS"},
		"password in the URI":   {ImageURI: "https://u:" + secretPW + "@h/x"},
		"empty password in URI": {ImageURI: "https://u:@h/x"},
		"no scheme no protocol": {ImageURI: "h/x"},
		"control character":     {ImageURI: "https://h/x\r\nHost: evil"},
		"too long":              {ImageURI: "https://h/" + strings.Repeat("a", 3000)},
		"target not allowed":    {ImageURI: "https://h/x", Targets: []string{"/redfish/v1/Systems/1"}},
	} {
		_, err := c.SimpleUpdate(ctx, o)
		if err == nil {
			t.Errorf("%s: accepted", name)
			continue
		}
		if strings.Contains(err.Error(), secretPW) {
			t.Errorf("%s: the error holds the password: %v", name, err)
		}
	}
	if n := len(b.SimpleUpdates()); n != 0 {
		t.Fatalf("%d refused updates reached the service", n)
	}
	// A user name alone in the URI names the account, and scp is allowed.
	if _, err := c.SimpleUpdate(ctx, redfish.SimpleUpdateOptions{ImageURI: "scp://deploy@h/x.bin", Protocol: "SCP"}); err != nil {
		t.Fatalf("scp with a user name: %v", err)
	}
	_, srv2 := newMock(t, mockbmc.Options{Tree: mockbmc.TreeEmulator})
	_, err := newClient(t, srv2, redfish.Config{}).SimpleUpdate(ctx, redfish.SimpleUpdateOptions{ImageURI: "https://h/x"})
	if !errors.Is(err, redfish.ErrNotFound) {
		t.Fatalf("emulator: %v", err)
	}
}

func TestUpdateStatusAndHandles(t *testing.T) {
	_, srv := newMock(t, mockbmc.Options{})
	c := newClient(t, srv, redfish.Config{})
	ctx := context.Background()
	if _, err := c.UpdateStatus(ctx, ""); err == nil {
		t.Fatal("an empty handle was accepted")
	}
	for _, h := range []string{"/etc/passwd", "relative/path", "/redfish/../etc"} {
		if _, err := c.UpdateStatus(ctx, h); !errors.Is(err, redfish.ErrMalformed) {
			t.Errorf("handle %q: %v", h, err)
		}
	}
	if _, err := c.UpdateStatus(ctx, "/redfish/v1/TaskService/Tasks/99"); !errors.Is(err, redfish.ErrNotFound) {
		t.Fatalf("unknown task: %v", err)
	}
	// An absolute URL is followed as its path, on this service only.
	res, _ := c.SimpleUpdate(ctx, redfish.SimpleUpdateOptions{ImageURI: "https://h/x"})
	st, err := c.UpdateStatus(ctx, "https://elsewhere.example"+res.Status.Handle)
	if err != nil || st.Status != "Running" || st.Handle != res.Status.Handle {
		t.Fatalf("status = %+v, %v", st, err)
	}
}

func TestParseUpdateStatus(t *testing.T) {
	const loc = "/redfish/v1/TaskService/Tasks/7"
	task := func(state, msgs string) []byte {
		return []byte(`{"@odata.type":"#Task.v1_7_3.Task","TaskState":"` + state + `","Messages":[` + msgs + `]}`)
	}
	reset := `{"MessageId":"Base.1.19.ResetRequired","MessageArgs":["/redfish/v1/Systems/1","GracefulRestart"]}`
	for _, tc := range []struct {
		name   string
		code   int
		loc    string
		body   []byte
		handle string
		status string
		want   string // the handle
		resets int
	}{
		{"no content", 204, "", nil, "/h", "Completed", "", 0},
		{"accepted task", 202, loc, task("Running", ""), "", "Running", loc, 0},
		{"completed task clears the handle", 200, "", task("Completed", ""), "/h", "Completed", "", 0},
		{"job", 200, "", []byte(`{"@odata.type":"#Job.v1_0_0.Job","JobState":"Running"}`), "/j", "Running", "/j", 0},
		{"empty 202", 202, loc, nil, "", "New", loc, 0},
		{"202 with a message object is a task going on (departure)", 202, loc, []byte(`{"@Message.ExtendedInfo":[{"MessageId":"Base.1.19.Accepted"}]}`), "", "New", loc, 0},
		{"reset required", 200, "", task("Running", reset), "/h", "Running", "/h", 1},
		{"reset outside the service is ignored", 200, "", task("Running", `{"MessageId":"Base.1.19.ResetRequired","MessageArgs":["http://evil/x","ForceRestart"]}`), "/h", "Running", "/h", 0},
		{"reset without arguments", 200, "", task("Running", `{"MessageId":"Base.1.19.ResetRequired"}`), "/h", "Running", "/h", 0},
		{"transitioned to a job", 202, loc, task("Running", reset+`,{"MessageId":"Update.1.0.OperationTransitionedToJob","MessageArgs":["/redfish/v1/JobService/Jobs/3"]}`), "", "New", "/redfish/v1/JobService/Jobs/3", 0},
		{"error object", 200, "", []byte(`{"error":{"@Message.ExtendedInfo":[{"MessageId":"X.1.0.Y","Message":"m"}]}}`), "/h", "Completed", "", 0},
		{"status 500 without body", 500, "", nil, "/h", "Exception", "", 0},
		{"not JSON", 200, "", []byte("<html>"), "/h", "Completed", "", 0},
	} {
		st := redfish.ParseUpdateStatus(tc.code, tc.loc, tc.body, tc.handle)
		if st.Status != tc.status || st.Handle != tc.want || len(st.ResetsRequested) != tc.resets {
			t.Errorf("%s: %+v", tc.name, st)
		}
	}
}

func TestPerformRequestedOperations(t *testing.T) {
	b, srv := newMock(t, mockbmc.Options{ResetRequired: true})
	c := newClient(t, srv, redfish.Config{})
	ctx := context.Background()
	res, err := c.SimpleUpdate(ctx, redfish.SimpleUpdateOptions{ImageURI: "https://h/x"})
	if err != nil {
		t.Fatal(err)
	}
	st, err := c.UpdateStatus(ctx, res.Status.Handle)
	if err != nil || len(st.ResetsRequested) != 1 || st.ResetsRequested[0].Type != "GracefulRestart" || !strings.HasPrefix(st.ResetsRequested[0].URI, "/redfish/v1/Systems/") {
		t.Fatalf("status = %+v, %v", st, err)
	}
	out, err := c.PerformRequestedOperations(ctx, res.Status.Handle)
	if err != nil || !out.Changed || !strings.Contains(out.Message, "reset to continue") {
		t.Fatalf("PerformRequestedOperations = %+v, %v", out, err)
	}
	// The Reset action of the resource is used, not a POST to the resource.
	r := b.Resets()
	if len(r) != 1 || !strings.HasSuffix(r[0], "/Actions/ComputerSystem.Reset GracefulRestart") {
		t.Fatalf("resets = %v", r)
	}
	b2, srv2 := newMock(t, mockbmc.Options{})
	c2 := newClient(t, srv2, redfish.Config{})
	res2, _ := c2.SimpleUpdate(ctx, redfish.SimpleUpdateOptions{ImageURI: "https://h/x"})
	out, err = c2.PerformRequestedOperations(ctx, res2.Status.Handle)
	if err != nil || out.Changed || out.Message != "No operations required for the update" || len(b2.Resets()) != 0 {
		t.Fatalf("without a request: %+v, %v", out, err)
	}
	if _, err := c2.PerformRequestedOperations(ctx, ""); err == nil {
		t.Fatal("an empty handle was accepted")
	}
}

func writeImage(t *testing.T, name string, size int) (string, string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.New()
	chunk := make([]byte, 4096)
	for i := range chunk {
		chunk[i] = byte(i*7 + 3)
	}
	for left := size; left > 0; {
		n := min(left, len(chunk))
		if _, err := f.Write(chunk[:n]); err != nil {
			t.Fatal(err)
		}
		h.Write(chunk[:n])
		left -= n
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return p, hex.EncodeToString(h.Sum(nil))
}

func TestMultipartPush(t *testing.T) {
	b, srv := newMock(t, mockbmc.Options{})
	c := newClient(t, srv, redfish.Config{})
	ctx := context.Background()
	img, sum := writeImage(t, `fw "v2".bin`, 100_000)
	res, err := c.MultipartHTTPPushUpdate(ctx, redfish.PushOptions{ImageFile: img, Targets: []string{"/redfish/v1/Managers/BMC"}, ApplyTime: "OnReset",
		OEMParams: redfish.Props{"Contoso": map[string]any{"Force": true}}, CustomHeader: "OemPart", CustomMIMEType: "text/plain", CustomParams: "extra=1"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Changed || res.Message != "MultipartHTTPPushUpdate requested" || res.Status.Handle == "" || res.Monitor == "" {
		t.Fatalf("result = %+v", res)
	}
	ups := b.Uploads()
	if len(ups) != 1 {
		t.Fatalf("uploads = %+v", ups)
	}
	u := ups[0]
	if u.Size != 100_000 || u.SHA256 != sum {
		t.Fatalf("the service received %d bytes, %s; want 100000, %s", u.Size, u.SHA256, sum)
	}
	if u.Filename != `fw "v2".bin` || strings.Contains(u.Filename, string(filepath.Separator)) {
		t.Fatalf("filename = %q (the base name, quotes kept)", u.Filename)
	}
	if !slices.Equal(u.Parts, []string{"UpdateParameters", "OemPart", "UpdateFile"}) || u.Extra["OemPart"] != "extra=1" {
		t.Fatalf("parts = %v %v", u.Parts, u.Extra)
	}
	if u.ContentLength <= 100_000 {
		t.Fatalf("the upload was chunked or short: Content-Length %d", u.ContentLength)
	}
	if u.Params["@Redfish.OperationApplyTime"] != "OnReset" || fmt.Sprint(u.Params["Targets"]) != "[/redfish/v1/Managers/BMC]" || fmt.Sprint(u.Params["Oem"]) != "map[Contoso:map[Force:true]]" {
		t.Fatalf("UpdateParameters = %v", u.Params)
	}
	st, err := c.WaitUpdate(ctx, res.Status.Handle, 100*time.Millisecond, nil)
	if err != nil || st.Status != "Completed" {
		t.Fatalf("WaitUpdate = %+v, %v", st, err)
	}
	// With a session the token authenticates the upload.
	cs := newClient(t, srv, redfish.Config{Session: true})
	if _, err := cs.MultipartHTTPPushUpdate(ctx, redfish.PushOptions{ImageFile: img}); err != nil {
		t.Fatal(err)
	}
	if ups := b.Uploads(); len(ups) != 2 || ups[1].Auth != "token" || ups[0].Auth != "basic" {
		t.Fatalf("auth of the uploads = %+v", ups)
	}
}

// The image is streamed: pushing 32 MiB allocates a small fraction of it.
func TestMultipartPushStreams(t *testing.T) {
	const size = 32 << 20
	b, srv := newMock(t, mockbmc.Options{MaxUpload: 64 << 20})
	c := newClient(t, srv, redfish.Config{})
	img, sum := writeImage(t, "big.bin", size)
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	if _, err := c.MultipartHTTPPushUpdate(context.Background(), redfish.PushOptions{ImageFile: img, Timeout: time.Minute}); err != nil {
		t.Fatal(err)
	}
	runtime.ReadMemStats(&after)
	if ups := b.Uploads(); len(ups) != 1 || ups[0].SHA256 != sum || ups[0].Size != size {
		t.Fatalf("uploads = %+v", ups)
	}
	if d := after.TotalAlloc - before.TotalAlloc; d > size/2 {
		t.Fatalf("pushing %d bytes allocated %d: the image is not streamed", size, d)
	}
}

func TestMultipartPushRefusals(t *testing.T) {
	b, srv := newMock(t, mockbmc.Options{MaxUpload: 4096})
	c := newClient(t, srv, redfish.Config{})
	ctx := context.Background()
	big, _ := writeImage(t, "big.bin", 5000)
	small, _ := writeImage(t, "small.bin", 100)

	// The client's cap: nothing is sent, not even a read of the service.
	_, err := c.MultipartHTTPPushUpdate(ctx, redfish.PushOptions{ImageFile: big, MaxSize: 1000})
	if !errors.Is(err, redfish.ErrImageTooLarge) {
		t.Fatalf("over the client cap: %v", err)
	}
	if n := len(b.Log()); n != 0 {
		t.Fatalf("a refused image caused %d requests: %v", n, b.Log())
	}
	// The default cap applies to a file of any size.
	if _, err := c.MultipartHTTPPushUpdate(ctx, redfish.PushOptions{ImageFile: big, MaxSize: -1}); errors.Is(err, redfish.ErrImageTooLarge) {
		t.Fatalf("a negative cap means the default: %v", err)
	}

	for name, o := range map[string]redfish.PushOptions{
		"no file":             {},
		"missing":             {ImageFile: filepath.Join(t.TempDir(), "nope")},
		"directory":           {ImageFile: t.TempDir()},
		"device":              {ImageFile: os.DevNull},
		"part name injection": {ImageFile: small, CustomHeader: "x\"; filename=\"y\r\nX: z", CustomParams: "p"},
		"reserved part name":  {ImageFile: small, CustomHeader: "UpdateFile", CustomParams: "p"},
		"params without name": {ImageFile: small, CustomParams: "p", CustomHeader: ""},
		"bad media type":      {ImageFile: small, CustomHeader: "Oem", CustomMIMEType: "text/plain\r\nX: y", CustomParams: "p"},
		"apply time":          {ImageFile: small, ApplyTime: "AtMaintenanceWindowStart"},
	} {
		before := len(b.Uploads())
		if _, err := c.MultipartHTTPPushUpdate(ctx, o); err == nil {
			t.Errorf("%s: accepted", name)
		}
		if len(b.Uploads()) != before {
			t.Errorf("%s: an upload reached the service", name)
		}
	}

	// The service's own bound: 413, nothing kept.
	_, err = c.MultipartHTTPPushUpdate(ctx, redfish.PushOptions{ImageFile: big})
	if err == nil {
		t.Fatal("an upload over the service's bound was accepted")
	}
	var se *redfish.StatusError
	if errors.As(err, &se) && se.Code != 413 {
		t.Fatalf("status = %d", se.Code)
	}
	if len(b.Uploads()) != 0 {
		t.Fatalf("uploads = %+v", b.Uploads())
	}
	// The emulator tree has no UpdateService.
	_, srv2 := newMock(t, mockbmc.Options{Tree: mockbmc.TreeEmulator})
	if _, err := newClient(t, srv2, redfish.Config{}).MultipartHTTPPushUpdate(ctx, redfish.PushOptions{ImageFile: small}); !errors.Is(err, redfish.ErrNotFound) {
		t.Fatalf("emulator: %v", err)
	}
}

func TestMultipartFrame(t *testing.T) {
	prefix, suffix, ct, err := redfish.MultipartFrame([]byte(`{"Targets":[]}`), "dir/a\"b\r\nc.bin", "Custom", "", "hello")
	if err != nil {
		t.Fatal(err)
	}
	mt, params, err := mime.ParseMediaType(ct)
	if err != nil || mt != "multipart/form-data" {
		t.Fatalf("content type %q: %v", ct, err)
	}
	body := append(append(append([]byte{}, prefix...), []byte("FILEBYTES")...), suffix...)
	mr := multipart.NewReader(bytes.NewReader(body), params["boundary"])
	var seen []string
	for {
		p, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(p)
		seen = append(seen, p.FormName()+"="+string(data)+"|"+p.FileName()+"|"+p.Header.Get("Content-Type"))
	}
	want := []string{`UpdateParameters={"Targets":[]}||application/json`, "Custom=hello||text/plain", "UpdateFile=FILEBYTES|a\"b__c.bin|application/octet-stream"}
	if !slices.Equal(seen, want) {
		t.Fatalf("parts = %q\nwant    %q", seen, want)
	}
}
