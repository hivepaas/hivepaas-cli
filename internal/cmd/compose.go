package cmd

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/hivepaas/hivepaas-cli/internal/api"
	"github.com/hivepaas/hivepaas-cli/internal/client"
	"github.com/hivepaas/hivepaas-cli/internal/exitcode"
	"github.com/hivepaas/hivepaas-cli/internal/resolve"
)

// composeNames are the names a compose file is looked for by, as docker compose
// looks for them.
var composeNames = []string{"compose.yaml", "compose.yml", "docker-compose.yaml", "docker-compose.yml"}

// What the server takes of the files a compose file reads: the dashboard's
// limits, and the server's.
const (
	composeFilesMax     = 100
	composeFilesMaxSize = 5 << 20
	composeFileMaxSize  = 500 << 10
	// composeRounds is how many times a review is asked: each round gives what
	// the last one asked for.
	composeRounds = 4
	// defaultComposeEnv is the env a compose file's services go to.
	defaultComposeEnv = "production"
)

// What reads a file a compose file needs, as the server says it.
const (
	needDirectory = "directory"
	needCompose   = "compose"
)

type composeFlags struct {
	file, envFile, name string
	vars, profiles      []string
	noDeploy, yes       bool
	noWait              bool
	timeout             time.Duration
}

func (a *App) composeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "compose",
		Short: "Docker Compose files: their services as apps",
	}
	cmd.AddCommand(a.composeUpCmd())
	return cmd
}

func (a *App) composeUpCmd() *cobra.Command {
	var flags composeFlags
	cmd := &cobra.Command{
		Use:   "up",
		Short: "Make a project of a compose file - or add its services to one - and deploy them",
		Long: "Make a project of a Docker Compose file, its services as apps, or with -p add them to the\n" +
			"project's environment -e (production by default, made when missing), as the dashboard's import\n" +
			"does. The files it reads are sent from beside it; a variable with no value is asked, or given\n" +
			"with --var. The plan is shown first - each service as the app it becomes, then the issues -\n" +
			"and made once confirmed: asked at a terminal, --yes elsewhere. The apps are deployed and waited\n" +
			"for unless --no-deploy or --no-wait.",
		Example: "  hivepaas compose up\n" +
			"  hivepaas compose up -f stack/compose.yaml -p shop -e staging --var DB_PASSWORD=$DB_PASSWORD --yes",
		Args: usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.composeUp(cmd.Context(), flags)
		},
	}
	f := cmd.Flags()
	f.StringVarP(&flags.file, "file", "f", "", "the compose file; compose.yaml and the like here by default")
	f.StringVar(&flags.envFile, "env-file", "", "the .env file; the one beside the compose file by default")
	f.StringVar(&flags.name, "name", "", "a new project's name; the compose file's by default")
	f.StringArrayVar(&flags.vars, "var", nil, "a variable of the compose file, K=V; repeat for more")
	f.StringArrayVar(&flags.profiles, "profile", nil, "a profile whose services are made too; repeat for more")
	f.BoolVar(&flags.noDeploy, "no-deploy", false, "make the apps, and do not deploy them")
	f.BoolVarP(&flags.yes, "yes", "y", false, "do not ask: for a script")
	f.BoolVar(&flags.noWait, "no-wait", false, "do not wait for the deployments")
	f.DurationVar(&flags.timeout, "timeout", defaultDeployTimeout, "how long to wait for the deployments")
	return cmd
}

// composeInput is what a review is asked with, and the files that can give
// what it needs.
type composeInput struct {
	dir   string
	body  api.SpecdtoValidateComposeReq
	files map[string][]int
	vars  map[string]api.SpecdtoComposeVariableReq
}

