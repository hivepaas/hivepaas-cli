package cmd

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/hivepaas/hivepaas-cli/internal/api"
	"github.com/hivepaas/hivepaas-cli/internal/client"
	"github.com/hivepaas/hivepaas-cli/internal/exitcode"
	"github.com/hivepaas/hivepaas-cli/internal/resolve"
)

// Template parameter types the CLI turns from what a person types into what
// the API takes.
const (
	paramInt    = "int"
	paramBool   = "bool"
	paramVolume = "volume"
	paramApp    = "app"
)

type templateDeployFlags struct {
	name, version, variant, imageTag string
	params, depParams                []string
	resetStorage, keepStorage        bool
	noWait                           bool
	timeout                          time.Duration
}

func (a *App) templatesDeployCmd() *cobra.Command {
	var flags templateDeployFlags
	cmd := &cobra.Command{
		Use:   "deploy TEMPLATE",
		Short: "Create an app from a template, with what it depends on, and deploy it",
		Long: "Create an app from a template of the store, with the apps it depends on, and deploy them.\n\n" +
			"--param NAME=VALUE gives a parameter, and --dep-param DEP.NAME=VALUE one of a dependency's:\n" +
			"a volume by its name, an app by its name or key. A volume parameter left out takes the\n" +
			"project's only volume.\n\n" +
			"First the server checks what a previous install left where the new apps would keep their\n" +
			"data. If it finds some, the apps are created only with --keep-storage, to start on it, or\n" +
			"--reset-storage, to delete it first - or with the answer to a question in a terminal.",
		Example: "  hivepaas template deploy postgres --name orders-db -p shop -e staging --param dataVolume=default",
		Args:    usageArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.templatesDeploy(cmd.Context(), args[0], flags)
		},
	}
	f := cmd.Flags()
	f.StringVar(&flags.name, "name", "", "the app's name: the template's when left out")
	f.StringVar(&flags.version, "version", "", "the template's version: its default when left out")
	f.StringVar(&flags.variant, "variant", "", "the version's variant: its default when left out")
	f.StringVar(&flags.imageTag, "image-tag", "", "an image tag instead of the version's")
	f.StringArrayVar(&flags.params, "param", nil, "a parameter, NAME=VALUE; repeat for more")
	f.StringArrayVar(&flags.depParams, "dep-param", nil, "a dependency's parameter, DEP.NAME=VALUE")
	f.BoolVar(&flags.resetStorage, "reset-storage", false, "delete what a previous install left, then create")
	f.BoolVar(&flags.keepStorage, "keep-storage", false, "create the apps on what a previous install left")
	f.BoolVar(&flags.noWait, "no-wait", false, "create the apps and return, without waiting for their deployments")
	f.DurationVar(&flags.timeout, "timeout", defaultDeployTimeout, "how long to wait for the deployments")
	cmd.MarkFlagsMutuallyExclusive("reset-storage", "keep-storage")
	return cmd
}

func (a *App) templatesDeploy(ctx context.Context, name string, flags templateDeployFlags) error {
	c, err := a.client()
	if err != nil {
		return err
	}
	sel, err := a.selectTarget(ctx, c, scopeEnv)
	if err != nil {
		return err
	}
	resp, err := c.GetAppTemplateWithResponse(ctx, name)
	if err = client.Check(resp, err); err != nil {
		return err
	}
	tmpl := resp.JSON200.Data

	in := &paramInputs{a: a, c: c, sel: sel}
	params, err := in.params(ctx, resolve.Deref(tmpl.Parameters), flags.params, "--param ")
	if err != nil {
		return err
	}
	depParams, err := in.dependencyParams(ctx, resolve.Deref(tmpl.Dependencies), flags.depParams)
	if err != nil {
		return err
	}
	req := api.ApptemplatedtoCreateAppFromTemplateReq{
		Template: tmpl.Name, Name: firstOf(flags.name, tmpl.Name), Version: flags.version,
		Variant: flags.variant, ImageTag: flags.imageTag, Params: &params, DependencyParams: &depParams,
	}
	if req.ResetStorage, err = a.preflight(ctx, c, sel, req, flags); err != nil {
		return err
	}

	created, err := c.CreateAppFromTemplateWithResponse(ctx, sel.Project.Id, sel.Env, req)
	if err = client.Check(created, err); err != nil {
		return err
	}
	data := created.JSON201.Data
	a.printer.Successf("Created %s (%s) in %s / %s.", req.Name, templateLabel(tmpl, flags.version),
		sel.Project.Name, sel.Env)
	if a.printer.Structured() {
		if err = a.printer.Data(data); err != nil {
			return err
		}
	}
	if flags.noWait {
		return nil
	}

	waitCtx, cancel := context.WithTimeout(ctx, flags.timeout)
	defer cancel()
	return a.waitForCreated(ctx, waitCtx, c, createdDeployments(sel, req.Name, data), flags.timeout)
}

