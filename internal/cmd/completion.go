package cmd

import (
	"context"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/hivepaas/hivepaas-cli/internal/api"
	"github.com/hivepaas/hivepaas-cli/internal/client"
	"github.com/hivepaas/hivepaas-cli/internal/resolve"
)

// completionTimeout is how long Tab waits for the server: a shell that hangs on
// Tab is worse than one that offers nothing.
const completionTimeout = 3 * time.Second

// completions names what Tab offers, read from the installation: projects and
// apps by key, with their names beside them, environments, templates and
// deployments. It asks the same target the command would act on - the flags,
// the environment variables, the directory's link - and never asks the person:
// a failure offers nothing.
func (a *App) registerCompletions(root *cobra.Command) {
	_ = root.RegisterFlagCompletionFunc("project", a.complete(a.projectChoices))
	_ = root.RegisterFlagCompletionFunc("env", a.complete(a.envChoices))
	_ = root.RegisterFlagCompletionFunc("app", a.complete(a.appChoices))
	_ = root.RegisterFlagCompletionFunc("output", cobra.FixedCompletions(
		[]cobra.Completion{"table", "json", "yaml"}, cobra.ShellCompDirectiveNoFileComp))

	byPath := map[string]func(context.Context, *client.Client) ([]cobra.Completion, error){
		"project get":     a.projectChoices,
		"app get":         a.appChoices,
		"app stop":        a.appChoices,
		"app start":       a.appChoices,
		"restart":         a.appChoices,
		"open":            a.appChoices,
		"exec":            a.appChoices,
		"template deploy": a.templateChoices,
		"deploy get":      a.deploymentChoices,
		"deploy cancel":   a.deploymentChoices,
	}
	walk(root, func(cmd *cobra.Command) {
		path := strings.TrimPrefix(cmd.CommandPath(), root.Name()+" ")
		if choices, found := byPath[path]; found {
			cmd.ValidArgsFunction = a.completeFirstArg(a.complete(choices))
		}
	})
}

func walk(cmd *cobra.Command, visit func(*cobra.Command)) {
	visit(cmd)
	for _, child := range cmd.Commands() {
		walk(child, visit)
	}
}

// completeFirstArg completes a command's argument, which they take one of.
func (a *App) completeFirstArg(f cobra.CompletionFunc) cobra.CompletionFunc {
	return func(cmd *cobra.Command, args []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
		if len(args) > 0 {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		return f(cmd, args, toComplete)
	}
}

// complete is a completion function reading choices from the installation.
func (a *App) complete(choices func(context.Context, *client.Client) ([]cobra.Completion, error)) cobra.CompletionFunc {
	return func(cmd *cobra.Command, _ []string, _ string) ([]cobra.Completion, cobra.ShellCompDirective) {
		none := cobra.ShellCompDirectiveNoFileComp
		a.completing = true
		if a.cfg == nil {
			if err := a.init(); err != nil {
				return nil, none
			}
		}
		c, err := a.client()
		if err != nil {
			return nil, none
		}
		ctx, cancel := context.WithTimeout(cmd.Context(), completionTimeout)
		defer cancel()
		list, err := choices(ctx, c)
		if err != nil {
			return nil, none
		}
		return list, none
	}
}

func (a *App) projectChoices(ctx context.Context, c *client.Client) ([]cobra.Completion, error) {
	projects, err := resolve.New(c).Projects(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]cobra.Completion, 0, len(projects))
	for _, p := range projects {
		out = append(out, cobra.CompletionWithDesc(p.Key, p.Name))
	}
	return out, nil
}

func (a *App) envChoices(ctx context.Context, c *client.Client) ([]cobra.Completion, error) {
	project, err := a.completionProject(ctx, c)
	if err != nil {
		return nil, err
	}
	envs := resolve.Deref(project.Envs)
	out := make([]cobra.Completion, 0, len(envs))
	for _, env := range envs {
		out = append(out, env.Name)
	}
	return out, nil
}

func (a *App) appChoices(ctx context.Context, c *client.Client) ([]cobra.Completion, error) {
	project, env, err := a.completionEnv(ctx, c)
	if err != nil {
		return nil, err
	}
	apps, err := resolve.New(c).Apps(ctx, project.Id, env)
	if err != nil {
		return nil, err
	}
	out := make([]cobra.Completion, 0, len(apps))
	for _, app := range apps {
		out = append(out, cobra.CompletionWithDesc(app.Key, app.Name))
	}
	return out, nil
}

func (a *App) templateChoices(ctx context.Context, c *client.Client) ([]cobra.Completion, error) {
	resp, err := c.ListAppTemplatesWithResponse(ctx, &api.ListAppTemplatesParams{PageLimit: ptr(templatesPage)})
	if err = client.Check(resp, err); err != nil {
		return nil, err
	}
	templates := resolve.Deref(resp.JSON200.Data)
	out := make([]cobra.Completion, 0, len(templates))
	for _, t := range templates {
		out = append(out, cobra.CompletionWithDesc(t.Name, t.Title))
	}
	return out, nil
}

func (a *App) deploymentChoices(ctx context.Context, c *client.Client) ([]cobra.Completion, error) {
	sel, err := a.selectTarget(ctx, c, scopeApp)
	if err != nil {
		return nil, err
	}
	resp, err := c.ListAppDeploymentWithResponse(ctx, sel.Project.Id, sel.Env, sel.App.Id,
		&api.ListAppDeploymentParams{PageLimit: ptr(deploymentsShown)})
	if err = client.Check(resp, err); err != nil {
		return nil, err
	}
	deployments := resolve.Deref(resp.JSON200.Data)
	out := make([]cobra.Completion, 0, len(deployments))
	for _, d := range deployments {
		out = append(out, cobra.CompletionWithDesc(d.Id, string(d.Status)+", "+d.CreatedAt))
	}
	return out, nil
}

// completionProject is the project the command line names so far.
func (a *App) completionProject(ctx context.Context, c *client.Client) (*api.ProjectdtoProjectResp, error) {
	sel, err := a.selectTarget(ctx, c, scopeProject)
	if err != nil {
		return nil, err
	}
	return sel.Project, nil
}

// completionEnv is the project and environment the command line names so far:
// a project's only environment needs no naming.
func (a *App) completionEnv(ctx context.Context, c *client.Client) (*api.ProjectdtoProjectResp, string, error) {
	sel, err := a.selectTarget(ctx, c, scopeEnv)
	if err != nil {
		return nil, "", err
	}
	return sel.Project, sel.Env, nil
}
