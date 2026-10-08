package cmd

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/hivepaas/hivepaas-cli/internal/api"
	"github.com/hivepaas/hivepaas-cli/internal/client"
	"github.com/hivepaas/hivepaas-cli/internal/output"
	"github.com/hivepaas/hivepaas-cli/internal/resolve"
)

func (a *App) appsCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "app", Aliases: []string{"apps"}, Short: "An environment's apps"}
	cmd.AddCommand(&cobra.Command{
		Use:   "ls",
		Short: "List an environment's apps",
		Args:  usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			sel, err := a.selectTarget(cmd.Context(), c, scopeEnv)
			if err != nil {
				return err
			}
			apps, err := resolve.New(c).Apps(cmd.Context(), sel.Project.Id, sel.Env)
			if err != nil {
				return err
			}
			if a.printer.Structured() {
				return a.printer.Data(apps)
			}
			now := time.Now()
			rows := make([][]string, 0, len(apps))
			for _, app := range apps {
				name := app.Name
				if app.ParentApp != nil {
					name += a.printer.Dim(" (of " + app.ParentApp.Name + ")")
				}
				rows = append(rows, []string{name, app.Key, kindOf(app), string(app.Status),
					output.Ago(app.UpdatedAt, now)})
			}
			return a.printer.Table([]string{colName, colKey, colKind, colStatus, "UPDATED"}, rows)
		},
	}, &cobra.Command{
		Use:   "get [APP]",
		Short: "Show an app",
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
			resp, err := c.GetAppWithResponse(cmd.Context(), sel.Project.Id, sel.Env, sel.App.Id,
				&api.GetAppParams{GetStats: ptr(true)})
			if err = client.Check(resp, err); err != nil {
				return err
			}
			app := resp.JSON200.Data
			if a.printer.Structured() {
				return a.printer.Data(app)
			}
			fmt.Fprintf(a.stdout, "%s %s\n", app.Name, a.printer.Dim(app.Id))
			fmt.Fprintf(a.stdout, "Key:     %s\n", app.Key)
			fmt.Fprintf(a.stdout, "In:      %s / %s\n", sel.Project.Name, sel.Env)
			fmt.Fprintf(a.stdout, "Kind:    %s\n", kindOf(*app))
			fmt.Fprintf(a.stdout, "Status:  %s\n", app.Status)
			if links := resolve.Deref(app.AccessLinks); len(links) > 0 {
				fmt.Fprintf(a.stdout, "Links:   %s\n", strings.Join(links, ", "))
			}
			fmt.Fprintf(a.stdout, "Updated: %s\n", output.Ago(app.UpdatedAt, time.Now()))
			return nil
		},
	})
	cmd.AddCommand(a.appRunningCmd("stop", false), a.appRunningCmd("start", true))
	return cmd
}

// appRunningCmd stops an app, or starts it again: its service, scaled to none
// or back, with its settings and data kept.
func (a *App) appRunningCmd(verb string, running bool) *cobra.Command {
	short := map[bool]string{
		false: "Stop an app: it keeps its settings and data, and runs nothing until started",
		true:  "Start an app that was stopped",
	}[running]
	done := map[bool]string{false: "Stopped", true: "Started"}[running]
	return &cobra.Command{
		Use:   verb + " [APP]",
		Short: short,
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
			resp, err := c.AppActionSetRunningWithResponse(cmd.Context(), sel.Project.Id, sel.Env, sel.App.Id,
				api.AppactiondtoSetAppRunningReq{Running: running})
			if err = client.Check(resp, err); err != nil {
				return err
			}
			a.printer.Successf("%s %s.", done, sel.where())
			return nil
		},
	}
}

// kindOf is what an app is, as its list says it: its engine, else its category.
func kindOf(app api.AppdtoAppResp) string {
	if app.Engine != nil && *app.Engine != "" {
		return *app.Engine
	}
	if app.Category != nil && *app.Category != "" {
		return string(*app.Category)
	}
	return "-"
}

func ptr[T any](v T) *T { return &v }
