package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/go-redfish/redfish"
	"github.com/spf13/cobra"
)

// watchFlags are the flags of a command that starts a task: without --watch the
// task monitor is returned, not awaited (redfishtool's -N/--NonBlocking is the
// default); with it the monitor is followed and the progress goes to stderr.
type watchFlags struct {
	watch   bool
	poll    time.Duration
	timeout time.Duration
}

func (w *watchFlags) add(cmd *cobra.Command) {
	f := cmd.Flags()
	f.BoolVar(&w.watch, "watch", false, "follow the task monitor until the task ends, with its progress on stderr (default: return the monitor)")
	f.DurationVar(&w.poll, "poll", 2*time.Second, "with --watch: the delay between two polls when the service gives no Retry-After")
	f.DurationVar(&w.timeout, "watch-timeout", time.Hour, "with --watch: give up after this long")
}

// follow waits for the task of st and replaces st with its final status.
func (w *watchFlags) follow(cmd *cobra.Command, c *redfish.Client, st *redfish.UpdateStatus) error {
	if !w.watch || st.Handle == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(cmd.Context(), w.timeout)
	defer cancel()
	errw := cmd.ErrOrStderr()
	final, err := c.WaitUpdate(ctx, st.Handle, w.poll, func(t redfish.Task) {
		pct := "?"
		if t.PercentComplete != nil {
			pct = fmt.Sprintf("%.0f%%", *t.PercentComplete)
		}
		fmt.Fprintf(errw, "task: %s %s\n", clean(t.State), pct)
	})
	if err != nil {
		return fmt.Errorf("following %s: %w", st.Handle, err)
	}
	*st = final
	return nil
}

// showUpdate prints an update that was requested.
func (g *globals) showUpdate(cmd *cobra.Command, c *redfish.Client, w *watchFlags, res redfish.UpdateResult) error {
	if res.Monitor != "" && !w.watch {
		fmt.Fprintf(cmd.ErrOrStderr(), "task: accepted; monitor: %s\n", clean(res.Monitor))
	}
	if err := w.follow(cmd, c, &res.Status); err != nil {
		return err
	}
	return g.show(cmd, map[string]any{"changed": res.Changed, "message": res.Message, "update_status": res.Status})
}

func updateCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "update",
		Short: "UpdateService: simple, push, status, operations, inventory, capabilities",
		Long: `update drives the UpdateService (the Update commands of redfish_command and
redfish_info): simple (SimpleUpdate: the BMC fetches an image), push
(MultipartHTTPPushUpdate: the image is uploaded, streamed from the file),
status (GetUpdateStatus), operations (PerformRequestedOperations), inventory
(GetFirmwareInventory, GetSoftwareInventory) and capabilities
(GetFirmwareUpdateCapabilities).

simple and push start a task: the task monitor is printed on stderr and in
update_status.handle and not awaited (redfishtool's -N); --watch follows it.
The password of the image server comes from --image-password-file (mode 0600)
or --image-password-stdin, never from a flag or the environment, and is never
printed.`,
		Args: cobra.NoArgs,
	}
	cmd.AddCommand(updateSimpleCmd(g), updatePushCmd(g), updateStatusCmd(g), updateOperationsCmd(g), updateInventoryCmd(g), updateCapabilitiesCmd(g))
	return cmd
}

func updateSimpleCmd(g *globals) *cobra.Command {
	var o redfish.SimpleUpdateOptions
	var pwFile string
	var pwStdin bool
	var w watchFlags
	cmd := &cobra.Command{
		Use:   "simple --image URI",
		Short: "Ask the BMC to fetch an image and update with it (SimpleUpdate)",
		Long: `simple posts the UpdateService.SimpleUpdate action. The transfer protocol and
the targets are checked against the service's allowable values, and the scheme
of the image URI must be one of its transfer protocols; a URI that holds a
password is refused (use --image-username and --image-password-file).`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			opt := o
			pw, err := imagePassword(cmd, pwFile, pwStdin)
			if err != nil {
				return err
			}
			opt.Password = pw
			c, err := g.client(cmd, false)
			if err != nil {
				return err
			}
			res, err := c.SimpleUpdate(cmd.Context(), opt)
			if err != nil {
				return err
			}
			return g.showUpdate(cmd, c, &w, res)
		},
	}
	f := cmd.Flags()
	f.StringVar(&o.ImageURI, "image", "", "the URI the BMC fetches the image from (required)")
	f.StringVar(&o.Protocol, "protocol", "", "the transfer protocol: HTTP, HTTPS, SCP, TFTP...")
	f.StringArrayVar(&o.Targets, "target", nil, "a resource to update (repeatable)")
	f.StringVar(&o.Username, "image-username", "", "the user name on the image server")
	f.StringVar(&pwFile, "image-password-file", "", "file holding the password on the image server (mode 0600)")
	f.BoolVar(&pwStdin, "image-password-stdin", false, "read the password on the image server from the standard input (one line)")
	f.StringVar(&o.ApplyTime, "apply-time", "", "when to apply: Immediate, OnReset...")
	w.add(cmd)
	_ = cmd.MarkFlagRequired("image")
	return cmd
}