// paramInputs turn --param values into what the API takes: a number for an
// int, true or false for a bool, a volume's id for a volume, an app's key for
// an app.
type paramInputs struct {
	a       *App
	c       *client.Client
	sel     *selection
	volumes []api.VolumedtoVolumeResp
}

func (in *paramInputs) params(ctx context.Context, defs []api.ApptemplatedtoAppTemplateParamResp, args []string,
	flag string,
) (map[string]any, error) {
	params := map[string]any{}
	for _, arg := range args {
		key, value, found := strings.Cut(arg, "=")
		if !found || key == "" {
			return nil, exitcode.New(exitcode.Usage, "%s%q is not NAME=VALUE", flag, arg)
		}
		i := slices.IndexFunc(defs, func(d api.ApptemplatedtoAppTemplateParamResp) bool { return d.Name == key })
		if i < 0 {
			return nil, exitcode.New(exitcode.Usage, "the template has no parameter %q: %s", key, paramNames(defs))
		}
		v, err := in.value(ctx, defs[i], value, flag)
		if err != nil {
			return nil, err
		}
		params[key] = v
	}
	for _, def := range defs {
		if _, given := params[def.Name]; given || def.Type != paramVolume || def.Optional {
			continue
		}
		id, err := in.defaultVolume(ctx, def, flag)
		if err != nil {
			return nil, err
		}
		params[def.Name] = id
	}
	return params, nil
}

func (in *paramInputs) dependencyParams(ctx context.Context, deps []api.ApptemplatedtoAppTemplateDependencyResp,
	args []string,
) (map[string]map[string]any, error) {
	byDep := map[string][]string{}
	for _, arg := range args {
		dep, rest, found := strings.Cut(arg, ".")
		if !found || !slices.ContainsFunc(deps, func(d api.ApptemplatedtoAppTemplateDependencyResp) bool {
			return d.Name == dep
		}) {
			names := make([]string, 0, len(deps))
			for _, d := range deps {
				names = append(names, d.Name)
			}
			return nil, exitcode.New(exitcode.Usage, "--dep-param %q names no dependency of the template: "+
				"DEP.NAME=VALUE, with DEP one of: %s", arg, firstOf(strings.Join(names, ", "), "it has none"))
		}
		byDep[dep] = append(byDep[dep], rest)
	}
	out := map[string]map[string]any{}
	for _, dep := range deps {
		params, err := in.params(ctx, resolve.Deref(dep.Parameters), byDep[dep.Name], "--dep-param "+dep.Name+".")
		if err != nil {
			return nil, err
		}
		if len(params) > 0 {
			out[dep.Name] = params
		}
	}
	return out, nil
}

func (in *paramInputs) value(ctx context.Context, def api.ApptemplatedtoAppTemplateParamResp, value, flag string) (
	any, error,
) {
	switch def.Type {
	case paramInt:
		n, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
		if err != nil {
			return nil, exitcode.New(exitcode.Usage, "%s%s takes a whole number, not %q", flag, def.Name, value)
		}
		return n, nil
	case paramBool:
		b, err := strconv.ParseBool(strings.TrimSpace(value))
		if err != nil {
			return nil, exitcode.New(exitcode.Usage, "%s%s takes true or false, not %q", flag, def.Name, value)
		}
		return b, nil
	case paramVolume:
		volumes, err := in.projectVolumes(ctx)
		if err != nil {
			return nil, err
		}
		volume, err := resolve.Volume(volumes, in.sel.Project.Name, value)
		if err != nil {
			return nil, err
		}
		return volume.Id, nil
	case paramApp:
		app, err := resolve.New(in.c).App(ctx, in.sel.Project.Id, in.sel.Project.Name, in.sel.Env, value)
		if err != nil {
			return nil, err
		}
		return app.Key, nil
	}
	return value, nil
}

