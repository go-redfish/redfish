package mockbmc

import (
	"errors"
	"fmt"
	"net/http"
	"path"
	"sort"
	"strconv"
	"strings"
)

// The AccountService rules the mock enforces (AccountService 1.15, ManagerAccount
// 1.12, DSP8010; DSP0266 1.23.0 for the protocol):
//
//   - POST to the Accounts collection creates an account (UserName and Password
//     required, 409 for a name that exists), with a RoleId that names a Role of the
//     service, a Password of at least AccountService.MinPasswordLength characters;
//   - PATCH of an account: UserName renames it (409 when the name is another
//     account's, "" empties the slot), Enabled false stops it from authenticating,
//     Password changes its password (write-only: reads back as it was in the
//     mockup), RoleId must name a Role;
//   - an account with PasswordChangeRequired true may only read and PATCH itself:
//     every other request is 403 with the Base.1.x.PasswordChangeRequired message
//     whose argument is the account (AddAccount);
//   - the Allow header of a resource says what the mock does with it (RFC 9110
//     10.2.1); Options.FixedSlots and Options.OmitAllow make the shapes some
//     BMCs have.

const accountsRoot = root + "/AccountService/Accounts"

// AddAccount adds an account resource directly (a test fixture): name, password
// and role, enabled, and with PasswordChangeRequired as given. It returns the
// path of the account.
func (b *BMC) AddAccount(name, password, role string, passwordChangeRequired bool) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if name == "" || password == "" {
		return "", errors.New("mockbmc: an account needs a name and a password")
	}
	if b.nameTaken(name, "") {
		return "", fmt.Errorf("mockbmc: the account %q exists", name)
	}
	if b.res[accountsRoot] == nil {
		return "", errors.New("mockbmc: the tree has no Accounts collection")
	}
	n := 1
	for b.res[accountsRoot+"/"+strconv.Itoa(n)] != nil {
		n++
	}
	p := accountsRoot + "/" + strconv.Itoa(n)
	b.accounts[name] = password
	b.addMember(accountsRoot, p, map[string]any{"@odata.type": "#ManagerAccount.v1_12_0.ManagerAccount", "Id": strconv.Itoa(n),
		"Name": "User Account", "UserName": name, "Enabled": true, "Locked": false, "RoleId": role, "Password": nil,
		"PasswordChangeRequired": passwordChangeRequired, "AccountTypes": []any{"Redfish"}})
	return p, nil
}

// augmentAccounts gives a service with FixedSlots its empty accounts.
func (b *BMC) augmentAccounts() {
	if b.opts.FixedSlots <= 0 || b.res[accountsRoot] == nil {
		return
	}
	for n := 1; n <= b.opts.FixedSlots; n++ {
		p := accountsRoot + "/" + strconv.Itoa(n)
		if b.res[p] != nil {
			continue
		}
		b.addMember(accountsRoot, p, map[string]any{"@odata.type": "#ManagerAccount.v1_12_0.ManagerAccount", "Id": strconv.Itoa(n),
			"Name": "User Account", "UserName": "", "Enabled": false, "Locked": false, "RoleId": b.defaultRole(), "Password": nil,
			"AccountTypes": []any{"Redfish"}})
	}
}

