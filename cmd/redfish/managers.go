package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/go-redfish/redfish"
	"github.com/spf13/cobra"
)

// picker resolves the resource a command acts on: the client and the path.
type picker func(*cobra.Command) (*redfish.Client, string, error)

// propFlags are the flags that give a set of properties: --set NAME=VALUE
// (repeatable, a dot nests: SSH.Port=22) and --json (an object).
type propFlags struct {
	sets []string
	js   string
}

func (p *propFlags) add(cmd *cobra.Command) {
	cmd.Flags().StringArrayVar(&p.sets, "set", nil, "NAME=VALUE (repeatable; a dot nests: SSH.Port=2222; a number, true, false or null is sent as such)")
	cmd.Flags().StringVar(&p.js, "json", "", "a JSON object of properties (for the values --set cannot say)")
}

// props merges the --json object and the --set pairs (--set wins).
func (p *propFlags) props() (redfish.Props, error) {
	out := redfish.Props{}
	if p.js != "" {
		d := json.NewDecoder(bytes.NewReader([]byte(p.js)))
		d.UseNumber()
		if err := d.Decode(&out); err != nil || d.More() || out == nil {
			return nil, errors.New("--json: want one JSON object")
		}
	}
	pairs, err := parseAttrs(p.sets)
	if err != nil {
		return nil, errors.New(strings.Replace(err.Error(), "--attr", "--set", 1))
	}
	for k, v := range pairs {
		path := strings.Split(k, ".")
		m := out
		for _, seg := range path[:len(path)-1] {
			next, ok := m[seg].(map[string]any)
			if !ok {
				next = map[string]any{}
				m[seg] = next
			}
			m = next
		}
		m[path[len(path)-1]] = v
	}
	return out, nil
}

// pickerOf is a picker from a resolver of the client's.
func pickerOf(g *globals, resolve func(ctx context.Context, c *redfish.Client) (string, error)) picker {
	return func(cmd *cobra.Command) (*redfish.Client, string, error) {
		c, err := g.client(cmd, false)
		if err != nil {
			return nil, "", err
		}
		u, err := resolve(cmd.Context(), c)
		return c, u, err
	}
}

// outcomeRun is a command that changes the resource pick resolves.
func outcomeRun(g *globals, pick picker, fn func(ctx context.Context, c *redfish.Client, uri string, args []string) (redfish.Outcome, error)) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		c, u, err := pick(cmd)
		if err != nil {
			return err
		}
		o, err := fn(cmd.Context(), c, u, args)
		if err != nil {
			return err
		}
		return g.showOutcome(cmd, o)
	}
}

// valueRun is a command that reads the resource pick resolves.
func valueRun(g *globals, pick picker, fn func(ctx context.Context, c *redfish.Client, uri string, args []string) (any, error)) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		c, u, err := pick(cmd)
		if err != nil {
			return err
		}
		v, err := fn(cmd.Context(), c, u, args)
		if err != nil {
			return err
		}
		return g.show(cmd, v)
	}
}

// logsCmd is `logs` and `logs clear` of a system, a manager or a chassis.
func logsCmd(g *globals, what string, pick picker) *cobra.Command {
	var max int
	cmd := &cobra.Command{
		Use:   "logs",
		Short: "The log services of the " + what + ": the entries (GetLogs), and clear",
		Long: `logs shows the entries of every LogService of the ` + what + ` (the Manager
command GetLogs of redfish_info; the oracle reads a manager, redfishtool also a
system or a chassis). An entry the collection lists only by link is read.
logs clear runs the ClearLog action of each log service that has it (ClearLogs).`,
		Args: cobra.NoArgs,
		RunE: valueRun(g, pick, func(ctx context.Context, c *redfish.Client, u string, _ []string) (any, error) {
			return c.Logs(ctx, u, max)
		}),
	}
	cmd.Flags().IntVar(&max, "max", 0, "the most entries to read of a log (default 64, at most 1024)")
	cmd.AddCommand(&cobra.Command{
		Use: "clear", Short: "Clear the logs (ClearLogs)", Args: cobra.NoArgs,
		RunE: outcomeRun(g, pick, func(ctx context.Context, c *redfish.Client, u string, _ []string) (redfish.Outcome, error) {
			return c.ClearLogs(ctx, u)
		}),
	})
	return cmd
}

