package cmd

import (
	"slices"

	"github.com/spf13/cobra"

	"github.com/hivepaas/hivepaas-cli/internal/exitcode"
)

func (a *App) contextCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "context",
		Short: "The installations the CLI is logged in to",
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "ls",
		Short: "List the contexts",
		Args:  usageArgs(cobra.NoArgs),
		RunE: func(_ *cobra.Command, _ []string) error {
			names := make([]string, 0, len(a.cfg.Contexts))
			for name := range a.cfg.Contexts {
				names = append(names, name)
			}
			slices.Sort(names)
			if a.printer.Structured() {
				type listed struct {
					Name    string `json:"name"`
					URL     string `json:"url"`
					KeyID   string `json:"keyId"`
					User    string `json:"user,omitempty"`
					Current bool   `json:"current"`
				}
				out := make([]listed, 0, len(names))
				for _, name := range names {
					ctx := a.cfg.Contexts[name]
					out = append(out, listed{name, ctx.URL, ctx.KeyID, ctx.User, name == a.cfg.Current})
				}
				return a.printer.Data(out)
			}
			rows := make([][]string, 0, len(names))
			for _, name := range names {
				ctx := a.cfg.Contexts[name]
				current := ""
				if name == a.cfg.Current {
					current = "*"
				}
				rows = append(rows, []string{current, name, ctx.URL, ctx.User})
			}
			return a.printer.Table([]string{"CURRENT", colName, "URL", "USER"}, rows)
		},
	}, &cobra.Command{
		Use:   "use NAME",
		Short: "Make a context the current one",
		Args:  usageArgs(cobra.ExactArgs(1)),
		RunE: func(_ *cobra.Command, args []string) error {
			if a.cfg.Contexts[args[0]] == nil {
				return exitcode.New(exitcode.NotFound, "there is no context %q: `hivepaas context ls` lists them", args[0])
			}
			a.cfg.Current = args[0]
			if err := a.cfg.Save(); err != nil {
				return err
			}
			a.printer.Successf("Now using %q (%s).", args[0], a.cfg.Contexts[args[0]].URL)
			return nil
		},
	}, &cobra.Command{
		Use:   "rm NAME",
		Short: "Forget a context and its key's secret",
		Args:  usageArgs(cobra.ExactArgs(1)),
		RunE: func(_ *cobra.Command, args []string) error {
			return a.removeContext(args[0])
		},
	})
	return cmd
}
