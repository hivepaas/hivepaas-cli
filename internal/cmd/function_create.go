package cmd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/hivepaas/hivepaas-cli/internal/api"
	"github.com/hivepaas/hivepaas-cli/internal/client"
	"github.com/hivepaas/hivepaas-cli/internal/exitcode"
	"github.com/hivepaas/hivepaas-cli/internal/funccode"
	"github.com/hivepaas/hivepaas-cli/internal/link"
)

type functionCreateFlags struct {
	*functionFlags
	runtime string
	domain  string
	noLink  bool
	noWait  bool
	timeout time.Duration
}

func (a *App) functionCreateCmd() *cobra.Command {
	var flags functionCreateFlags
	cmd := &cobra.Command{
		Use:   "create NAME [DIR]",
		Short: "Make a function of a directory's code, or of a repository's, and deploy it",
		Long: "Make a function in an environment, of DIR's code - . by default - or with --repo of a\n" +
			"repository's, and deploy it: the command waits for the deployment while following its logs, as\n" +
			"deploy does. The directory is linked to the function, so that `function deploy` there needs no\n" +
			"flags, unless --no-link or it is linked to another app already.\n\n" +
			"What is sent of a directory leaves out .git, node_modules, __pycache__, .venv, venv, .hivepaas\n" +
			"and what its .gitignore ignores; --list shows it.",
		Example: "  hivepaas function create hello ./hello -p shop -e staging --runtime node24\n" +
			"  hivepaas function create resize -p shop -e staging --runtime python313 \\\n" +
			"    --repo https://github.com/acme/functions.git --ref main --path resize --call-timeout 2m",
		Args: usageArgs(cobra.RangeArgs(1, createArgsMax)),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.functionCreate(cmd.Context(), args[0], args[1:], flags)
		},
	}
	flags.functionFlags = addFunctionFlags(cmd)
	f := cmd.Flags()
	f.StringVar(&flags.runtime, "runtime", "", "node24, bun1, python313 or go127")
	f.StringVar(&flags.domain, "domain", "", "serve it at this domain, over HTTPS")
	f.BoolVar(&flags.noLink, "no-link", false, "do not link the directory to the function")
	f.BoolVar(&flags.noWait, "no-wait", false, "do not wait for the deployment")
	f.DurationVar(&flags.timeout, "timeout", defaultDeployTimeout, "how long to wait for the deployment")
	return cmd
}

// createArgsMax is create's arguments at most: its name, and its directory.
const createArgsMax = 2

// checkSource refuses a function's code given two ways, or a repository's
// flags without its repository.
func (flags functionCreateFlags) checkSource(args []string) error {
	if err := flags.check(); err != nil {
		return err
	}
	if flags.changed(flagRepo) {
		if len(args) > 0 {
			return exitcode.New(exitcode.Usage, "a function's code is a directory's or a repository's: give %s "+
				"or --repo, not both", args[0])
		}
		return nil
	}
	if given := flags.repoGiven(); len(given) > 0 {
		return exitcode.New(exitcode.Usage, "%s %s for a function built from a repository: give --repo",
			strings.Join(given, " and "), map[bool]string{true: "are", false: "is"}[len(given) > 1])
	}
	return nil
}

