package cmd

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/hivepaas/hivepaas-cli/internal/api"
	"github.com/hivepaas/hivepaas-cli/internal/client"
	"github.com/hivepaas/hivepaas-cli/internal/exitcode"
	"github.com/hivepaas/hivepaas-cli/internal/output"
	"github.com/hivepaas/hivepaas-cli/internal/resolve"
)

// deploymentsShown is how many deployments `deploy ls` shows unless asked.
const deploymentsShown = 20

func (a *App) deployLsCmd() *cobra.Command {
	var limit int
	var status string
	cmd := &cobra.Command{
		Use:   "ls",
		Short: "List an app's deployments, the newest first",
		Args:  usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			if limit < 1 {
				return exitcode.New(exitcode.Usage, "--limit takes a number of deployments, not %d", limit)
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			sel, err := a.selectTarget(cmd.Context(), c, scopeApp)
			if err != nil {
				return err
			}
			params := &api.ListAppDeploymentParams{PageLimit: &limit}
			if status != "" {
				params.Status = &status
			}
			resp, err := c.ListAppDeploymentWithResponse(cmd.Context(), sel.Project.Id, sel.Env, sel.App.Id, params)
			if err = client.Check(resp, err); err != nil {
				return err
			}
			deployments := resolve.Deref(resp.JSON200.Data)
			if a.printer.Structured() {
				return a.printer.Data(deployments)
			}
			now := time.Now()
			rows := make([][]string, 0, len(deployments))
			for _, d := range deployments {
				rows = append(rows, []string{d.Id, string(d.Status), triggerOf(d), output.Ago(d.CreatedAt, now),
					tookOrDash(&d), sourceOf(d)})
			}
			return a.printer.Table([]string{"ID", colStatus, "BY", "CREATED", "TOOK", "SOURCE"}, rows)
		},
	}
	cmd.Flags().IntVar(&limit, "limit", deploymentsShown, "how many to show")
	cmd.Flags().StringVar(&status, "status", "",
		"only these: not-started, in-progress, done, failed, canceled - several with commas")
	return cmd
}

func (a *App) deployGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "get DEPLOYMENT-ID",
		Short: "Show a deployment",
		Args:  usageArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			sel, err := a.selectTarget(cmd.Context(), c, scopeApp)
			if err != nil {
				return err
			}
			d, err := a.deployment(cmd.Context(), c, deploymentRef{sel: sel, id: args[0]})
			if err != nil {
				return err
			}
			if a.printer.Structured() {
				return a.printer.Data(d)
			}
			now := time.Now()
			fmt.Fprintf(a.stdout, "Deployment %s of %s\n", d.Id, sel.where())
			fmt.Fprintf(a.stdout, "Status:  %s\n", d.Status)
			fmt.Fprintf(a.stdout, "By:      %s\n", triggerOf(*d))
			if source := sourceOf(*d); source != "-" {
				fmt.Fprintf(a.stdout, "Source:  %s\n", source)
			}
			fmt.Fprintf(a.stdout, "Created: %s\n", output.Ago(d.CreatedAt, now))
			if d.StartedAt != nil {
				fmt.Fprintf(a.stdout, "Started: %s\n", output.Ago(*d.StartedAt, now))
			}
			if d.EndedAt != nil {
				fmt.Fprintf(a.stdout, "Took:    %s\n", took(d))
			}
			if d.Output != nil && d.Output.Error != nil && *d.Output.Error != "" {
				fmt.Fprintf(a.stdout, "Error:   %s\n", strings.ReplaceAll(*d.Output.Error, "\n", "\n         "))
			}
			a.printer.Infof("Its logs: hivepaas logs --deployment %s%s", d.Id, sel.flags())
			return nil
		},
	}
}

// triggerOf says what started a deployment: who, from where.
func triggerOf(d api.AppdeploymentdtoDeploymentResp) string {
	if d.Trigger == nil {
		return "-"
	}
	by := string(d.Trigger.Source)
	if d.Trigger.Source == api.DeploymentTriggerSourceRepoWebhook {
		by = "push"
	}
	if user := d.Trigger.SourceUser; user != nil && user.Username != "" {
		by += " (" + user.Username + ")"
	}
	return by
}

// sourceOf is what a deployment deployed: the image, or the commit.
func sourceOf(d api.AppdeploymentdtoDeploymentResp) string {
	if d.Output != nil && d.Output.CommitHashShort != nil && *d.Output.CommitHashShort != "" {
		source := *d.Output.CommitHashShort
		if d.Output.CommitTitle != nil && *d.Output.CommitTitle != "" {
			source += " " + truncate(*d.Output.CommitTitle, maxCommitTitle)
		}
		return source
	}
	if d.Settings != nil && d.Settings.ImageSource != nil && d.Settings.ImageSource.Image != "" {
		return d.Settings.ImageSource.Image
	}
	return "-"
}

// maxCommitTitle is how much of a commit's title a table shows.
const maxCommitTitle = 50

func truncate(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n-1]) + "…"
}

func tookOrDash(d *api.AppdeploymentdtoDeploymentResp) string {
	if d.EndedAt == nil {
		return "-"
	}
	return took(d)
}
