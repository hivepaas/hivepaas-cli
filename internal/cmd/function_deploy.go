package cmd

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/hivepaas/hivepaas-cli/internal/api"
	"github.com/hivepaas/hivepaas-cli/internal/carry"
	"github.com/hivepaas/hivepaas-cli/internal/client"
	"github.com/hivepaas/hivepaas-cli/internal/exitcode"
	"github.com/hivepaas/hivepaas-cli/internal/funccode"
)

// What a function's code is: a directory's, sent inline, or a repository's.
const (
	codeInline = "inline"
	codeRepo   = string(api.DeploymentMethodRepo)
)

type functionDeployFlags struct {
	*functionFlags
	use     string
	noWait  bool
	timeout time.Duration
}

func (a *App) functionDeployCmd() *cobra.Command {
	var flags functionDeployFlags
	cmd := &cobra.Command{
		Use:   "deploy [DIR]",
		Short: "Send a function's code and settings, and wait for its deployment",
		Long: "Send a function's code - DIR's, . by default, in place of the function's, whole - and the\n" +
			"settings its flags change, which deploys it; the command waits as deploy does. A function built\n" +
			"from a repository takes no directory: --ref, --commit and --path change what is built, and\n" +
			"--use inline sends a directory's code instead. The same code and settings deploy it again.",
		Example: "  hivepaas function deploy                  # in a directory linked to the function\n" +
			"  hivepaas function deploy ./hello --call-timeout 1m -a hello\n" +
			"  hivepaas function deploy --commit $GITHUB_SHA -a resize",
		Args: usageArgs(cobra.MaximumNArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.functionDeploy(cmd.Context(), args, flags)
		},
	}
	flags.functionFlags = addFunctionFlags(cmd)
	f := cmd.Flags()
	f.StringVar(&flags.use, "use", "", "switch where the code is: inline, or repo")
	f.BoolVar(&flags.noWait, "no-wait", false, "start the deployment and return")
	f.DurationVar(&flags.timeout, "timeout", defaultDeployTimeout, "how long to wait for the deployment")
	return cmd
}

