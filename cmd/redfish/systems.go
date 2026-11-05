package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/go-redfish/redfish"
	"github.com/spf13/cobra"
)

// show prints a value as JSON, or as a table with -o table.
func (g *globals) show(cmd *cobra.Command, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return g.printDoc(cmd.OutOrStdout(), b)
}

// showOutcome prints what a command that changes something did: the outcome on
// stdout, the warning and the task monitor on stderr.
func (g *globals) showOutcome(cmd *cobra.Command, o redfish.Outcome) error {
	if o.Warning != "" {
		fmt.Fprintf(cmd.ErrOrStderr(), "warning: %s\n", clean(o.Warning))
	}
	if o.Monitor != "" {
		fmt.Fprintf(cmd.ErrOrStderr(), "task: accepted; monitor: %s\n", clean(o.Monitor))
	}
	m := map[string]any{"changed": o.Changed, "message": o.Message}
	return g.show(cmd, m)
}

// sysAction runs fn against the system picked by --id (the only one when it is
// not given; redfish.ErrAmbiguous when there are several).
func sysAction(g *globals, id *string, fn func(ctx context.Context, cmd *cobra.Command, c *redfish.Client, sys string, args []string) error) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		c, err := g.client(cmd, false)
		if err != nil {
			return err
		}
		ctx := cmd.Context()
		sys, err := c.ResolveSystem(ctx, *id)
		if err != nil {
			return err
		}
		return fn(ctx, cmd, c, sys, args)
	}
}

// parseAttrs reads KEY=VALUE pairs: the value is JSON when it parses as a
// number, a boolean or null, else the text itself.
func parseAttrs(pairs []string) (redfish.Props, error) {
	out := redfish.Props{}
	for _, p := range pairs {
		k, v, ok := strings.Cut(p, "=")
		if !ok || k == "" {
			return nil, fmt.Errorf("--attr %q: want NAME=VALUE", p)
		}
		var x any
		d := json.NewDecoder(strings.NewReader(v))
		d.UseNumber()
		if d.Decode(&x) != nil || d.More() || !(x == nil || isScalar(x)) {
			x = v
		}
		out[k] = x
	}
	return out, nil
}

func isScalar(x any) bool {
	switch x.(type) {
	case json.Number, bool:
		return true
	}
	return false
}

