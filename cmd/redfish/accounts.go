package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/go-redfish/redfish"
	"github.com/spf13/cobra"
)

// readPassword reads a password from a stream: one line, at most
// maxPasswordFile bytes, the final line break dropped, never empty, no control
// character (a password that holds a line break or a NUL is a file that is not
// one). The error never holds what was read.
func readPassword(r io.Reader) (redfish.Secret, error) {
	b, err := io.ReadAll(io.LimitReader(r, maxPasswordFile+1))
	if err != nil {
		return "", fmt.Errorf("reading the password: %w", err)
	}
	defer clear(b)
	if len(b) > maxPasswordFile {
		return "", fmt.Errorf("the password is larger than %d bytes", maxPasswordFile)
	}
	b = bytes.TrimSuffix(bytes.TrimSuffix(b, []byte("\n")), []byte("\r"))
	if len(b) == 0 {
		return "", errors.New("the password is empty")
	}
	for _, c := range b {
		if c < 0x20 || c == 0x7f {
			return "", errors.New("the password holds a control character or more than one line")
		}
	}
	return redfish.Secret(b), nil
}

// newPassword is where the password of an account comes from: a file (mode 0600,
// as --password-file) or the standard input; never a flag or the environment.
type newPassword struct {
	file  string
	stdin bool
}

func (n *newPassword) add(cmd *cobra.Command) {
	cmd.Flags().StringVar(&n.file, "new-password-file", "", "file holding the account's password (mode 0600)")
	cmd.Flags().BoolVar(&n.stdin, "new-password-stdin", false, "read the account's password from the standard input (one line)")
}

// secret reads the password; with required false, "none given" is an empty one.
func (n *newPassword) secret(cmd *cobra.Command, required bool) (redfish.Secret, error) {
	switch {
	case n.file != "" && n.stdin:
		return "", errors.New("--new-password-file and --new-password-stdin are exclusive")
	case n.file != "":
		f, err := openSecretFile(n.file)
		if err != nil {
			return "", err
		}
		defer f.Close()
		pw, err := readPassword(f)
		if err != nil {
			return "", fmt.Errorf("%s: %w", n.file, err)
		}
		return pw, nil
	case n.stdin:
		return readPassword(cmd.InOrStdin())
	case required:
		return "", errors.New("the password is required: --new-password-file FILE (mode 0600) or --new-password-stdin")
	}
	return "", nil
}

// openSecretFile opens a regular file that only its owner can read.
func openSecretFile(p string) (*os.File, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, fmt.Errorf("password file: %w", err)
	}
	st, err := f.Stat()
	switch {
	case err != nil:
		f.Close()
		return nil, fmt.Errorf("password file: %w", err)
	case !st.Mode().IsRegular():
		f.Close()
		return nil, fmt.Errorf("password file %s is not a regular file", p)
	case st.Mode().Perm()&0o077 != 0:
		f.Close()
		return nil, fmt.Errorf("password file %s is readable by group or others (mode %04o): chmod 600 it", p, st.Mode().Perm())
	}
	return f, nil
}