func (a *App) functionDeploy(ctx context.Context, args []string, flags functionDeployFlags) error {
	if err := flags.check(); err != nil {
		return err
	}
	switch flags.use {
	case "", codeInline, codeRepo:
	default:
		return exitcode.New(exitcode.Usage, "--use takes inline or repo, not %q", flags.use)
	}
	dir := dirArg(args)
	if len(args) > 0 {
		if err := a.linkFromDir(dir); err != nil {
			return err
		}
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	sel, err := a.selectTarget(ctx, c, scopeApp)
	if err != nil {
		return err
	}
	settings, err := functionSettings(ctx, c, sel)
	if err != nil {
		return err
	}
	use, code, err := a.deployedCode(sel, settings, args, flags)
	if err != nil {
		return err
	}
	if flags.list {
		if code == nil {
			return exitcode.New(exitcode.Usage, "--list is for a directory's code, and %s's is a repository's",
				sel.App.Name)
		}
		return a.listCode(code)
	}
	if code != nil {
		entry := flags.entrypoint
		if stored := settings.FunctionSource.Entrypoint; !flags.changed("entrypoint") && stored != nil {
			entry = stored.File
		}
		if err = checkEntrypoint(dirArg(args), string(settings.FunctionSource.Runtime), entry, code); err != nil {
			return err
		}
	}
	refs, err := flags.refs(ctx, c, sel)
	if err != nil {
		return err
	}

	deploymentID, err := a.changeFunction(ctx, c, sel,
		func(req *api.AppsettingsdtoUpdateAppDeploymentSettingsReq, current *api.AppsettingsdtoDeploymentSettingsResp,
			ch *changes,
		) error {
			if err := isFunction(sel.App.Name, current); err != nil {
				return err
			}
			src := req.FunctionSource
			if use == codeInline {
				applyInline(&src.Code, code.Files, ch)
			} else if err := flags.applyRepo(sel.App.Name, &src.Code, repoOfFunction(current.FunctionSource), refs,
				ch); err != nil {
				return err
			}
			flags.applySettings(src, refOf(current.FunctionSource.PushToRegistry), refs, ch)
			return nil
		})
	if err != nil {
		return err
	}
	if deploymentID == "" {
		resp, err := c.AppActionDeployWithResponse(ctx, sel.Project.Id, sel.Env, sel.App.Id,
			api.AppactiondtoDeployAppReq{})
		if err = client.Check(resp, err); err != nil {
			return err
		}
		deploymentID = resp.JSON200.Data.DeploymentId
	}
	return a.awaitDeployment(ctx, c, sel, deploymentID, flags.noWait, flags.timeout)
}

// deployedCode is where the code deploy sends is - inline, or a repository -
// and, inline, the directory's code.
func (a *App) deployedCode(sel *selection, settings *api.AppsettingsdtoDeploymentSettingsResp, args []string,
	flags functionDeployFlags,
) (string, *funccode.Code, error) {
	use := flags.use
	if use == "" {
		use = codeInline
		if repoOfFunction(settings.FunctionSource) != nil {
			use = codeRepo
		}
	}
	if use == codeRepo {
		if len(args) > 0 {
			return "", nil, exitcode.New(exitcode.Usage, "%s builds from %s: --use inline sends %s's code instead",
				sel.App.Name, repoWords(settings.FunctionSource), args[0])
		}
		return use, nil, nil
	}
	if given := flags.repoGiven(); len(given) > 0 {
		if flags.use == codeInline {
			return "", nil, exitcode.New(exitcode.Usage, "%s %s for a repository's code, and --use inline sends a "+
				"directory's", strings.Join(given, " and "), isOrAre(len(given)))
		}
		return "", nil, exitcode.New(exitcode.Usage, "%s's code is inline: --use repo --repo URL builds it from a "+
			"repository instead", sel.App.Name)
	}
	code, err := a.collectCode(dirArg(args))
	return use, code, err
}

// awaitDeployment waits for a deployment as deploy does - following its logs,
// up to timeout - unless noWait.
func (a *App) awaitDeployment(ctx context.Context, c *client.Client, sel *selection, id string, noWait bool,
	timeout time.Duration,
) error {
	a.printer.Infof("Deploying %s, deployment %s", sel.where(), id)
	if noWait {
		a.printer.Infof("  Follow it:  hivepaas logs --deployment %s -f%s", id, sel.flags())
		return nil
	}
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ref := deploymentRef{sel: sel, id: id}
	deployment, err := a.waitFor(waitCtx, c, ref, true)
	if err != nil {
		return a.stoppedWaiting(ctx, err, []deploymentRef{ref}, timeout)
	}
	if a.printer.Structured() {
		if err = a.printer.Data(deployment); err != nil {
			return err
		}
	}
	return a.outcome(deployment, ref)
}

// changeFunction changes a function's deployment settings as change asks: it
// reads them, changes them and writes them back under their updateVer, as
// changeSource does. It answers the deployment the write started; none when
// nothing was to change - which it says.
func (a *App) changeFunction(ctx context.Context, c *client.Client, sel *selection,
	change func(*api.AppsettingsdtoUpdateAppDeploymentSettingsReq, *api.AppsettingsdtoDeploymentSettingsResp,
		*changes) error,
) (string, error) {
	var current *api.AppsettingsdtoDeploymentSettingsResp
	var ch *changes
	var deploymentID string
	read := func(ctx context.Context) (*api.AppsettingsdtoDeploymentSettingsResp, error) {
		resp, err := c.GetAppDeploymentSettingsWithResponse(ctx, sel.Project.Id, sel.Env, sel.App.Id)
		if err = client.Check(resp, err); err != nil {
			return nil, err
		}
		current = &api.AppsettingsdtoDeploymentSettingsResp{}
		if resp.JSON200 != nil && resp.JSON200.Data != nil {
			current = resp.JSON200.Data
		}
		return current, nil
	}
	apply := func(req *api.AppsettingsdtoUpdateAppDeploymentSettingsReq) (string, error) {
		ch = &changes{}
		if err := change(req, current, ch); err != nil {
			return "", err
		}
		if len(ch.lines) == 0 {
			return "", errUnchanged
		}
		return "", nil
	}
	write := func(ctx context.Context, req *api.AppsettingsdtoUpdateAppDeploymentSettingsReq) error {
		resp, err := c.UpdateAppDeploymentSettingsWithResponse(ctx, sel.Project.Id, sel.Env, sel.App.Id, *req)
		if err = client.Check(resp, err); err != nil {
			return err
		}
		if resp.JSON200 != nil && resp.JSON200.Data != nil && resp.JSON200.Data.DeploymentId != nil {
			deploymentID = *resp.JSON200.Data.DeploymentId
		}
		return nil
	}
	_, err := writeBack(ctx, read, carry.DeploymentSettings, apply, write)
	if errors.Is(err, errUnchanged) {
		a.printer.Infof("The code and settings are as deployed already: deploying them again.")
		return "", nil
	}
	if err != nil {
		return "", err
	}
	for _, line := range ch.lines {
		a.printer.Infof("%s", line)
	}
	return deploymentID, nil
}
