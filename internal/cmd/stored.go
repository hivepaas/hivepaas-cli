package cmd

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/hivepaas/hivepaas-cli/internal/api"
	"github.com/hivepaas/hivepaas-cli/internal/client"
	"github.com/hivepaas/hivepaas-cli/internal/exitcode"
	"github.com/hivepaas/hivepaas-cli/internal/resolve"
)

// What HivePaaS keeps for an app - its secrets, its config files - is kept
// for an environment and a project too, and shared from there with their
// apps. --scope says whose a command acts on: the app's, its environment's or
// its project's.

const (
	levelApp     = "app"
	levelEnv     = "env"
	levelProject = "project"
)

// addScopeFlag gives a command of what is kept --scope, which its subcommands
// and the shell's Tab read.
func (a *App) addScopeFlag(cmd *cobra.Command, what string) {
	cmd.PersistentFlags().StringVar(&a.storeScope, "scope", levelApp,
		"whose "+what+": the app's, its environment's (env) or its project's (project)")
	_ = cmd.RegisterFlagCompletionFunc("scope", cobra.FixedCompletions(
		[]cobra.Completion{levelApp, levelEnv, levelProject}, cobra.ShellCompDirectiveNoFileComp))
}

// storeLevel is the scope --scope names.
func (a *App) storeLevel() (scope, error) {
	switch a.storeScope {
	case "", levelApp:
		return scopeApp, nil
	case levelEnv:
		return scopeEnv, nil
	case levelProject:
		return scopeProject, nil
	}
	return scopeApp, exitcode.New(exitcode.Usage, "--scope is app, env or project, not %q", a.storeScope)
}

// store is whose secrets and config files a command acts on: the selection,
// as far as the scope needs it.
type store struct {
	c     *client.Client
	sel   *selection
	level scope
}

// openStore finds whose secrets and config files a command acts on.
func (a *App) openStore(ctx context.Context) (*store, error) {
	c, err := a.client()
	if err != nil {
		return nil, err
	}
	return a.storeWith(ctx, c)
}

// storeWith is openStore with a client of the caller's: the shell's Tab.
func (a *App) storeWith(ctx context.Context, c *client.Client) (*store, error) {
	level, err := a.storeLevel()
	if err != nil {
		return nil, err
	}
	sel, err := a.selectTarget(ctx, c, level)
	if err != nil {
		return nil, err
	}
	return &store{c: c, sel: sel, level: level}, nil
}

// where is whose they are, as messages say it: api (shop / production), the
// environment shop / production, the project shop.
func (s *store) where() string {
	switch s.level {
	case scopeProject:
		return "the project " + s.sel.Project.Name
	case scopeEnv:
		return fmt.Sprintf("the environment %s / %s", s.sel.Project.Name, s.sel.Env)
	case scopeApp:
	}
	return s.sel.where()
}

// own is what a listed one that is not inherited is: this app's, this
// environment's, this project's.
func (s *store) own() string {
	switch s.level {
	case scopeProject:
		return "this project"
	case scopeEnv:
		return "this env"
	case scopeApp:
	}
	return "this app"
}

// owner says whose a listed one is: the store's own, or inherited from above.
func (s *store) owner(inherited *bool) string {
	if inherited != nil && *inherited {
		return "inherited"
	}
	return s.own()
}

// sharedColumn heads whether one is shared below: with an app's pull request
// previews, or with an environment's or a project's apps.
func (s *store) sharedColumn() string {
	if s.level == scopeApp {
		return "PREVIEWS"
	}
	return "FOR APPS"
}

// columns head a list of what the store keeps.
func (s *store) columns(first string) []string {
	return []string{first, colSize, colType, s.sharedColumn(), "OF", colUpdated}
}

// shareFlags are whether what is written is shared below: --inheritable at
// every scope, --previews for an app, where sharing is with its previews.
type shareFlags struct {
	inheritable, notInheritable, previews, noPreviews bool
}

func (f *shareFlags) add(cmd *cobra.Command) {
	cmd.Flags().BoolVar(&f.inheritable, "inheritable", false,
		"shared below: with an app's pull request previews, an environment's or a project's apps (default)")
	cmd.Flags().BoolVar(&f.notInheritable, "no-inheritable", false, "not shared below")
	cmd.Flags().BoolVar(&f.previews, "previews", false, "an app's: its pull request previews get it too (default)")
	cmd.Flags().BoolVar(&f.noPreviews, "no-previews", false, "an app's: its pull request previews do not get it")
	cmd.MarkFlagsMutuallyExclusive("inheritable", "no-inheritable", "previews", "no-previews")
}

