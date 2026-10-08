package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/hivepaas/hivepaas-cli/internal/api"
	"github.com/hivepaas/hivepaas-cli/internal/version"
)

func (a *App) versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "The CLI's version and API level, and the current installation's",
		Args:  usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			type cliInfo struct {
				Version  string `json:"version"`
				APILevel int    `json:"apiLevel"`
				SpecRef  string `json:"specRef"`
			}
			info := struct {
				CLI    cliInfo                       `json:"cli"`
				Server *api.SessiondtoServerInfoResp `json:"server,omitempty"`
			}{CLI: cliInfo{version.Version, api.APILevel, api.SpecRef}}
			// The installation's, when there is one to ask: version answers
			// without it.
			reached := ""
			if c, err := a.client(); err == nil {
				if me, meErr := getMe(cmd.Context(), c); meErr == nil {
					info.Server, reached = me.Server, c.Target.URL
				}
			}
			if a.printer.Structured() {
				return a.printer.Data(info)
			}
			fmt.Fprintf(a.stdout, "hivepaas %s (API level %d, from hivepaas %s)\n",
				version.Version, api.APILevel, api.SpecRef)
			switch {
			case info.Server != nil:
				fmt.Fprintf(a.stdout, "server: HivePaaS %s (API level %d)\n", info.Server.Version, info.Server.ApiLevel)
			case reached != "":
				fmt.Fprintf(a.stdout, "server: %s, a release from before API levels\n", reached)
			}
			return nil
		},
	}
}
