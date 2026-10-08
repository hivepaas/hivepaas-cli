package cmd

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/hivepaas/hivepaas-cli/internal/api"
	"github.com/hivepaas/hivepaas-cli/internal/carry"
	"github.com/hivepaas/hivepaas-cli/internal/client"
	"github.com/hivepaas/hivepaas-cli/internal/exitcode"
	"github.com/hivepaas/hivepaas-cli/internal/resolve"
)

const (
	defaultDeployTimeout = 30 * time.Minute
	// errUpdateVerMismatched is the API's answer to a write over a change made
	// since the object was read.
	errUpdateVerMismatched = "ERR_UPDATE_VER_MISMATCHED"
)

type deployFlags struct {
	source   *sourceFlags
	noCache  bool
	changeID string
	noWait   bool
	timeout  time.Duration
}

func (a *App) deployCmd() *cobra.Command {
	var flags deployFlags
	cmd := &cobra.Command{
		Use:   "deploy",
		Short: "Deploy an app, and wait for the result while following its logs",
		Long: "Deploy an app: the image it runs, or a build of its repository. The flags of its source -\n" +
			"--image, or --repo, --ref, --commit and the rest - and of its commands change the deployment\n" +
			"settings first, in one write, which deploys them. `hivepaas deploy settings` shows them.\n\n" +
			"The command waits for the deployment to finish, following its logs on stderr. Ctrl-C stops\n" +
			"the waiting, not the deployment: `hivepaas deploy cancel` cancels it.",
		Example: "  hivepaas deploy --image ghcr.io/acme/api:1.4.3\n" +
			"  hivepaas deploy --commit $GITHUB_SHA\n" +
			"  hivepaas deploy --ref release --dockerfile docker/Dockerfile --push-to ghcr\n" +
			"  hivepaas deploy --use repo --repo https://github.com/acme/api.git --git-credential acme",
		Args: usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.deploy(cmd.Context(), flags)
		},
	}
	flags.source = addSourceFlags(cmd)
	cmd.Flags().BoolVar(&flags.noCache, "no-cache", false, "build without the build cache")
	cmd.Flags().StringVar(&flags.changeID, "change-id", "",
		"what the deployment is for: pr-12 has its result commented on that pull request")
	cmd.Flags().BoolVar(&flags.noWait, "no-wait", false, "start the deployment and return")
	cmd.Flags().DurationVar(&flags.timeout, "timeout", defaultDeployTimeout, "how long to wait for the deployment")
	cmd.AddCommand(a.deployCancelCmd(), a.deployLsCmd(), a.deployGetCmd(), a.deploySettingsCmd())
	return cmd
}

func (a *App) deploy(ctx context.Context, flags deployFlags) error {
	c, err := a.client()
	if err != nil {
		return err
	}
	sel, err := a.selectTarget(ctx, c, scopeApp)
	if err != nil {
		return err
	}
	var started api.AppactiondtoDeployAppDataResp
	if flags.source.asked() {
		in, err := flags.source.input(ctx, c, sel, a.stdin)
		if err != nil {
			return err
		}
		// A change of the deployment settings deploys it: the server starts the
		// deployment itself.
		started.DeploymentId, err = a.changeSource(ctx, c, sel, in,
			func(req *api.AppsettingsdtoUpdateAppDeploymentSettingsReq, changed bool) error {
				return postOnly(req, changed, flags)
			})
		if err != nil {
			return err
		}
	}
	if started.DeploymentId == "" {
		resp, err := c.AppActionDeployWithResponse(ctx, sel.Project.Id, sel.Env, sel.App.Id,
			api.AppactiondtoDeployAppReq{NoCache: flags.noCache, ChangeId: flags.changeID})
		if err = client.Check(resp, err); err != nil {
			return err
		}
		started = *resp.JSON200.Data
	}
	a.printer.Infof("Deploying %s, deployment %s", sel.where(), started.DeploymentId)
	if flags.noWait {
		a.printer.Infof("  Follow it:  hivepaas logs --deployment %s -f%s", started.DeploymentId, sel.flags())
		if a.printer.Structured() {
			return a.printer.Data(started)
		}
		return nil
	}

	waitCtx, cancel := context.WithTimeout(ctx, flags.timeout)
	defer cancel()
	ref := deploymentRef{sel: sel, id: started.DeploymentId}
	deployment, err := a.waitFor(waitCtx, c, ref, true)
	if err != nil {
		return a.stoppedWaiting(ctx, err, []deploymentRef{ref}, flags.timeout)
	}
	if a.printer.Structured() {
		if err = a.printer.Data(deployment); err != nil {
			return err
		}
	}
	return a.outcome(deployment, ref)
}