func (a *App) functionCreate(ctx context.Context, name string, args []string, flags functionCreateFlags) error {
	if err := flags.checkSource(args); err != nil {
		return err
	}
	fromRepo := flags.changed(flagRepo)
	runtime, err := a.runtimeOf(ctx, flags.runtime)
	if err != nil {
		return err
	}
	dir := dirArg(args)
	var code *funccode.Code
	if !fromRepo {
		if code, err = a.collectCode(dir); err != nil {
			return err
		}
		if flags.list {
			return a.listCode(code)
		}
		if err = a.linkFromDir(dir); err != nil {
			return err
		}
	}

	c, err := a.client()
	if err != nil {
		return err
	}
	sel, err := a.selectTarget(ctx, c, scopeEnv)
	if err != nil {
		return err
	}
	src, err := a.newFunctionSource(ctx, c, sel, name, runtime, code, flags)
	if err != nil {
		return err
	}
	resp, err := c.CreateFunctionWithResponse(ctx, sel.Project.Id, sel.Env, api.AppdtoCreateFunctionReq{
		Name: name, Status: api.AppStatusActive, Tags: &[]string{}, Domain: flags.domain, Source: src,
	})
	if err = client.Check(resp, err); err != nil {
		return err
	}
	if resp.JSON201 == nil || resp.JSON201.Data == nil {
		return exitcode.New(exitcode.Server, "the server answered no function")
	}
	created := resp.JSON201.Data
	sel.App = &api.AppdtoAppResp{Id: created.Id, Key: created.Id, Name: name}
	got, getErr := c.GetAppWithResponse(ctx, sel.Project.Id, sel.Env, created.Id, &api.GetAppParams{})
	if client.Check(got, getErr) == nil && got.JSON200 != nil && got.JSON200.Data != nil {
		sel.App = got.JSON200.Data // its key, for the commands it suggests
	}
	a.printer.Successf("Created function %s in %s / %s.", name, sel.Project.Name, sel.Env)
	if !fromRepo && !flags.noLink {
		a.linkFunction(dir, c, sel)
	}
	if created.DeploymentId == "" {
		return nil
	}
	return a.awaitDeployment(ctx, c, sel, created.DeploymentId, flags.noWait, flags.timeout)
}

// newFunctionSource is a new function's source: its runtime, its code - code's,
// or a repository's - and the settings its flags give.
func (a *App) newFunctionSource(ctx context.Context, c *client.Client, sel *selection, name, runtime string,
	code *funccode.Code, flags functionCreateFlags,
) (*api.AppsettingsdtoDeploymentFunctionSourceReq, error) {
	refs, err := flags.refs(ctx, c, sel)
	if err != nil {
		return nil, err
	}
	src := &api.AppsettingsdtoDeploymentFunctionSourceReq{Runtime: api.BaseFunctionRuntime(runtime),
		SystemPackages: &[]string{}}
	ch := &changes{}
	if code == nil {
		if err = flags.applyRepo(name, &src.Code, nil, refs, ch); err != nil {
			return nil, err
		}
	} else {
		applyInline(&src.Code, code.Files, ch)
		if !flags.changed("entrypoint") {
			if entry := funccode.Entrypoint(runtime, code.Files); entry != "" {
				src.Entrypoint.File = entry
				a.printer.Infof("Entrypoint: %s", entry)
			}
		}
	}
	flags.applySettings(src, settingRef{}, refs, ch)
	return src, nil
}

// linkFunction links dir to the function just made, unless dir is linked to
// another app already: that link is kept, and said. A link that cannot be
// written is said, and the function stays made.
func (a *App) linkFunction(dir string, c *client.Client, sel *selection) {
	if _, err := os.Stat(filepath.Join(dir, link.FileName)); err == nil {
		l, err := link.Find(dir)
		switch {
		case err != nil:
			a.printer.Warnf("%v: the link is kept", err)
			return
		case l != nil && l.App.ID != sel.App.Id:
			a.printer.Warnf("%s is linked to %s already: the link is kept", dir, l.App.Name)
			return
		}
	}
	path, err := link.Write(dir, &link.Link{
		URL:     c.Target.URL,
		Project: link.Named{ID: sel.Project.Id, Name: sel.Project.Name},
		Env:     sel.Env,
		App:     link.Named{ID: sel.App.Id, Name: sel.App.Name},
	})
	if err != nil {
		a.printer.Warnf("%v: link it with hivepaas link%s", err, sel.flags())
		return
	}
	a.printer.Infof("Linked %s to it (%s).", dir, filepath.Base(path))
}
