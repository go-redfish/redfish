package redfish

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
)

// Accounts and sessions: the Accounts commands of community.general
// redfish_command (AddUser, EnableUser, DeleteUser, DisableUser, UpdateUserRole,
// UpdateUserPassword, UpdateUserName, UpdateUserAccountTypes,
// UpdateAccountServiceProperties), redfish_info (ListUsers,
// GetAccountServiceConfig) and the Sessions commands (ClearSessions of
// redfish_command, SetSessionService of redfish_config, GetSessions of
// redfish_info). Oracle: plugins/module_utils/_redfish_utils.py at 33e521d6; each
// function is named after the method it ports. Resources: AccountService 1.15,
// ManagerAccount 1.12, SessionService 1.1, Session 1.7 (DSP8010); the Allow header
// is RFC 9110 10.2.1, the PasswordChangeRequired message is Base.1.x of DSP8011.
//
// A password is a Secret: it prints as "[redacted]" with every fmt verb, as JSON
// and in a log, and an error that carries it (a service that echoes what it was
// sent) is redacted before it leaves the library.

// Secret is a password: it never shows itself.
type Secret string

// Reveal is the secret's value, for the one place that sends it.
func (s Secret) Reveal() string { return string(s) }

// String is "[redacted]".
func (s Secret) String() string { return "[redacted]" }

// GoString is "[redacted]" too (%#v).
func (s Secret) GoString() string { return "[redacted]" }

// Format prints "[redacted]" for every verb.
func (s Secret) Format(f fmt.State, _ rune) { _, _ = f.Write([]byte("[redacted]")) }

// MarshalJSON is "[redacted]": the payload sent to a service is built from Reveal.
func (s Secret) MarshalJSON() ([]byte, error) { return []byte(`"[redacted]"`), nil }

// MarshalText is "[redacted]" (encoding.TextMarshaler, used by log encoders).
func (s Secret) MarshalText() ([]byte, error) { return []byte("[redacted]"), nil }

// LogValue keeps a password out of a log/slog record.
func (s Secret) LogValue() slog.Value { return slog.StringValue("[redacted]") }

// ErrNoAccount: no account has the Id or the UserName asked for (the oracle's
// no_match).
var ErrNoAccount = errors.New("redfish: no account with the given id or username")

// maxPassword bounds a password (DSP8010 gives no limit; a service refuses what
// it does not take).
const maxPassword = 1024

// AccountSpec says which account a command acts on and what to set (the module's
// account_id, account_username, account_password, account_roleid,
// account_accounttypes, account_oemaccounttypes and account_updatename).
type AccountSpec struct {
	ID, Username string // the account: its Id, or its UserName
	Password     Secret // AddUser, UpdateUserPassword
	RoleID       string
	AccountTypes []string
	// OEMAccountTypes is the OEMAccountTypes property.
	OEMAccountTypes []string
	// NewUsername is the name UpdateUserName gives (account_updatename).
	NewUsername string
}

// String lists the spec without its password.
func (a AccountSpec) String() string {
	return fmt.Sprintf("account{id=%q username=%q role=%q}", a.ID, a.Username, a.RoleID)
}

// redacted is an error that had the password in it.
type redactedError struct{ kind error }

func (e *redactedError) Error() string {
	return "redfish: the request failed (the answer repeated the password and was redacted)"
}
func (e *redactedError) Unwrap() error { return e.kind }

