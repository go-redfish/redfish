package main

import (
	"context"

	"github.com/go-redfish/redfish"
	"github.com/spf13/cobra"
)

func chassisCmd(g *globals) *cobra.Command {
	var id string
	cmd := &cobra.Command{
		Use:   "chassis",
		Short: "Chassis: LED, inventory, power, thermals, sensors, health",
		Long: `chassis reads and drives the Chassis resources of the service (the Chassis
commands of the Ansible modules redfish_command and redfish_info).

--id names one chassis (the last segment of its path). The reading commands
(inventory, fans, power, thermals, psu, health, hpe) cover every chassis when it
is not given, as the Ansible modules do; the others (led, sensors,
power-subsystem, thermal-subsystem, metrics) act on one chassis: the only one,
or the one --id names, else the command fails and lists them (ErrAmbiguous).`,
		Args: cobra.NoArgs,
	}
	cmd.PersistentFlags().StringVar(&id, "id", "", "the chassis to use")

	// one resolves the chassis to act on.
	one := func(fn func(ctx context.Context, cmd *cobra.Command, c *redfish.Client, chassis string) (any, error)) func(*cobra.Command, []string) error {
		return func(cmd *cobra.Command, _ []string) error {
			c, err := g.client(cmd, false)
			if err != nil {
				return err
			}
			u, err := c.ResolveChassis(cmd.Context(), id)
			if err != nil {
				return err
			}
			v, err := fn(cmd.Context(), cmd, c, u)
			if err != nil {
				return err
			}
			return g.show(cmd, v)
		}
	}
	// many reads the chassis --id names, or all of them.
	many := func(fn func(ctx context.Context, c *redfish.Client, uris []string) (any, error)) func(*cobra.Command, []string) error {
		return func(cmd *cobra.Command, _ []string) error {
			c, err := g.client(cmd, false)
			if err != nil {
				return err
			}
			var uris []string
			if id != "" {
				u, err := c.ResolveChassis(cmd.Context(), id)
				if err != nil {
					return err
				}
				uris = []string{u}
			}
			v, err := fn(cmd.Context(), c, uris)
			if err != nil {
				return err
			}
			return g.show(cmd, v)
		}
	}
	add := func(use, short string, run func(*cobra.Command, []string) error) *cobra.Command {
		c := &cobra.Command{Use: use, Short: short, Args: cobra.NoArgs, RunE: run}
		cmd.AddCommand(c)
		return c
	}

	add("list", "List the chassis (their paths)", func(cmd *cobra.Command, _ []string) error {
		c, err := g.client(cmd, false)
		if err != nil {
			return err
		}
		u, err := c.ChassisURIs(cmd.Context())
		if err != nil {
			return err
		}
		return g.show(cmd, u)
	})
	cmd.AddCommand(ledCmd(g, "chassis", func(cmd *cobra.Command) (*redfish.Client, string, error) {
		c, err := g.client(cmd, false)
		if err != nil {
			return nil, "", err
		}
		u, err := c.ResolveChassis(cmd.Context(), id)
		return c, u, err
	}))

	cmd.AddCommand(logsCmd(g, "chassis", pickerOf(g, func(ctx context.Context, c *redfish.Client) (string, error) { return c.ResolveChassis(ctx, id) })))

	// The oracle's reads.
	add("inventory", "Identity of the chassis (GetChassisInventory)", many(func(ctx context.Context, c *redfish.Client, u []string) (any, error) {
		return c.ChassisInventory(ctx, u...)
	}))
	add("fans", "Fans of the Thermal resources (GetFanInventory)", many(func(ctx context.Context, c *redfish.Client, u []string) (any, error) {
		return c.FanInventory(ctx, u...)
	}))
	add("power", "PowerControl of the Power resources (GetChassisPower)", many(func(ctx context.Context, c *redfish.Client, u []string) (any, error) {
		return c.ChassisPower(ctx, u...)
	}))
	add("thermals", "Temperatures of the Thermal resources (GetChassisThermals)", many(func(ctx context.Context, c *redfish.Client, u []string) (any, error) {
		return c.ChassisThermals(ctx, u...)
	}))
	add("psu", "Power supplies present (GetPsuInventory)", many(func(ctx context.Context, c *redfish.Client, _ []string) (any, error) {
		return c.PSUInventory(ctx)
	}))
	add("health", "Health of the chassis, its power supplies, fans and PCIe devices (GetHealthReport)", many(func(ctx context.Context, c *redfish.Client, u []string) (any, error) {
		return c.ChassisHealthReports(ctx, u...)
	}))
	hpe := &cobra.Command{Use: "hpe", Short: "HPE settings: thermal-config, fan-percent-min", Args: cobra.NoArgs}
	hpe.AddCommand(&cobra.Command{Use: "thermal-config", Short: "Oem.Hpe.ThermalConfiguration (GetHPEThermalConfig)", Args: cobra.NoArgs,
		RunE: many(func(ctx context.Context, c *redfish.Client, u []string) (any, error) {
			return c.HPEThermalConfig(ctx, u...)
		})})
	hpe.AddCommand(&cobra.Command{Use: "fan-percent-min", Short: "Oem.Hpe.FanPercentMinimum (GetHPEFanPercentMin)", Args: cobra.NoArgs,
		RunE: many(func(ctx context.Context, c *redfish.Client, u []string) (any, error) {
			return c.HPEFanPercentMin(ctx, u...)
		})})
	cmd.AddCommand(hpe)

	// The newer resources.
	add("sensors", "The Sensor resources of the chassis (Sensors collection)", one(func(ctx context.Context, _ *cobra.Command, c *redfish.Client, u string) (any, error) {
		return c.ChassisSensors(ctx, u)
	}))
	add("power-subsystem", "PowerSubsystem and its power supplies", one(func(ctx context.Context, _ *cobra.Command, c *redfish.Client, u string) (any, error) {
		return c.ChassisPowerSubsystem(ctx, u)
	}))
	add("thermal-subsystem", "ThermalSubsystem, its fans and ThermalMetrics", one(func(ctx context.Context, _ *cobra.Command, c *redfish.Client, u string) (any, error) {
		return c.ChassisThermalSubsystem(ctx, u)
	}))
	add("metrics", "EnvironmentMetrics: power, energy, temperature, fan speeds", one(func(ctx context.Context, _ *cobra.Command, c *redfish.Client, u string) (any, error) {
		return c.ChassisEnvironmentMetrics(ctx, u)
	}))
	return cmd
}
