package redfish_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-redfish/redfish"
	"github.com/go-redfish/redfish/mockbmc"
)

const accountsPath = "/redfish/v1/AccountService/Accounts"

// accountShapes are the services the Accounts commands meet: the two mockups,
// and the services that have a fixed number of accounts, with the Allow header
// and without it (the 405 alone tells).
var accountShapes = []struct {
	name string
	opts mockbmc.Options
	post bool // a POST creates accounts
}{
	{"rackmount", mockbmc.Options{Tree: mockbmc.TreeRackmount}, true},
	{"rackmount-slots", mockbmc.Options{Tree: mockbmc.TreeRackmount, FixedSlots: 3}, false},
	{"rackmount-slots-noallow", mockbmc.Options{Tree: mockbmc.TreeRackmount, FixedSlots: 3, OmitAllow: true}, false},
	{"emulator-slots", mockbmc.Options{Tree: mockbmc.TreeEmulator, FixedSlots: 3}, false},
	{"emulator-slots-noallow", mockbmc.Options{Tree: mockbmc.TreeEmulator, FixedSlots: 3, OmitAllow: true}, false},
}

func as(t *testing.T, srv *httptest.Server, user, pw string) *redfish.Client {
	t.Helper()
	return newClient(t, srv, redfish.Config{Credentials: redfish.Static{User: user, Password: pw}})
}

func loginOK(t *testing.T, srv *httptest.Server, user, pw string) bool {
	t.Helper()
	_, err := as(t, srv, user, pw).Get(context.Background(), "/redfish/v1/")
	if err != nil {
		return false
	}
	_, err = as(t, srv, user, pw).Get(context.Background(), accountsPath)
	return err == nil
}

func names(users []redfish.Props) []string {
	var out []string
	for _, u := range users {
		out = append(out, fmt.Sprint(u["UserName"]))
	}
	return out
}