// redact returns err unchanged unless it carries the password, in its text or in
// the fields of the StatusError it wraps, in which case it returns an error of
// the same kind (ErrAuth, ErrNotFound, *StatusError with its code) without the
// text.
func redact(err error, pw Secret) error {
	if err == nil || pw == "" {
		return err
	}
	needles := []string{pw.Reveal()}
	if b, e := json.Marshal(pw.Reveal()); e == nil && len(b) > 2 {
		if j := string(b[1 : len(b)-1]); j != pw.Reveal() {
			needles = append(needles, j)
		}
	}
	has := func(s string) bool {
		for _, n := range needles {
			if strings.Contains(s, n) {
				return true
			}
		}
		return false
	}
	var se *StatusError
	if errors.As(err, &se) {
		leak := has(se.Error()) || has(se.ErrorCode) || has(se.Message) || has(se.Path)
		for _, m := range se.Info {
			leak = leak || has(m.MessageID) || has(m.Message) || has(m.Resolution) || has(strings.Join(m.MessageArgs, "\x00")) || has(strings.Join(m.RelatedProperties, "\x00"))
		}
		if !leak {
			return err
		}
		return &StatusError{Method: se.Method, Path: "[redacted]", Code: se.Code, Message: "[redacted]"}
	}
	if has(err.Error()) {
		for _, k := range []error{ErrAuth, ErrNotFound, ErrUnsupported, ErrMalformed, ErrTooLarge} {
			if errors.Is(err, k) {
				return &redactedError{kind: k}
			}
		}
		return &redactedError{}
	}
	return err
}

// ---- Finding the service and the accounts ----

// accountsOf is the Accounts collection of the AccountService
// (_find_accountservice_resource; a trailing slash is dropped). The oracle reads
// the AccountService's Accounts link; the DMTF emulator's older AccountService
// links it under Links, which is read as well.
func (c *Client) accountsOf(ctx context.Context) (string, error) {
	svc, err := c.rootLink(ctx, "AccountService")
	if err != nil {
		return "", err
	}
	s, _, err := c.getProps(ctx, svc)
	if err != nil {
		return "", err
	}
	l := linkOf(s, "Accounts")
	if l == "" {
		l = linkOf(sub(s, "Links"), "Accounts")
	}
	if l == "" {
		return "", fmt.Errorf("%w: the AccountService links no Accounts collection", ErrNotFound)
	}
	return strings.TrimRight(l, "/"), nil
}

type foundAccount struct {
	uri   string
	data  Props
	allow []string // the Allow header, nil when the service sent none
	hasAl bool
}

func allowOf(h http.Header) (methods []string, present bool) {
	v, ok := h["Allow"]
	if !ok {
		return nil, false
	}
	for _, m := range strings.Split(strings.Join(v, ","), ",") {
		if m = strings.TrimSpace(m); m != "" {
			methods = append(methods, strings.ToUpper(m))
		}
	}
	return methods, true
}

func idEquals(want string, v any) bool {
	switch x := v.(type) {
	case string:
		return x == want
	case json.Number:
		return x.String() == want
	}
	return false
}

// findAccount is _find_account_uri: the account whose UserName is username or
// whose Id is id (the first that matches either, in the order of the collection;
// a member that cannot be read is skipped, but an authentication failure is not).
// With passwordChange it reads that one resource instead of walking the
// collection. ErrNoAccount when none matches.
func (c *Client) findAccount(ctx context.Context, accounts, username, id, passwordChange string) (foundAccount, error) {
	if username == "" && id == "" {
		return foundAccount{}, errors.New("redfish: must provide an account id or an account username")
	}
	match := func(d Props) bool {
		return (username != "" && d["UserName"] == username) || (id != "" && idEquals(id, d["Id"]))
	}
	var uris []string
	if passwordChange != "" {
		uris = []string{passwordChange}
	} else {
		var err error
		if uris, err = c.members(ctx, accounts); err != nil {
			return foundAccount{}, err
		}
	}
	for _, u := range uris {
		d, resp, err := c.getProps(ctx, u)
		if err != nil {
			if errors.Is(err, ErrAuth) || passwordChange != "" {
				return foundAccount{}, err
			}
			continue
		}
		if match(d) {
			al, ok := allowOf(resp.Header)
			return foundAccount{uri: u, data: d, allow: al, hasAl: ok}, nil
		}
	}
	return foundAccount{}, ErrNoAccount
}

