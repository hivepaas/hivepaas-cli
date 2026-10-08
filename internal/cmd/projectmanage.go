package cmd

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/hivepaas/hivepaas-cli/internal/api"
	"github.com/hivepaas/hivepaas-cli/internal/carry"
	"github.com/hivepaas/hivepaas-cli/internal/client"
	"github.com/hivepaas/hivepaas-cli/internal/exitcode"
	"github.com/hivepaas/hivepaas-cli/internal/resolve"
)

// The envs a project starts with unless told, and their colors, as the
// dashboard's form starts one; an env added later is slate.
var (
	defaultEnvs  = []string{"development", "production"}
	envColors    = map[string]string{"development": "#a855f7", "production": "#84cc16"}
	addedEnvTint = "#64748b"
)

func envColor(name string) string {
	if color, found := envColors[name]; found {
		return color
	}
	return addedEnvTint
}

func (a *App) projectCreateCmd() *cobra.Command {
	var envs []string
	var note string
	cmd := &cobra.Command{
		Use:   "create NAME",
		Short: "Create a project, with its environments",
		Long: "Create a project. Its environments are those --envs names, development and production\n" +
			"unless given: lowercase letters, digits, - and _, starting with a letter.",
		Example: "  hivepaas project create shop\n  hivepaas project create shop --envs staging,production",
		Args:    usageArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			if len(envs) == 0 {
				envs = defaultEnvs
			}
			list := make([]api.ProjectdtoProjectEnvReq, 0, len(envs))
			for _, env := range envs {
				list = append(list, api.ProjectdtoProjectEnvReq{Name: env, Color: envColor(env)})
			}
			resp, err := c.CreateProjectWithResponse(cmd.Context(), api.ProjectdtoCreateProjectReq{
				Name: args[0], Note: note, Envs: &list, Tags: &[]string{}, Status: api.ProjectStatusActive,
			})
			if err = client.Check(resp, err); err != nil {
				return err
			}
			a.printer.Successf("Created project %s, with %s.", args[0], strings.Join(envs, ", "))
			if a.printer.Structured() && resp.JSON201 != nil {
				return a.printer.Data(resp.JSON201.Data)
			}
			a.printer.Infof("  Its first app:  hivepaas app create <name> -p %s -e %s", shellWord(args[0]),
				shellWord(envs[len(envs)-1]))
			return nil
		},
	}
	// Not --env, which names the environment, as for every command.
	cmd.Flags().StringSliceVar(&envs, "envs", nil, "the project's environments, with commas")
	cmd.Flags().StringVar(&note, "note", "", "a note on the project")
	return cmd
}

func (a *App) projectDeleteCmd() *cobra.Command {
	var removeStorage, yes bool
	cmd := &cobra.Command{
		Use:   "delete [PROJECT]",
		Short: "Delete a project, with its environments and their apps",
		Long: "Delete a project: its environments and all their apps. Their data on the volumes stays\n" +
			"unless --remove-storage.\n\nAt a terminal it asks for the project's name to be typed; elsewhere it " +
			"needs --yes.",
		Args: usageArgs(cobra.MaximumNArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 {
				a.project = args[0]
			}
			ctx := cmd.Context()
			c, err := a.clientWithTimeout(deleteTimeout)
			if err != nil {
				return err
			}
			sel, err := a.selectTarget(ctx, c, scopeProject)
			if err != nil {
				return err
			}
			what := fmt.Sprintf("project %s, with %s and their apps", sel.Project.Name, envNames(sel.Project.Envs))
			if what, err = a.confirmDelete(ctx, what, removeStorage, yes, sel.Project.Name, sel.Project.Key); err != nil {
				return err
			}
			a.printer.Infof("Deleting %s ...", sel.Project.Name)
			resp, err := c.DeleteProjectWithResponse(ctx, sel.Project.Id, &api.DeleteProjectParams{
				RemoveStorage: &removeStorage,
			})
			if err = client.Check(resp, err); err != nil {
				return err
			}
			a.printer.Successf("Deleted %s.", what)
			return nil
		},
	}
	cmd.Flags().BoolVar(&removeStorage, "remove-storage", false, "delete the data the apps keep on volumes too")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask: for a script")
	return cmd
}