// postOnly checks the flags of POST .../deploy against the settings about to be
// written: a change starts a deployment of its own, which takes neither.
func postOnly(req *api.AppsettingsdtoUpdateAppDeploymentSettingsReq, changed bool, flags deployFlags) error {
	if flags.noCache && req.ActiveMethod == api.DeploymentMethodImage {
		return exitcode.New(exitcode.Usage, "--no-cache is for an app built from source, and an image is not built")
	}
	var given []string
	if flags.noCache {
		given = append(given, "--no-cache")
	}
	if flags.changeID != "" {
		given = append(given, "--change-id")
	}
	if changed && len(given) > 0 {
		return exitcode.New(exitcode.Usage, "%s cannot go with a change of the settings, whose write starts a "+
			"deployment of its own: deploy the change, then deploy again with %s",
			strings.Join(given, " and "), strings.Join(given, " and "))
	}
	return nil
}

// errUnchanged stops a write of settings that are as asked already.
var errUnchanged = errors.New("the deployment settings are as asked already")

// changeSource changes the app's deployment settings as in asks: it reads them,
// changes them and writes them back under their updateVer, so that a change
// made meanwhile is not overwritten - it is read, and changed, once more. check
// sees the settings about to be written, and whether they change. It answers
// the deployment the write started; none when nothing was to change.
func (a *App) changeSource(ctx context.Context, c *client.Client, sel *selection, in *sourceInput,
	check func(*api.AppsettingsdtoUpdateAppDeploymentSettingsReq, bool) error,
) (string, error) {
	var current *api.AppsettingsdtoDeploymentSettingsResp
	var lines []string
	var written *api.AppsettingsdtoUpdateAppDeploymentSettingsReq
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
	change := func(req *api.AppsettingsdtoUpdateAppDeploymentSettingsReq) (string, error) {
		var err error
		if lines, err = applySource(sel.App.Name, current, req, in); err != nil {
			return "", err
		}
		if check != nil {
			if err = check(req, len(lines) > 0); err != nil {
				return "", err
			}
		}
		if len(lines) == 0 {
			return "", errUnchanged
		}
		written = req
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
	_, err := writeBack(ctx, read, carry.DeploymentSettings, change, write)
	if errors.Is(err, errUnchanged) {
		a.printer.Infof("The deployment settings are as asked already.")
		return "", nil
	}
	if err != nil {
		return "", err
	}
	for _, line := range lines {
		a.printer.Infof("%s", line)
	}
	if repo := written.RepoSource; in.commit != nil && *in.commit != "" &&
		written.ActiveMethod == api.DeploymentMethodRepo && repo != nil && autoDeploys(repo.AutoDeploy) {
		a.printer.Warnf("%s also deploys each push to %s: this commit may be deployed twice, once by its push. "+
			"--no-auto-deploy leaves deploying to this command.", sel.App.Name, refWords(repo.RepoRef))
	}
	return deploymentID, nil
}

func methodName(method api.BaseDeploymentMethod) string {
	switch method { //nolint:exhaustive // the others are said as they are
	case api.DeploymentMethodRepo:
		return "repository"
	case api.DeploymentMethodFunction:
		return "function source"
	}
	return string(method)
}

// imageChange says an image changing, its repository once when only the tag
// moves: ghcr.io/acme/api:1.4.2 -> 1.4.3.
func imageChange(old, image string) string {
	oldRepo, _, oldTagged := cutTag(old)
	repo, tag, tagged := cutTag(image)
	if oldTagged && tagged && oldRepo == repo {
		return old + " -> " + tag
	}
	return old + " -> " + image
}

// cutTag splits an image reference at its tag: the last colon after the last
// slash, which a registry's port is not.
func cutTag(ref string) (repo, tag string, found bool) {
	i := strings.LastIndex(ref, ":")
	if i < 0 || i < strings.LastIndex(ref, "/") || strings.Contains(ref, "@") {
		return ref, "", false
	}
	return ref[:i], ref[i+1:], true
}

func (a *App) deployCancelCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "cancel [DEPLOYMENT-ID]",
		Short: "Cancel a deployment: the app's running one when no id is given",
		Args:  usageArgs(cobra.MaximumNArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			c, err := a.client()
			if err != nil {
				return err
			}
			sel, err := a.selectTarget(ctx, c, scopeApp)
			if err != nil {
				return err
			}
			var id string
			if len(args) == 1 {
				id = args[0]
			} else if id, err = a.runningDeployment(ctx, c, sel); err != nil {
				return err
			}
			resp, err := c.CancelAppDeploymentWithResponse(ctx, sel.Project.Id, sel.Env, sel.App.Id, id,
				api.AppdeploymentdtoCancelDeploymentReq{})
			if err = client.Check(resp, err); err != nil {
				return err
			}
			if resp.JSON200 != nil && resp.JSON200.Data != nil && resp.JSON200.Data.Canceled {
				a.printer.Successf("Canceled deployment %s of %s.", id, sel.where())
			} else {
				a.printer.Successf("Canceling deployment %s of %s: the server is stopping it.", id, sel.where())
			}
			if a.printer.Structured() && resp.JSON200 != nil {
				return a.printer.Data(resp.JSON200.Data)
			}
			return nil
		},
	}
}