func TestAccountLifecycle(t *testing.T) {
	for _, sh := range accountShapes {
		t.Run(sh.name, func(t *testing.T) {
			b, srv := newMock(t, sh.opts)
			c := newClient(t, srv, redfish.Config{})
			ctx := context.Background()
			before, err := c.ListUsers(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if roles, err := c.Roles(ctx); err != nil || len(roles) < 2 || roles[0]["Id"] == nil {
				t.Fatalf("Roles = %v, %v", roles, err)
			}
			for _, u := range before {
				if u["UserName"] == "" {
					t.Fatalf("an empty slot is listed: %v", u)
				}
				if _, ok := u["Password"]; ok {
					t.Fatalf("ListUsers keeps a property that is not on its list: %v", u)
				}
			}

			spec := redfish.AccountSpec{Username: "bob", Password: "bob-password-1", RoleID: "Operator", AccountTypes: []string{"Redfish"}}
			if sh.opts.Tree == mockbmc.TreeEmulator {
				spec.RoleID = "Operator"
			}
			o, err := c.AddUser(ctx, spec)
			if err != nil || !o.Changed {
				t.Fatalf("AddUser: %+v %v", o, err)
			}
			if !loginOK(t, srv, "bob", "bob-password-1") {
				t.Fatal("the new account cannot log in")
			}
			posted := false
			for _, l := range b.Log() {
				posted = posted || strings.HasPrefix(l, "POST "+accountsPath+" 201")
			}
			if posted != sh.post {
				t.Errorf("POST created the account = %v, want %v (%v)", posted, sh.post, b.Log())
			}
			if o, err = c.AddUser(ctx, spec); err != nil || o.Changed {
				t.Fatalf("AddUser again: %+v %v", o, err)
			}
			users, _ := c.ListUsers(ctx)
			if len(users) != len(before)+1 {
				t.Fatalf("users = %v, before %v", names(users), names(before))
			}

			// Role: a change, no change, a role the service does not have.
			if o, err = c.UpdateUserRole(ctx, redfish.AccountSpec{Username: "bob", RoleID: "ReadOnly"}); sh.opts.Tree == mockbmc.TreeEmulator {
				if err == nil || !strings.Contains(err.Error(), "400") {
					t.Fatalf("a role the tree does not have: %+v %v", o, err)
				}
				o, err = c.UpdateUserRole(ctx, redfish.AccountSpec{Username: "bob", RoleID: "ReadOnlyUser"})
			}
			if err != nil || !o.Changed {
				t.Fatalf("UpdateUserRole: %+v %v", o, err)
			}
			role := "ReadOnly"
			if sh.opts.Tree == mockbmc.TreeEmulator {
				role = "ReadOnlyUser"
			}
			if o, err = c.UpdateUserRole(ctx, redfish.AccountSpec{Username: "bob", RoleID: role}); err != nil || o.Changed {
				t.Fatalf("UpdateUserRole again: %+v %v", o, err)
			}
			if _, err = c.UpdateUserRole(ctx, redfish.AccountSpec{Username: "bob", RoleID: "NoSuchRole"}); err == nil {
				t.Fatal("a role that does not exist was accepted")
			}
			if _, err = c.UpdateUserRole(ctx, redfish.AccountSpec{Username: "bob"}); err == nil {
				t.Fatal("no role given")
			}

			// Disable and enable, by Id.
			id := fmt.Sprint(func() any {
				for _, u := range mustList(t, c) {
					if u["UserName"] == "bob" {
						return u["Id"]
					}
				}
				return ""
			}())
			if o, err = c.DisableUser(ctx, redfish.AccountSpec{ID: id}); err != nil || !o.Changed {
				t.Fatalf("DisableUser: %+v %v", o, err)
			}
			if loginOK(t, srv, "bob", "bob-password-1") {
				t.Fatal("a disabled account logs in")
			}
			if o, err = c.DisableUser(ctx, redfish.AccountSpec{Username: "bob"}); err != nil || o.Changed {
				t.Fatalf("DisableUser again: %+v %v", o, err)
			}
			if o, err = c.EnableUser(ctx, redfish.AccountSpec{Username: "bob"}); err != nil || !o.Changed {
				t.Fatalf("EnableUser: %+v %v", o, err)
			}
			if !loginOK(t, srv, "bob", "bob-password-1") {
				t.Fatal("an enabled account cannot log in")
			}

			// Password: the new one works, the old one does not; an unchanged value is
			// still sent (a password is write-only, so it cannot be compared).
			if o, err = c.UpdateUserPassword(ctx, redfish.AccountSpec{Username: "bob", Password: "bob-password-2"}); err != nil || !o.Changed {
				t.Fatalf("UpdateUserPassword: %+v %v", o, err)
			}
			if loginOK(t, srv, "bob", "bob-password-1") || !loginOK(t, srv, "bob", "bob-password-2") {
				t.Fatal("the password did not change")
			}
			if o, err = c.UpdateUserPassword(ctx, redfish.AccountSpec{Username: "bob", Password: "bob-password-2"}); err != nil || !o.Changed {
				t.Fatalf("UpdateUserPassword, same value: %+v %v", o, err)
			}
			if _, err = c.UpdateUserPassword(ctx, redfish.AccountSpec{Username: "bob"}); err == nil {
				t.Fatal("no password given")
			}
			if _, err = c.UpdateUserPassword(ctx, redfish.AccountSpec{Username: "bob", Password: "short"}); err == nil || strings.Contains(err.Error(), "short") {
				t.Fatalf("a short password: %v", err)
			}

			// Account types.
			if o, err = c.UpdateUserAccountTypes(ctx, redfish.AccountSpec{Username: "bob", AccountTypes: []string{"Redfish", "WebUI"}}); err != nil || !o.Changed {
				t.Fatalf("UpdateUserAccountTypes: %+v %v", o, err)
			}
			if o, err = c.UpdateUserAccountTypes(ctx, redfish.AccountSpec{Username: "bob", AccountTypes: []string{"Redfish", "WebUI"}}); err != nil || o.Changed {
				t.Fatalf("UpdateUserAccountTypes again: %+v %v", o, err)
			}
			if _, err = c.UpdateUserAccountTypes(ctx, redfish.AccountSpec{Username: "bob"}); err == nil {
				t.Fatal("no account types given")
			}
			if _, err = c.UpdateUserAccountTypes(ctx, redfish.AccountSpec{Username: "bob", OEMAccountTypes: []string{"x"}}); !errors.Is(err, redfish.ErrUnsupported) {
				t.Fatalf("OEMAccountTypes the account does not have: %v", err)
			}

			// Rename.
			if o, err = c.UpdateUserName(ctx, redfish.AccountSpec{Username: "bob", NewUsername: "robert"}); err != nil || !o.Changed {
				t.Fatalf("UpdateUserName: %+v %v", o, err)
			}
			if !loginOK(t, srv, "robert", "bob-password-2") || loginOK(t, srv, "bob", "bob-password-2") {
				t.Fatal("the account was not renamed")
			}
			if _, err = c.UpdateUserName(ctx, redfish.AccountSpec{Username: "robert"}); err == nil {
				t.Fatal("no new name")
			}
			if _, err = c.UpdateUserName(ctx, redfish.AccountSpec{Username: "robert", NewUsername: "admin"}); err == nil {
				t.Fatal("a name that is another account's")
			}
			if _, err = c.EnableUser(ctx, redfish.AccountSpec{Username: "nobody"}); !errors.Is(err, redfish.ErrNoAccount) {
				t.Fatalf("an account that does not exist: %v", err)
			}
			if _, err = c.EnableUser(ctx, redfish.AccountSpec{}); err == nil {
				t.Fatal("neither an id nor a name")
			}

			// Delete: by name, then again (nothing to do).
			if o, err = c.DeleteUser(ctx, redfish.AccountSpec{Username: "robert"}); err != nil || !o.Changed {
				t.Fatalf("DeleteUser: %+v %v", o, err)
			}
			if loginOK(t, srv, "robert", "bob-password-2") {
				t.Fatal("a deleted account logs in")
			}
			if o, err = c.DeleteUser(ctx, redfish.AccountSpec{Username: "robert"}); err != nil || o.Changed {
				t.Fatalf("DeleteUser again: %+v %v", o, err)
			}
			users, _ = c.ListUsers(ctx)
			if len(users) != len(before) {
				t.Fatalf("users after the delete = %v, before %v", names(users), names(before))
			}
			deleted := false
			for _, l := range b.Log() {
				deleted = deleted || strings.HasPrefix(l, "DELETE "+accountsPath+"/") && strings.HasSuffix(l, " 204")
			}
			if deleted != sh.post {
				t.Errorf("DELETE removed the account = %v, want %v", deleted, sh.post)
			}
		})
	}
}

func mustList(t *testing.T, c *redfish.Client) []redfish.Props {
	t.Helper()
	u, err := c.ListUsers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return u
}

// A service with fixed slots takes AddUser with an Id by changing that slot; one
// with no free slot and no POST is refused.
func TestAddUserWithoutAPlaceToPut(t *testing.T) {
	_, srv := newMock(t, mockbmc.Options{Tree: mockbmc.TreeEmulator})
	c := newClient(t, srv, redfish.Config{})
	ctx := context.Background()
	if _, err := c.AddUser(ctx, redfish.AccountSpec{Username: "bob", Password: "bob-password-1", RoleID: "Operator"}); !errors.Is(err, redfish.ErrNoAccount) {
		t.Fatalf("no POST and no empty slot: %v", err)
	}
	if _, err := c.AddUser(ctx, redfish.AccountSpec{Password: "bob-password-1"}); err == nil {
		t.Fatal("no user name")
	}
	if _, err := c.AddUser(ctx, redfish.AccountSpec{Username: "bob", Password: redfish.Secret(strings.Repeat("x", 2000))}); err == nil {
		t.Fatal("a password of 2000 bytes")
	}

	_, srv = newMock(t, mockbmc.Options{Tree: mockbmc.TreeRackmount, FixedSlots: 4})
	c = newClient(t, srv, redfish.Config{})
	if o, err := c.AddUser(ctx, redfish.AccountSpec{ID: "3", Username: "dave", Password: "dave-password-1", RoleID: "ReadOnly"}); err != nil || !o.Changed {
		t.Fatalf("AddUser with an Id: %+v %v", o, err)
	}
	if !loginOK(t, srv, "dave", "dave-password-1") {
		t.Fatal("dave cannot log in")
	}
	if _, err := c.AddUser(ctx, redfish.AccountSpec{ID: "99", Username: "erin", Password: "erin-password-1"}); !errors.Is(err, redfish.ErrNoAccount) {
		t.Fatalf("an Id that does not exist: %v", err)
	}
}

func TestPasswordChangeRequired(t *testing.T) {
	for _, tree := range trees {
		t.Run(tree, func(t *testing.T) {
			b, srv := newMock(t, mockbmc.Options{Tree: tree})
			uri, err := b.AddAccount("carol", "carol-password-1", "Operator", true)
			if err != nil {
				t.Fatal(err)
			}
			carol := as(t, srv, "carol", "carol-password-1")
			ctx := context.Background()
			_, err = carol.ListUsers(ctx)
			if !errors.Is(err, redfish.ErrAuth) || redfish.PasswordChangeURI(err) != uri {
				t.Fatalf("the refusal: %v, PasswordChangeURI = %q, want %q", err, redfish.PasswordChangeURI(err), uri)
			}
			if _, err := carol.EnableUser(ctx, redfish.AccountSpec{Username: "carol"}); !errors.Is(err, redfish.ErrAuth) {
				t.Fatalf("another command: %v", err)
			}
			// The password change goes through the account the message names.
			if o, err := carol.UpdateUserPassword(ctx, redfish.AccountSpec{Username: "carol", Password: "carol-password-2"}); err != nil || !o.Changed {
				t.Fatalf("UpdateUserPassword: %+v %v", o, err)
			}
			if !loginOK(t, srv, "carol", "carol-password-2") {
				t.Fatal("carol is still refused after the change")
			}
			// Another account's name is not found there.
			b.AddAccount("dan", "dan-password-12", "Operator", true)
			dan := as(t, srv, "dan", "dan-password-12")
			if _, err := dan.UpdateUserPassword(ctx, redfish.AccountSpec{Username: "carol", Password: "carol-password-3"}); !errors.Is(err, redfish.ErrNoAccount) {
				t.Fatalf("a password change for another account: %v", err)
			}
			if redfish.PasswordChangeURI(errors.New("x")) != "" || redfish.PasswordChangeURI(nil) != "" {
				t.Fatal("PasswordChangeURI of an error that is not one")
			}
		})
	}
}

func TestAccountServiceConfig(t *testing.T) {
	for _, tree := range trees {
		t.Run(tree, func(t *testing.T) {
			_, srv := newMock(t, mockbmc.Options{Tree: tree})
			c := newClient(t, srv, redfish.Config{})
			ctx := context.Background()
			cfg, err := c.AccountServiceConfig(ctx)
			if err != nil || cfg["MinPasswordLength"] == nil || cfg["Id"] != "AccountService" {
				t.Fatalf("AccountServiceConfig: %v %v", cfg, err)
			}
			o, err := c.UpdateAccountServiceProperties(ctx, redfish.Props{"MinPasswordLength": 12})
			if err != nil || !o.Changed || o.Message != "Modified account service" {
				t.Fatalf("update: %+v %v", o, err)
			}
			if o, err = c.UpdateAccountServiceProperties(ctx, redfish.Props{"MinPasswordLength": 12}); err != nil || o.Changed {
				t.Fatalf("update again: %+v %v", o, err)
			}
			if _, err = c.UpdateAccountServiceProperties(ctx, redfish.Props{"NoSuchProperty": 1}); !errors.Is(err, redfish.ErrUnsupported) {
				t.Fatalf("an unsupported property: %v", err)
			}
			if _, err = c.UpdateAccountServiceProperties(ctx, nil); err == nil {
				t.Fatal("nil properties")
			}
			if o, err = c.UpdateAccountServiceProperties(ctx, redfish.Props{}); err != nil || o.Changed {
				t.Fatalf("empty properties: %+v %v", o, err)
			}
			// The mock enforces the length it was given: 11 characters are too few now.
			_, err = c.AddUser(ctx, redfish.AccountSpec{Username: "erin", Password: "elevenchars", RoleID: map[string]string{mockbmc.TreeRackmount: "Operator", mockbmc.TreeEmulator: "Operator"}[tree]})
			if tree == mockbmc.TreeRackmount && (err == nil || strings.Contains(err.Error(), "elevenchars")) {
				t.Fatalf("a password below the minimum: %v", err)
			}
		})
	}
}

func TestSessions(t *testing.T) {
	t.Run(mockbmc.TreeRackmount, func(t *testing.T) {
		_, srv := newMock(t, mockbmc.Options{})
		ctx := context.Background()
		c := newClient(t, srv, redfish.Config{Session: true})
		list, err := c.GetSessions(ctx)
		if err != nil || len(list) != 3 {
			t.Fatalf("GetSessions = %v, %v (two in the mockup and ours)", list, err)
		}
		for _, s := range list {
			if s["Id"] == nil || s["UserName"] == nil || s["@odata.id"] != nil {
				t.Fatalf("session = %v", s)
			}
		}
		o, err := c.ClearSessions(ctx)
		if err != nil || !o.Changed || o.Message != "Cleared all sessions successfully" {
			t.Fatalf("ClearSessions: %+v %v", o, err)
		}
		if tok, _ := c.Session(); tok != "" {
			t.Fatal("the client keeps the token of a session it ended")
		}
		// The client logs in again by itself, and is the only session.
		if list, err = c.GetSessions(ctx); err != nil || len(list) != 1 {
			t.Fatalf("after the clear: %v %v", list, err)
		}
		basic := newClient(t, srv, redfish.Config{})
		if o, err = basic.ClearSessions(ctx); err != nil || !o.Changed {
			t.Fatalf("basic ClearSessions: %+v %v", o, err)
		}
		if o, err = basic.ClearSessions(ctx); err != nil || o.Changed || o.Message != "There are no active sessions" {
			t.Fatalf("ClearSessions with none: %+v %v", o, err)
		}

		o, err = basic.SetSessionService(ctx, redfish.Props{"SessionTimeout": 60})
		if err != nil || !o.Changed || o.Message != "Modified session service" {
			t.Fatalf("SetSessionService: %+v %v", o, err)
		}
		if o, err = basic.SetSessionService(ctx, redfish.Props{"SessionTimeout": 60}); err != nil || o.Changed {
			t.Fatalf("again: %+v %v", o, err)
		}
		if _, err = basic.SetSessionService(ctx, redfish.Props{"NoSuch": 1}); !errors.Is(err, redfish.ErrUnsupported) {
			t.Fatalf("an unsupported property: %v", err)
		}
		if _, err = basic.SetSessionService(ctx, nil); err == nil {
			t.Fatal("nil")
		}
	})
	t.Run(mockbmc.TreeEmulator, func(t *testing.T) {
		_, srv := newMock(t, mockbmc.Options{Tree: mockbmc.TreeEmulator})
		ctx := context.Background()
		c := newClient(t, srv, redfish.Config{})
		if list, err := c.GetSessions(ctx); err != nil || len(list) != 0 {
			t.Fatalf("GetSessions = %v, %v", list, err)
		}
		if o, err := c.ClearSessions(ctx); err != nil || o.Changed {
			t.Fatalf("ClearSessions: %+v %v", o, err)
		}
		// The emulator's SessionService has no SessionTimeout: the oracle's check
		// refuses it.
		if _, err := c.SetSessionService(ctx, redfish.Props{"SessionTimeout": 60}); !errors.Is(err, redfish.ErrUnsupported) {
			t.Fatalf("SetSessionService: %v", err)
		}
	})
}

// A password never prints: with any fmt verb, as JSON, in a log, in the spec.
func TestSecretNeverPrints(t *testing.T) {
	pw := redfish.Secret("hunter2-hunter2")
	spec := redfish.AccountSpec{Username: "u", Password: pw}
	var out []string
	for _, v := range []any{pw, &pw, spec, &spec, []redfish.Secret{pw}, map[string]any{"p": pw}, struct{ P redfish.Secret }{pw}} {
		out = append(out, fmt.Sprintf("%v|%+v|%#v|%s|%q|%x|%d", v, v, v, v, v, v, v))
	}
	b, err := json.Marshal(map[string]any{"s": pw, "spec": spec})
	out = append(out, string(b))
	if err != nil {
		t.Fatal(err)
	}
	var sb strings.Builder
	slog.New(slog.NewTextHandler(&sb, nil)).Info("x", "pw", pw, "spec", spec)
	slog.New(slog.NewJSONHandler(&sb, nil)).Info("x", "pw", pw, "spec", spec)
	out = append(out, sb.String())
	for _, s := range out {
		if strings.Contains(s, "hunter2") {
			t.Errorf("the password shows: %s", s)
		}
	}
	if pw.Reveal() != "hunter2-hunter2" {
		t.Error("Reveal")
	}
}

// echoBMC is a service that repeats what it was sent in its errors, the worst
// case for a client that holds a password.
func echoBMC(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	write := func(w http.ResponseWriter, code int, doc string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_, _ = w.Write([]byte(doc))
	}
	mux.HandleFunc("/redfish/v1/", func(w http.ResponseWriter, r *http.Request) {
		write(w, 200, `{"AccountService":{"@odata.id":"/redfish/v1/AccountService"}}`)
	})
	mux.HandleFunc("/redfish/v1/AccountService", func(w http.ResponseWriter, r *http.Request) {
		write(w, 200, `{"Accounts":{"@odata.id":"/redfish/v1/AccountService/Accounts"}}`)
	})
	mux.HandleFunc("/redfish/v1/AccountService/Accounts", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			echo(w, body)
			return
		}
		w.Header().Set("Allow", "GET, POST")
		write(w, 200, `{"Members":[{"@odata.id":"/redfish/v1/AccountService/Accounts/1"}]}`)
	})
	mux.HandleFunc("/redfish/v1/AccountService/Accounts/1", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPatch {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			echo(w, body)
			return
		}
		write(w, 200, `{"Id":"1","UserName":"bob","Password":null,"Enabled":true}`)
	})
	srv := httptest.NewTLSServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// echo answers 400 with the whole request in every place a service puts text.
