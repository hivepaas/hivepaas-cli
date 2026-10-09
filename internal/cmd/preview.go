package cmd

import (
	"context"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/hivepaas/hivepaas-cli/internal/api"
	"github.com/hivepaas/hivepaas-cli/internal/client"
	"github.com/hivepaas/hivepaas-cli/internal/exitcode"
	"github.com/hivepaas/hivepaas-cli/internal/output"
	"github.com/hivepaas/hivepaas-cli/internal/resolve"
)

// defaultPreviewTimeout is how long create waits for the task that makes a
// preview: it clones the app and deploys it.
const defaultPreviewTimeout = 30 * time.Minute

func (a *App) previewCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "preview",
		Aliases: []string{"previews"},
		Short:   "An app's previews: copies of it for a branch or a pull request",
		Long: "An app's previews: the copies HivePaaS makes of it for a branch or a pull request, each an app\n" +
			"of its own - -a takes it for logs, deploy and the rest.",
	}
	cmd.AddCommand(a.previewLsCmd(), a.previewCreateCmd(), a.previewRmCmd())
	return cmd
}

// previews are the selected app's previews.
func previews(ctx context.Context, c *client.Client, sel *selection) ([]api.AppdtoAppResp, error) {
	resp, err := c.ListAppPreviewWithResponse(ctx, sel.Project.Id, sel.Env, sel.App.Id, &api.ListAppPreviewParams{})
	if err = client.Check(resp, err); err != nil {
		return nil, err
	}
	if resp.JSON200 == nil {
		return nil, nil
	}
	return resolve.Deref(resp.JSON200.Data), nil
}

func (a *App) previewLsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "ls [APP]",
		Short: "List an app's previews",
		Args:  usageArgs(cobra.MaximumNArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 {
				a.app = args[0]
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			sel, err := a.selectTarget(cmd.Context(), c, scopeApp)
			if err != nil {
				return err
			}
			list, err := previews(cmd.Context(), c, sel)
			if err != nil {
				return err
			}
			if a.printer.Structured() {
				return a.printer.Data(list)
			}
			now := time.Now()
			rows := make([][]string, 0, len(list))
			for _, p := range list {
				rows = append(rows, []string{p.Name, p.Key, string(p.Status), output.Ago(p.UpdatedAt, now)})
			}
			return a.printer.Table([]string{colName, colKey, colStatus, colUpdated}, rows)
		},
	}
}

type previewCreateFlags struct {
	ref, subdomain string
	noDB, noStart  bool
	noWait         bool
	timeout        time.Duration
}

func (a *App) previewCreateCmd() *cobra.Command {
	var flags previewCreateFlags
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Make a preview of an app, and wait for it",
		Long: "Make a preview of an app: a copy of it, built from --ref - the app's own branch by default -\n" +
			"deployed at a subdomain of its own. The secrets a preview is not given are said. The command\n" +
			"waits for the task that makes it, following its log; Ctrl-C stops the waiting, not the task.",
		Example: "  hivepaas preview create --ref feature/checkout\n" +
			"  hivepaas preview create --ref feature/checkout --subdomain checkout --no-db",
		Args: usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.previewCreate(cmd.Context(), flags)
		},
	}
	f := cmd.Flags()
	f.StringVar(&flags.ref, "ref", "", "the branch to build; the app's own when not given")
	f.StringVar(&flags.subdomain, "subdomain", "", "the preview's subdomain; one is made when not given")
	f.BoolVar(&flags.noDB, "no-db", false, "do not clone the databases the app uses")
	f.BoolVar(&flags.noStart, "no-start", false, "make it, and do not start it")
	f.BoolVar(&flags.noWait, "no-wait", false, "start making it and return")
	f.DurationVar(&flags.timeout, "timeout", defaultPreviewTimeout, "how long to wait")
	return cmd
}