// imagePassword reads the password of the image server (none given: empty).
func imagePassword(cmd *cobra.Command, file string, stdin bool) (redfish.Secret, error) {
	if file != "" && stdin {
		return "", errors.New("--image-password-file and --image-password-stdin are exclusive")
	}
	return (&newPassword{file: file, stdin: stdin}).secret(cmd, false)
}

func updatePushCmd(g *globals) *cobra.Command {
	var o redfish.PushOptions
	var oem string
	var w watchFlags
	cmd := &cobra.Command{
		Use:   "push --file IMAGE",
		Short: "Upload an image to the MultipartHttpPushUri (MultipartHTTPPushUpdate)",
		Long: `push sends the image as a multipart/form-data upload (an UpdateParameters part,
then the UpdateFile part). The file is streamed, never loaded whole in memory,
and must be a regular file of at most --max-size bytes (default 4 GiB): a larger
one is refused before anything is sent. --upload-timeout bounds the transfer.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			opt := o
			if oem != "" {
				d := json.NewDecoder(bytes.NewReader([]byte(oem)))
				d.UseNumber()
				var p redfish.Props
				if err := d.Decode(&p); err != nil || d.More() || p == nil {
					return errors.New("--oem: want one JSON object")
				}
				opt.OEMParams = p
			}
			c, err := g.client(cmd, false)
			if err != nil {
				return err
			}
			res, err := c.MultipartHTTPPushUpdate(cmd.Context(), opt)
			if err != nil {
				return err
			}
			return g.showUpdate(cmd, c, &w, res)
		},
	}
	f := cmd.Flags()
	f.StringVar(&o.ImageFile, "file", "", "the image to upload (required)")
	f.StringArrayVar(&o.Targets, "target", nil, "a resource to update (repeatable)")
	f.StringVar(&o.ApplyTime, "apply-time", "", "when to apply: Immediate, OnReset...")
	f.StringVar(&oem, "oem", "", "a JSON object: the Oem property of UpdateParameters")
	f.StringVar(&o.CustomHeader, "oem-header", "", "the name of an extra part of the upload")
	f.StringVar(&o.CustomMIMEType, "oem-mime", "", "the media type of the extra part (default text/plain)")
	f.StringVar(&o.CustomParams, "oem-params", "", "the content of the extra part")
	f.Int64Var(&o.MaxSize, "max-size", 0, "the largest image accepted, in bytes (default 4 GiB)")
	f.DurationVar(&o.Timeout, "upload-timeout", 0, "bound the whole upload (default 30m)")
	w.add(cmd)
	_ = cmd.MarkFlagRequired("file")
	return cmd
}

func updateStatusCmd(g *globals) *cobra.Command {
	var w watchFlags
	cmd := &cobra.Command{
		Use:   "status HANDLE",
		Short: "The status of an update: its task or job (GetUpdateStatus)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := g.client(cmd, false)
			if err != nil {
				return err
			}
			st, err := c.UpdateStatus(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if err := w.follow(cmd, c, &st); err != nil {
				return err
			}
			return g.show(cmd, st)
		},
	}
	w.add(cmd)
	return cmd
}

func updateOperationsCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "operations HANDLE",
		Short: "Do what the update asks to continue: its resets (PerformRequestedOperations)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := g.client(cmd, false)
			if err != nil {
				return err
			}
			o, err := c.PerformRequestedOperations(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			return g.showOutcome(cmd, o)
		},
	}
}

func updateInventoryCmd(g *globals) *cobra.Command {
	var software bool
	cmd := &cobra.Command{
		Use:   "inventory",
		Short: "The firmware inventory (GetFirmwareInventory), or with --software the software inventory (GetSoftwareInventory)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := g.client(cmd, false)
			if err != nil {
				return err
			}
			var items []redfish.Props
			if software {
				items, err = c.SoftwareInventory(cmd.Context())
			} else {
				items, err = c.FirmwareInventory(cmd.Context())
			}
			if err != nil {
				return err
			}
			return g.show(cmd, items)
		},
	}
	cmd.Flags().BoolVar(&software, "software", false, "the SoftwareInventory instead of the FirmwareInventory")
	return cmd
}

func updateCapabilitiesCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "capabilities",
		Short: "What the UpdateService can do: multipart push, actions and transfer protocols (GetFirmwareUpdateCapabilities)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := g.client(cmd, false)
			if err != nil {
				return err
			}
			caps, err := c.FirmwareUpdateCapabilities(cmd.Context())
			if err != nil {
				return err
			}
			return g.show(cmd, caps)
		},
	}
}
