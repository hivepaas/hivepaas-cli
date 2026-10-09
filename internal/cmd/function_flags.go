package cmd

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/hivepaas/hivepaas-cli/internal/api"
	"github.com/hivepaas/hivepaas-cli/internal/client"
	"github.com/hivepaas/hivepaas-cli/internal/exitcode"
	"github.com/hivepaas/hivepaas-cli/internal/funccode"
	"github.com/hivepaas/hivepaas-cli/internal/resolve"
)

// A call's timeout, as the server bounds it.
const (
	callTimeoutMin = time.Second
	callTimeoutMax = 15 * time.Minute
)

// The names of flags a function's repository takes.
const (
	flagRepo         = "repo"
	flagPath         = "path"
	flagAutoDeploy   = "auto-deploy"
	flagNoAutoDeploy = "no-auto-deploy"
)

// The flags of a function's code from a repository.
var functionRepoFlagNames = []string{flagRepo, "ref", "commit", flagPath, "git-credential", flagAutoDeploy,
	flagNoAutoDeploy}

// functionFlags are the flags of a function's settings, and of its code from a
// repository, which create and deploy take.
type functionFlags struct {
	cmd *cobra.Command

	entrypoint, handler, callTimeout, maxBody, packages, pushTo string
	maxConcurrency                                              int

	repo, ref, commit, path, gitCredential string
	autoDeploy, noAutoDeploy               bool

	list bool
}

func addFunctionFlags(cmd *cobra.Command) *functionFlags {
	fl := &functionFlags{cmd: cmd}
	f := cmd.Flags()
	f.StringVar(&fl.entrypoint, "entrypoint", "", "the file the handler is in; for Go, its package's directory")
	f.StringVar(&fl.handler, "handler", "", "the handler's name: default, handler, Handle")
	f.StringVar(&fl.callTimeout, "call-timeout", "", "how long one call may take: 30s, 2m; 1s to 15m")
	f.IntVar(&fl.maxConcurrency, "max-concurrency", 0, "the calls one replica takes at once")
	f.StringVar(&fl.maxBody, "max-body", "", "the largest request body: 6MB")
	f.StringVar(&fl.packages, "packages", "", `Debian packages the runtime image gets: curl,jq; "" for none`)
	f.StringVar(&fl.pushTo, "push-to", "", "push the built image to the registry of this credential; none keeps it "+
		"on the node")
	f.StringVar(&fl.repo, flagRepo, "", "build the function from this git repository")
	f.StringVar(&fl.ref, "ref", "", "the branch to build, or tags/NAME for a tag")
	f.StringVar(&fl.commit, "commit", "", "build this commit, its full SHA, and stay at it; none to follow the ref")
	f.StringVar(&fl.path, flagPath, "", "the function's directory in the repository")
	f.StringVar(&fl.gitCredential, "git-credential", "", "clone with this git credential; none for none")
	f.BoolVar(&fl.autoDeploy, flagAutoDeploy, false, "deploy each push to the ref")
	f.BoolVar(&fl.noAutoDeploy, flagNoAutoDeploy, false, "do not deploy on push")
	f.BoolVar(&fl.list, "list", false, "show the files that would be sent, and send nothing")
	cmd.MarkFlagsMutuallyExclusive(flagAutoDeploy, flagNoAutoDeploy)
	return fl
}

func (fl *functionFlags) changed(name string) bool { return fl.cmd.Flags().Changed(name) }

// repoGiven are the repository's flags the command line gives.
func (fl *functionFlags) repoGiven() []string {
	var given []string
	for _, name := range functionRepoFlagNames {
		if fl.changed(name) {
			given = append(given, "--"+name)
		}
	}
	return given
}

// functionRefs are the credentials the flags name, found: nil for one not given.
type functionRefs struct {
	pushTo, gitCredential *settingRef
}

