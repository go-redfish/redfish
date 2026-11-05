package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/go-redfish/redfish"
	"github.com/spf13/cobra"
)

// showMonitors prints the task monitors an outcome carries on stderr.
func showMonitors(cmd *cobra.Command, o redfish.VolumeResult) {
	for _, m := range o.Monitors {
		fmt.Fprintf(cmd.ErrOrStderr(), "task: accepted; monitor: %s\n", clean(m))
	}
}

func storageCmd(g *globals) *cobra.Command {
	var id string
	cmd := &cobra.Command{
		Use:   "storage",
		Short: "Storage: controllers, disks, volumes, create and delete a volume",
		Long: `storage drives the Storage of a system (GetStorageControllerInventory,
GetDiskInventory and GetVolumeInventory of redfish_info; CreateVolume and
DeleteVolumes of redfish_config): controllers, disks, volumes, volume create,
volume delete. With several systems, --id names the one to use (as for systems).

disks reads Storage, and the older SimpleStorage when the system has no Storage.
A volume is created or deleted in a Storage subsystem named by --storage (the
last segment of its path, as listed by disks). Creating and deleting change the
service; deleting destroys data (see volume delete).`,
		Args: cobra.NoArgs,
	}
	cmd.PersistentFlags().StringVar(&id, "id", "", "the system to use (default: the only one)")
	read := func(use, short string, fn func(ctx context.Context, c *redfish.Client, sys string) ([]redfish.Props, error)) *cobra.Command {
		return &cobra.Command{Use: use, Short: short, Args: cobra.NoArgs,
			RunE: sysAction(g, &id, func(ctx context.Context, cmd *cobra.Command, c *redfish.Client, sys string, _ []string) error {
				v, err := fn(ctx, c, sys)
				if err != nil {
					return err
				}
				return g.show(cmd, v)
			})}
	}
	cmd.AddCommand(
		read("controllers", "The storage controllers (GetStorageControllerInventory)", func(ctx context.Context, c *redfish.Client, sys string) ([]redfish.Props, error) {
			return c.StorageControllerInventory(ctx, sys)
		}),
		read("disks", "The drives of each controller (GetDiskInventory)", func(ctx context.Context, c *redfish.Client, sys string) ([]redfish.Props, error) {
			return c.DiskInventory(ctx, sys)
		}),
		read("volumes", "The volumes of each controller (GetVolumeInventory)", func(ctx context.Context, c *redfish.Client, sys string) ([]redfish.Props, error) {
			return c.VolumeInventory(ctx, sys)
		}),
		storageVolumeCmd(g, &id),
	)
	return cmd
}

func storageVolumeCmd(g *globals, id *string) *cobra.Command {
	cmd := &cobra.Command{Use: "volume", Short: "Create or delete a volume", Args: cobra.NoArgs}
	cmd.AddCommand(volumeCreateCmd(g, id), volumeDeleteCmd(g, id))
	return cmd
}

func volumeCreateCmd(g *globals, id *string) *cobra.Command {
	var storage, raid, name string
	var capacity int64
	var drives, attrs []string
	var delNone bool
	cmd := &cobra.Command{
		Use:   "create --storage ID --raid-type TYPE --drive PATH...",
		Short: "Create a volume (CreateVolume)",
		Long: `create posts a Volume to the Volumes collection of the Storage subsystem
--storage. --raid-type and at least one --drive (the path of a drive, as disks
prints it in RedfishURI) are required. The RAID type must be one the controller
lists in SupportedRAIDTypes and the drives must belong to the subsystem: a
request that breaks either is refused before anything is sent. --attr NAME=VALUE
adds other properties of the Volume (as systems patch does).

--delete-none-volumes first deletes the volumes of RAIDType None of the
subsystem (storage_none_volume_deletion); they are printed on stderr before the
first deletion. That destroys data: it is never implied.

A service that answers 202 starts a task: its monitor is printed on stderr.`,
		Args: cobra.NoArgs,
		RunE: sysAction(g, id, func(ctx context.Context, cmd *cobra.Command, c *redfish.Client, sys string, _ []string) error {
			details, err := parseAttrs(attrs)
			if err != nil {
				return err
			}
			details["RAIDType"] = raid
			if name != "" {
				details["Name"] = name
			}
			if cmd.Flags().Changed("capacity-bytes") {
				details["CapacityBytes"] = capacity
			}
			o, err := c.CreateVolume(ctx, sys, storage, redfish.VolumeRequest{Details: details, Drives: drives, DeleteNoneVolumes: delNone,
				OnDelete: func(u []string) {
					fmt.Fprintf(cmd.ErrOrStderr(), "deleting the volumes of RAIDType None first: %s\n", clean(strings.Join(u, ", ")))
				}})
			if err != nil {
				return err
			}
			showMonitors(cmd, o)
			o.Monitor = ""
			return g.showOutcome(cmd, o.Outcome)
		}),
	}
	f := cmd.Flags()
	f.StringVar(&storage, "storage", "", "the Storage subsystem: the last segment of its path (required)")
	f.StringVar(&raid, "raid-type", "", "the RAID type: RAID0, RAID1, RAID5... (required)")
	f.StringArrayVar(&drives, "drive", nil, "the path of a drive of the subsystem (repeatable, required)")
	f.StringVar(&name, "name", "", "the name of the volume")
	f.Int64Var(&capacity, "capacity-bytes", 0, "the size of the volume (default: the service decides)")
	f.StringArrayVar(&attrs, "attr", nil, "another property of the Volume, NAME=VALUE (repeatable)")
	f.BoolVar(&delNone, "delete-none-volumes", false, "first delete the volumes of RAIDType None of the subsystem (destroys data)")
	_ = cmd.MarkFlagRequired("storage")
	_ = cmd.MarkFlagRequired("raid-type")
	_ = cmd.MarkFlagRequired("drive")
	return cmd
}

func volumeDeleteCmd(g *globals, id *string) *cobra.Command {
	var storage string
	var dry bool
	cmd := &cobra.Command{
		Use:   "delete --storage ID VOLUME_ID...",
		Short: "Delete the named volumes (DeleteVolumes); destroys their data",
		Long: `delete removes the volumes named by their Id (the last segment of their path,
as volumes lists them) from the Storage subsystem --storage. Every volume must
be named: there is no way to delete all of them. The paths to delete are
printed on stderr before the first deletion, and if one name is not a volume of
the subsystem nothing is deleted. --dry-run prints them and deletes nothing.

Deleting a volume destroys the data it holds. A service that answers 202
starts a task: its monitors are printed on stderr.`,
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return errors.New("name the volumes to delete (their Ids): there is no delete-all")
			}
			return nil
		},
		RunE: sysAction(g, id, func(ctx context.Context, cmd *cobra.Command, c *redfish.Client, sys string, args []string) error {
			o, err := c.DeleteVolumes(ctx, sys, storage, args, redfish.DeleteVolumesOptions{DryRun: dry, OnPlan: func(u []string) {
				verb := "deleting"
				if dry {
					verb = "would delete"
				}
				for _, p := range u {
					fmt.Fprintf(cmd.ErrOrStderr(), "%s %s\n", verb, clean(p))
				}
			}})
			showMonitors(cmd, o)
			if err != nil {
				return err
			}
			o.Monitor = ""
			return g.showOutcome(cmd, o.Outcome)
		}),
	}
	cmd.Flags().StringVar(&storage, "storage", "", "the Storage subsystem: the last segment of its path (required)")
	cmd.Flags().BoolVar(&dry, "dry-run", false, "print the volumes that would be deleted and delete nothing")
	_ = cmd.MarkFlagRequired("storage")
	return cmd
}