func echo(w http.ResponseWriter, body map[string]any) {
	raw, _ := json.Marshal(body)
	pw, _ := body["Password"].(string)
	esc, _ := json.Marshal(pw)
	doc := map[string]any{"error": map[string]any{
		"code": "Base.1.19.PropertyValueIncorrect " + pw, "message": "bad request " + string(raw),
		"@Message.ExtendedInfo": []any{
			map[string]any{"MessageId": "Base.1.19.PropertyValueIncorrect", "Message": "the value " + pw + " is wrong", "MessageArgs": []any{pw, string(esc)}, "RelatedProperties": []any{"#/Password=" + pw}},
			map[string]any{"MessageId": "Base.1.19.Other", "Message": string(esc), "Resolution": pw},
		}}}
	b, _ := json.Marshal(doc)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	_, _ = w.Write(b)
}

// A service that repeats the password in its error does not get it back out of
// the library: not in the text, not in any field of the StatusError.
func TestAnErrorNeverCarriesThePassword(t *testing.T) {
	srv := echoBMC(t)
	ctx := context.Background()
	for _, pw := range []string{"s3cret-Passw0rd", `qu"ote\back/s<la>sh-1`, "ünï-cödé-pässwörd"} {
		c := newClient(t, srv, redfish.Config{})
		for name, run := range map[string]func() error{
			"AddUser": func() error {
				_, err := c.AddUser(ctx, redfish.AccountSpec{Username: "newbie", Password: redfish.Secret(pw), RoleID: "Operator"})
				return err
			},
			"UpdateUserPassword": func() error {
				_, err := c.UpdateUserPassword(ctx, redfish.AccountSpec{Username: "bob", Password: redfish.Secret(pw)})
				return err
			},
		} {
			err := run()
			var se *redfish.StatusError
			if err == nil || !errors.As(err, &se) || se.Code != 400 {
				t.Fatalf("%s: want the 400, got %v", name, err)
			}
			doc := fmt.Sprintf("%v|%+v|%#v|%s", err, err, se, se.Error())
			doc += se.ErrorCode + se.Message + se.Path + se.Method
			for _, m := range se.Info {
				doc += fmt.Sprintf("%+v", m)
			}
			esc, _ := json.Marshal(pw)
			for _, needle := range []string{pw, string(esc[1 : len(esc)-1])} {
				if strings.Contains(doc, needle) {
					t.Errorf("%s: the password %q is in the error: %s", name, pw, doc)
				}
			}
		}
	}
	// An answer that does not repeat it is left as it is, with its text.
	_, srvOK := newMock(t, mockbmc.Options{})
	c := newClient(t, srvOK, redfish.Config{})
	_, err := c.AddUser(ctx, redfish.AccountSpec{Username: "frank", Password: "short12", RoleID: "Operator"})
	if err == nil || !strings.Contains(err.Error(), "minimum length") {
		t.Fatalf("an honest error lost its text: %v", err)
	}
	// And the outcome never has it.
	o, err := c.AddUser(ctx, redfish.AccountSpec{Username: "gina", Password: "gina-password-1", RoleID: "Operator"})
	if err != nil || strings.Contains(fmt.Sprint(o), "gina-password-1") {
		t.Fatalf("outcome: %+v %v", o, err)
	}
}
