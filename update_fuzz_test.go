package redfish_test

import (
	"bytes"
	"io"
	"mime"
	"mime/multipart"
	"net/url"
	"strings"
	"testing"

	"github.com/go-redfish/redfish"
)

// Every decoder of the Update area takes what a BMC sends, and every validator
// what a caller or a BMC names: none may panic, and each keeps its bounds.

func FuzzOperationResults(f *testing.F) {
	f.Add(202, "/redfish/v1/TaskService/Tasks/1", []byte(`{"@odata.type":"#Task.v1_7_3.Task","TaskState":"Running","Messages":[{"MessageId":"Base.1.19.ResetRequired","MessageArgs":["/redfish/v1/Systems/1","GracefulRestart"]}]}`), "/h")
	f.Add(200, "", []byte(`{"error":{"@Message.ExtendedInfo":[{"MessageId":"Update.1.0.OperationTransitionedToJob","MessageArgs":["/redfish/v1/Jobs/1"]}]}}`), "")
	f.Add(204, "http://evil/redfish/x", []byte(nil), "x")
	f.Add(202, "/redfish/../etc", []byte(`{"Messages":[{"MessageArgs":[1,2,{"a":1}]}]}`), "")
	f.Fuzz(func(t *testing.T, code int, location string, body []byte, handle string) {
		st := redfish.ParseUpdateStatus(code, location, body, handle)
		if st.Messages == nil || st.ResetsRequested == nil {
			t.Fatal("nil lists: they must encode as []")
		}
		if len(st.Messages) > 64 || len(st.ResetsRequested) > 64 {
			t.Fatal("unbounded")
		}
		switch st.Status {
		case "Completed", "Cancelled", "Exception", "Killed":
			if st.Handle != "" {
				t.Fatalf("a finished operation keeps the handle %q", st.Handle)
			}
		}
		for _, r := range st.ResetsRequested {
			if !strings.HasPrefix(r.URI, "/redfish/") || strings.Contains(r.URI, "..") {
				t.Fatalf("a reset outside the service: %q", r.URI)
			}
		}
		if st.Handle != "" && st.Handle != handle && !strings.HasPrefix(st.Handle, "/redfish/") {
			t.Fatalf("a handle outside the service: %q", st.Handle)
		}
	})
}

func FuzzUpdateReads(f *testing.F) {
	f.Add([]byte(`{"MultipartHttpPushUri":"/x","Actions":{"#UpdateService.SimpleUpdate":{"title":"t","TransferProtocol@Redfish.AllowableValues":["HTTP"]}}}`))
	f.Add([]byte(`{"Actions":{"a":null,"b":[],"c":{"TransferProtocol@Redfish.AllowableValues":5}}}`))
	f.Add([]byte(`{"Id":"BIOS","Version":"1","Updateable":true,"Status":{"State":"Enabled"}}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		c, err := redfish.ParseUpdateCapabilities(data)
		if err == nil && len(c.Entries) > 64 {
			t.Fatal("unbounded entries")
		}
		for _, v := range c.Entries {
			if len(v) > 256 {
				t.Fatal("unbounded values")
			}
		}
		if p, err := redfish.Pick(data, "Name", "Id", "Status", "Version", "Updateable", "SoftwareId", "LowestSupportedVersion", "Manufacturer", "ReleaseDate"); err == nil && len(p) > 9 {
			t.Fatal("Pick kept a property it was not asked for")
		}
	})
}

func FuzzCheckImageURI(f *testing.F) {
	allowed := []string{"HTTP", "HTTPS", "SCP", "CIFS"}
	f.Add("https://images.example/fw.bin", "HTTPS")
	f.Add("https://user:pw@images.example/fw.bin", "")
	f.Add("scp://deploy@h/x", "SCP")
	f.Add("smb://h/share", "CIFS")
	f.Add("h/x\r\n", "HTTP")
	f.Fuzz(func(t *testing.T, uri, protocol string) {
		err := redfish.CheckImageURI(uri, protocol, allowed)
		if err != nil {
			if pw := passwordOf(uri); len(pw) >= 4 && strings.Contains(err.Error(), pw) {
				t.Fatalf("the error repeats the password: %v", err)
			}
			return
		}
		if len(uri) > 2048 || strings.ContainsAny(uri, " \r\n\t\x00") {
			t.Fatalf("accepted %q", uri)
		}
		if pw := passwordOf(uri); pw != "" || strings.Contains(uri, ":@") {
			t.Fatalf("accepted a password in %q", uri)
		}
		if u, err := url.Parse(uri); err == nil && u.Scheme != "" {
			ok := false
			for _, a := range allowed {
				ok = ok || strings.EqualFold(a, u.Scheme) || (a == "CIFS" && strings.EqualFold(u.Scheme, "smb"))
			}
			if !ok {
				t.Fatalf("accepted the scheme of %q", uri)
			}
		}
	})
}

func passwordOf(uri string) string {
	if u, err := url.Parse(uri); err == nil && u.User != nil {
		pw, _ := u.User.Password()
		return pw
	}
	return ""
}

func FuzzMultipartFrame(f *testing.F) {
	f.Add([]byte(`{"Targets":[]}`), "fw.bin", "Oem", "text/plain", "x")
	f.Add([]byte(`{}`), "a\"b\r\n/c", "", "", "")
	f.Add([]byte(`{}`), "f", "Bad Name", "text/plain\r\nX: y", "x")
	f.Fuzz(func(t *testing.T, params []byte, filename, name, mt, content string) {
		prefix, suffix, ct, err := redfish.MultipartFrame(params, filename, name, mt, content)
		if err != nil {
			return
		}
		_, p, err := mime.ParseMediaType(ct)
		if err != nil {
			t.Fatalf("content type %q: %v", ct, err)
		}
		body := append(append(append([]byte{}, prefix...), []byte("FILE")...), suffix...)
		mr := multipart.NewReader(bytes.NewReader(body), p["boundary"])
		var names []string
		var file string
		for {
			part, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatalf("the frame is not a multipart body: %v", err)
			}
			data, _ := io.ReadAll(part)
			names = append(names, part.FormName())
			if part.FormName() == "UpdateFile" {
				file = string(data)
				if strings.ContainsAny(part.FileName(), "\r\n") {
					t.Fatalf("a line break in the filename %q", part.FileName())
				}
			}
		}
		if len(names) < 2 || len(names) > 3 || names[0] != "UpdateParameters" || names[len(names)-1] != "UpdateFile" || file != "FILE" {
			t.Fatalf("parts = %v file %q", names, file)
		}
	})
}