// confirmDelete asks for a name to be typed before what is deleted, at a
// terminal; a script says --yes. It answers what is deleted, as said.
func (a *App) confirmDelete(ctx context.Context, what string, removeStorage, yes bool, names ...string) (
	string, error,
) {
	if removeStorage {
		what += ", and the data on the volumes"
	}
	if yes {
		return what, nil
	}
	if !a.interactive() {
		return "", exitcode.New(exitcode.Usage, "deleting %s: give --yes to delete without being asked", what)
	}
	a.printer.Warnf("this deletes %s", what)
	typed, err := a.prompt(ctx, fmt.Sprintf("Type %s to go on: ", names[0]), "--yes")
	if err != nil {
		return "", err
	}
	if !slices.Contains(names, typed) {
		a.printer.Infof("Nothing was deleted.")
		return "", exitcode.Reported(exitcode.Failure)
	}
	return what, nil
}

func (a *App) projectEnvCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "env", Short: "A project's environments: add one, remove one"}
	cmd.AddCommand(a.projectEnvAddCmd(), a.projectEnvRmCmd())
	return cmd
}

func (a *App) projectEnvAddCmd() *cobra.Command {
	var color string
	cmd := &cobra.Command{
		Use:     "add NAME",
		Short:   "Add an environment to a project",
		Example: "  hivepaas project env add staging -p shop",
		Args:    usageArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			c, err := a.client()
			if err != nil {
				return err
			}
			sel, err := a.selectTarget(ctx, c, scopeProject)
			if err != nil {
				return err
			}
			name := args[0]
			if color == "" {
				color = addedEnvTint
			}
			_, err = writeBack(ctx, projectReader(c, sel.Project.Id), carry.Project,
				func(req *api.ProjectdtoUpdateProjectReq) (string, error) {
					envs := resolve.Deref(req.Envs)
					for _, env := range envs {
						if env.Name == name {
							return "", exitcode.New(exitcode.Invalid, "%s has an environment %s already",
								sel.Project.Name, name)
						}
					}
					envs = append(envs, api.ProjectdtoProjectEnvReq{Name: name, Color: color})
					req.Envs = &envs
					return "", nil
				}, projectWriter(c, sel.Project.Id))
			if err != nil {
				return err
			}
			a.printer.Successf("Added environment %s to %s.", name, sel.Project.Name)
			return nil
		},
	}
	cmd.Flags().StringVar(&color, "color", "", "its color in the dashboard, such as #0ea5e9")
	return cmd
}

func (a *App) projectEnvRmCmd() *cobra.Command {
	var removeStorage, yes bool
	cmd := &cobra.Command{
		Use:   "rm NAME",
		Short: "Remove an environment from a project, with its apps",
		Long: "Remove an environment and all its apps. Their data on the volumes stays unless\n" +
			"--remove-storage.\n\nAt a terminal it asks for the environment's name to be typed; elsewhere it " +
			"needs --yes.",
		Args: usageArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			c, err := a.clientWithTimeout(deleteTimeout)
			if err != nil {
				return err
			}
			sel, err := a.selectTarget(ctx, c, scopeProject)
			if err != nil {
				return err
			}
			env, err := resolve.Env(sel.Project, args[0])
			if err != nil {
				return err
			}
			what := fmt.Sprintf("environment %s of %s, with its apps", env, sel.Project.Name)
			if what, err = a.confirmDelete(ctx, what, removeStorage, yes, env); err != nil {
				return err
			}
			a.printer.Infof("Deleting %s ...", env)
			resp, err := c.DeleteProjectEnvWithResponse(ctx, sel.Project.Id, env, &api.DeleteProjectEnvParams{
				RemoveStorage: &removeStorage,
			})
			if err = client.Check(resp, err); err != nil {
				return err
			}
			a.printer.Successf("Deleted %s.", what)
			return nil
		},
	}
	cmd.Flags().BoolVar(&removeStorage, "remove-storage", false, "delete the data the apps keep on volumes too")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask: for a script")
	return cmd
}

func projectReader(c *client.Client, projectID string) func(context.Context) (*api.ProjectdtoProjectResp, error) {
	return func(ctx context.Context) (*api.ProjectdtoProjectResp, error) {
		resp, err := c.GetProjectWithResponse(ctx, projectID, &api.GetProjectParams{})
		if err = client.Check(resp, err); err != nil {
			return nil, err
		}
		if resp.JSON200 == nil || resp.JSON200.Data == nil {
			return nil, exitcode.New(exitcode.Server, "the server answered no project")
		}
		return resp.JSON200.Data, nil
	}
}

func projectWriter(c *client.Client, projectID string) func(context.Context, *api.ProjectdtoUpdateProjectReq) error {
	return func(ctx context.Context, req *api.ProjectdtoUpdateProjectReq) error {
		resp, err := c.UpdateProjectWithResponse(ctx, projectID, *req)
		return client.Check(resp, err)
	}
}
