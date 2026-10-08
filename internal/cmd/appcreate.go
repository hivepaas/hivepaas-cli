package cmd

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/hivepaas/hivepaas-cli/internal/api"
	"github.com/hivepaas/hivepaas-cli/internal/carry"
	"github.com/hivepaas/hivepaas-cli/internal/client"
	"github.com/hivepaas/hivepaas-cli/internal/exitcode"
	"github.com/hivepaas/hivepaas-cli/internal/resolve"
)

type appCreateFlags struct {
	image   string
	port    int
	vars    []string
	noWait  bool
	timeout time.Duration
}

func (a *App) appCreateCmd() *cobra.Command {
	var flags appCreateFlags
	cmd := &cobra.Command{
		Use:   "create NAME",
		Short: "Create an app, and with --image deploy it",
		Long: "Create an app in an environment. With --image it runs that image: its variables (--var) and\n" +
			"port (--port) are set first, then the image, which deploys it; the command waits for the\n" +
			"deployment as deploy does. Without --image the app is created empty, for the dashboard or\n" +
			"`hivepaas deploy --image` to fill.",
		Example: "  hivepaas app create api -p shop -e staging --image ghcr.io/acme/api:1.4.3 --port 8080 \\\n" +
			"    --var LOG_LEVEL=info --var DATABASE_URL='${db.HIVEPAAS_URL}'",
		Args: usageArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.appCreate(cmd.Context(), args[0], flags)
		},
	}
	f := cmd.Flags()
	f.StringVar(&flags.image, "image", "", "the image the app runs: ghcr.io/acme/api:1.4.3")
	f.IntVar(&flags.port, "port", 0, "the port the app listens on")
	// Not --env, which names the environment, as for every command.
	f.StringArrayVar(&flags.vars, "var", nil, "a runtime variable, KEY=VALUE; repeat for more")
	f.BoolVar(&flags.noWait, "no-wait", false, "do not wait for the deployment")
	f.DurationVar(&flags.timeout, "timeout", defaultDeployTimeout, "how long to wait for the deployment")
	return cmd
}

