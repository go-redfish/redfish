package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/go-redfish/redfish"
	"github.com/spf13/cobra"
)

func sessionCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "session",
		Short: "Open, show or end a Redfish session (the token is kept in a 0600 file)",
		Args:  cobra.NoArgs,
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "login",
		Short: "Log in (DSP0266 13.3.4) and save the session token",
		Long: `Log in with --user and --password-file: a POST to the Sessions collection, whose
answer carries the X-Auth-Token and the Session resource. The token is saved
(mode 0600) under the user's config directory, and never printed.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if g.session {
				return errors.New("--session is for the other commands: login uses --user and --password-file")
			}
			c, err := g.client(cmd, true)
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			if err := c.Login(ctx); err != nil {
				return err
			}
			tok, uri := c.Session()
			p, err := g.sessionFile()
			if err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
				return err
			}
			b, _ := json.Marshal(savedSession{Endpoint: g.endpoint, Token: tok, URI: uri})
			if err := writeSecret(p, b); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "session opened: %s\n", uri)
			return nil
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "logout",
		Short: "End the saved session at the BMC and remove its file",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			g.session = true
			c, err := g.client(cmd, false)
			if err != nil {
				return err
			}
			p, _ := g.sessionFile()
			cerr := c.Close(cmd.Context())
			// The file goes whatever the BMC said: a token that no longer works
			// is of no use, and one that does must not linger.
			if rerr := os.Remove(p); rerr != nil && !errors.Is(rerr, os.ErrNotExist) {
				return rerr
			}
			if cerr != nil && !errors.Is(cerr, redfish.ErrAuth) {
				return fmt.Errorf("the session file was removed, but ending the session failed: %w", cerr)
			}
			fmt.Fprintln(cmd.OutOrStdout(), "session closed")
			return nil
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "status",
		Short: "Say whether a session is saved for the endpoint",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if g.endpoint == "" {
				return errors.New("--endpoint is required")
			}
			s, err := g.loadSession()
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "session saved for %s: %s\n", s.Endpoint, s.URI)
			return nil
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List the active sessions of the service (GetSessions)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := g.client(cmd, false)
			if err != nil {
				return err
			}
			l, err := c.GetSessions(cmd.Context())
			if err != nil {
				return err
			}
			return g.show(cmd, l)
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "clear",
		Short: "End every active session of the service (ClearSessions)",
		Long: `clear deletes every session of the Sessions collection, the other users' too.
The session this command runs with (--session) is ended last, and its saved file
is removed with it.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := g.client(cmd, false)
			if err != nil {
				return err
			}
			o, err := c.ClearSessions(cmd.Context())
			if err != nil {
				return err
			}
			if tok, _ := c.Session(); g.session && tok == "" {
				if p, perr := g.sessionFile(); perr == nil {
					if rerr := os.Remove(p); rerr != nil && !errors.Is(rerr, os.ErrNotExist) {
						return rerr
					}
				}
			}
			return g.showOutcome(cmd, o)
		},
	})
	var pf propFlags
	cfg := &cobra.Command{
		Use:   "config",
		Short: "Change SessionService properties (SetSessionService)",
		Long: `config changes properties of the SessionService: --set SessionTimeout=600, or
--json. A property the resource does not have is refused; nothing is sent when
it already holds the value.`,
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
			o, err := c.SetSessionService(cmd.Context(), p)
			if err != nil {
				return err
			}
			return g.showOutcome(cmd, o)
		},
	}
	pf.add(cfg)
	cmd.AddCommand(cfg)
	return cmd
}

// writeSecret writes a file that only its owner can read, replacing any.
func writeSecret(p string, b []byte) error {
	tmp := p + ".new"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}
