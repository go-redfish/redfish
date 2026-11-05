package main

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/go-redfish/redfish"
	"github.com/spf13/cobra"
)

// maxData bounds a request body read from a file or stdin.
const maxData = 1 << 20

func readData(cmd *cobra.Command, spec string) ([]byte, error) {
	switch {
	case spec == "":
		return nil, nil
	case spec == "-":
		return readBounded(cmd.InOrStdin())
	case strings.HasPrefix(spec, "@"):
		f, err := os.Open(spec[1:])
		if err != nil {
			return nil, err
		}
		defer f.Close()
		return readBounded(f)
	}
	if len(spec) > maxData {
		return nil, errors.New("--data is too large")
	}
	return []byte(spec), nil
}

func readBounded(r io.Reader) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, maxData+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxData {
		return nil, errors.New("the request body is larger than 1 MiB")
	}
	return b, nil
}

func rawCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "raw",
		Short: "Send any Redfish request: get, patch, post, delete",
		Long: `raw sends one request to a path of the service (/redfish/...) and prints the
body. A 202 answer names a task monitor: it is printed on stderr, and the body
(the Task) on stdout. A non-2xx answer is an error carrying what the service
said (its code, message and ExtendedInfo).`,
		Args: cobra.NoArgs,
	}
	var data, ifMatch string
	do := func(method string) func(*cobra.Command, []string) error {
		return func(cmd *cobra.Command, args []string) error {
			c, err := g.client(cmd, false)
			if err != nil {
				return err
			}
			body, err := readData(cmd, data)
			if err != nil {
				return err
			}
			resp, err := c.Do(cmd.Context(), redfish.Request{Method: method, Path: args[0], Body: body, IfMatch: ifMatch})
			if err != nil {
				return err
			}
			if method == http.MethodHead {
				fmt.Fprintf(cmd.OutOrStdout(), "status: %d\netag: %s\ncontent-type: %s\n", resp.Status, clean(resp.ETag()), clean(resp.Header.Get("Content-Type")))
				return nil
			}
			if tag := resp.ETag(); tag != "" {
				fmt.Fprintf(cmd.ErrOrStderr(), "etag: %s\n", clean(tag))
			}
			if resp.IsTask() {
				fmt.Fprintf(cmd.ErrOrStderr(), "task: %s accepted (HTTP %d); monitor: %s\n", method, resp.Status, clean(resp.Location()))
			} else if loc := resp.Location(); loc != "" && resp.Status == http.StatusCreated {
				fmt.Fprintf(cmd.ErrOrStderr(), "created: %s\n", clean(loc))
			}
			return g.printDoc(cmd.OutOrStdout(), resp.Body)
		}
	}
	mk := func(use, short, method string, nargs int, body, match bool) *cobra.Command {
		c := &cobra.Command{Use: use, Short: short, Args: cobra.ExactArgs(nargs), RunE: do(method)}
		if body {
			c.Flags().StringVar(&data, "data", "", "JSON body: the text itself, @file, or - for stdin")
		}
		if match {
			c.Flags().StringVar(&ifMatch, "if-match", "", "the ETag the resource must still have (412 otherwise)")
		}
		return c
	}
	cmd.AddCommand(
		mk("get PATH", "GET a resource", http.MethodGet, 1, false, false),
		mk("patch PATH", "PATCH properties of a resource", http.MethodPatch, 1, true, true),
		mk("post PATH", "POST: create a member or invoke an action", http.MethodPost, 1, true, false),
		mk("delete PATH", "DELETE a resource", http.MethodDelete, 1, false, true),
		mk("put PATH", "PUT: replace a resource", http.MethodPut, 1, true, true),
		mk("head PATH", "HEAD: the headers of a resource (status, ETag)", http.MethodHead, 1, false, false),
	)
	return cmd
}