// refs finds the credentials the flags name among the environment's.
func (fl *functionFlags) refs(ctx context.Context, c *client.Client, sel *selection) (functionRefs, error) {
	var refs functionRefs
	r := resolve.New(c)
	where := fmt.Sprintf(" in %s / %s", sel.Project.Name, sel.Env)
	if fl.changed("push-to") {
		ref, err := registryCredential(ctx, r, sel, where, fl.pushTo)
		if err != nil {
			return refs, err
		}
		refs.pushTo = ref
	}
	if fl.changed("git-credential") {
		ref, err := gitCredential(ctx, r, sel, where, fl.gitCredential)
		if err != nil {
			return refs, err
		}
		refs.gitCredential = ref
	}
	return refs, nil
}

// check refuses what the server would, before anything is asked of it.
func (fl *functionFlags) check() error {
	if fl.changed("call-timeout") {
		d, err := time.ParseDuration(fl.callTimeout)
		if err != nil || d < callTimeoutMin || d > callTimeoutMax {
			return exitcode.New(exitcode.Usage, "--call-timeout takes a duration from 1s to 15m, not %q",
				fl.callTimeout)
		}
	}
	if fl.changed("max-concurrency") && fl.maxConcurrency < 1 {
		return exitcode.New(exitcode.Usage, "--max-concurrency takes 1 or more, not %d", fl.maxConcurrency)
	}
	return nil
}

// applySettings changes a function's settings as the flags ask, saying each
// change; was is the push credential there.
func (fl *functionFlags) applySettings(src *api.AppsettingsdtoDeploymentFunctionSourceReq, was settingRef,
	refs functionRefs, ch *changes,
) {
	text := func(name, value string) *string {
		if !fl.changed(name) {
			return nil
		}
		return &value
	}
	ch.text("Entrypoint", &src.Entrypoint.File, text("entrypoint", fl.entrypoint))
	ch.text("Handler", &src.Entrypoint.Handler, text("handler", fl.handler))
	if fl.changed("call-timeout") {
		d, _ := time.ParseDuration(fl.callTimeout) // checked
		if was, err := time.ParseDuration(src.Timeout); err != nil || was != d {
			ch.note("Call timeout", src.Timeout, fl.callTimeout)
			src.Timeout = fl.callTimeout
		}
	}
	if fl.changed("max-concurrency") && fl.maxConcurrency != src.MaxConcurrency {
		ch.note("Max concurrency", strconv.Itoa(src.MaxConcurrency), strconv.Itoa(fl.maxConcurrency))
		src.MaxConcurrency = fl.maxConcurrency
	}
	if fl.changed("max-body") && !strings.EqualFold(fl.maxBody, src.MaxBodySize) {
		ch.note("Max body", src.MaxBodySize, fl.maxBody)
		src.MaxBodySize = fl.maxBody
	}
	if fl.changed("packages") {
		packages := splitList(fl.packages)
		if was := resolve.Deref(src.SystemPackages); !slices.Equal(was, packages) {
			ch.note("Packages", orNone(strings.Join(was, ", ")), orNone(strings.Join(packages, ", ")))
			src.SystemPackages = &packages
		}
	}
	ch.ref("Push to", &src.PushToRegistry, was, refs.pushTo)
}

// splitList is a comma-separated list, its items trimmed, the empty ones left out.
func splitList(list string) []string {
	items := []string{}
	for item := range strings.SplitSeq(list, ",") {
		if item = strings.TrimSpace(item); item != "" && !slices.Contains(items, item) {
			items = append(items, item)
		}
	}
	return items
}