func accountsCmd(g *globals) *cobra.Command {
	var id string
	cmd := &cobra.Command{
		Use:   "accounts",
		Short: "Accounts: list, add, delete, enable, disable, password, role, rename, types, service config",
		Long: `accounts drives the AccountService (the Accounts commands of redfish_command,
ListUsers and GetAccountServiceConfig of redfish_info).

An account is named by its USERNAME, or by --id (its Id, the last segment of its
path). The password of an account comes from --new-password-file (mode 0600) or
--new-password-stdin, never from a flag or the environment, and is never
printed: an error that holds it is redacted. A service that has a fixed number of
accounts is handled as the Ansible modules do: add takes an empty slot (or the
one --id names) by a PATCH, and delete empties the account, when POST or DELETE
is not allowed.`,
		Args: cobra.NoArgs,
	}
	cmd.PersistentFlags().StringVar(&id, "id", "", "the account's Id (add: the slot to use)")

	spec := func(args []string) redfish.AccountSpec {
		a := redfish.AccountSpec{ID: id}
		if len(args) > 0 {
			a.Username = args[0]
		}
		return a
	}
	// run builds the client and runs one command on the account named.
	run := func(fn func(ctx context.Context, c *redfish.Client, a redfish.AccountSpec) (redfish.Outcome, error), prep func(cmd *cobra.Command, a *redfish.AccountSpec) error) func(*cobra.Command, []string) error {
		return func(cmd *cobra.Command, args []string) error {
			a := spec(args)
			if prep != nil {
				if err := prep(cmd, &a); err != nil {
					return err
				}
			}
			c, err := g.client(cmd, false)
			if err != nil {
				return err
			}
			o, err := fn(cmd.Context(), c, a)
			if err != nil {
				return err
			}
			return g.showOutcome(cmd, o)
		}
	}
	named := cobra.MaximumNArgs(1)

	cmd.AddCommand(&cobra.Command{
		Use: "list", Short: "List the accounts, without the empty slots (ListUsers)", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := g.client(cmd, false)
			if err != nil {
				return err
			}
			u, err := c.ListUsers(cmd.Context())
			if err != nil {
				return err
			}
			return g.show(cmd, u)
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use: "roles", Short: "List the roles of the service (redfishtool AccountService Roles)", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := g.client(cmd, false)
			if err != nil {
				return err
			}
			r, err := c.Roles(cmd.Context())
			if err != nil {
				return err
			}
			return g.show(cmd, r)
		},
	})

	var types, oemTypes []string
	typeFlags := func(c *cobra.Command) {
		c.Flags().StringArrayVar(&types, "type", nil, "an AccountTypes value: Redfish, SNMP, OEM, HostConsole, ManagerConsole, IPMI, KVMIP, VirtualMedia, WebUI (repeatable)")
		c.Flags().StringArrayVar(&oemTypes, "oem-type", nil, "an OEMAccountTypes value (repeatable)")
	}

	var role string
	var pw newPassword
	add := &cobra.Command{
		Use:   "add USERNAME",
		Short: "Create an account (AddUser)",
		Long: `add creates the account with a POST to the Accounts collection; nothing is done
when a user of that name exists. When the service does not allow POST (its Allow
header, or a 405), an empty slot, or the account --id names, is changed by a
PATCH. The password: --new-password-file or --new-password-stdin.`,
		Args: cobra.ExactArgs(1),
		RunE: run(func(ctx context.Context, c *redfish.Client, a redfish.AccountSpec) (redfish.Outcome, error) {
			return c.AddUser(ctx, a)
		}, func(cmd *cobra.Command, a *redfish.AccountSpec) error {
			var err error
			a.RoleID, a.AccountTypes, a.OEMAccountTypes = role, types, oemTypes
			a.Password, err = pw.secret(cmd, false)
			return err
		}),
	}
	pw.add(add)
	add.Flags().StringVar(&role, "role", "", "the RoleId (Administrator, Operator, ReadOnly...)")
	typeFlags(add)
	cmd.AddCommand(add)

	one := func(use, short string, fn func(c *redfish.Client, ctx context.Context, a redfish.AccountSpec) (redfish.Outcome, error)) {
		cmd.AddCommand(&cobra.Command{
			Use: use + " [USERNAME]", Short: short, Args: named,
			RunE: run(func(ctx context.Context, c *redfish.Client, a redfish.AccountSpec) (redfish.Outcome, error) {
				return fn(c, ctx, a)
			}, nil),
		})
	}
	one("delete", "Delete the account (DeleteUser); nothing is done when it does not exist", (*redfish.Client).DeleteUser)
	one("enable", "Enable the account (EnableUser)", (*redfish.Client).EnableUser)
	one("disable", "Disable the account (DisableUser)", (*redfish.Client).DisableUser)

	var pwFlags newPassword
	passwd := &cobra.Command{
		Use:   "password [USERNAME]",
		Short: "Set the password of the account (UpdateUserPassword)",
		Long: `password sets the password from --new-password-file or --new-password-stdin. A
user whose password the service says must be changed (Base.1.x.PasswordChangeRequired)
changes it with its own credentials, the account being the one the service names.`,
		Args: named,
		RunE: run(func(ctx context.Context, c *redfish.Client, a redfish.AccountSpec) (redfish.Outcome, error) {
			return c.UpdateUserPassword(ctx, a)
		}, func(cmd *cobra.Command, a *redfish.AccountSpec) (err error) {
			a.Password, err = pwFlags.secret(cmd, true)
			return err
		}),
	}
	pwFlags.add(passwd)
	cmd.AddCommand(passwd)

	cmd.AddCommand(&cobra.Command{
		Use: "role [USERNAME] ROLE", Short: "Set the RoleId of the account (UpdateUserRole)", Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			// with --id, the only argument is the role
			a := redfish.AccountSpec{ID: id}
			switch {
			case len(args) == 2:
				a.Username, a.RoleID = args[0], args[1]
			case id != "":
				a.RoleID = args[0]
			default:
				return errors.New("want USERNAME ROLE, or --id ID ROLE")
			}
			return run(func(ctx context.Context, c *redfish.Client, a redfish.AccountSpec) (redfish.Outcome, error) {
				return c.UpdateUserRole(ctx, a)
			}, func(_ *cobra.Command, x *redfish.AccountSpec) error { *x = a; return nil })(cmd, nil)
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use: "rename [USERNAME] NEWNAME", Short: "Rename the account (UpdateUserName)", Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			a := redfish.AccountSpec{ID: id}
			switch {
			case len(args) == 2:
				a.Username, a.NewUsername = args[0], args[1]
			case id != "":
				a.NewUsername = args[0]
			default:
				return errors.New("want USERNAME NEWNAME, or --id ID NEWNAME")
			}
			return run(func(ctx context.Context, c *redfish.Client, a redfish.AccountSpec) (redfish.Outcome, error) {
				return c.UpdateUserName(ctx, a)
			}, func(_ *cobra.Command, x *redfish.AccountSpec) error { *x = a; return nil })(cmd, nil)
		},
	})
	var uTypes, uOEM []string
	ut := &cobra.Command{
		Use: "types [USERNAME]", Short: "Set AccountTypes and/or OEMAccountTypes (UpdateUserAccountTypes)", Args: named,
		RunE: run(func(ctx context.Context, c *redfish.Client, a redfish.AccountSpec) (redfish.Outcome, error) {
			return c.UpdateUserAccountTypes(ctx, a)
		}, func(cmd *cobra.Command, a *redfish.AccountSpec) error {
			if !cmd.Flags().Changed("type") && !cmd.Flags().Changed("oem-type") {
				return errors.New("--type or --oem-type is required")
			}
			a.AccountTypes, a.OEMAccountTypes = uTypes, uOEM
			return nil
		}),
	}
	ut.Flags().StringArrayVar(&uTypes, "type", nil, "an AccountTypes value (repeatable)")
	ut.Flags().StringArrayVar(&uOEM, "oem-type", nil, "an OEMAccountTypes value (repeatable)")
	cmd.AddCommand(ut)

	// The AccountService resource.
	cfg := &cobra.Command{
		Use: "config", Short: "The AccountService: show it (GetAccountServiceConfig), or change it with set", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := g.client(cmd, false)
			if err != nil {
				return err
			}
			p, err := c.AccountServiceConfig(cmd.Context())
			if err != nil {
				return err
			}
			return g.show(cmd, p)
		},
	}
	var pf propFlags
	set := &cobra.Command{
		Use: "set", Short: "Change AccountService properties (UpdateAccountServiceProperties)",
		Long: `set changes properties of the AccountService (--set MinPasswordLength=12,
--set AccountLockoutThreshold=5, --json for the others). A property the resource
does not have is refused; nothing is sent when it already holds every value.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := pf.props()
			if err != nil {
				return err
			}
			if len(p) == 0 {
				return errors.New("--set or --json is required")
			}
			c, err := g.client(cmd, false)
			if err != nil {
				return err
			}
			o, err := c.UpdateAccountServiceProperties(cmd.Context(), p)
			if err != nil {
				return err
			}
			return g.showOutcome(cmd, o)
		},
	}
	pf.add(set)
	cfg.AddCommand(set)
	cmd.AddCommand(cfg)
	return cmd
}
