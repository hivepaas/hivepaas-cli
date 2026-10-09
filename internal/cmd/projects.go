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

func (a *App) projectsCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "project", Aliases: []string{"projects"}, Short: "Projects and their environments"}
	cmd.AddCommand(&cobra.Command{
		Use:   "ls",
		Short: "List the projects",
		Args:  usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			projects, err := resolve.New(c).Projects(cmd.Context())
			if err != nil {
				return err
			}
			if a.printer.Structured() {
				return a.printer.Data(projects)
			}
			rows := make([][]string, 0, len(projects))
			for _, p := range projects {
				rows = append(rows, []string{p.Name, p.Key, envNames(p.Envs), string(p.Status)})
			}
			return a.printer.Table([]string{colName, colKey, "ENVS", colStatus}, rows)
		},
	}, &cobra.Command{
		Use:   "get [PROJECT]",
		Short: "Show a project",
		Args:  usageArgs(cobra.MaximumNArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 {
				a.project = args[0]
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			sel, err := a.selectTarget(cmd.Context(), c, scopeProject)
			if err != nil {
				return err
			}
			resp, err := c.GetProjectWithResponse(cmd.Context(), sel.Project.Id, &api.GetProjectParams{})
			if err = client.Check(resp, err); err != nil {
				return err
			}
			project := resp.JSON200.Data
			if a.printer.Structured() {
				return a.printer.Data(project)
			}
			fmt.Fprintf(a.stdout, "%s %s\n", project.Name, a.printer.Dim(project.Id))
			fmt.Fprintf(a.stdout, "Key:     %s\n", project.Key)
			fmt.Fprintf(a.stdout, "Status:  %s\n", project.Status)
			fmt.Fprintf(a.stdout, "Envs:    %s\n", envNames(project.Envs))
			if project.Note != "" {
				fmt.Fprintf(a.stdout, "Note:    %s\n", project.Note)
			}
			fmt.Fprintf(a.stdout, "Updated: %s\n", output.Ago(project.UpdatedAt, time.Now()))
			return nil
		},
	})
	cmd.AddCommand(a.projectCreateCmd(), a.projectDeleteCmd(), a.projectEnvCmd())
	return cmd
}

// The heads of the columns several lists have.
const (
	colName = "NAME"
	// colKey is what -p and -a take, beside the name: a key does not change when
	// the name does.
	colKey     = "KEY"
	colKind    = "KIND"
	colType    = "TYPE"
	colStatus  = "STATUS"
	colUpdated = "UPDATED"
	colSize    = "SIZE"
)

func envNames(envs *[]api.ProjectdtoProjectEnvResp) string {
	list := resolve.Deref(envs)
	names := make([]string, 0, len(list))
	for _, env := range list {
		names = append(names, env.Name)
	}
	return strings.Join(names, ", ")
}
