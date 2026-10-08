package cmd

import (
	"context"
	"fmt"
	"net/url"
	"os/exec"
	"runtime"

	"github.com/spf13/cobra"

	"github.com/hivepaas/hivepaas-cli/internal/api"
	"github.com/hivepaas/hivepaas-cli/internal/client"
	"github.com/hivepaas/hivepaas-cli/internal/exitcode"
	"github.com/hivepaas/hivepaas-cli/internal/resolve"
)

func (a *App) openCmd() *cobra.Command {
	var printOnly bool
	cmd := &cobra.Command{
		Use:   "open [APP]",
		Short: "Open an app's address in the browser",
		Long: "Open an app's address in the browser: the first of its addresses, its domain or a published\n" +
			"port. --print writes them instead, one a line, for a script or a machine with no browser.",
		Args: usageArgs(cobra.MaximumNArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 {
				a.app = args[0]
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			sel, err := a.selectTarget(cmd.Context(), c, scopeApp)
			if err != nil {
				return err
			}
			resp, err := c.GetAppWithResponse(cmd.Context(), sel.Project.Id, sel.Env, sel.App.Id, &api.GetAppParams{})
			if err = client.Check(resp, err); err != nil {
				return err
			}
			links := webLinks(resolve.Deref(resp.JSON200.Data.AccessLinks))
			if len(links) == 0 {
				return exitcode.New(exitcode.NotFound, "%s has no address to open: give it a domain, or publish a port",
					sel.where())
			}
			if printOnly || a.printer.Structured() {
				if a.printer.Structured() {
					return a.printer.Data(links)
				}
				for _, link := range links {
					fmt.Fprintln(a.stdout, link)
				}
				return nil
			}
			if err = openBrowser(cmd.Context(), links[0]); err != nil {
				return exitcode.Wrap(exitcode.Failure, fmt.Errorf("opening %s: %w; --print writes it instead", links[0], err))
			}
			a.printer.Infof("Opened %s", links[0])
			for _, other := range links[1:] {
				a.printer.Infof("  also: %s", other)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&printOnly, "print", false, "write the addresses instead of opening one")
	return cmd
}

// webLinks are the addresses a browser opens: http and https only, so that what
// the server answers is never handed to the OS to open as something else.
func webLinks(links []string) []string {
	var out []string
	for _, link := range links {
		if u, err := url.Parse(link); err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" {
			out = append(out, u.String())
		}
	}
	return out
}

// openBrowser opens a URL in the user's browser, and does not wait for it.
func openBrowser(ctx context.Context, link string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.CommandContext(ctx, "open", link)
	case "windows":
		cmd = exec.CommandContext(ctx, "rundll32", "url.dll,FileProtocolHandler", link)
	default:
		cmd = exec.CommandContext(ctx, "xdg-open", link)
	}
	return cmd.Start() //nolint:wrapcheck // the caller says what it opened
}
