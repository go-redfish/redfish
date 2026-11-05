package redfish_test

import (
	"context"
	"testing"

	"github.com/go-redfish/redfish"
	"github.com/go-redfish/redfish/mockbmc"
)

// Discovery and the session flow must not be tuned to one vendor tree: both
// mockups are walked the same way.
func TestDiscoveryOnEveryTree(t *testing.T) {
	for _, tc := range []struct{ tree, chassis string }{
		{mockbmc.TreeRackmount, "/redfish/v1/Chassis/1U"},
		{mockbmc.TreeEmulator, "/redfish/v1/Chassis/1U"},
	} {
		t.Run(tc.tree, func(t *testing.T) {
			_, srv := newMock(t, mockbmc.Options{Tree: tc.tree})
			c := newClient(t, srv, redfish.Config{Session: true})
			ctx := context.Background()
			ch, err := c.ChassisURIs(ctx)
			if err != nil || len(ch) != 1 || ch[0] != tc.chassis {
				t.Fatalf("ChassisURIs = %v, %v; want [%s]", ch, err, tc.chassis)
			}
			// a session can be opened and resumed on a tree whose root has no Links.Sessions
			if err := c.Login(ctx); err != nil {
				t.Fatalf("Login: %v", err)
			}
			tok, uri := c.Session()
			if tok == "" || uri == "" {
				t.Fatalf("no session: %q %q", tok, uri)
			}
			rc := newClient(t, srv, redfish.Config{Token: tok, SessionURI: uri})
			if _, err := rc.Root(ctx); err != nil {
				t.Fatalf("resumed session: %v", err)
			}
			if err := rc.Close(ctx); err != nil {
				t.Fatalf("Close: %v", err)
			}
		})
	}
}

func TestUnknownTreeIsRefused(t *testing.T) {
	if _, err := mockbmc.New(mockbmc.Options{Tree: "nope"}); err == nil {
		t.Fatal("an unknown tree was accepted")
	}
}
