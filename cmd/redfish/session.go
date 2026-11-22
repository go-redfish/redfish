package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/go-authn/secretsource"
	"github.com/go-redfish/redfish"
	"github.com/spf13/cobra"
)

func sessionCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "session",
		Short: "Open, show or end a Redfish session (the token is kept in a 0600 file)",
		Args:  cobra.NoArgs,
	}
	var store string
	login := &cobra.Command{
		Use:   "login",
		Short: "Log in (DSP0266 13.3.4) and save the session token",
		Long: `Log in with --user and --password-file: a POST to the Sessions collection, whose
answer carries the X-Auth-Token and the Session resource. The token is saved
(mode 0600) under the user's config directory, and never printed.

--session-store REF keeps the token in a secret manager instead: the 0600 file
then holds the endpoint, the session URI and REF, and no token, which never
touches the disk. REF is keychain:SERVICE/ACCOUNT (macOS Keychain),
secretservice:SERVICE/ACCOUNT (Secret Service) or keepassxc:URL (KeePassXC);
file: and systemd-creds: cannot be written and are refused, before the BMC is
contacted. A new login replaces the entry. session logout (or clear) ends the
session, then deletes the entry (KeePassXC asks you to confirm, and moves it to
its Recycle Bin) and the file.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if g.session {
				return errors.New("--session is for the other commands: login uses --user and --password-file")
			}
			if store != "" {
				if err := checkStoreRef(store); err != nil {
					return err
				}
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
			// abandon ends the session just opened, so none is left behind.
			abandon := func(err error) error {
				if cerr := c.Close(ctx); cerr != nil {
					return fmt.Errorf("%w (the BMC session %s could not be ended either: %v)", err, clean(uri), clean(cerr.Error()))
				}
				return err
			}
			p, err := g.sessionFile()
			if err != nil {
				return abandon(err)
			}
			ss := savedSession{Endpoint: g.endpoint, Token: tok, URI: uri}
			if store != "" {
				tb := []byte(tok)
				err := storeSecretRef(ctx, store, tb, secretsource.StoreOptions{Replace: true})
				clear(tb)
				if err != nil {
					return abandon(fmt.Errorf("--session-store %s: %w", clean(store), err))
				}
				ss = savedSession{Endpoint: g.endpoint, URI: uri, TokenRef: store}
			}
			if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
				return abandon(undoStore(ctx, store, err))
			}
			b, _ := json.Marshal(ss)
			if err := writeSecret(p, b); err != nil {
				return abandon(undoStore(ctx, store, err))
			}
			if store != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "session opened: %s (token in %s)\n", clean(uri), clean(store))
				return nil
			}
			fmt.Fprintf(cmd.OutOrStdout(), "session opened: %s\n", clean(uri))
			return nil
		},
	}
	login.Flags().StringVar(&store, "session-store", "", "keep the token in a secret manager: keychain:SERVICE/ACCOUNT, secretservice:SERVICE/ACCOUNT or keepassxc:URL")
	cmd.AddCommand(login)
	cmd.AddCommand(&cobra.Command{
		Use:   "logout",
		Short: "End the saved session at the BMC and remove its file",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			g.session = true
			p, _ := g.sessionFile()
			ref := ""
			if sf, err := g.readSessionFile(); err == nil {
				ref = sf.TokenRef
			}
			c, err := g.client(cmd, false)
			if err != nil {
				// A token that cannot be read back cannot end the BMC session,
				// but the entry and the file still go.
				var te *tokenOpenError
				if errors.As(err, &te) {
					if derr := dropSession(cmd.Context(), p, ref); derr != nil {
						return fmt.Errorf("%w; %v", err, derr)
					}
					return fmt.Errorf("the session file and the token entry were removed, but the BMC session was not ended: %w", err)
				}
				return err
			}
			cerr := c.Close(cmd.Context())
			// The entry and the file go whatever the BMC said: a token that no
			// longer works is of no use, and one that does must not linger.
			if rerr := dropSession(cmd.Context(), p, ref); rerr != nil {
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
			s, err := g.readSessionFile()
			if err != nil {
				return err
			}
			if s.TokenRef != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "session saved for %s: %s (token in %s)\n", clean(s.Endpoint), clean(s.URI), clean(s.TokenRef))
				return nil
			}
			fmt.Fprintf(cmd.OutOrStdout(), "session saved for %s: %s\n", clean(s.Endpoint), clean(s.URI))
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
					ref := ""
					if sf, err := g.readSessionFile(); err == nil {
						ref = sf.TokenRef
					}
					if rerr := dropSession(cmd.Context(), p, ref); rerr != nil {
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