// virtualMediaCmd is `virtual-media get|insert|eject` of a system or a manager.
func virtualMediaCmd(g *globals, what string, pick picker) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "virtual-media",
		Short: "The virtual media of the " + what + ": get, insert, eject",
		Long: `virtual-media drives the VirtualMedia resources of the ` + what + ` (GetVirtualMedia,
VirtualMediaInsert and VirtualMediaEject of the Ansible modules). The service
decides which resource holds them: a system or a manager.`,
		Args: cobra.NoArgs,
	}
	cmd.AddCommand(&cobra.Command{
		Use: "get", Short: "List the virtual media (GetVirtualMedia)", Args: cobra.NoArgs,
		RunE: valueRun(g, pick, func(ctx context.Context, c *redfish.Client, u string, _ []string) (any, error) {
			return c.VirtualMedia(ctx, u)
		}),
	})
	var o redfish.VirtualMediaOptions
	var inserted, writeProtected bool
	var pwFile string
	ins := &cobra.Command{
		Use:   "insert --image URL",
		Short: "Insert an image in a free slot (VirtualMediaInsert)",
		Long: `insert puts the image in the first free slot that supports one of the
--media-type values (else the first free slot), with the InsertMedia action when
the slot has it, else by a PATCH. Nothing is done when the image is already
inserted. The password to fetch the image is read from --image-password-file
(mode 0600), never given on the command line.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			opt := o
			opt.Inserted = &inserted // the flags default to true, the module's defaults
			opt.WriteProtected = &writeProtected
			if pwFile != "" {
				_, pw, err := filePassword{path: pwFile}.Get(cmd.Context())
				if err != nil {
					return err
				}
				opt.Password = pw
			}
			return outcomeRun(g, pick, func(ctx context.Context, c *redfish.Client, u string, _ []string) (redfish.Outcome, error) {
				return c.VirtualMediaInsert(ctx, u, opt)
			})(cmd, args)
		},
	}
	f := ins.Flags()
	f.StringVar(&o.ImageURL, "image", "", "the URL of the image (required)")
	f.StringArrayVar(&o.MediaTypes, "media-type", nil, "a media type the slot must support: CD, DVD, Floppy, USBStick (repeatable)")
	f.BoolVar(&inserted, "inserted", true, "treat the image as inserted when the command completes")
	f.BoolVar(&writeProtected, "write-protected", true, "treat the media as write-protected")
	f.StringVar(&o.Username, "image-username", "", "the user name to fetch the image")
	f.StringVar(&pwFile, "image-password-file", "", "file holding the password to fetch the image (mode 0600)")
	f.StringVar(&o.TransferProtocolType, "transfer-protocol", "", "the protocol to fetch the image: HTTP, HTTPS, NFS...")
	f.StringVar(&o.TransferMethod, "transfer-method", "", "the transfer method: Stream, Upload...")
	_ = ins.MarkFlagRequired("image")
	cmd.AddCommand(ins)
	var image string
	eject := &cobra.Command{
		Use: "eject --image URL", Short: "Eject the image from the slot that holds it (VirtualMediaEject)", Args: cobra.NoArgs,
		RunE: outcomeRun(g, pick, func(ctx context.Context, c *redfish.Client, u string, _ []string) (redfish.Outcome, error) {
			return c.VirtualMediaEject(ctx, u, image)
		}),
	}
	eject.Flags().StringVar(&image, "image", "", "the URL of the image to eject (required)")
	_ = eject.MarkFlagRequired("image")
	cmd.AddCommand(eject)
	return cmd
}

func managersCmd(g *globals) *cobra.Command {
	var id string
	cmd := &cobra.Command{
		Use:   "managers",
		Short: "Managers (BMCs): reset, network protocols, NICs, date and time, logs, virtual media",
		Long: `managers drives the Manager resources of the service (the Manager commands of
the Ansible modules redfish_command, redfish_config and redfish_info).

--id names one manager (the last segment of its path). inventory, health and nic
get cover every manager when it is not given, as the Ansible modules do; the
other commands act on one manager: the only one, or the one --id names, else the
command fails and lists them (ErrAmbiguous).`,
		Args: cobra.NoArgs,
	}
	cmd.PersistentFlags().StringVar(&id, "id", "", "the manager to use")
	pick := pickerOf(g, func(ctx context.Context, c *redfish.Client) (string, error) { return c.ResolveManager(ctx, id) })
	value := func(fn func(ctx context.Context, c *redfish.Client, m string) (any, error)) func(*cobra.Command, []string) error {
		return valueRun(g, pick, func(ctx context.Context, c *redfish.Client, m string, _ []string) (any, error) { return fn(ctx, c, m) })
	}
	outcome := func(fn func(ctx context.Context, c *redfish.Client, m string, args []string) (redfish.Outcome, error)) func(*cobra.Command, []string) error {
		return outcomeRun(g, pick, fn)
	}
	// each reads the manager --id names, or all of them.
	each := func(fn func(ctx context.Context, c *redfish.Client, m string) (any, error)) func(*cobra.Command, []string) error {
		return func(cmd *cobra.Command, _ []string) error {
			c, err := g.client(cmd, false)
			if err != nil {
				return err
			}
			var uris []string
			if id != "" {
				u, err := c.ResolveManager(cmd.Context(), id)
				if err != nil {
					return err
				}
				uris = []string{u}
			}
			v, err := c.EachManager(cmd.Context(), uris, func(ctx context.Context, u string) (any, error) { return fn(ctx, c, u) })
			if err != nil {
				return err
			}
			return g.show(cmd, v)
		}
	}

	cmd.AddCommand(&cobra.Command{
		Use: "list", Short: "List the managers (their paths)", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := g.client(cmd, false)
			if err != nil {
				return err
			}
			u, err := c.ManagerURIs(cmd.Context())
			if err != nil {
				return err
			}
			return g.show(cmd, u)
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use: "inventory", Short: "Identity and firmware of the managers (GetManagerInventory)", Args: cobra.NoArgs,
		RunE: each(func(ctx context.Context, c *redfish.Client, m string) (any, error) { return c.ManagerInventory(ctx, m) }),
	})
	cmd.AddCommand(&cobra.Command{
		Use: "health", Short: "The health of the managers (GetHealthReport)", Args: cobra.NoArgs,
		RunE: each(func(ctx context.Context, c *redfish.Client, m string) (any, error) {
			return c.ManagerHealthReport(ctx, m)
		}),
	})

	// Power and reset.
	var wait bool
	var waitDelay, waitTimeout time.Duration
	power := &cobra.Command{
		Use:   "power COMMAND",
		Short: "Power: GracefulRestart, PowerOn, PowerForceOff, PowerForceRestart, PowerGracefulRestart, PowerGracefulShutdown, PowerReboot",
		Long: `power runs the Reset action of the manager with the ResetType the service allows
(its ActionInfo or AllowableValues); GracefulRestart is PowerGracefulRestart.
Nothing is sent when the manager already has the PowerState asked. With --wait
the command blocks until the service answers again: it sleeps --wait-delay first
(a service answers while it shuts down), then asks every 5 seconds until
--wait-timeout.`,
		Args: cobra.ExactArgs(1),
		RunE: outcome(func(ctx context.Context, c *redfish.Client, m string, a []string) (redfish.Outcome, error) {
			o, err := c.ManagerPower(ctx, m, a[0])
			if err == nil && wait && o.Changed {
				err = c.WaitAvailable(ctx, waitDelay, 5*time.Second, waitTimeout)
			}
			return o, err
		}),
	}
	power.Flags().BoolVar(&wait, "wait", false, "block until the service is available again")
	power.Flags().DurationVar(&waitDelay, "wait-delay", 30*time.Second, "with --wait, how long to wait before the first check")
	power.Flags().DurationVar(&waitTimeout, "wait-timeout", 120*time.Second, "with --wait, how long to wait for the service")
	cmd.AddCommand(power)
	cmd.AddCommand(&cobra.Command{
		Use:   "reset-to-defaults MODE",
		Short: "Reset the manager to its factory defaults: ResetAll, PreserveNetworkAndUsers, PreserveNetwork (ResetToDefaults)",
		Long: `reset-to-defaults runs the ResetToDefaults action of the manager. MODE is
ResetAll, PreserveNetworkAndUsers or PreserveNetwork, and one the service allows.
This erases the manager's configuration.`,
		Args: cobra.ExactArgs(1),
		RunE: outcome(func(ctx context.Context, c *redfish.Client, m string, a []string) (redfish.Outcome, error) {
			return c.ManagerResetToDefaults(ctx, m, a[0])
		}),
	})

	// Network protocols and NTP.
	np := &cobra.Command{Use: "network-protocols", Short: "The network services of the manager: get, set", Args: cobra.NoArgs}
	np.AddCommand(&cobra.Command{
		Use: "get", Short: "Show SNMP, SSH, HTTPS, NTP... (GetNetworkProtocols)", Args: cobra.NoArgs,
		RunE: value(func(ctx context.Context, c *redfish.Client, m string) (any, error) { return c.NetworkProtocols(ctx, m) }),
	})
	var npf propFlags
	npSet := &cobra.Command{
		Use:   "set --set SERVICE.PROPERTY=VALUE...",
		Short: "Change services, for example SSH.ProtocolEnabled=off SSH.Port=2222 (SetNetworkProtocols)",
		Long: `set changes the properties of the services of the manager's NetworkProtocol
resource: SNMP, VirtualMedia, Telnet, SSDP, IPMI, SSH, KVMIP, NTP, HTTP, HTTPS,
DHCP, DHCPv6, RDP, RFB. ProtocolEnabled takes true, false, on, off, 1 or 0; Port
a number. A service the resource does not have is refused.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := npf.props()
			if err != nil {
				return err
			}
			if len(p) == 0 {
				return errors.New("--set or --json is required")
			}
			return outcome(func(ctx context.Context, c *redfish.Client, m string, _ []string) (redfish.Outcome, error) {
				return c.SetNetworkProtocols(ctx, m, p)
			})(cmd, args)
		},
	}
	npf.add(npSet)
	np.AddCommand(npSet)
	cmd.AddCommand(np)

	ntp := &cobra.Command{Use: "ntp", Short: "The NTP service of the manager: set", Args: cobra.NoArgs}
	var servers []string
	var ntpOn, ntpOff bool
	ntpSet := &cobra.Command{
		Use: "set", Short: "Set the NTP servers and whether NTP is used", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if ntpOn && ntpOff {
				return errors.New("--enable and --disable are exclusive")
			}
			var en *bool
			if ntpOn || ntpOff {
				en = &ntpOn
			}
			return outcome(func(ctx context.Context, c *redfish.Client, m string, _ []string) (redfish.Outcome, error) {
				return c.SetNTP(ctx, m, servers, en)
			})(cmd, args)
		},
	}
	ntpSet.Flags().StringArrayVar(&servers, "server", nil, "an NTP server (repeatable; replaces the list)")
	ntpSet.Flags().BoolVar(&ntpOn, "enable", false, "use NTP")
	ntpSet.Flags().BoolVar(&ntpOff, "disable", false, "do not use NTP")
	ntp.AddCommand(ntpSet)
	cmd.AddCommand(ntp)

	// Date and time.
	dt := &cobra.Command{Use: "datetime", Short: "The date, time and UTC offset of the manager: get, set", Args: cobra.NoArgs}
	dt.AddCommand(&cobra.Command{
		Use: "get", Short: "Show DateTime and DateTimeLocalOffset", Args: cobra.NoArgs,
		RunE: value(func(ctx context.Context, c *redfish.Client, m string) (any, error) { return c.ManagerDateTime(ctx, m) }),
	})
	var when, offset string
	dtSet := &cobra.Command{
		Use: "set", Short: "Set the date and time (RFC 3339) and/or the offset (+hh:mm)", Args: cobra.NoArgs,
		RunE: outcome(func(ctx context.Context, c *redfish.Client, m string, _ []string) (redfish.Outcome, error) {
			return c.SetManagerDateTime(ctx, m, when, offset)
		}),
	}
	dtSet.Flags().StringVar(&when, "time", "", "the date and time, RFC 3339: 2026-11-05T17:00:00+01:00")
	dtSet.Flags().StringVar(&offset, "offset", "", "the offset from UTC: +01:00")
	dt.AddCommand(dtSet)
	cmd.AddCommand(dt)

	// Manager NIC.
	nic := &cobra.Command{Use: "nic", Short: "The manager's network interfaces: get, set", Args: cobra.NoArgs}
	nic.AddCommand(&cobra.Command{
		Use: "get", Short: "List the EthernetInterfaces of the managers (GetManagerNicInventory)", Args: cobra.NoArgs,
		RunE: each(func(ctx context.Context, c *redfish.Client, m string) (any, error) {
			return c.ManagerNICInventory(ctx, m)
		}),
	})
	var nicAddr string
	var nicf propFlags
	nicSet := &cobra.Command{
		Use:   "set --set PROPERTY=VALUE...",
		Short: "Change the interface that holds an address (SetManagerNic)",
		Long: `set changes the manager's EthernetInterface whose properties contain --address
(a MAC or IP address; default the host of --endpoint). A property that is an
array in the interface and an object in --json becomes an array of that one
object: --json '{"IPv4StaticAddresses":{"Address":"192.0.2.9","SubnetMask":"255.255.255.0","Gateway":"192.0.2.1"}}'.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := nicf.props()
			if err != nil {
				return err
			}
			if len(p) == 0 {
				return errors.New("--set or --json is required")
			}
			return outcome(func(ctx context.Context, c *redfish.Client, m string, _ []string) (redfish.Outcome, error) {
				return c.SetManagerNIC(ctx, m, nicAddr, p)
			})(cmd, args)
		},
	}
	nicSet.Flags().StringVar(&nicAddr, "address", "", "an address the interface holds (default: the host of --endpoint)")
	nicf.add(nicSet)
	nic.AddCommand(nicSet)
	cmd.AddCommand(nic)

	// Host interfaces.
	hi := &cobra.Command{Use: "host-interface", Short: "The host interfaces of the manager: get, set", Args: cobra.NoArgs}
	hi.AddCommand(&cobra.Command{
		Use: "get", Short: "List the host interfaces and their NICs (GetHostInterfaces)", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := g.client(cmd, false)
			if err != nil {
				return err
			}
			var uris []string
			if id != "" {
				u, err := c.ResolveManager(cmd.Context(), id)
				if err != nil {
					return err
				}
				uris = []string{u}
			}
			v, err := c.HostInterfaces(cmd.Context(), uris...)
			if err != nil {
				return err
			}
			return g.show(cmd, v)
		},
	})
	var hiID string
	var hif propFlags
	hiSet := &cobra.Command{
		Use: "set --set PROPERTY=VALUE...", Short: "Change a host interface, for example InterfaceEnabled=false (SetHostInterface)",
		Long: `set changes the HostInterface named --interface (its Id), or the only one.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := hif.props()
			if err != nil {
				return err
			}
			return outcome(func(ctx context.Context, c *redfish.Client, m string, _ []string) (redfish.Outcome, error) {
				return c.SetHostInterface(ctx, m, hiID, p)
			})(cmd, args)
		},
	}
	hiSet.Flags().StringVar(&hiID, "interface", "", "the Id of the host interface (default: the only one)")
	hif.add(hiSet)
	hi.AddCommand(hiSet)
	cmd.AddCommand(hi)

	// Service identification.
	sid := &cobra.Command{Use: "service-id", Short: "The ServiceIdentification of the manager: get, set", Args: cobra.NoArgs}
	sid.AddCommand(&cobra.Command{
		Use: "get", Short: "Show the ServiceIdentification (GetServiceIdentification)", Args: cobra.NoArgs,
		Long: `get shows the ServiceIdentification of the manager --id names; without --id, of the
only manager, or the only one that has one.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := g.client(cmd, false)
			if err != nil {
				return err
			}
			u, err := c.ResolveServiceIdentification(cmd.Context(), id)
			if err != nil {
				return err
			}
			s, err := c.ServiceIdentification(cmd.Context(), u)
			if err != nil {
				return err
			}
			return g.show(cmd, map[string]any{"manager_uri": u, "service_identification": s})
		},
	})
	sid.AddCommand(&cobra.Command{
		Use: "set ID", Short: "Set the ServiceIdentification (SetServiceIdentification)", Args: cobra.ExactArgs(1),
		RunE: outcome(func(ctx context.Context, c *redfish.Client, m string, a []string) (redfish.Outcome, error) {
			return c.SetServiceIdentification(ctx, m, a[0])
		}),
	})
	cmd.AddCommand(sid)

	cmd.AddCommand(logsCmd(g, "manager", pick))
	cmd.AddCommand(virtualMediaCmd(g, "manager", pick))
	return cmd
}