// accountResources are the paths of the account resources, sorted.
func (b *BMC) accountResources() []string {
	var out []string
	for p := range b.res {
		if strings.HasPrefix(p, accountsRoot+"/") && strings.Count(p, "/") == strings.Count(accountsRoot, "/")+1 {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}

// accountOf is the account resource of a user name: its path, whether it may
// authenticate (an account without a resource, the administrator, may), and
// whether it was found.
func (b *BMC) accountOf(user string) (p string, enabled, found bool) {
	for _, q := range b.accountResources() {
		if u, _ := b.res[q]["UserName"].(string); u == user && u != "" {
			en, has := b.res[q]["Enabled"].(bool)
			return q, !has || en, true
		}
	}
	return "", true, false
}

// nameTaken: another account (not the resource except) has the name.
func (b *BMC) nameTaken(name, except string) bool {
	for _, q := range b.accountResources() {
		if u, _ := b.res[q]["UserName"].(string); u == name && q != except {
			return true
		}
	}
	if _, ok := b.accounts[name]; ok {
		_, _, found := b.accountOf(name)
		return !found || except == "" // the administrator has no resource
	}
	return false
}

func (b *BMC) defaultRole() string {
	for _, r := range []string{"ReadOnly", "ReadOnlyUser"} {
		if b.res[root+"/AccountService/Roles/"+r] != nil {
			return r
		}
	}
	return "ReadOnly"
}

func (b *BMC) minPassword() int {
	switch v := b.res[root+"/AccountService"]["MinPasswordLength"].(type) {
	case float64:
		return int(v)
	case int:
		return v
	}
	return 0
}

// checkAccountProps refuses a RoleId that names no Role and a password shorter
// than the service's MinPasswordLength; it never repeats the password.
func (b *BMC) checkAccountProps(w http.ResponseWriter, in map[string]any, password string) bool {
	if rid, ok := in["RoleId"]; ok {
		s, _ := rid.(string)
		if s == "" || b.res[root+"/AccountService/Roles/"+s] == nil {
			b.fail(w, http.StatusBadRequest, "", "The value for the property RoleId is not in the list of acceptable values.", "Base.1.19.PropertyValueNotInList")
			return false
		}
	}
	if password != "" && len(password) < b.minPassword() {
		b.fail(w, http.StatusBadRequest, "", "The value for the property Password is below the minimum length of the service.", "Base.1.19.PropertyValueIncorrect")
		return false
	}
	return true
}

// accountPatch applies the rules of a PATCH of an account to in (it changes
// in), and the side effects on who may authenticate; false when it answered an
// error.
func (b *BMC) accountPatch(w http.ResponseWriter, m, in map[string]any) bool {
	old, _ := m["UserName"].(string)
	name := old
	if v, ok := in["UserName"]; ok {
		s, isStr := v.(string)
		if !isStr {
			b.fail(w, http.StatusBadRequest, "", "The value for the property UserName is of the wrong type.", "Base.1.19.PropertyValueTypeError")
			return false
		}
		if s != old && s != "" && b.nameTaken(s, fmt.Sprint(m["@odata.id"])) {
			b.fail(w, http.StatusConflict, "", "The account already exists.", "Base.1.19.ResourceAlreadyExists")
			return false
		}
		name = s
	}
	pw, hasPw := in["Password"].(string)
	if !b.checkAccountProps(w, in, pw) {
		return false
	}
	if name != old {
		if old != "" {
			if secret, ok := b.accounts[old]; ok {
				delete(b.accounts, old)
				if name != "" {
					b.accounts[name] = secret
				}
			}
		}
	}
	if old == "" && name != "" {
		// Naming an empty slot takes it (the oracle sets no Enabled).
		if _, ok := in["Enabled"]; !ok {
			in["Enabled"] = true
		}
	}
	if hasPw {
		if name != "" {
			b.accounts[name] = pw
		}
		delete(in, "Password")
		if _, ok := m["PasswordChangeRequired"]; ok {
			in["PasswordChangeRequired"] = false
		}
	}
	return true
}

// passwordChangeRefused answers 403 PasswordChangeRequired to the user whose
// account says so, unless the request reads or patches that account; it reports
// whether it answered.
func (b *BMC) passwordChangeRefused(w http.ResponseWriter, r *http.Request, p string) bool {
	if r.Header.Get("X-Auth-Token") != "" {
		return false
	}
	u, _, ok := r.BasicAuth()
	if !ok {
		return false
	}
	own, _, found := b.accountOf(u)
	if !found || b.res[own]["PasswordChangeRequired"] != true {
		return false
	}
	if p == own && (r.Method == http.MethodGet || r.Method == http.MethodPatch) {
		return false
	}
	body := map[string]any{"error": map[string]any{
		"code": "Base.1.19.GeneralError", "message": "The password must be changed.",
		"@Message.ExtendedInfo": []any{map[string]any{"@odata.type": "#Message.v1_1_2.Message", "MessageId": "Base.1.19.PasswordChangeRequired",
			"Message": "The password provided for this account must be changed before access is granted.", "MessageArgs": []any{own}, "Severity": "Critical"}},
	}}
	writeJSON(w, http.StatusForbidden, body, "")
	return true
}

// allowed is the Allow header of a resource: what the mock does with it.
func (b *BMC) allowed(p string) string {
	m := []string{"GET", "HEAD"}
	if c := b.res[p]; c != nil {
		_, coll := c["Members"]
		if coll && (creatable[p] || b.isVolumes(p)) && !(p == accountsRoot && b.opts.FixedSlots > 0) {
			m = append(m, "POST")
		}
		if !coll {
			m = append(m, "PATCH")
			dir := path.Dir(p)
			if _, parentColl := b.res[dir]["Members"]; parentColl && (creatable[dir] || b.isVolumes(dir)) && !(dir == accountsRoot && b.opts.FixedSlots > 0) {
				m = append(m, "DELETE")
			}
		}
	}
	return strings.Join(m, ", ")
}