// emptySlot is _find_empty_account_slot: a service with a fixed number of
// accounts keeps the free ones with an empty UserName, disabled; the first slot
// may be reserved, so it is tried last.
func (c *Client) emptySlot(ctx context.Context, accounts string) (foundAccount, error) {
	uris, err := c.members(ctx, accounts)
	if err != nil {
		return foundAccount{}, err
	}
	if len(uris) > 1 {
		uris = append(uris[1:], uris[0])
	}
	for _, u := range uris {
		d, _, err := c.getProps(ctx, u)
		if err != nil {
			if errors.Is(err, ErrAuth) {
				return foundAccount{}, err
			}
			continue
		}
		// UserName == "" and not Enabled (an absent Enabled counts as true).
		en, has := d["Enabled"].(bool)
		if un, ok := d["UserName"].(string); ok && un == "" && has && !en {
			return foundAccount{uri: u, data: d}, nil
		}
	}
	return foundAccount{}, fmt.Errorf("%w: no empty account slot found", ErrNoAccount)
}

// ---- Reads ----

// userProperties are the properties ListUsers keeps, the ones the oracle gets
// "but does not fail if not found".
var userProperties = []string{"Id", "Name", "UserName", "RoleId", "Locked", "Enabled", "AccountTypes", "OEMAccountTypes"}

// ListUsers lists the accounts (list_users), without the empty slots of a service
// that has a fixed number of accounts (an empty UserName on a disabled account).
func (c *Client) ListUsers(ctx context.Context) ([]Props, error) {
	accounts, err := c.accountsOf(ctx)
	if err != nil {
		return nil, err
	}
	members, err := c.memberResources(ctx, accounts, MaxMembers, 8, nil)
	if err != nil {
		return nil, err
	}
	out := []Props{}
	for _, m := range members {
		user := pick(m.Props, userProperties...)
		if un, ok := user["UserName"]; !ok || un == "" {
			if en, _ := user["Enabled"].(bool); !en {
				continue
			}
		}
		out = append(out, user)
	}
	return out, nil
}

// accountServiceOf is the AccountService resource's path.
func (c *Client) accountServiceOf(ctx context.Context) (string, error) {
	svc, err := c.rootLink(ctx, "AccountService")
	if err != nil {
		return "", fmt.Errorf("%w: AccountService resource not found", ErrNotFound)
	}
	return svc, nil
}

// AccountServiceConfig is the AccountService resource (get_accountservice_properties).
func (c *Client) AccountServiceConfig(ctx context.Context) (Props, error) {
	svc, err := c.accountServiceOf(ctx)
	if err != nil {
		return nil, err
	}
	p, _, err := c.getProps(ctx, svc)
	return p, err
}

// roleProperties are what Roles keeps of a Role (Role 1.3, DSP8010).
var roleProperties = []string{"Id", "Name", "RoleId", "IsPredefined", "AssignedPrivileges", "OemPrivileges", "Description"}

// Roles lists the Roles of the AccountService (the redfishtool AccountService
// Roles operation; the oracle only names a RoleId). The collection is the
// AccountService's Roles link, else the one under Links.
func (c *Client) Roles(ctx context.Context) ([]Props, error) {
	svc, err := c.accountServiceOf(ctx)
	if err != nil {
		return nil, err
	}
	s, _, err := c.getProps(ctx, svc)
	if err != nil {
		return nil, err
	}
	l := linkOf(s, "Roles")
	if l == "" {
		l = linkOf(sub(s, "Links"), "Roles")
	}
	if l == "" {
		return nil, fmt.Errorf("%w: the AccountService links no Roles collection", ErrNotFound)
	}
	members, err := c.memberResources(ctx, l, MaxMembers, 8, nil)
	if err != nil {
		return nil, err
	}
	out := []Props{}
	for _, m := range members {
		out = append(out, pick(m.Props, roleProperties...))
	}
	return out, nil
}

// ---- Changes ----

// payload is the properties of an account the spec gives, the way add_user and
// add_user_via_patch build it (only what is not empty); the password is returned
// apart.
func (a AccountSpec) payload() Props {
	p := Props{}
	if a.Username != "" {
		p["UserName"] = a.Username
	}
	if a.RoleID != "" {
		p["RoleId"] = a.RoleID
	}
	if len(a.AccountTypes) > 0 {
		p["AccountTypes"] = anyList(a.AccountTypes)
	}
	if len(a.OEMAccountTypes) > 0 {
		p["OEMAccountTypes"] = anyList(a.OEMAccountTypes)
	}
	return p
}