func (a *App) composeUp(ctx context.Context, flags composeFlags) error {
	in, err := readCompose(flags)
	if err != nil {
		return err
	}
	if a.env != "" {
		in.body.Project.Env = a.env
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	var project *api.ProjectdtoProjectResp
	if a.project != "" {
		if project, err = resolve.New(c).Project(ctx, a.project); err != nil {
			return err
		}
		in.body.Project.NewEnv = !slices.ContainsFunc(resolve.Deref(project.Envs),
			func(e api.ProjectdtoProjectEnvResp) bool { return e.Name == in.body.Project.Env })
	}
	review, err := a.composeReview(ctx, c, project, in)
	if err != nil {
		return err
	}
	if a.printer.Structured() {
		if err = a.printer.Data(review); err != nil {
			return err
		}
	}
	if err = a.showComposePlan(review); err != nil {
		return err
	}
	if err = a.confirmYes(ctx, "Make it?", flags.yes); err != nil {
		return err
	}
	apply := api.SpecdtoApplyComposeReq{Compose: in.body.Compose, DotEnv: in.body.DotEnv, Files: in.body.Files,
		Variables: in.body.Variables, Project: in.body.Project, Profiles: in.body.Profiles, Deploy: in.body.Deploy,
		PlanHash: review.Plan.PlanHash, AcceptIssues: true}
	applied, err := a.composeApply(ctx, c, project, apply)
	if err != nil {
		return err
	}
	return a.composeMade(ctx, c, review, applied, flags)
}

// readCompose reads the compose file and the .env beside it, and the flags'
// variables.
func readCompose(flags composeFlags) (*composeInput, error) {
	file := flags.file
	if file == "" {
		for _, name := range composeNames {
			if _, err := os.Stat(name); err == nil {
				file = name
				break
			}
		}
		if file == "" {
			return nil, exitcode.New(exitcode.Usage, "no compose file here (%s): give -f",
				strings.Join(composeNames, ", "))
		}
	}
	compose, err := os.ReadFile(file) //nolint:gosec // the file the command line names
	if err != nil {
		return nil, exitcode.New(exitcode.Usage, "reading %s: %v", file, err)
	}
	dir := filepath.Dir(file)
	envFile, envGiven := flags.envFile, flags.envFile != ""
	if !envGiven {
		envFile = filepath.Join(dir, ".env")
	}
	dotEnv, err := os.ReadFile(envFile) //nolint:gosec // the .env the command line names, or beside the file
	if err != nil && (envGiven || !errors.Is(err, fs.ErrNotExist)) {
		return nil, exitcode.New(exitcode.Usage, "reading %s: %v", envFile, err)
	}
	pairs, err := keyValues(flags.vars)
	if err != nil {
		return nil, err
	}
	in := &composeInput{dir: dir, files: map[string][]int{}, vars: map[string]api.SpecdtoComposeVariableReq{}}
	for _, pair := range pairs {
		in.vars[pair[0]] = api.SpecdtoComposeVariableReq{Value: ptr(pair[1])}
	}
	in.body = api.SpecdtoValidateComposeReq{Compose: string(compose), DotEnv: string(dotEnv), Files: &in.files,
		Variables: &in.vars, Profiles: &flags.profiles, Deploy: !flags.noDeploy,
		Project: api.SpecdtoComposeProjectReq{Name: flags.name, Env: defaultComposeEnv}}
	return in, nil
}

// composeReview asks the server what the compose file would make, round after
// round - each giving the files and the variables the last one asked for -
// until it answers a plan.
func (a *App) composeReview(ctx context.Context, c *client.Client, project *api.ProjectdtoProjectResp,
	in *composeInput,
) (*api.SpecdtoValidateComposeData, error) {
	for range composeRounds {
		review, err := composeValidate(ctx, c, project, in.body)
		if err != nil {
			return nil, err
		}
		if review.Plan != nil {
			return review, nil
		}
		added, err := in.giveFiles(review, a)
		if err != nil {
			return nil, err
		}
		given, err := a.giveVariables(ctx, review, in)
		if err != nil {
			return nil, err
		}
		if !added && !given {
			return nil, exitcode.New(exitcode.Invalid, "the server makes no plan of the compose file, and says "+
				"nothing more it needs")
		}
	}
	return nil, exitcode.New(exitcode.Invalid, "the server makes no plan of the compose file after %d tries",
		composeRounds)
}

func composeValidate(ctx context.Context, c *client.Client, project *api.ProjectdtoProjectResp,
	body api.SpecdtoValidateComposeReq,
) (*api.SpecdtoValidateComposeData, error) {
	if project != nil {
		resp, err := c.ValidateProjectComposeWithResponse(ctx, project.Id, body)
		if err = client.Check(resp, err); err != nil {
			return nil, err
		}
		return resp.JSON200.Data, nil
	}
	resp, err := c.ValidateComposeWithResponse(ctx, body)
	if err = client.Check(resp, err); err != nil {
		return nil, err
	}
	return resp.JSON200.Data, nil
}

// giveFiles reads, from beside the compose file, the files the review says it
// reads and were not given; a directory's files when they fit. A compose file
// an include reads, missing, stops it.
func (in *composeInput) giveFiles(review *api.SpecdtoValidateComposeData, a *App) (bool, error) {
	added := false
	for _, need := range resolve.Deref(review.Needs) {
		if need.Given {
			continue
		}
		local := filepath.Join(in.dir, filepath.FromSlash(need.Path))
		if need.As == needDirectory {
			if in.giveDirectory(need.Path, local, a) {
				added = true
			}
			continue
		}
		if _, there := in.files[need.Path]; there {
			continue
		}
		data, err := os.ReadFile(local) //nolint:gosec // a file the compose file reads, beside it
		if err != nil {
			if need.As == needCompose {
				return false, exitcode.New(exitcode.Usage, "%s, which the compose file includes, is not there: %v",
					need.Path, err)
			}
			a.printer.Warnf("%s, which %s reads, is not there: it is left out", need.Path,
				strings.Join(resolve.Deref(need.By), ", "))
			continue
		}
		in.files[need.Path] = bytesOf(data)
		added = true
	}
	return added, nil
}

// giveDirectory gives the files under a directory a service mounts, when they
// fit what the server takes; it says so when they do not.
func (in *composeInput) giveDirectory(path, local string, a *App) bool {
	files := map[string][]int{}
	size := 0
	for _, f := range in.files {
		size += len(f)
	}
	fits := true
	err := filepath.WalkDir(local, func(p string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !entry.Type().IsRegular() {
			return err
		}
		rel, err := filepath.Rel(in.dir, p)
		if err != nil {
			return err //nolint:wrapcheck // the walk's own
		}
		data, err := os.ReadFile(p) //nolint:gosec // under a directory the compose file mounts
		if err != nil {
			return err //nolint:wrapcheck // said below
		}
		size += len(data)
		if len(data) > composeFileMaxSize || size > composeFilesMaxSize ||
			len(in.files)+len(files) >= composeFilesMax {
			fits = false
			return filepath.SkipAll
		}
		files[filepath.ToSlash(rel)] = bytesOf(data)
		return nil
	})
	if err != nil || !fits || len(files) == 0 {
		if err == nil && !fits {
			a.printer.Warnf("the files under %s are more than the server takes: they are left out", path)
		}
		return false
	}
	for name, data := range files {
		in.files[name] = data
	}
	return true
}

// countOf is n things, said: 1 app, 2 apps.
func countOf(n int, thing string) string {
	if n == 1 {
		return "1 " + thing
	}
	return fmt.Sprintf("%d %ss", n, thing)
}

// bytesOf is a file as the generated client sends it: the API's []byte.
func bytesOf(data []byte) []int {
	out := make([]int, len(data))
	for i, b := range data {
		out[i] = int(b)
	}
	return out
}

// giveVariables gives the required variables with no value: asked at a
// terminal - hidden for a secret - and refused elsewhere, all of them named.
func (a *App) giveVariables(ctx context.Context, review *api.SpecdtoValidateComposeData, in *composeInput) (
	bool, error,
) {
	var missing []api.ComposeserviceVariableView
	for _, v := range resolve.Deref(review.Variables) {
		if _, given := in.vars[v.Name]; v.Required && !v.Given && !given {
			missing = append(missing, v)
		}
	}
	if len(missing) == 0 {
		return false, nil
	}
	if !a.interactive() {
		names := make([]string, 0, len(missing))
		for _, v := range missing {
			names = append(names, v.Name)
		}
		return false, exitcode.New(exitcode.Usage, "the compose file needs %s: give --var %s=...",
			strings.Join(names, ", "), names[0])
	}
	for _, v := range missing {
		ask := a.prompt
		if v.Secret {
			ask = a.promptSecret
		}
		value, err := ask(ctx, v.Name+": ", "--var "+v.Name+"=...")
		if err != nil {
			return false, err
		}
		in.vars[v.Name] = api.SpecdtoComposeVariableReq{Value: &value}
	}
	return true, nil
}

// showComposePlan says what the plan makes - each service as its app, how its
// ports are reached - then its issues; a blocking one stops it.
func (a *App) showComposePlan(review *api.SpecdtoValidateComposeData) error {
	if p := review.Project; p != nil {
		where := p.Name
		if p.Id == "" {
			where += " (new)"
		}
		env := p.Env
		if p.NewEnv {
			env += " (new)"
		}
		a.printer.Infof("Project %s, environment %s", where, env)
	}
	for _, svc := range resolve.Deref(review.Services) {
		a.printer.Infof("%s", serviceLine(svc))
		for _, port := range resolve.Deref(svc.Ports) {
			a.printer.Infof("    %s", portLine(port))
		}
		if dropped := resolve.Deref(svc.Dropped); len(dropped) > 0 {
			a.printer.Infof("    left out: %s", strings.Join(dropped, ", "))
		}
	}
	blocked := 0
	for _, node := range resolve.Deref(review.Plan.Nodes) {
		for _, issue := range append(resolve.Deref(node.Issues), resolve.Deref(node.Notes)...) {
			if issue.Severity == api.SeverityBlocked {
				blocked++
			}
			line := fmt.Sprintf("  %-8s %s  %s", issue.Severity, issue.Code, issue.Path)
			if issue.Hint != nil && *issue.Hint != "" {
				line += ": " + *issue.Hint
			}
			a.printer.Infof("%s", line)
		}
	}
	if blocked > 0 {
		return exitcode.New(exitcode.Invalid, "the plan has %s blocking it: nothing was made",
			countOf(blocked, "issue"))
	}
	return nil
}

// serviceLine is a service as the app it becomes.
func serviceLine(svc api.ComposeserviceServiceView) string {
	if svc.Skipped {
		return fmt.Sprintf("  %s: left out, %s", svc.Name, svc.Reason)
	}
	source := svc.Image
	if svc.Build && source == "" {
		source = "built from its source"
	}
	line := fmt.Sprintf("  %s -> app %s: %s", svc.Name, svc.App, source)
	if svc.UseExisting || svc.Existing != "" {
		line += ", into the existing app"
	}
	return line
}

// portLine is how a port of a service is reached.
func portLine(port api.ComposeservicePortView) string {
	spec := fmt.Sprintf("%d/%s", port.Target, port.Protocol)
	if port.Published > 0 {
		spec = fmt.Sprintf("%d:%s", port.Published, spec)
	}
	switch port.As {
	case api.PortAsDomain:
		return spec + " at https://" + firstOf(port.Domain, port.Suggested)
	case api.PortAsNode:
		return fmt.Sprintf("%s published on the nodes' port %d", spec, port.Published)
	case api.PortAsNone:
	}
	return spec + " not reached from outside"
}

// confirmYes asks a yes or no, unless yes; a script gives --yes.
func (a *App) confirmYes(ctx context.Context, question string, yes bool) error {
	if yes {
		return nil
	}
	if !a.interactive() {
		return exitcode.New(exitcode.Usage, "give --yes to go on without being asked")
	}
	answer, err := a.prompt(ctx, question+" [y/N] ", "--yes")
	if err != nil {
		return err
	}
	if a := strings.ToLower(answer); a != "y" && a != "yes" {
		return exitcode.New(exitcode.Failure, "nothing was made")
	}
	return nil
}

func (a *App) composeApply(ctx context.Context, c *client.Client, project *api.ProjectdtoProjectResp,
	body api.SpecdtoApplyComposeReq,
) (*api.SpecdtoApplyComposeData, error) {
	var data *api.SpecdtoApplyComposeData
	if project != nil {
		resp, err := c.ApplyProjectComposeWithResponse(ctx, project.Id, body)
		if err = client.Check(resp, err); err != nil {
			return nil, err
		}
		data = resp.JSON200.Data
	} else {
		resp, err := c.ApplyComposeWithResponse(ctx, body)
		if err = client.Check(resp, err); err != nil {
			return nil, err
		}
		data = resp.JSON200.Data
	}
	if data == nil || data.Project == nil {
		return nil, exitcode.New(exitcode.Server, "the server answered nothing made")
	}
	return data, nil
}

// composeMade says what was made, and waits for the deployments queued.
func (a *App) composeMade(ctx context.Context, c *client.Client, review *api.SpecdtoValidateComposeData,
	applied *api.SpecdtoApplyComposeData, flags composeFlags,
) error {
	apps := resolve.Deref(applied.Apps)
	names := map[string]string{}
	for _, app := range apps {
		names[app.Id] = app.App
	}
	project := &api.ProjectdtoProjectResp{Id: applied.Project.Id, Name: review.Project.Name}
	a.printer.Successf("Made %s in %s / %s.", countOf(len(apps), "app"), project.Name, review.Project.Env)
	deployments := resolve.Deref(applied.Deployments)
	if len(deployments) == 0 || flags.noWait {
		return nil
	}
	refs := make([]deploymentRef, 0, len(deployments))
	for _, d := range deployments {
		sel := &selection{Project: project, Env: review.Project.Env,
			App: &api.AppdtoAppResp{Id: d.AppId, Key: names[d.AppId], Name: names[d.AppId]}}
		refs = append(refs, deploymentRef{sel: sel, id: d.DeploymentId})
		a.printer.Infof("Deploying %s, deployment %s", names[d.AppId], d.DeploymentId)
	}
	waitCtx, cancel := context.WithTimeout(ctx, flags.timeout)
	defer cancel()
	return a.waitForCreated(ctx, waitCtx, c, refs, flags.timeout)
}
