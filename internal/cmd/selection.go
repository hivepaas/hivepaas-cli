package cmd

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/hivepaas/hivepaas-cli/internal/api"
	"github.com/hivepaas/hivepaas-cli/internal/client"
	"github.com/hivepaas/hivepaas-cli/internal/config"
	"github.com/hivepaas/hivepaas-cli/internal/exitcode"
	"github.com/hivepaas/hivepaas-cli/internal/link"
	"github.com/hivepaas/hivepaas-cli/internal/resolve"
)

// scope is how much of the target a command acts on.
type scope int

const (
	scopeProject scope = iota
	scopeEnv
	scopeApp
)

// selection is what a command acts on.
type selection struct {
	Project *api.ProjectdtoProjectResp
	Env     string
	App     *api.AppdtoAppResp

	// linked says which of the three the directory's link named.
	linked struct{ project, env, app bool }
}

// where is the app's place, as messages say it: api (shop / production).
func (s *selection) where() string {
	return fmt.Sprintf("%s (%s / %s)", s.App.Name, s.Project.Name, s.Env)
}

// flags are what a later command needs to act on the same target: none for
// what the directory's link names.
func (s *selection) flags() string {
	var parts []string
	if s.Project != nil && !s.linked.project {
		parts = append(parts, "-p "+shellWord(s.Project.Key))
	}
	if s.Env != "" && !s.linked.env {
		parts = append(parts, "-e "+shellWord(s.Env))
	}
	if s.App != nil && !s.linked.app {
		parts = append(parts, "-a "+shellWord(s.App.Key))
	}
	if len(parts) == 0 {
		return ""
	}
	return " " + strings.Join(parts, " ")
}

// shellWord is word as a shell reads it back.
func shellWord(word string) string {
	if word != "" && strings.IndexFunc(word, needsQuote) < 0 {
		return word
	}
	return "'" + strings.ReplaceAll(word, "'", `'\''`) + "'"
}

// selectTarget finds the project, the environment and the app a command acts
// on, as far as it needs: from the flags, else the environment variables, else
// the directory's link, else by asking - section 4 of the design.
func (a *App) selectTarget(ctx context.Context, c *client.Client, need scope) (*selection, error) {
	in, err := a.targetInputs(c)
	if err != nil {
		return nil, err
	}
	r := resolve.New(c)
	sel := &selection{}
	sel.linked.project, sel.linked.env, sel.linked.app = in.linked.project, in.linked.env, in.linked.app
	if in.project == "" {
		if in.project, err = a.chooseProject(ctx, r); err != nil {
			return nil, err
		}
	}
	if sel.Project, err = r.Project(ctx, in.project); err != nil {
		return nil, err
	}
	if need == scopeProject {
		return sel, nil
	}
	if sel.Env, err = a.selectEnv(ctx, sel.Project, in.env); err != nil {
		return nil, err
	}
	if need == scopeEnv {
		return sel, nil
	}
	if sel.App, err = a.selectApp(ctx, r, sel, in.app); err != nil {
		return nil, err
	}
	return sel, nil
}

// targetInputs are what names the target: the flags, else the environment
// variables, else the directory's link - which fills the project and its
// environment when no flag names another, and the app when the environment is
// the link's too, so that `-a web` in a linked directory means the link's
// environment's web.
type targetInputs struct {
	project, env, app string
	linked            struct{ project, env, app bool }
}

// needsQuote says a shell takes r for something other than itself.
func needsQuote(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		return false
	}
	return !strings.ContainsRune("-_.:/@", r)
}