// runningDeployment is the id of the app's deployment that has not finished.
func (a *App) runningDeployment(ctx context.Context, c *client.Client, sel *selection) (string, error) {
	status := string(api.DeploymentStatusInProgress) + "," + string(api.DeploymentStatusNotStarted)
	resp, err := c.ListAppDeploymentWithResponse(ctx, sel.Project.Id, sel.Env, sel.App.Id,
		&api.ListAppDeploymentParams{Status: &status, PageLimit: ptr(maxListed)})
	if err = client.Check(resp, err); err != nil {
		return "", err
	}
	running := resolve.Deref(resp.JSON200.Data)
	switch len(running) {
	case 0:
		return "", exitcode.New(exitcode.NotFound, "no deployment of %s is running", sel.where())
	case 1:
		return running[0].Id, nil
	}
	ids := make([]string, 0, len(running))
	for _, d := range running {
		ids = append(ids, d.Id+" ("+string(d.Status)+")")
	}
	return "", exitcode.New(exitcode.Usage, "%d deployments of %s have not finished - give the id of one: %s",
		len(running), sel.where(), strings.Join(ids, ", "))
}

// maxListed is how many of the deployments that have not finished are listed.
const maxListed = 20

// outcome says how a deployment ended, and exits 8 when it did not succeed.
func (a *App) outcome(d *api.AppdeploymentdtoDeploymentResp, ref deploymentRef) error {
	took := took(d)
	switch d.Status { //nolint:exhaustive // waitFor answers a finished deployment
	case api.DeploymentStatusDone:
		a.printer.Successf("Deployed in %s.", took)
		return nil
	case api.DeploymentStatusCanceled:
		a.printer.Errorf("The deployment was canceled after %s.", took)
	default:
		reason := ""
		if d.Output != nil && d.Output.Error != nil && *d.Output.Error != "" {
			reason = ": " + *d.Output.Error
		}
		a.printer.Errorf("Deployment failed after %s%s", took, reason)
	}
	a.printer.Infof("Its logs: hivepaas logs --deployment %s%s", d.Id, ref.sel.flags())
	return exitcode.Reported(exitcode.Deployment)
}

// took is how long a deployment ran, as the server timed it.
func took(d *api.AppdeploymentdtoDeploymentResp) string {
	if d.StartedAt == nil || d.EndedAt == nil {
		return "?"
	}
	start, err1 := time.Parse(time.RFC3339, *d.StartedAt)
	end, err2 := time.Parse(time.RFC3339, *d.EndedAt)
	if err1 != nil || err2 != nil {
		return "?"
	}
	return end.Sub(start).Round(time.Second).String()
}

// stoppedWaiting says the deployments go on without the CLI - after Ctrl-C, or
// at --timeout - and how to pick them up again: section 5 of the design. Any
// other error is the error.
func (a *App) stoppedWaiting(ctx context.Context, err error, refs []deploymentRef, timeout time.Duration) error {
	code := exitcode.Timeout
	switch {
	case ctx.Err() != nil:
		code = exitcode.Interrupted
		a.printer.Infof("")
		a.printer.Infof("Stopped waiting.")
	case errors.Is(err, context.DeadlineExceeded):
		a.printer.Warnf("stopped waiting after %s.", timeout)
	default:
		return err
	}
	for _, ref := range refs {
		a.printer.Infof("Deployment %s of %s is still running on the server.", ref.id, ref.sel.App.Name)
		a.printer.Infof("  Follow it:  hivepaas logs --deployment %s -f%s", ref.id, ref.sel.flags())
		a.printer.Infof("  Cancel it:  hivepaas deploy cancel %s%s", ref.id, ref.sel.flags())
	}
	return exitcode.Reported(code)
}

// deploymentRef is a deployment and the app it deploys.
type deploymentRef struct {
	sel *selection
	id  string
}

func (r deploymentRef) path() string {
	return fmt.Sprintf("/projects/%s/%s/apps/%s/deployments/%s", r.sel.Project.Id, r.sel.Env, r.sel.App.Id, r.id)
}

// transient says a failed request may go through when tried again: the server
// could not be reached, or failed.
func transient(err error) bool {
	var apiErr *client.APIError
	if errors.As(err, &apiErr) {
		return apiErr.Status >= http.StatusInternalServerError
	}
	return client.IsUnreachable(err)
}