func anyList(s []string) []any {
	out := make([]any, len(s))
	for i, v := range s {
		out[i] = v
	}
	return out
}

func checkPassword(pw Secret) error {
	if len(pw) > maxPassword {
		return fmt.Errorf("redfish: the password is longer than %d bytes", maxPassword)
	}
	return nil
}

// patchAccount is PatchChecked for an account: the Password is write-only
// (ManagerAccount: it reads back null, or is not there), so it is never compared
// with the resource: a password given is always sent. An error that repeats it is
// redacted.
func (c *Client) patchAccount(ctx context.Context, uri string, p Props, pw Secret) (Outcome, error) {
	var write Props
	if pw != "" {
		write = Props{"Password": pw.Reveal()}
	}
	o, err := c.patchChecked(ctx, uri, p, write)
	return o, redact(err, pw)
}

func (c *Client) addUserViaPatch(ctx context.Context, accounts string, a AccountSpec) (Outcome, error) {
	var f foundAccount
	var err error
	if a.ID != "" {
		f, err = c.findAccount(ctx, accounts, "", a.ID, "")
	} else {
		f, err = c.emptySlot(ctx, accounts)
	}
	if err != nil {
		return Outcome{}, err
	}
	return c.patchAccount(ctx, f.uri, a.payload(), a.Password)
}

// AddUser creates an account (add_user): nothing is done when the UserName
// exists. The account is created with a POST to the Accounts collection; when the
// service does not allow POST (no POST in its Allow header, or a 405 answer), an
// empty slot, or the account whose Id is given, is changed by a PATCH.
func (c *Client) AddUser(ctx context.Context, a AccountSpec) (Outcome, error) {
	if a.Username == "" {
		return Outcome{}, errors.New("redfish: must provide an account username for AddUser")
	}
	if err := checkPassword(a.Password); err != nil {
		return Outcome{}, err
	}
	accounts, err := c.accountsOf(ctx)
	if err != nil {
		return Outcome{}, err
	}
	switch _, err := c.findAccount(ctx, accounts, a.Username, "", ""); {
	case err == nil:
		return Outcome{Message: "The account " + clipText(a.Username) + " already exists"}, nil
	case errors.Is(err, ErrAuth):
		return Outcome{}, err
	}
	_, resp, err := c.getProps(ctx, accounts)
	if err != nil {
		return Outcome{}, err
	}
	if methods, present := allowOf(resp.Header); present && !slices.Contains(methods, http.MethodPost) {
		return c.addUserViaPatch(ctx, accounts, a)
	}
	body := a.payload()
	if a.Password != "" {
		body["Password"] = a.Password.Reveal()
	}
	if a.ID != "" {
		body["Id"] = a.ID
	}
	r, err := c.Post(ctx, accounts, body)
	if err != nil {
		var se *StatusError
		if errors.As(err, &se) && se.Code == http.StatusMethodNotAllowed {
			return c.addUserViaPatch(ctx, accounts, a)
		}
		return Outcome{}, redact(err, a.Password)
	}
	return outcomeOf(r, Outcome{Changed: true, Message: "Created the account " + clipText(a.Username)}), nil
}

func (c *Client) account(ctx context.Context, a AccountSpec, passwordChange string) (foundAccount, error) {
	accounts := ""
	if passwordChange == "" {
		var err error
		if accounts, err = c.accountsOf(ctx); err != nil {
			return foundAccount{}, err
		}
	}
	return c.findAccount(ctx, accounts, a.Username, a.ID, passwordChange)
}

func (c *Client) setAccount(ctx context.Context, a AccountSpec, p Props) (Outcome, error) {
	f, err := c.account(ctx, a, "")
	if err != nil {
		return Outcome{}, err
	}
	return c.patchAccount(ctx, f.uri, p, "")
}

// EnableUser enables the account (enable_user).
func (c *Client) EnableUser(ctx context.Context, a AccountSpec) (Outcome, error) {
	return c.setAccount(ctx, a, Props{"Enabled": true})
}

