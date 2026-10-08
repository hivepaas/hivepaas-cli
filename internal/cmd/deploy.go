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
	image   string
	noCache bool
	noWait  bool
	timeout time.Duration
}

func (a *App) deployCmd() *cobra.Command {
	var flags deployFlags
	cmd := &cobra.Command{
		Use:   "deploy",
		Short: "Deploy an app, and wait for the result while following its logs",
		Long: "Deploy an app. With --image, the image in its deployment settings is changed first.\n\n" +
			"The command waits for the deployment to finish, following its logs on stderr. Ctrl-C stops\n" +
			"the waiting, not the deployment: `hivepaas deploy cancel` cancels it.",
		Args: usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.deploy(cmd.Context(), flags)
		},
	}
	cmd.Flags().StringVar(&flags.image, "image", "", "deploy this image: ghcr.io/acme/api:1.4.3")
	cmd.Flags().BoolVar(&flags.noCache, "no-cache", false, "build without the build cache")
	cmd.Flags().BoolVar(&flags.noWait, "no-wait", false, "start the deployment and return")
	cmd.Flags().DurationVar(&flags.timeout, "timeout", defaultDeployTimeout, "how long to wait for the deployment")
	cmd.AddCommand(a.deployCancelCmd())
	return cmd
}

func (a *App) deploy(ctx context.Context, flags deployFlags) error {
	if flags.image != "" && flags.noCache {
		return exitcode.New(exitcode.Usage, "--no-cache is for an app built from source, and --image deploys an image")
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	sel, err := a.selectTarget(ctx, c, scopeApp)
	if err != nil {
		return err
	}
	var started api.AppactiondtoDeployAppDataResp
	if flags.image != "" {
		// A change of the deployment settings deploys it: the server starts the
		// deployment itself.
		if started.DeploymentId, err = a.setImage(ctx, c, sel, flags.image); err != nil {
			return err
		}
	}
	if started.DeploymentId == "" {
		resp, err := c.AppActionDeployWithResponse(ctx, sel.Project.Id, sel.Env, sel.App.Id,
			api.AppactiondtoDeployAppReq{NoCache: flags.noCache})
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

// setImage changes the image in the app's deployment settings: it reads them,
// changes the image and writes them back under their updateVer, so that a
// change made meanwhile is not overwritten - it is read, and the image set on
// it, once more. It answers the deployment the change started, none when the
// image was the app's already.
func (a *App) setImage(ctx context.Context, c *client.Client, sel *selection, image string) (string, error) {
	for attempt := 0; ; attempt++ {
		resp, err := c.GetAppDeploymentSettingsWithResponse(ctx, sel.Project.Id, sel.Env, sel.App.Id)
		if err = client.Check(resp, err); err != nil {
			return "", err
		}
		settings := resp.JSON200.Data
		if settings.ActiveMethod != api.DeploymentMethodImage || settings.ImageSource == nil {
			return "", exitcode.New(exitcode.Invalid, "%s deploys from its %s, not an image: --image is for an app "+
				"that deploys one", sel.App.Name, methodName(settings.ActiveMethod))
		}
		old := settings.ImageSource.Image
		if old == image {
			a.printer.Infof("Image: %s, unchanged", image)
			return "", nil
		}
		req, err := carry.DeploymentSettings(settings)
		if err != nil {
			return "", err
		}
		req.ImageSource.Image = image
		update, err := c.UpdateAppDeploymentSettingsWithResponse(ctx, sel.Project.Id, sel.Env, sel.App.Id, *req)
		err = client.Check(update, err)
		var apiErr *client.APIError
		if attempt == 0 && errors.As(err, &apiErr) && apiErr.Info.Code == errUpdateVerMismatched {
			continue
		}
		if err != nil {
			return "", err
		}
		a.printer.Infof("Image: %s", imageChange(old, image))
		if update.JSON200 == nil || update.JSON200.Data == nil || update.JSON200.Data.DeploymentId == nil {
			return "", nil
		}
		return *update.JSON200.Data.DeploymentId, nil
	}
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
