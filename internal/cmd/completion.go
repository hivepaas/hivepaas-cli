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
		"project get":      a.projectChoices,
		"app get":          a.appChoices,
		"app stop":         a.appChoices,
		"app start":        a.appChoices,
		"restart":          a.appChoices,
		"open":             a.appChoices,
		"exec":             a.appChoices,
		"app scale":        a.appChoices,
		"domain rm":        a.domainChoices,
		"template deploy":  a.templateChoices,
		"deploy get":       a.deploymentChoices,
		"deploy cancel":    a.deploymentChoices,
		"ps":               a.appChoices,
		"job run":          a.jobChoices,
		"job enable":       a.jobChoices,
		"job disable":      a.jobChoices,
		"task get":         a.taskChoices,
		"task cancel":      a.taskChoices,
		"secret rm":        a.secretChoices,
		"config-file push": a.configFileChoices,
		"config-file pull": a.configFileChoices,
		"config-file rm":   a.configFileChoices,
	}
	walk(root, func(cmd *cobra.Command) {
		path := strings.TrimPrefix(cmd.CommandPath(), root.Name()+" ")
		if choices, found := byPath[path]; found {
			cmd.ValidArgsFunction = a.completeFirstArg(a.complete(choices))
		}
		// The commands that take a source's flags: deploy, app create.
		if cmd.Flags().Lookup("push-to") != nil {
			_ = cmd.RegisterFlagCompletionFunc("registry-auth", a.complete(a.registryAuthChoices))
			_ = cmd.RegisterFlagCompletionFunc("push-to", a.complete(a.registryAuthChoices))
			_ = cmd.RegisterFlagCompletionFunc("git-credential", a.complete(a.gitCredentialChoices))
			_ = cmd.RegisterFlagCompletionFunc("use", cobra.FixedCompletions(
				[]cobra.Completion{string(api.DeploymentMethodImage), codeRepo}, cobra.ShellCompDirectiveNoFileComp))
		}
	})
}

// noCredential is what Tab offers first for a credential: none.
var noCredential = cobra.CompletionWithDesc(none, "no credential")

func (a *App) registryAuthChoices(ctx context.Context, c *client.Client) ([]cobra.Completion, error) {
	project, env, err := a.completionEnv(ctx, c)
	if err != nil {
		return nil, err
	}
	auths, err := resolve.New(c).RegistryAuths(ctx, project.Id, env)
	if err != nil {
		return nil, err
	}
	out := []cobra.Completion{noCredential}
	for _, auth := range auths {
		out = append(out, cobra.CompletionWithDesc(auth.Name, string(auth.Kind)+", "+auth.Address))
	}
	return out, nil
}

func (a *App) gitCredentialChoices(ctx context.Context, c *client.Client) ([]cobra.Completion, error) {
	project, env, err := a.completionEnv(ctx, c)
	if err != nil {
		return nil, err
	}
	creds, err := resolve.New(c).GitCredentials(ctx, project.Id, env)
	if err != nil {
		return nil, err
	}
	out := []cobra.Completion{noCredential}
	for _, cred := range creds {
		out = append(out, cobra.CompletionWithDesc(cred.Name, cred.Kind))
	}
	return out, nil
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

func (a *App) domainChoices(ctx context.Context, c *client.Client) ([]cobra.Completion, error) {
	sel, err := a.selectTarget(ctx, c, scopeApp)
	if err != nil {
		return nil, err
	}
	settings, err := routingSettings(ctx, c, sel)
	if err != nil {
		return nil, err
	}
	domains := resolve.Deref(settings.Domains)
	out := make([]cobra.Completion, 0, len(domains))
	for _, d := range domains {
		out = append(out, d.Domain)
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

func (a *App) jobChoices(ctx context.Context, c *client.Client) ([]cobra.Completion, error) {
	sel, err := a.selectTarget(ctx, c, scopeApp)
	if err != nil {
		return nil, err
	}
	jobs, err := schedJobs(ctx, c, sel)
	if err != nil {
		return nil, err
	}
	out := make([]cobra.Completion, 0, len(jobs))
	for _, j := range jobs {
		out = append(out, cobra.CompletionWithDesc(j.Name, string(j.JobType)+", "+string(j.Status)))
	}
	return out, nil
}

func (a *App) taskChoices(ctx context.Context, c *client.Client) ([]cobra.Completion, error) {
	sel, err := a.selectTarget(ctx, c, scopeApp)
	if err != nil {
		return nil, err
	}
	resp, err := c.ListAppTaskWithResponse(ctx, sel.Project.Id, sel.Env, sel.App.Id,
		&api.ListAppTaskParams{PageLimit: ptr(tasksShown)})
	if err = client.Check(resp, err); err != nil {
		return nil, err
	}
	tasks := resolve.Deref(resp.JSON200.Data)
	out := make([]cobra.Completion, 0, len(tasks))
	for _, t := range tasks {
		out = append(out, cobra.CompletionWithDesc(t.Id, taskType(t.Type)+", "+string(t.Status)+", "+taskWhat(t)))
	}
	return out, nil
}

func (a *App) secretChoices(ctx context.Context, c *client.Client) ([]cobra.Completion, error) {
	sel, err := a.selectTarget(ctx, c, scopeApp)
	if err != nil {
		return nil, err
	}
	secrets, err := appSecrets(ctx, c, sel)
	if err != nil {
		return nil, err
	}
	var out []cobra.Completion
	for _, s := range secrets {
		if s.Inherited == nil || !*s.Inherited {
			out = append(out, s.Key)
		}
	}
	return out, nil
}

func (a *App) configFileChoices(ctx context.Context, _ *client.Client) ([]cobra.Completion, error) {
	_, _, files, err := a.configFiles(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]cobra.Completion, 0, len(files))
	for _, f := range files {
		out = append(out, cobra.CompletionWithDesc(f.Name, owner(f.Inherited)))
	}
	return out, nil
}