// DisableUser disables the account (disable_user).
func (c *Client) DisableUser(ctx context.Context, a AccountSpec) (Outcome, error) {
	return c.setAccount(ctx, a, Props{"Enabled": false})
}

// UpdateUserRole changes the RoleId of the account (update_user_role).
func (c *Client) UpdateUserRole(ctx context.Context, a AccountSpec) (Outcome, error) {
	if a.RoleID == "" {
		return Outcome{}, errors.New("redfish: must provide a role id for UpdateUserRole")
	}
	return c.setAccount(ctx, a, Props{"RoleId": a.RoleID})
}

// UpdateUserName renames the account to NewUsername (update_user_name).
func (c *Client) UpdateUserName(ctx context.Context, a AccountSpec) (Outcome, error) {
	if a.NewUsername == "" {
		return Outcome{}, errors.New("redfish: must provide the new username for UpdateUserName")
	}
	return c.setAccount(ctx, a, Props{"UserName": a.NewUsername})
}

// UpdateUserAccountTypes sets AccountTypes and/or OEMAccountTypes
// (update_user_accounttypes); one of them at least is required (a nil list is
// "not given").
func (c *Client) UpdateUserAccountTypes(ctx context.Context, a AccountSpec) (Outcome, error) {
	if a.AccountTypes == nil && a.OEMAccountTypes == nil {
		return Outcome{}, errors.New("redfish: must provide account types or OEM account types for UpdateUserAccountTypes")
	}
	p := Props{}
	if len(a.AccountTypes) > 0 {
		p["AccountTypes"] = anyList(a.AccountTypes)
	}
	if len(a.OEMAccountTypes) > 0 {
		p["OEMAccountTypes"] = anyList(a.OEMAccountTypes)
	}
	return c.setAccount(ctx, a, p)
}

// UpdateUserPassword sets the password of the account (update_user_password).
// When the service refuses to answer because the password of the user the client
// authenticates as must be changed (the Base.1.x.PasswordChangeRequired message,
// whose first argument is the account: PasswordChangeURI), the account is that
// one, as the module does, and the password can be changed with it.
func (c *Client) UpdateUserPassword(ctx context.Context, a AccountSpec) (Outcome, error) {
	if a.Password == "" {
		return Outcome{}, errors.New("redfish: must provide a password for UpdateUserPassword")
	}
	if err := checkPassword(a.Password); err != nil {
		return Outcome{}, err
	}
	f, err := c.account(ctx, a, "")
	if err != nil {
		uri := PasswordChangeURI(err)
		if uri == "" {
			return Outcome{}, redact(err, a.Password)
		}
		if f, err = c.account(ctx, a, uri); err != nil {
			return Outcome{}, redact(err, a.Password)
		}
	}
	return c.patchAccount(ctx, f.uri, Props{}, a.Password)
}

// DeleteUser removes the account (delete_user); nothing is done when it does not
// exist. A service that does not allow DELETE (no DELETE in the Allow header of
// the account, or a 405) gets the account emptied by a PATCH instead: UserName
// "" and, when it is enabled, Enabled false (delete_user_via_patch).
func (c *Client) DeleteUser(ctx context.Context, a AccountSpec) (Outcome, error) {
	f, err := c.account(ctx, a, "")
	if errors.Is(err, ErrNoAccount) {
		return Outcome{Message: "The account does not exist"}, nil
	}
	if err != nil {
		return Outcome{}, err
	}
	if f.hasAl && !slices.Contains(f.allow, http.MethodDelete) {
		return c.deleteViaPatch(ctx, f)
	}
	r, err := c.Delete(ctx, f.uri, "")
	if err != nil {
		var se *StatusError
		if errors.As(err, &se) && se.Code == http.StatusMethodNotAllowed {
			return c.deleteViaPatch(ctx, f)
		}
		return Outcome{}, err
	}
	return outcomeOf(r, Outcome{Changed: true, Message: "Deleted " + f.uri}), nil
}

func (c *Client) deleteViaPatch(ctx context.Context, f foundAccount) (Outcome, error) {
	p := Props{"UserName": ""}
	if en, _ := f.data["Enabled"].(bool); en {
		p["Enabled"] = false
	}
	return c.patchAccount(ctx, f.uri, p, "")
}

