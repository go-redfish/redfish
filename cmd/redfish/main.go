// redfish is a command-line client for the DMTF Redfish API of a BMC, built on
// github.com/go-redfish/redfish, and a mock BMC to try it against.
//
//	redfish --endpoint https://bmc.example --user admin --password-file ~/.bmc-pw raw get /redfish/v1/Systems
//	redfish mock serve
//
// Secrets: the password is read from a file (mode 0600), never from a flag or
// the environment. `session login` exchanges it once for a session token, kept
// in a 0600 file under the user's config directory; later commands use it with
// --session, and `session logout` ends it at the BMC and removes the file.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
)

// osExit allows tests to override os.Exit.
var osExit = os.Exit

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if code := execute(ctx, RootCmd(), os.Stderr); code != 0 {
		stop()
		osExit(code)
	}
}

// execute runs the command and prints its error once, on stderr (the root
// silences cobra's own print), returning the exit status.
func execute(ctx context.Context, root *cobra.Command, stderr io.Writer) int {
	if err := root.ExecuteContext(ctx); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

// RootCmd returns the top-level cobra command. Exported so tests in the same
// package can exercise the wiring without spawning a child process.
func RootCmd() *cobra.Command {
	g := &globals{}
	root := &cobra.Command{
		Use:   "redfish",
		Short: "A Redfish client for a server's BMC, and a mock BMC to test it",
		Long: `redfish talks to the Redfish service of a BMC (DMTF DSP0266).

Connection flags are global. The password comes from a file (--password-file,
mode 0600): there is no password flag and no environment variable. A session
token (redfish session login) is kept in a 0600 file under the user's config
directory and used with --session.`,
		SilenceUsage:  true,
		SilenceErrors: true, // main prints the error, once
		Args:          cobra.NoArgs,
	}
	f := root.PersistentFlags()
	f.StringVar(&g.endpoint, "endpoint", "", "the BMC's address, https://host[:port]")
	f.StringVar(&g.user, "user", "", "user name")
	f.StringVar(&g.passwordFile, "password-file", "", "file holding the password (mode 0600)")
	f.StringVar(&g.caFile, "ca-file", "", "PEM file of the CAs that sign the BMC's certificate (default: the system roots)")
	f.BoolVar(&g.insecure, "insecure", false, "do not verify the BMC's certificate (explicit; warns on stderr)")
	f.BoolVar(&g.session, "session", false, "use the session saved by 'redfish session login', not the password")
	f.DurationVar(&g.timeout, "timeout", 0, "per-request timeout (default 10s)")
	f.StringVarP(&g.output, "output", "o", "json", "output format: json or table")
	root.AddCommand(sessionCmd(g), rawCmd(g), systemsCmd(g), chassisCmd(g), mockCmd(g))
	return root
}