// shared is what the flags ask - nil for none of them, which keeps what one has
// and shares a new one. --previews names an app's sharing, not a project's.
func (f *shareFlags) shared(level scope) (*bool, error) {
	if (f.previews || f.noPreviews) && level != scopeApp {
		return nil, exitcode.New(exitcode.Usage, "--previews and --no-previews are an app's: "+
			"for an environment's or a project's apps, --inheritable or --no-inheritable")
	}
	switch {
	case f.inheritable || f.previews:
		return ptr(true), nil
	case f.notInheritable || f.noPreviews:
		return ptr(false), nil
	}
	return nil, nil
}

// secrets are the store's secrets - an app's and an environment's among them
// those they inherit - page by page: the server answers 50 unless asked.
func (s *store) secrets(ctx context.Context) ([]api.SecretdtoSecretResp, error) {
	var all []api.SecretdtoSecretResp
	for offset := 0; ; offset += storedPage {
		var (
			list *api.SecretdtoListSecretResp
			err  error
		)
		p, sel := s.sel.Project.Id, s.sel
		switch s.level {
		case scopeProject:
			resp, e := s.c.ListProjectSecretWithResponse(ctx, p, nil, page(offset))
			err = client.Check(resp, e)
			if err == nil {
				list = resp.JSON200
			}
		case scopeEnv:
			resp, e := s.c.ListProjectEnvSecretWithResponse(ctx, p, sel.Env, nil, page(offset))
			err = client.Check(resp, e)
			if err == nil {
				list = resp.JSON200
			}
		case scopeApp:
			resp, e := s.c.ListAppSecretWithResponse(ctx, p, sel.Env, sel.App.Id, page(offset))
			err = client.Check(resp, e)
			if err == nil {
				list = resp.JSON200
			}
		}
		if err != nil {
			return nil, err
		}
		if list == nil {
			return all, nil
		}
		items := resolve.Deref(list.Data)
		all = append(all, items...)
		if len(items) < storedPage {
			return all, nil
		}
	}
}

func (s *store) createSecret(ctx context.Context, body api.SecretdtoCreateSecretReq) (*api.BasedtoMeta, error) {
	p, sel := s.sel.Project.Id, s.sel
	var created *api.SecretdtoCreateSecretResp
	switch s.level {
	case scopeProject:
		resp, err := s.c.CreateProjectSecretWithResponse(ctx, p, body)
		if err = client.Check(resp, err); err != nil {
			return nil, err
		}
		created = resp.JSON201
	case scopeEnv:
		resp, err := s.c.CreateProjectEnvSecretWithResponse(ctx, p, sel.Env, body)
		if err = client.Check(resp, err); err != nil {
			return nil, err
		}
		created = resp.JSON201
	case scopeApp:
		resp, err := s.c.CreateAppSecretWithResponse(ctx, p, sel.Env, sel.App.Id, body)
		if err = client.Check(resp, err); err != nil {
			return nil, err
		}
		created = resp.JSON201
	}
	if created == nil {
		return nil, nil
	}
	return created.Meta, nil
}

func (s *store) updateSecret(ctx context.Context, id string, body api.SecretdtoUpdateSecretReq) (
	*api.BasedtoMeta, error,
) {
	p, sel := s.sel.Project.Id, s.sel
	var updated *api.SecretdtoUpdateSecretResp
	switch s.level {
	case scopeProject:
		resp, err := s.c.UpdateProjectSecretWithResponse(ctx, p, id, body)
		if err = client.Check(resp, err); err != nil {
			return nil, err
		}
		updated = resp.JSON200
	case scopeEnv:
		resp, err := s.c.UpdateProjectEnvSecretWithResponse(ctx, p, sel.Env, id, body)
		if err = client.Check(resp, err); err != nil {
			return nil, err
		}
		updated = resp.JSON200
	case scopeApp:
		resp, err := s.c.UpdateAppSecretWithResponse(ctx, p, sel.Env, sel.App.Id, id, body)
		if err = client.Check(resp, err); err != nil {
			return nil, err
		}
		updated = resp.JSON200
	}
	if updated == nil {
		return nil, nil
	}
	return updated.Meta, nil
}

func (s *store) deleteSecret(ctx context.Context, id string) error {
	p, sel := s.sel.Project.Id, s.sel
	switch s.level {
	case scopeProject:
		resp, err := s.c.DeleteProjectSecretWithResponse(ctx, p, id)
		return client.Check(resp, err)
	case scopeEnv:
		resp, err := s.c.DeleteProjectEnvSecretWithResponse(ctx, p, sel.Env, id)
		return client.Check(resp, err)
	case scopeApp:
	}
	resp, err := s.c.DeleteAppSecretWithResponse(ctx, p, sel.Env, sel.App.Id, id)
	return client.Check(resp, err)
}