func (a *App) appCreate(ctx context.Context, name string, flags appCreateFlags) error {
	vars, err := keyValues(flags.vars)
	if err != nil {
		return err
	}
	if flags.port < 0 {
		return exitcode.New(exitcode.Usage, "--port takes a port, not %d", flags.port)
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	sel, err := a.selectTarget(ctx, c, scopeEnv)
	if err != nil {
		return err
	}
	resp, err := c.CreateAppWithResponse(ctx, sel.Project.Id, sel.Env, api.AppdtoCreateAppReq{
		Name: name, Status: api.AppStatusActive, Tags: &[]string{},
	})
	if err = client.Check(resp, err); err != nil {
		return err
	}
	if resp.JSON201 == nil || resp.JSON201.Data == nil {
		return exitcode.New(exitcode.Server, "the server answered no app")
	}
	sel.App = &api.AppdtoAppResp{Id: resp.JSON201.Data.Id, Key: resp.JSON201.Data.Id, Name: name}
	got, getErr := c.GetAppWithResponse(ctx, sel.Project.Id, sel.Env, sel.App.Id, &api.GetAppParams{})
	if client.Check(got, getErr) == nil && got.JSON200 != nil && got.JSON200.Data != nil {
		sel.App = got.JSON200.Data // its key, for the commands it suggests
	}
	a.printer.Successf("Created %s.", sel.where())

	// What fails from here leaves the app created: say what is left to do.
	unfinished := func(err error, rest string) error {
		a.printer.Errorf("%s", err)
		a.printer.Infof("%s exists; %s", name, rest)
		return exitcode.Reported(exitcode.Of(err))
	}
	if len(vars) > 0 {
		if _, err = writeBack(ctx, envReader(c, sel), carry.EnvVars, setVars(vars), envWriter(c, sel)); err != nil {
			return unfinished(err, "set its variables with hivepaas env set"+sel.flags())
		}
	}
	if flags.port > 0 {
		if _, err = writeBack(ctx, routingReader(c, sel), carry.RoutingSettings,
			func(req *api.AppsettingsdtoUpdateAppRoutingSettingsReq) (string, error) {
				req.Port = flags.port
				return "", nil
			}, routingWriter(c, sel)); err != nil {
			return unfinished(err, "set its port in the dashboard")
		}
	}
	if flags.image == "" {
		a.printer.Infof("It runs nothing yet: hivepaas deploy --image <image>%s", sel.flags())
		return nil
	}
	deploymentID, err := a.changeSource(ctx, c, sel,
		&sourceInput{image: &flags.image, imageFlags: []string{"--image"}}, nil)
	if err != nil {
		return unfinished(err, "deploy it with hivepaas deploy --image "+flags.image+sel.flags())
	}
	if deploymentID == "" {
		return nil
	}
	a.printer.Infof("Deploying %s, deployment %s", sel.where(), deploymentID)
	if flags.noWait {
		return nil
	}
	waitCtx, cancel := context.WithTimeout(ctx, flags.timeout)
	defer cancel()
	ref := deploymentRef{sel: sel, id: deploymentID}
	deployment, err := a.waitFor(waitCtx, c, ref, true)
	if err != nil {
		return a.stoppedWaiting(ctx, err, []deploymentRef{ref}, flags.timeout)
	}
	return a.outcome(deployment, ref)
}

func setVars(vars [][2]string) func(*api.AppsettingsdtoUpdateAppEnvVarsReq) (string, error) {
	return func(req *api.AppsettingsdtoUpdateAppEnvVarsReq) (string, error) {
		list := resolve.Deref(req.RuntimeEnvVars)
		for _, pair := range vars {
			list = append(list, api.BasedtoEnvVarReq{Key: pair[0], Value: pair[1]})
		}
		req.RuntimeEnvVars = &list
		return "", nil
	}
}

func envReader(c *client.Client, sel *selection) func(context.Context) (*api.AppsettingsdtoEnvVarsResp, error) {
	return func(ctx context.Context) (*api.AppsettingsdtoEnvVarsResp, error) { return envVars(ctx, c, sel) }
}

func envWriter(c *client.Client, sel *selection) func(context.Context, *api.AppsettingsdtoUpdateAppEnvVarsReq) error {
	return func(ctx context.Context, req *api.AppsettingsdtoUpdateAppEnvVarsReq) error {
		resp, err := c.UpdateAppEnvVarsWithResponse(ctx, sel.Project.Id, sel.Env, sel.App.Id, *req)
		return client.Check(resp, err)
	}
}

func routingReader(c *client.Client, sel *selection) func(context.Context) (
	*api.AppsettingsdtoRoutingSettingsResp, error,
) {
	return func(ctx context.Context) (*api.AppsettingsdtoRoutingSettingsResp, error) {
		return routingSettings(ctx, c, sel)
	}
}

func routingWriter(c *client.Client, sel *selection) func(context.Context,
	*api.AppsettingsdtoUpdateAppRoutingSettingsReq) error {
	return func(ctx context.Context, req *api.AppsettingsdtoUpdateAppRoutingSettingsReq) error {
		resp, err := c.UpdateAppRoutingSettingsWithResponse(ctx, sel.Project.Id, sel.Env, sel.App.Id, *req)
		return client.Check(resp, err)
	}
}

func (a *App) appDeleteCmd() *cobra.Command {
	var removeStorage, yes bool
	cmd := &cobra.Command{
		Use:   "delete [APP]",
		Short: "Delete an app, with the apps that go with it",
		Long: "Delete an app, and the apps that go with it: its previews, and what a template created for\n" +
			"it. Its data on the volumes stays unless --remove-storage.\n\n" +
			"At a terminal it asks for the app's name to be typed; elsewhere it needs --yes.",
		Args: usageArgs(cobra.MaximumNArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 {
				a.app = args[0]
			}
			return a.appDelete(cmd.Context(), removeStorage, yes)
		},
	}
	cmd.Flags().BoolVar(&removeStorage, "remove-storage", false, "delete the data the apps keep on volumes too")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask: for a script")
	return cmd
}

// deleteTimeout is how long a deletion may take: the server removes the apps,
// and with --remove-storage their data, before it answers.
const deleteTimeout = 10 * time.Minute

func (a *App) appDelete(ctx context.Context, removeStorage, yes bool) error {
	c, err := a.clientWithTimeout(deleteTimeout)
	if err != nil {
		return err
	}
	sel, err := a.selectTarget(ctx, c, scopeApp)
	if err != nil {
		return err
	}
	with := goingWith(*sel.App)
	what := sel.where()
	if len(with) > 0 {
		what += ", with " + strings.Join(with, ", ")
	}
	if removeStorage {
		what += map[bool]string{false: ", and its data", true: ", and their data"}[len(with) > 0] + " on the volumes"
	}
	if !yes {
		if !a.interactive() {
			return exitcode.New(exitcode.Usage, "deleting %s: give --yes to delete without being asked", what)
		}
		a.printer.Warnf("this deletes %s", what)
		typed, err := a.prompt(ctx, fmt.Sprintf("Type %s to go on: ", sel.App.Name), "--yes")
		if err != nil {
			return err
		}
		if typed != sel.App.Name && typed != sel.App.Key {
			a.printer.Infof("Nothing was deleted.")
			return exitcode.Reported(exitcode.Failure)
		}
	}
	a.printer.Infof("Deleting %s ...", sel.App.Name)
	resp, err := c.DeleteAppWithResponse(ctx, sel.Project.Id, sel.Env, sel.App.Id,
		&api.DeleteAppParams{RemoveStorage: &removeStorage}, api.AppdtoDeleteAppReq{})
	if err = client.Check(resp, err); err != nil {
		return err
	}
	a.printer.Successf("Deleted %s.", what)
	return nil
}

// goingWith are the apps deleted with an app: its children - previews - and its
// logical children - what a template created for it - and theirs.
func goingWith(app api.AppdtoAppResp) []string {
	var names []string
	for _, list := range []*[]api.AppdtoAppResp{app.ChildApps, app.LogicalChildApps} {
		for _, child := range resolve.Deref(list) {
			names = append(names, child.Name)
			names = append(names, goingWith(child)...)
		}
	}
	return names
}