// defaultVolume is the volume a volume parameter left out takes: the
// project's only one, or the one a person picks.
func (in *paramInputs) defaultVolume(ctx context.Context, def api.ApptemplatedtoAppTemplateParamResp, flag string) (
	string, error,
) {
	volumes, err := in.projectVolumes(ctx)
	if err != nil {
		return "", err
	}
	if len(volumes) == 1 {
		return volumes[0].Id, nil
	}
	names := make([]string, 0, len(volumes))
	for _, v := range volumes {
		names = append(names, v.Name)
	}
	if !in.a.interactive() || len(volumes) == 0 {
		return "", exitcode.New(exitcode.Usage, "which volume for %s (%s)? Give %s%s=NAME, of: %s", def.Name,
			def.Title, flag, def.Name, firstOf(strings.Join(names, ", "), "the project has none"))
	}
	choice, err := in.a.choose(ctx, "volume for "+def.Name, flag+def.Name, names)
	if err != nil {
		return "", err
	}
	volume, err := resolve.Volume(volumes, in.sel.Project.Name, choice)
	if err != nil {
		return "", err
	}
	return volume.Id, nil
}

func (in *paramInputs) projectVolumes(ctx context.Context) ([]api.VolumedtoVolumeResp, error) {
	if in.volumes != nil {
		return in.volumes, nil
	}
	volumes, err := resolve.New(in.c).Volumes(ctx, in.sel.Project.Id)
	if err != nil {
		return nil, err
	}
	in.volumes = append([]api.VolumedtoVolumeResp{}, volumes...)
	return in.volumes, nil
}

func paramNames(defs []api.ApptemplatedtoAppTemplateParamResp) string {
	if len(defs) == 0 {
		return "it has none"
	}
	names := make([]string, 0, len(defs))
	for _, d := range defs {
		names = append(names, d.Name+" ("+d.Type+")")
	}
	return strings.Join(names, ", ")
}

// preflight asks the server what the creation would meet: data a previous
// install left where the new apps would keep theirs, and what it would refuse.
// It answers resetStorage: whether to delete that data first, nil when there is
// none. The check is advisory, as in the dashboard: when it cannot answer, the
// creation goes on and reports its own failures.
func (a *App) preflight(ctx context.Context, c *client.Client, sel *selection,
	req api.ApptemplatedtoCreateAppFromTemplateReq, flags templateDeployFlags,
) (*bool, error) {
	resp, err := c.PreflightAppFromTemplateWithResponse(ctx, sel.Project.Id, sel.Env,
		api.ApptemplatedtoPreflightAppFromTemplateReq{
			Template: req.Template, Name: req.Name, Version: req.Version, Variant: req.Variant,
			ImageTag: req.ImageTag, Params: req.Params, DependencyParams: req.DependencyParams,
		})
	if err = client.Check(resp, err); err != nil {
		if exitcode.Of(err) == exitcode.CLIOutdated {
			return nil, err
		}
		a.printer.Warnf("could not check what a previous install left: %s", err)
		return nil, nil
	}
	result := resp.JSON200.Data
	unchecked := resolve.Deref(result.StorageUnchecked)
	if len(unchecked) > 0 {
		a.printer.Warnf("could not check the storage of %s: if these apps ran here before, their data is "+
			"still in place", storageApps(unchecked))
	}
	if issues := resolve.Deref(result.Issues); len(issues) > 0 {
		for _, issue := range issues {
			a.printer.Errorf("%s", issue.Detail)
		}
		return nil, exitcode.Reported(exitcode.Invalid)
	}
	found := resolve.Deref(result.Storage)
	if len(found) == 0 {
		if len(unchecked) == 0 {
			a.printer.Infof("Nothing is left on the volumes by a previous install.")
		}
		return nil, nil
	}

	a.printer.Infof("A previous install left data where these apps would keep theirs:")
	databases := false
	for _, f := range found {
		note := ""
		if f.IsDatabase {
			note, databases = " (a database)", true
		}
		a.printer.Infof("  %s%s: %s on volume %s", f.App, note, f.Path, f.Volume.Name)
	}
	if databases {
		a.printer.Infof("A database started on old data keeps the password that data was made with, " +
			"not the one generated now.")
	}
	switch {
	case flags.resetStorage:
		a.printer.Infof("--reset-storage: it is deleted before the apps are created.")
		return ptr(true), nil
	case flags.keepStorage:
		return ptr(false), nil
	case !a.interactive():
		return nil, exitcode.New(exitcode.Invalid, "give --keep-storage to start on that data, or --reset-storage "+
			"to delete it first")
	}
	answer, err := a.prompt(ctx, "Keep it, reset it, or cancel? [k/r/c] ", "--keep-storage or --reset-storage")
	if err != nil {
		return nil, err
	}
	switch strings.ToLower(answer) {
	case "k", "keep":
		return ptr(false), nil
	case "r", "reset":
		return ptr(true), nil
	}
	a.printer.Infof("Nothing was created.")
	return nil, exitcode.Reported(exitcode.Failure)
}

