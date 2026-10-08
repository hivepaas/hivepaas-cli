package cmd

import (
	"github.com/spf13/cobra"

	"github.com/hivepaas/hivepaas-cli/internal/api"
	"github.com/hivepaas/hivepaas-cli/internal/client"
)

func (a *App) restartCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "restart [APP]",
		Short: "Restart an app",
		Args:  usageArgs(cobra.MaximumNArgs(1)),
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
			resp, err := c.AppActionRestartWithResponse(cmd.Context(), sel.Project.Id, sel.Env, sel.App.Id,
				api.AppactiondtoRestartAppReq{})
			if err = client.Check(resp, err); err != nil {
				return err
			}
			a.printer.Successf("Restarted %s.", sel.where())
			return nil
		},
	}
}
