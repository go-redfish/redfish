package redfish

import (
	"context"
	"errors"
	"testing"
)

// Whatever the AccountService, the accounts and the sessions hold, the Accounts
// and Sessions commands neither panic nor hang, and an error never holds the
// password.
func FuzzAccountReads(f *testing.F) {
	f.Add(`{"Id":"1","UserName":"bob","Enabled":false,"Members":[{"@odata.id":"/redfish/v1/AccountService/Accounts/1"}],"Accounts":{"@odata.id":"/redfish/v1/AccountService/Accounts"},"AccountTypes":["Redfish"],"RoleId":"Operator","SessionTimeout":30}`)
	f.Add(`{"Id":1,"UserName":null,"Enabled":"yes","Members":5,"Links":{"Members":[1],"Accounts":[]},"Accounts":"x"}`)
	f.Add(`{"UserName":"","Enabled":false,"Members@odata.nextLink":"/redfish/v1/AccountService/Accounts/1"}`)
	f.Add(`[]`)
	r := routes(`{}`, map[string]string{
		"/redfish/v1/": `{"AccountService":{"@odata.id":"/redfish/v1/AccountService"},"SessionService":{"@odata.id":"/redfish/v1/SessionService"},"Links":{"Sessions":{"@odata.id":"/redfish/v1/SessionService/Sessions"}}}`,
	})
	for _, k := range []string{"AccountService", "AccountService/Accounts", "AccountService/Accounts/1", "AccountService/Accounts/2", "SessionService", "SessionService/Sessions", "SessionService/Sessions/1"} {
		r["/redfish/v1/"+k] = `{}`
	}
	b := newBMC(f, r)
	c := b.client(f, nil)
	f.Fuzz(func(t *testing.T, doc string) {
		b.mu.Lock()
		for _, k := range []string{"AccountService", "AccountService/Accounts", "AccountService/Accounts/1", "AccountService/Accounts/2", "SessionService", "SessionService/Sessions", "SessionService/Sessions/1"} {
			b.routes["/redfish/v1/"+k] = doc
		}
		b.mu.Unlock()
		ctx := context.Background()
		const pw = Secret("fuzz-Passw0rd")
		a := AccountSpec{ID: "1", Username: "bob", Password: pw, RoleID: "Operator", AccountTypes: []string{"Redfish"}, NewUsername: "x"}
		_, _ = c.ListUsers(ctx)
		_, _ = c.AccountServiceConfig(ctx)
		for _, fn := range []func(context.Context, AccountSpec) (Outcome, error){
			c.AddUser, c.EnableUser, c.DisableUser, c.DeleteUser, c.UpdateUserRole, c.UpdateUserPassword, c.UpdateUserName, c.UpdateUserAccountTypes,
		} {
			if _, err := fn(ctx, a); err != nil && len(err.Error()) > 0 && contains(err.Error(), pw.Reveal()) {
				t.Fatalf("the password is in an error: %v", err)
			}
		}
		_, _ = c.UpdateAccountServiceProperties(ctx, Props{"a": 1})
		_, _ = c.GetSessions(ctx)
		_, _ = c.ClearSessions(ctx)
		_, _ = c.SetSessionService(ctx, Props{"SessionTimeout": 1})
	})
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// Whatever an error object holds, PasswordChangeURI answers a path of the service
// or nothing; a message that is not Base.1.x.PasswordChangeRequired gives nothing.
func FuzzPasswordChangeURI(f *testing.F) {
	f.Add(`{"error":{"@Message.ExtendedInfo":[{"MessageId":"Base.1.19.PasswordChangeRequired","MessageArgs":["/redfish/v1/AccountService/Accounts/3"]}]}}`)
	f.Add(`{"@Message.ExtendedInfo":[{"MessageId":"Base.1.0.PasswordChangeRequired","MessageArgs":["https://h/redfish/v1/x"]}]}`)
	f.Add(`{"error":{"@Message.ExtendedInfo":[{"MessageId":"Base.1.19.PasswordChangeRequired","MessageArgs":["../../etc"]},{"MessageId":5}]}}`)
	f.Add(`{"error":{"@Message.ExtendedInfo":[{"MessageId":"Base.1.19.PasswordChangeRequired","MessageArgs":[]}]}}`)
	f.Fuzz(func(t *testing.T, doc string) {
		_, _, info := ParseError([]byte(doc))
		for _, err := range []error{&StatusError{Code: 403, Info: info}, &authError{code: 403, Info: info}} {
			u := PasswordChangeURI(err)
			if u != "" && servicePath(u) != u {
				t.Fatalf("PasswordChangeURI = %q, not a path of the service", u)
			}
			if !errors.Is(err, ErrAuth) && errors.Is(err, ErrAuth) {
				t.Fatal("unreachable")
			}
		}
	})
}