// configFiles are the store's config files, those it inherits among them,
// page by page.
func (s *store) configFiles(ctx context.Context) ([]api.ConfigfiledtoConfigFileResp, error) {
	var all []api.ConfigfiledtoConfigFileResp
	for offset := 0; ; offset += storedPage {
		var (
			list *api.ConfigfiledtoListConfigFileResp
			err  error
		)
		p, sel := s.sel.Project.Id, s.sel
		switch s.level {
		case scopeProject:
			resp, e := s.c.ListProjectConfigFileWithResponse(ctx, p, nil, page(offset))
			err = client.Check(resp, e)
			if err == nil {
				list = resp.JSON200
			}
		case scopeEnv:
			resp, e := s.c.ListProjectEnvConfigFileWithResponse(ctx, p, sel.Env, nil, page(offset))
			err = client.Check(resp, e)
			if err == nil {
				list = resp.JSON200
			}
		case scopeApp:
			resp, e := s.c.ListAppConfigFileWithResponse(ctx, p, sel.Env, sel.App.Id, page(offset))
			err = client.Check(resp, e)
			if err == nil {
				list = resp.JSON200
			}
		}
		if err != nil {
			return nil, err
		}
		if list == nil {
			return all, nil
		}
		items := resolve.Deref(list.Data)
		all = append(all, items...)
		if len(items) < storedPage {
			return all, nil
		}
	}
}

func (s *store) createConfigFile(ctx context.Context, body api.ConfigfiledtoCreateConfigFileReq) (
	*api.BasedtoMeta, error,
) {
	p, sel := s.sel.Project.Id, s.sel
	var created *api.ConfigfiledtoCreateConfigFileResp
	switch s.level {
	case scopeProject:
		resp, err := s.c.CreateProjectConfigFileWithResponse(ctx, p, body)
		if err = client.Check(resp, err); err != nil {
			return nil, err
		}
		created = resp.JSON201
	case scopeEnv:
		resp, err := s.c.CreateProjectEnvConfigFileWithResponse(ctx, p, sel.Env, body)
		if err = client.Check(resp, err); err != nil {
			return nil, err
		}
		created = resp.JSON201
	case scopeApp:
		resp, err := s.c.CreateAppConfigFileWithResponse(ctx, p, sel.Env, sel.App.Id, body)
		if err = client.Check(resp, err); err != nil {
			return nil, err
		}
		created = resp.JSON201
	}
	if created == nil {
		return nil, nil
	}
	return created.Meta, nil
}

func (s *store) updateConfigFile(ctx context.Context, id string, body api.ConfigfiledtoUpdateConfigFileReq) (
	*api.BasedtoMeta, error,
) {
	p, sel := s.sel.Project.Id, s.sel
	var updated *api.ConfigfiledtoUpdateConfigFileResp
	switch s.level {
	case scopeProject:
		resp, err := s.c.UpdateProjectConfigFileWithResponse(ctx, p, id, body)
		if err = client.Check(resp, err); err != nil {
			return nil, err
		}
		updated = resp.JSON200
	case scopeEnv:
		resp, err := s.c.UpdateProjectEnvConfigFileWithResponse(ctx, p, sel.Env, id, body)
		if err = client.Check(resp, err); err != nil {
			return nil, err
		}
		updated = resp.JSON200
	case scopeApp:
		resp, err := s.c.UpdateAppConfigFileWithResponse(ctx, p, sel.Env, sel.App.Id, id, body)
		if err = client.Check(resp, err); err != nil {
			return nil, err
		}
		updated = resp.JSON200
	}
	if updated == nil {
		return nil, nil
	}
	return updated.Meta, nil
}

func (s *store) deleteConfigFile(ctx context.Context, id string) error {
	p, sel := s.sel.Project.Id, s.sel
	switch s.level {
	case scopeProject:
		resp, err := s.c.DeleteProjectConfigFileWithResponse(ctx, p, id)
		return client.Check(resp, err)
	case scopeEnv:
		resp, err := s.c.DeleteProjectEnvConfigFileWithResponse(ctx, p, sel.Env, id)
		return client.Check(resp, err)
	case scopeApp:
	}
	resp, err := s.c.DeleteAppConfigFileWithResponse(ctx, p, sel.Env, sel.App.Id, id)
	return client.Check(resp, err)
}