// UpdateAccountServiceProperties changes properties of the AccountService
// (update_accountservice_properties; PatchChecked: a property the resource does
// not have is ErrUnsupported). props must not be nil.
func (c *Client) UpdateAccountServiceProperties(ctx context.Context, props Props) (Outcome, error) {
	if props == nil {
		return Outcome{}, errors.New("redfish: must provide the account service properties")
	}
	svc, err := c.accountServiceOf(ctx)
	if err != nil {
		return Outcome{}, err
	}
	o, err := c.PatchChecked(ctx, svc, props)
	if err == nil && o.Changed {
		o.Message = "Modified account service"
	}
	return o, err
}

// PasswordChangeURI is check_password_change_required: when a service answered
// that the password of the authenticated user must be changed (an extended
// message Base.1.x.PasswordChangeRequired, in the error object or at the top
// level), the account it names (its first argument; a path of the service), else
// "".
func PasswordChangeURI(err error) string {
	var info []Message
	var se *StatusError
	var ae *authError
	switch {
	case errors.As(err, &se):
		info = se.Info
	case errors.As(err, &ae):
		info = ae.Info
	}
	for _, m := range info {
		if strings.HasPrefix(m.MessageID, "Base.1.") && strings.HasSuffix(m.MessageID, ".PasswordChangeRequired") && len(m.MessageArgs) > 0 {
			return servicePath(m.MessageArgs[0])
		}
	}
	return ""
}

// ---- Sessions ----

// SessionServiceURI is the SessionService resource.
func (c *Client) SessionServiceURI(ctx context.Context) (string, error) {
	svc, err := c.rootLink(ctx, "SessionService")
	if err != nil {
		return "", fmt.Errorf("%w: SessionService resource not found", ErrNotFound)
	}
	return svc, nil
}

// sessionProperties are what GetSessions keeps of a Session.
var sessionProperties = []string{"Description", "Id", "Name", "UserName"}

// GetSessions lists the active sessions (get_sessions). The Sessions collection is
// the service root's Links.Sessions, else the default path (the oracle fails when
// the root has no Links.Sessions).
func (c *Client) GetSessions(ctx context.Context) ([]Props, error) {
	members, err := c.memberResources(ctx, c.sessionsPath(ctx), MaxMembers, 8, nil)
	if err != nil {
		return nil, err
	}
	out := []Props{}
	for _, m := range members {
		out = append(out, pick(m.Props, sessionProperties...))
	}
	return out, nil
}

// ClearSessions ends every active session (clear_sessions). When the client's own
// session is among them it is ended last (the oracle authenticates by password on
// each request; a client that holds a token would be refused for the rest).
func (c *Client) ClearSessions(ctx context.Context) (Outcome, error) {
	uris, err := c.members(ctx, c.sessionsPath(ctx))
	if err != nil {
		return Outcome{}, err
	}
	if len(uris) == 0 {
		return Outcome{Message: "There are no active sessions"}, nil
	}
	_, own := c.Session()
	closeOwn := false
	for _, u := range uris {
		if own != "" && strings.TrimRight(u, "/") == strings.TrimRight(own, "/") {
			closeOwn = true
			continue
		}
		if _, err := c.Delete(ctx, u, ""); err != nil {
			return Outcome{}, err
		}
	}
	if closeOwn {
		if err := c.Close(ctx); err != nil {
			return Outcome{}, err
		}
	}
	return Outcome{Changed: true, Message: "Cleared all sessions successfully"}, nil
}

// SetSessionService changes properties of the SessionService, SessionTimeout for
// one (set_session_service; PatchChecked).
func (c *Client) SetSessionService(ctx context.Context, props Props) (Outcome, error) {
	if props == nil {
		return Outcome{}, errors.New("redfish: must provide the session service properties")
	}
	svc, err := c.SessionServiceURI(ctx)
	if err != nil {
		return Outcome{}, err
	}
	o, err := c.PatchChecked(ctx, svc, props)
	if err == nil && o.Changed {
		o.Message = "Modified session service"
	}
	return o, err
}