func systemsCmd(g *globals) *cobra.Command {
	var id string
	cmd := &cobra.Command{
		Use:   "systems",
		Short: "Computer systems: power, boot, BIOS, secure boot, LED, inventories, health",
		Long: `systems drives the ComputerSystem resources of the service (the Systems
commands of the Ansible modules redfish_command, redfish_config and redfish_info).
With several systems, --id names the one to use (the last segment of its path);
without it, a service that has several answers with an error that lists them.`,
		Args: cobra.NoArgs,
	}
	cmd.PersistentFlags().StringVar(&id, "id", "", "the system to use (default: the only one)")
	act := func(fn func(ctx context.Context, cmd *cobra.Command, c *redfish.Client, sys string, args []string) error) func(*cobra.Command, []string) error {
		return sysAction(g, &id, fn)
	}
	outcome := func(fn func(ctx context.Context, c *redfish.Client, sys string, args []string) (redfish.Outcome, error)) func(*cobra.Command, []string) error {
		return act(func(ctx context.Context, cmd *cobra.Command, c *redfish.Client, sys string, args []string) error {
			o, err := fn(ctx, c, sys, args)
			if err != nil {
				return err
			}
			return g.showOutcome(cmd, o)
		})
	}
	value := func(fn func(ctx context.Context, c *redfish.Client, sys string) (any, error)) func(*cobra.Command, []string) error {
		return act(func(ctx context.Context, cmd *cobra.Command, c *redfish.Client, sys string, _ []string) error {
			v, err := fn(ctx, c, sys)
			if err != nil {
				return err
			}
			return g.show(cmd, v)
		})
	}

	cmd.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List the systems (their paths)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := g.client(cmd, false)
			if err != nil {
				return err
			}
			u, err := c.SystemURIs(cmd.Context())
			if err != nil {
				return err
			}
			return g.show(cmd, u)
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "power COMMAND",
		Short: "Power: PowerOn, PowerForceOff, PowerForceRestart, PowerGracefulRestart, PowerGracefulShutdown, PowerReboot, PowerCycle, PowerFullPowerCycle",
		Long: `power runs the Reset action of the system with the ResetType the service allows
(its ActionInfo or AllowableValues; On and ForceOn, ForceOff and GracefulShutdown,
GracefulRestart and ForceRestart stand for each other when only one is allowed).
Nothing is sent when the system is already in the state asked.`,
		Args: cobra.ExactArgs(1),
		RunE: outcome(func(ctx context.Context, c *redfish.Client, sys string, a []string) (redfish.Outcome, error) {
			return c.SystemPower(ctx, sys, a[0])
		}),
	})

	// Boot override.
	bo := &cobra.Command{Use: "boot-override", Short: "The boot source override: get, set, disable", Args: cobra.NoArgs}
	bo.AddCommand(&cobra.Command{
		Use: "get", Short: "Show the boot source override (GetBootOverride)", Args: cobra.NoArgs,
		RunE: value(func(ctx context.Context, c *redfish.Client, sys string) (any, error) {
			return c.BootOverrideOf(ctx, sys)
		}),
	})
	var boot redfish.BootOverride
	var once, continuous bool
	set := &cobra.Command{
		Use:   "set DEVICE",
		Short: "Set the override: one time (default) or continuous (SetOneTimeBoot, EnableContinuousBootOverride)",
		Long: `set DEVICE sets the boot source override target (Pxe, Cd, Hdd, UefiTarget,
UefiBootNext...), which must be one the system allows. UefiTarget takes
--uefi-target, UefiBootNext takes --boot-next. --continuous keeps the override
until it is disabled, instead of using it for the next boot only.`,
		Args: cobra.ExactArgs(1),
		RunE: outcome(func(ctx context.Context, c *redfish.Client, sys string, a []string) (redfish.Outcome, error) {
			if once && continuous {
				return redfish.Outcome{}, errors.New("--once and --continuous are exclusive")
			}
			b := boot
			b.Device, b.Enabled = a[0], "Once"
			if continuous {
				b.Enabled = "Continuous"
			}
			return c.SetBootOverride(ctx, sys, b)
		}),
	}
	set.Flags().StringVar(&boot.UefiTarget, "uefi-target", "", "the UEFI device path, for DEVICE UefiTarget")
	set.Flags().StringVar(&boot.BootNext, "boot-next", "", "the boot option, for DEVICE UefiBootNext")
	set.Flags().StringVar(&boot.Mode, "mode", "", "boot mode: Legacy or UEFI (default: leave it)")
	set.Flags().BoolVar(&once, "once", false, "use the override for the next boot only (the default)")
	set.Flags().BoolVar(&continuous, "continuous", false, "keep the override until it is disabled")
	bo.AddCommand(set)
	bo.AddCommand(&cobra.Command{
		Use: "disable", Short: "Disable the override (DisableBootOverride)", Args: cobra.NoArgs,
		RunE: outcome(func(ctx context.Context, c *redfish.Client, sys string, _ []string) (redfish.Outcome, error) {
			return c.SetBootOverride(ctx, sys, redfish.BootOverride{Enabled: "Disabled"})
		}),
	})
	cmd.AddCommand(bo)

	// Boot order.
	bord := &cobra.Command{Use: "boot-order", Short: "The boot order: get, set, default", Args: cobra.NoArgs}
	bord.AddCommand(&cobra.Command{
		Use: "get", Short: "Show the boot order (GetBootOrder)", Args: cobra.NoArgs,
		RunE: value(func(ctx context.Context, c *redfish.Client, sys string) (any, error) { return c.BootOrder(ctx, sys) }),
	})
	bord.AddCommand(&cobra.Command{
		Use: "set REFERENCE...", Short: "Set the boot order (SetBootOrder)", Args: cobra.MinimumNArgs(1),
		RunE: outcome(func(ctx context.Context, c *redfish.Client, sys string, a []string) (redfish.Outcome, error) {
			return c.SetBootOrder(ctx, sys, a)
		}),
	})
	bord.AddCommand(&cobra.Command{
		Use: "default", Short: "Reset the boot order to the default (SetDefaultBootOrder)", Args: cobra.NoArgs,
		RunE: outcome(func(ctx context.Context, c *redfish.Client, sys string, _ []string) (redfish.Outcome, error) {
			return c.SetDefaultBootOrder(ctx, sys)
		}),
	})
	cmd.AddCommand(bord)

	// BIOS.
	bios := &cobra.Command{Use: "bios", Short: "BIOS attributes: get, set, verify, reset, registry", Args: cobra.NoArgs}
	bios.AddCommand(&cobra.Command{
		Use: "get", Short: "Show the BIOS attributes (GetBiosAttributes)", Args: cobra.NoArgs,
		RunE: value(func(ctx context.Context, c *redfish.Client, sys string) (any, error) {
			return c.BiosAttributes(ctx, sys)
		}),
	})
	var attrs []string
	var applyTime string
	set2 := &cobra.Command{
		Use:   "set --attr NAME=VALUE...",
		Short: "Set BIOS attributes (SetBiosAttributes)",
		Long: `set sends the attributes through the Settings object of the Bios resource;
the BIOS usually applies them at its next reset. Attributes the BIOS does not
have are left out and named on stderr. A VALUE that is a number, true, false or
null is sent as such, anything else as text.`,
		Args: cobra.NoArgs,
		RunE: outcome(func(ctx context.Context, c *redfish.Client, sys string, _ []string) (redfish.Outcome, error) {
			p, err := parseAttrs(attrs)
			if err != nil {
				return redfish.Outcome{}, err
			}
			if len(p) == 0 {
				return redfish.Outcome{}, errors.New("--attr is required")
			}
			return c.SetBiosAttributes(ctx, sys, p, applyTime)
		}),
	}
	set2.Flags().StringArrayVar(&attrs, "attr", nil, "NAME=VALUE (repeatable)")
	set2.Flags().StringVar(&applyTime, "apply-time", "", "when to apply: Immediate, OnReset, AtMaintenanceWindowStart... (default: the service's)")
	bios.AddCommand(set2)
	var vattrs []string
	verify := &cobra.Command{
		Use: "verify --attr NAME=VALUE...", Short: "Check that the BIOS holds the attributes (VerifyBiosAttributes)", Args: cobra.NoArgs,
		RunE: outcome(func(ctx context.Context, c *redfish.Client, sys string, _ []string) (redfish.Outcome, error) {
			p, err := parseAttrs(vattrs)
			if err != nil {
				return redfish.Outcome{}, err
			}
			return c.VerifyBiosAttributes(ctx, sys, p)
		}),
	}
	verify.Flags().StringArrayVar(&vattrs, "attr", nil, "NAME=VALUE (repeatable)")
	bios.AddCommand(verify)
	bios.AddCommand(&cobra.Command{
		Use: "reset", Short: "Reset the BIOS to its default settings (SetBiosDefaultSettings)", Args: cobra.NoArgs,
		RunE: outcome(func(ctx context.Context, c *redfish.Client, sys string, _ []string) (redfish.Outcome, error) {
			return c.ResetBios(ctx, sys)
		}),
	})
	bios.AddCommand(&cobra.Command{
		Use: "registry", Short: "Show the BIOS attribute registry (GetBiosRegistries)", Args: cobra.NoArgs,
		RunE: value(func(ctx context.Context, c *redfish.Client, sys string) (any, error) {
			r, _, err := c.BiosRegistry(ctx, sys)
			return r, err
		}),
	})
	cmd.AddCommand(bios)

	// Secure boot.
	sb := &cobra.Command{Use: "secure-boot", Short: "UEFI Secure Boot: enable, disable", Args: cobra.NoArgs}
	for _, e := range []struct {
		use, short string
		on         bool
	}{{"enable", "Enable Secure Boot (EnableSecureBoot, SetSecureBoot)", true}, {"disable", "Disable Secure Boot (SetSecureBoot)", false}} {
		sb.AddCommand(&cobra.Command{
			Use: e.use, Short: e.short, Args: cobra.NoArgs,
			RunE: outcome(func(ctx context.Context, c *redfish.Client, sys string, _ []string) (redfish.Outcome, error) {
				return c.SetSecureBoot(ctx, sys, e.on)
			}),
		})
	}
	cmd.AddCommand(sb)

	// Power restore policy.
	prp := &cobra.Command{Use: "power-restore-policy", Short: "The power restore policy: get, set", Args: cobra.NoArgs}
	prp.AddCommand(&cobra.Command{
		Use: "get", Short: "Show the policy (GetPowerRestorePolicy)", Args: cobra.NoArgs,
		RunE: value(func(ctx context.Context, c *redfish.Client, sys string) (any, error) {
			return c.PowerRestorePolicy(ctx, sys)
		}),
	})
	prp.AddCommand(&cobra.Command{
		Use: "set POLICY", Short: "Set the policy: AlwaysOn, AlwaysOff or LastState (SetPowerRestorePolicy)", Args: cobra.ExactArgs(1),
		RunE: outcome(func(ctx context.Context, c *redfish.Client, sys string, a []string) (redfish.Outcome, error) {
			return c.SetPowerRestorePolicy(ctx, sys, a[0])
		}),
	})
	cmd.AddCommand(prp)

	// LED and asset tag.
	cmd.AddCommand(ledCmd(g, "system", func(cmd *cobra.Command) (*redfish.Client, string, error) {
		c, err := g.client(cmd, false)
		if err != nil {
			return nil, "", err
		}
		u, err := c.ResolveSystem(cmd.Context(), id)
		return c, u, err
	}))
	cmd.AddCommand(&cobra.Command{
		Use: "asset-tag TAG", Short: "Set the AssetTag of the system (redfishtool setAssetTag)", Args: cobra.ExactArgs(1),
		RunE: outcome(func(ctx context.Context, c *redfish.Client, sys string, a []string) (redfish.Outcome, error) {
			return c.SetAssetTag(ctx, sys, a[0])
		}),
	})

	// Inventories and health.
	inv := &cobra.Command{Use: "inventory", Short: "Inventories: system, cpu, memory, nic, psu", Args: cobra.NoArgs}
	for _, e := range []struct {
		use, short string
		fn         func(ctx context.Context, c *redfish.Client, sys string) (any, error)
	}{
		{"system", "The system's own properties (GetSystemInventory)", func(ctx context.Context, c *redfish.Client, s string) (any, error) { return c.SystemInventory(ctx, s) }},
		{"cpu", "The processors (GetCpuInventory)", func(ctx context.Context, c *redfish.Client, s string) (any, error) { return c.CPUInventory(ctx, s) }},
		{"memory", "The memory modules present (GetMemoryInventory)", func(ctx context.Context, c *redfish.Client, s string) (any, error) { return c.MemoryInventory(ctx, s) }},
		{"nic", "The network interfaces (GetNicInventory)", func(ctx context.Context, c *redfish.Client, s string) (any, error) { return c.NICInventory(ctx, s) }},
		{"psu", "The power supplies of the chassis (GetPsuInventory)", func(ctx context.Context, c *redfish.Client, _ string) (any, error) { return c.PSUInventory(ctx) }},
	} {
		inv.AddCommand(&cobra.Command{Use: e.use, Short: e.short, Args: cobra.NoArgs, RunE: value(e.fn)})
	}
	cmd.AddCommand(inv)
	cmd.AddCommand(&cobra.Command{
		Use: "health", Short: "The health of the system and its subsystems (GetHealthReport)", Args: cobra.NoArgs,
		RunE: value(func(ctx context.Context, c *redfish.Client, sys string) (any, error) {
			return c.SystemHealthReport(ctx, sys)
		}),
	})
	return cmd
}

// ledCmd is `led on|off|blink` for a system or a chassis; pick resolves the
// resource.
func ledCmd(g *globals, what string, pick func(*cobra.Command) (*redfish.Client, string, error)) *cobra.Command {
	cmd := &cobra.Command{Use: "led", Short: "The indicator LED of the " + what + ": on, off, blink", Args: cobra.NoArgs}
	for _, e := range []struct{ use, command string }{{"on", "IndicatorLedOn"}, {"off", "IndicatorLedOff"}, {"blink", "IndicatorLedBlink"}} {
		cmd.AddCommand(&cobra.Command{
			Use: e.use, Short: "Set IndicatorLED (" + e.command + ")", Args: cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				c, uri, err := pick(cmd)
				if err != nil {
					return err
				}
				o, err := c.SetIndicatorLED(cmd.Context(), uri, e.command)
				if err != nil {
					return err
				}
				return g.showOutcome(cmd, o)
			},
		})
	}
	return cmd
}