func (a *App) previewCreate(ctx context.Context, flags previewCreateFlags) error {
	c, err := a.client()
	if err != nil {
		return err
	}
	sel, err := a.selectTarget(ctx, c, scopeApp)
	if err != nil {
		return err
	}
	prep, err := c.PrepareCreateAppPreviewWithResponse(ctx, sel.Project.Id, sel.Env, sel.App.Id,
		api.ApppreviewdtoPrepareCreatePreviewReq{})
	if err = client.Check(prep, err); err != nil {
		return err
	}
	can := prep.JSON200.Data
	if can == nil || !can.Enabled {
		return exitcode.New(exitcode.Invalid, "previews of %s are off: its Feature Settings turn them on",
			sel.App.Name)
	}
	for _, s := range resolve.Deref(can.WithheldSecrets) {
		a.printer.Warnf("the preview is not given the secret %s (%s)", s.Name,
			strings.Join(resolve.Deref(s.EnvVars), ", "))
	}
	req := api.ApppreviewdtoCreatePreviewReq{RepoRef: flags.ref, CustomSubdomain: flags.subdomain,
		NoStart: flags.noStart}
	if flags.noDB {
		req.CloneDbApps = ptr(false)
	}
	resp, err := c.CreateAppPreviewWithResponse(ctx, sel.Project.Id, sel.Env, sel.App.Id, req)
	if err = client.Check(resp, err); err != nil {
		return err
	}
	if resp.JSON201 == nil || resp.JSON201.Data == nil {
		return exitcode.New(exitcode.Server, "the server answered no task for the preview")
	}
	ref := taskRef{sel: sel, id: resp.JSON201.Data.Id}
	a.printer.Infof("Making a preview of %s, task %s", sel.where(), ref.id)
	return a.awaitTask(ctx, c, ref, flags.noWait, flags.timeout)
}

// awaitTask waits for a task as job run does - following its log, up to
// timeout - unless noWait.
func (a *App) awaitTask(ctx context.Context, c *client.Client, ref taskRef, noWait bool,
	timeout time.Duration,
) error {
	if noWait {
		a.printer.Infof("  Follow it:  hivepaas logs --task %s -f%s", ref.id, ref.sel.flags())
		return nil
	}
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	task, err := a.waitForTask(waitCtx, c, ref)
	if err != nil {
		return a.stoppedWaitingForTask(ctx, err, ref, timeout)
	}
	if a.printer.Structured() {
		if err = a.printer.Data(task); err != nil {
			return err
		}
	}
	return a.taskOutcome(task, ref)
}

func (a *App) previewRmCmd() *cobra.Command {
	var removeStorage, yes bool
	cmd := &cobra.Command{
		Use:   "rm PREVIEW",
		Short: "Delete a preview of an app",
		Long: "Delete one of the app's previews, by its name or key, as app delete deletes an app: asked at a\n" +
			"terminal, --yes elsewhere. A name that is not one of the app's previews is refused.",
		Args: usageArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			c, err := a.clientWithTimeout(deleteTimeout)
			if err != nil {
				return err
			}
			sel, err := a.selectTarget(ctx, c, scopeApp)
			if err != nil {
				return err
			}
			list, err := previews(ctx, c, sel)
			if err != nil {
				return err
			}
			preview := findPreview(list, args[0])
			if preview == nil {
				return exitcode.New(exitcode.NotFound, "%s has no preview %s: hivepaas preview ls%s lists them",
					sel.App.Name, args[0], sel.flags())
			}
			previewSel := *sel
			previewSel.App = preview
			return a.deleteApp(ctx, c, &previewSel, removeStorage, yes)
		},
	}
	cmd.Flags().BoolVar(&removeStorage, "remove-storage", false, "delete the data it keeps on volumes too")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask: for a script")
	return cmd
}

// findPreview is the preview input names, by its name, key or id; nil for none.
func findPreview(list []api.AppdtoAppResp, input string) *api.AppdtoAppResp {
	for i := range list {
		if p := &list[i]; p.Name == input || p.Key == input || p.Id == input {
			return p
		}
	}
	return nil
}