func (a *App) targetInputs(c *client.Client) (targetInputs, error) {
	in := targetInputs{
		project: firstOf(a.project, a.getenv("HIVEPAAS_PROJECT")),
		env:     firstOf(a.env, a.getenv("HIVEPAAS_ENV")),
		app:     firstOf(a.app, a.getenv("HIVEPAAS_APP")),
	}
	if in.project != "" {
		return in, nil
	}
	lnk, err := a.findLink()
	if err != nil || lnk == nil {
		return in, err
	}
	if config.NormalizeURL(lnk.URL) != c.Target.URL {
		return in, exitcode.New(exitcode.Usage, "%s links this directory to %s, and this command talks to %s: "+
			"use the context of %s, or give -p, -e and -a", lnk.Path, lnk.URL, c.Target.URL, lnk.URL)
	}
	in.project, in.linked.project = lnk.Project.ID, true
	if in.env == "" {
		in.env, in.linked.env = lnk.Env, true
	}
	if in.app == "" && in.env == lnk.Env {
		in.app, in.linked.app = lnk.App.ID, true
	}
	return in, nil
}

func (a *App) selectEnv(ctx context.Context, project *api.ProjectdtoProjectResp, input string) (string, error) {
	if input == "" {
		envs := resolve.Deref(project.Envs)
		names := make([]string, 0, len(envs))
		for _, env := range envs {
			names = append(names, env.Name)
		}
		var err error
		if input, err = a.choose(ctx, "environment", "-e", names); err != nil {
			return "", err
		}
	}
	return resolve.Env(project, input)
}

func (a *App) selectApp(ctx context.Context, r *resolve.Resolver, sel *selection, input string) (
	*api.AppdtoAppResp, error,
) {
	if input == "" {
		apps, err := r.Apps(ctx, sel.Project.Id, sel.Env)
		if err != nil {
			return nil, err
		}
		names := make([]string, 0, len(apps))
		for _, app := range apps {
			names = append(names, app.Name)
		}
		if input, err = a.choose(ctx, "app", "-a", names); err != nil {
			return nil, err
		}
	}
	return r.App(ctx, sel.Project.Id, sel.Project.Name, sel.Env, input)
}

// selectTargetIgnoringLink is selectTarget without the directory's link: link
// asks for a new one.
func (a *App) selectTargetIgnoringLink(cmd *cobra.Command, c *client.Client) (*selection, error) {
	a.ignoreLink = true
	defer func() { a.ignoreLink = false }()
	return a.selectTarget(cmd.Context(), c, scopeApp)
}

func (a *App) findLink() (*link.Link, error) {
	if a.ignoreLink {
		return nil, nil
	}
	if a.linkFrom != "" {
		return link.Find(a.linkFrom)
	}
	wd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("finding the working directory: %w", err)
	}
	return link.Find(wd)
}

func (a *App) chooseProject(ctx context.Context, r *resolve.Resolver) (string, error) {
	if !a.interactive() {
		return "", missing("project", "-p")
	}
	projects, err := r.Projects(ctx)
	if err != nil {
		return "", err
	}
	names := make([]string, 0, len(projects))
	for _, project := range projects {
		names = append(names, project.Name)
	}
	return a.choose(ctx, "project", "-p", names)
}

// choose asks which of options, when it may: there is no choosing for a script.
func (a *App) choose(ctx context.Context, what, flag string, options []string) (string, error) {
	if len(options) == 1 && what == "environment" {
		return options[0], nil // the one environment is the one meant
	}
	if !a.interactive() {
		return "", missing(what, flag)
	}
	if len(options) == 0 {
		return "", exitcode.New(exitcode.NotFound, "there is no %s to choose from", what)
	}
	for i, option := range options {
		fmt.Fprintf(a.stderr, "  %d) %s\n", i+1, option)
	}
	answer, err := a.prompt(ctx, fmt.Sprintf("Which %s? [1-%d] ", what, len(options)), flag)
	if err != nil {
		return "", err
	}
	if n, convErr := strconv.Atoi(answer); convErr == nil && n >= 1 && n <= len(options) {
		return options[n-1], nil
	}
	return strings.TrimSpace(answer), nil
}

func missing(what, flag string) error {
	return exitcode.New(exitcode.Usage, "which %s? Give %s, or link this directory with `hivepaas link`", what, flag)
}

func firstOf(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