func storageApps(list []api.ApptemplatedtoPreflightStorageResult) string {
	var names []string
	for _, item := range list {
		if !slices.Contains(names, item.App) {
			names = append(names, item.App)
		}
	}
	return strings.Join(names, ", ")
}

// templateLabel is the template and version created: PostgreSQL 18.
func templateLabel(tmpl *api.ApptemplatedtoAppTemplateResp, version string) string {
	if version == "" {
		for _, v := range resolve.Deref(tmpl.Versions) {
			if v.Default {
				version = v.Name
			}
		}
	}
	return strings.TrimSpace(tmpl.Title + " " + version)
}

// createdDeployments are the deployments a creation started, in the order they
// run: the dependencies, the app, its components.
func createdDeployments(sel *selection, name string, data *api.ApptemplatedtoCreateAppFromTemplateDataResp) (
	refs []deploymentRef,
) {
	add := func(app, deployment *api.BasedtoObjectIDResp, appName string) {
		if app == nil || deployment == nil || deployment.Id == "" {
			return
		}
		s := *sel
		s.App = &api.AppdtoAppResp{Id: app.Id, Key: app.Id, Name: appName}
		s.linked.app = false
		refs = append(refs, deploymentRef{sel: &s, id: deployment.Id})
	}
	for _, dep := range resolve.Deref(data.Dependencies) {
		add(dep.App, dep.Deployment, dep.Name)
	}
	add(data.App, data.Deployment, name)
	for _, component := range resolve.Deref(data.Components) {
		add(component.App, component.Deployment, component.Name)
	}
	return refs
}

// waitForCreated waits for the deployments of a creation, one after the other,
// and says how each ended. It exits 8 when one did not succeed.
func (a *App) waitForCreated(ctx, waitCtx context.Context, c *client.Client, refs []deploymentRef,
	timeout time.Duration,
) error {
	if len(refs) == 1 {
		d, err := a.waitFor(waitCtx, c, refs[0], false)
		if err != nil {
			return a.stoppedWaiting(ctx, err, refs, timeout)
		}
		return a.outcome(d, refs[0])
	}
	started := time.Now()
	var failed []deploymentRef
	for i, ref := range refs {
		d, err := a.waitFor(waitCtx, c, ref, false)
		if err != nil {
			return a.stoppedWaiting(ctx, err, refs[i:], timeout)
		}
		if d.Status == api.DeploymentStatusDone {
			a.printer.Successf("  %s: deployed in %s", ref.sel.App.Name, took(d))
			continue
		}
		a.printer.Errorf("%s: the deployment %s after %s", ref.sel.App.Name, d.Status, took(d))
		failed = append(failed, ref)
	}
	if len(failed) == 0 {
		a.printer.Successf("Deployed in %s.", time.Since(started).Round(time.Second))
		return nil
	}
	for _, ref := range failed {
		a.printer.Infof("Logs of %s: hivepaas logs --deployment %s%s", ref.sel.App.Name, ref.id, ref.sel.flags())
	}
	return exitcode.Reported(exitcode.Deployment)
}