// applyRepo has a function's code built from a repository as the flags ask;
// was is the repository's as the settings answer it, nil for none.
func (fl *functionFlags) applyRepo(app string, code *api.AppsettingsdtoFunctionCodeReq,
	was *api.AppsettingsdtoFunctionRepoCodeResp, refs functionRefs, ch *changes,
) error {
	if code.Repo == nil {
		if !fl.changed(flagRepo) {
			return exitcode.New(exitcode.Usage, "%s has no repository to build: give --repo", app)
		}
		// As the dashboard's form starts one.
		code.Repo = &api.AppsettingsdtoFunctionRepoCodeReq{RepoType: api.RepoTypeGit}
		code.Inline = nil
		ch.lines = append(ch.lines, "Code: inline -> a repository")
	}
	repo := code.Repo
	moved := false
	if fl.changed(flagRepo) && fl.repo != repo.RepoURL {
		ch.note("Repository", repo.RepoURL, fl.repo)
		repo.RepoURL, moved = fl.repo, true
	}
	if fl.changed("ref") && normalRef(fl.ref) != normalRef(repo.RepoRef) {
		ch.note("Ref", refWords(repo.RepoRef), refWords(fl.ref))
		repo.RepoRef, moved = fl.ref, true
	}
	commit := fl.commit
	if strings.EqualFold(commit, none) {
		commit = ""
	}
	switch {
	case fl.changed("commit") && !strings.EqualFold(commit, repo.CommitHash):
		ch.lines = append(ch.lines, "Commit: "+orNone(repo.CommitHash)+" -> "+orNone(commit))
		repo.CommitHash = commit
	case moved && !fl.changed("commit") && repo.CommitHash != "":
		// A pinned commit is of the repository and the ref it was pinned on.
		ch.lines = append(ch.lines, "Commit: "+repo.CommitHash+" -> none, following "+refWords(repo.RepoRef))
		repo.CommitHash = ""
	}
	if fl.changed(flagPath) && fl.path != code.Dir {
		ch.note("Directory", orNone(code.Dir), orNone(fl.path))
		code.Dir = fl.path
	}
	var wasCred settingRef
	if was != nil {
		wasCred = refOf(was.Credentials)
	}
	ch.ref("Git credential", &repo.Credentials, wasCred, refs.gitCredential)
	switch {
	case fl.changed(flagAutoDeploy) && !autoDeploys(repo.AutoDeploy):
		ch.lines = append(ch.lines, "Auto-deploy: off -> on")
		repo.AutoDeploy = ptr(true)
	case fl.changed(flagNoAutoDeploy) && autoDeploys(repo.AutoDeploy):
		ch.lines = append(ch.lines, "Auto-deploy: on -> off")
		repo.AutoDeploy = ptr(false)
	}
	return nil
}

// applyInline has a function's code be files, whole, saying what changed.
func applyInline(code *api.AppsettingsdtoFunctionCodeReq, files []funccode.File, ch *changes) {
	was := map[string]string{}
	if code.Inline != nil {
		for _, f := range resolve.Deref(code.Inline.Files) {
			was[f.Path] = f.Content
		}
	}
	if code.Repo != nil {
		ch.lines = append(ch.lines, "Code: a repository -> inline")
		was = map[string]string{}
	}
	var changed, added []string
	sent := make([]api.AppsettingsdtoFunctionFileReq, 0, len(files))
	for _, f := range files {
		sent = append(sent, api.AppsettingsdtoFunctionFileReq{Path: f.Path, Content: f.Content})
		content, there := was[f.Path]
		switch {
		case !there:
			added = append(added, f.Path)
		case content != f.Content:
			changed = append(changed, f.Path)
		}
		delete(was, f.Path)
	}
	removed := slices.Sorted(maps.Keys(was))
	var parts []string
	for _, group := range []struct {
		paths []string
		verb  string
	}{{changed, "changed"}, {added, "added"}, {removed, "removed"}} {
		if len(group.paths) > 0 {
			parts = append(parts, pathsWords(group.paths)+" "+group.verb)
		}
	}
	if code.Repo == nil && len(parts) > 0 {
		ch.lines = append(ch.lines, "Code: "+strings.Join(parts, ", "))
	}
	code.Inline = &api.AppsettingsdtoFunctionInlineCodeReq{Files: &sent}
	code.Repo, code.Dir = nil, ""
}

// pathsShown is how many paths a change names before it counts the rest.
const pathsShown = 5

func pathsWords(paths []string) string {
	if len(paths) <= pathsShown {
		return strings.Join(paths, ", ")
	}
	return fmt.Sprintf("%s and %d more", strings.Join(paths[:pathsShown], ", "), len(paths)-pathsShown)
}
