package cmd

import (
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/hivepaas/hivepaas-cli/internal/api"
	"github.com/hivepaas/hivepaas-cli/internal/client"
	"github.com/hivepaas/hivepaas-cli/internal/exitcode"
	"github.com/hivepaas/hivepaas-cli/internal/funccode"
	"github.com/hivepaas/hivepaas-cli/internal/output"
	"github.com/hivepaas/hivepaas-cli/internal/resolve"
)

// codeFileMode is the mode of a file of a function's code the CLI writes.
const codeFileMode = 0o644

func (a *App) functionCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "function",
		Aliases: []string{"fn", "functions"},
		Short:   "Functions: code HivePaaS runs on a runtime, a handler answering one request",
		Long: "Functions: code HivePaaS runs on one of its runtimes - node24, bun1, python313, go127 - a handler\n" +
			"that answers one request. The code is a directory's here: init writes a runtime's starter code,\n" +
			"create makes a function of it, deploy sends it again, run calls it once before it is deployed.\n" +
			"A function's logs and domains are its app's: hivepaas logs, hivepaas domain.",
	}
	cmd.AddCommand(a.functionInitCmd(), a.functionCreateCmd(), a.functionDeployCmd(), a.functionRunCmd(),
		a.functionPullCmd(), a.functionGetCmd(), a.functionLsCmd(), a.functionMetricsCmd())
	return cmd
}

// dirWords is a directory as a message says it: this directory, for ".".
func dirWords(dir string) string {
	if filepath.Clean(dir) == "." {
		return "this directory"
	}
	return dir
}

// dirArg is the directory a command's argument names, "." when none.
func dirArg(args []string) string {
	if len(args) == 0 {
		return "."
	}
	return args[0]
}

// linkFromDir has the commands' target found from dir's link, not the working
// directory's: `function deploy ./fn` from anywhere deploys what fn is linked to.
func (a *App) linkFromDir(dir string) error {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return fmt.Errorf("finding %s: %w", dir, err)
	}
	a.linkFrom = abs
	return nil
}

func (a *App) functionInitCmd() *cobra.Command {
	var runtime string
	var typeScript, force bool
	cmd := &cobra.Command{
		Use:   "init [DIR]",
		Short: "Write a runtime's starter code into a directory",
		Long: "Write a runtime's starter code into DIR, . by default, made if missing: a handler answering\n" +
			"?name=Ada with {\"hello\":\"Ada\"}, as the dashboard starts a function. A file there already is\n" +
			"not overwritten unless --force.",
		Example: "  hivepaas function init hello --runtime node24\n" +
			"  hivepaas function create hello hello -p shop -e staging --runtime node24",
		Args: usageArgs(cobra.MaximumNArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.functionInit(cmd.Context(), dirArg(args), runtime, typeScript, force)
		},
	}
	cmd.Flags().StringVar(&runtime, "runtime", "", "node24, bun1, python313 or go127")
	cmd.Flags().BoolVar(&typeScript, "typescript", false, "Node.js's starter code in TypeScript")
	cmd.Flags().BoolVar(&force, "force", false, "overwrite the files there")
	return cmd
}

func (a *App) functionInit(ctx context.Context, dir, runtime string, typeScript, force bool) error {
	runtime, err := a.runtimeOf(ctx, runtime)
	if err != nil {
		return err
	}
	files, _, err := funccode.Template(runtime, typeScript)
	if err != nil {
		return err
	}
	if err = writeCode(dir, files, force); err != nil {
		return err
	}
	a.printer.Successf("Wrote %s's starter code into %s.", runtime, dir)
	a.printer.Infof("  Make a function of it:  hivepaas function create <name> %s --runtime %s -p <project> "+
		"-e <env>", shellWord(dir), runtime)
	return nil
}

// runtimeOf is the runtime --runtime names, else the one a terminal is asked.
func (a *App) runtimeOf(ctx context.Context, runtime string) (string, error) {
	if runtime == "" {
		var err error
		if runtime, err = a.choose(ctx, "runtime", "--runtime", funccode.Runtimes); err != nil {
			return "", err
		}
	}
	return runtime, funccode.CheckRuntime(runtime)
}

// writeCode writes files into dir, made if missing. A file there with other
// content stops it before anything is written, unless force.
func writeCode(dir string, files []funccode.File, force bool) error {
	if !force {
		var there []string
		for _, f := range files {
			data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(f.Path)))
			if err == nil && string(data) != f.Content {
				there = append(there, f.Path)
			}
		}
		if len(there) > 0 {
			return exitcode.New(exitcode.Failure, "%s has %s already: --force overwrites it", dir,
				strings.Join(there, ", "))
		}
	}
	for _, f := range files {
		path := filepath.Join(dir, filepath.FromSlash(f.Path))
		if err := os.MkdirAll(filepath.Dir(path), dirMode); err != nil {
			return fmt.Errorf("making %s: %w", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, []byte(f.Content), codeFileMode); err != nil { //nolint:gosec // code, no secret
			return fmt.Errorf("writing %s: %w", path, err)
		}
	}
	return nil
}

// dirMode is the mode of a directory the CLI makes for code.
const dirMode = 0o755

// collectCode reads dir's code, saying the links it left out.
func (a *App) collectCode(dir string) (*funccode.Code, error) {
	code, err := funccode.Collect(dir)
	if err != nil {
		return nil, err
	}
	for _, l := range code.Links {
		a.printer.Warnf("%s is a link: left out", l)
	}
	for _, f := range code.LeftOut {
		a.printer.Warnf("%s left out: a function's variables are its app's - hivepaas env set, hivepaas secret set", f)
	}
	return code, nil
}

// checkEntrypoint refuses code without the handler's file: the deployment would
// fail on it. A file there that the .gitignore left out is said.
func checkEntrypoint(dir, runtime, entry string, code *funccode.Code) error {
	missing := funccode.MissingEntrypoint(runtime, entry, code.Files)
	if missing == "" {
		return nil
	}
	if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(missing))); err == nil {
		return exitcode.New(exitcode.Invalid, "%s is the handler's file, and %s's .gitignore leaves it out",
			missing, dirWords(dir))
	}
	return exitcode.New(exitcode.Invalid, "%s has no %s, the handler's file: --entrypoint names another",
		dirWords(dir), missing)
}

// listCode shows the files a command would send, and their size.
func (a *App) listCode(code *funccode.Code) error {
	type listed struct {
		Path string `json:"path"`
		Size int    `json:"size"`
	}
	out := make([]listed, 0, len(code.Files))
	rows := make([][]string, 0, len(code.Files))
	for _, f := range code.Files {
		out = append(out, listed{Path: f.Path, Size: len(f.Content)})
		rows = append(rows, []string{f.Path, sizeOf(int64(len(f.Content)))})
	}
	if a.printer.Structured() {
		return a.printer.Data(out)
	}
	if err := a.printer.Table([]string{"PATH", colSize}, rows); err != nil {
		return err
	}
	a.printer.Infof("%d files, %s.", len(code.Files), sizeOf(code.Size))
	return nil
}

// sizeOf is a size as people say it.
func sizeOf(n int64) string {
	const kb, mb = 1 << 10, 1 << 20
	switch {
	case n >= mb:
		return fmt.Sprintf("%.1f MB", float64(n)/mb)
	case n >= kb:
		return fmt.Sprintf("%.1f KB", float64(n)/kb)
	}
	return fmt.Sprintf("%d B", n)
}

// functionSettings are a function's deployment settings; an app that is not a
// function is refused.
func functionSettings(ctx context.Context, c *client.Client, sel *selection) (
	*api.AppsettingsdtoDeploymentSettingsResp, error,
) {
	resp, err := c.GetAppDeploymentSettingsWithResponse(ctx, sel.Project.Id, sel.Env, sel.App.Id)
	if err = client.Check(resp, err); err != nil {
		return nil, err
	}
	var settings *api.AppsettingsdtoDeploymentSettingsResp
	if resp.JSON200 != nil {
		settings = resp.JSON200.Data
	}
	if err = isFunction(sel.App.Name, settings); err != nil {
		return nil, err
	}
	return settings, nil
}

// isFunction refuses deployment settings that are not a function's.
func isFunction(app string, settings *api.AppsettingsdtoDeploymentSettingsResp) error {
	if settings == nil || settings.ActiveMethod != api.DeploymentMethodFunction || settings.FunctionSource == nil {
		return exitcode.New(exitcode.Usage, "%s is not a function: hivepaas deploy deploys it", app)
	}
	return nil
}

// inlineFiles are a function's inline code; nil for code from a repository.
func inlineFiles(src *api.AppsettingsdtoDeploymentFunctionSourceResp) []api.AppsettingsdtoFunctionFileResp {
	if src.Code == nil || src.Code.Inline == nil {
		return nil
	}
	return resolve.Deref(src.Code.Inline.Files)
}

// repoOfFunction is a function's repository; nil for inline code.
func repoOfFunction(src *api.AppsettingsdtoDeploymentFunctionSourceResp) *api.AppsettingsdtoFunctionRepoCodeResp {
	if src.Code == nil {
		return nil
	}
	return src.Code.Repo
}

// repoWords is where a function's code is in its repository: URL, ref, commit
// and directory.
func repoWords(src *api.AppsettingsdtoDeploymentFunctionSourceResp) string {
	repo := repoOfFunction(src)
	if repo == nil {
		return ""
	}
	words := repo.RepoURL + " at " + refWords(repo.RepoRef)
	if repo.CommitHash != "" {
		words += ", commit " + repo.CommitHash
	}
	if dir := textOf(src.Code.Dir); dir != "" {
		words += ", in " + dir
	}
	return words
}

func (a *App) functionPullCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "pull [DIR]",
		Short: "Write a function's code, as kept on the server, into a directory",
		Long: "Write a function's code as the server keeps it - code written in the dashboard - into DIR, . by\n" +
			"default, made if missing. A file there with other content stops it, unless --force.",
		Args: usageArgs(cobra.MaximumNArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			dir := dirArg(args)
			if err := a.linkFromDir(dir); err != nil {
				return err
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
			src := settings.FunctionSource
			if repoOfFunction(src) != nil {
				return exitcode.New(exitcode.Usage, "%s's code is in %s: clone it", sel.App.Name, repoWords(src))
			}
			stored := inlineFiles(src)
			files := make([]funccode.File, 0, len(stored))
			for _, f := range stored {
				files = append(files, funccode.File{Path: f.Path, Content: f.Content})
			}
			if err = writeCode(dir, files, force); err != nil {
				return err
			}
			a.printer.Successf("Wrote %d files of %s into %s.", len(files), sel.App.Name, dir)
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "overwrite the files there")
	return cmd
}

func (a *App) functionGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "get [FUNCTION]",
		Short: "Show a function: its runtime, code and limits",
		Args:  usageArgs(cobra.MaximumNArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 {
				a.app = args[0]
			}
			ctx := cmd.Context()
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
			if a.printer.Structured() {
				return a.printer.Data(settings.FunctionSource)
			}
			fmt.Fprintf(a.stdout, "%s %s\n", sel.App.Name, a.printer.Dim(sel.App.Id))
			for _, row := range functionRows(settings.FunctionSource) {
				fmt.Fprintf(a.stdout, "%-17s%s\n", row[0]+":", row[1])
			}
			return nil
		},
	}
}

// functionRows are a function's settings as get shows them.
func functionRows(src *api.AppsettingsdtoDeploymentFunctionSourceResp) [][2]string {
	entry := "-"
	if src.Entrypoint != nil {
		entry = src.Entrypoint.File + ", handler " + src.Entrypoint.Handler
	}
	code := repoWords(src)
	if code == "" {
		files := inlineFiles(src)
		size := 0
		for _, f := range files {
			size += len(f.Content)
		}
		code = fmt.Sprintf("%d files, %s, inline", len(files), sizeOf(int64(size)))
	}
	packages := strings.Join(resolve.Deref(src.SystemPackages), ", ")
	return [][2]string{
		{"Runtime", string(src.Runtime)},
		{"Entrypoint", entry},
		{"Code", code},
		{"Packages", orNone(packages)},
		{"Call timeout", src.Timeout},
		{"Max concurrency", strconv.Itoa(src.MaxConcurrency)},
		{"Max body", src.MaxBodySize},
		{"Push to", refOf(src.PushToRegistry).String()},
	}
}

func (a *App) functionLsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "ls",
		Short: "List an environment's functions",
		Args:  usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			sel, err := a.selectTarget(cmd.Context(), c, scopeEnv)
			if err != nil {
				return err
			}
			apps, err := resolve.New(c).Apps(cmd.Context(), sel.Project.Id, sel.Env)
			if err != nil {
				return err
			}
			functions := slices.DeleteFunc(apps, func(app api.AppdtoAppResp) bool {
				return app.Category == nil || *app.Category != api.AppCategoryFunction
			})
			if a.printer.Structured() {
				return a.printer.Data(functions)
			}
			now := time.Now()
			rows := make([][]string, 0, len(functions))
			for _, app := range functions {
				rows = append(rows, []string{app.Name, app.Key, string(app.Status), output.Ago(app.UpdatedAt, now)})
			}
			return a.printer.Table([]string{colName, colKey, colStatus, colUpdated}, rows)
		},
	}
}

// metricsRanges are the ranges the server answers metrics over.
var metricsRanges = []string{"1h", "6h", "24h", "7d"}

func (a *App) functionMetricsCmd() *cobra.Command {
	var span string
	cmd := &cobra.Command{
		Use:   "metrics [FUNCTION]",
		Short: "Show a function's calls: how many, how they ended, how long they took",
		Args:  usageArgs(cobra.MaximumNArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !slices.Contains(metricsRanges, span) {
				return exitcode.New(exitcode.Usage, "--range takes %s, not %q", strings.Join(metricsRanges, ", "), span)
			}
			if len(args) == 1 {
				a.app = args[0]
			}
			return a.functionMetrics(cmd.Context(), span)
		},
	}
	cmd.Flags().StringVar(&span, "range", "24h", "1h, 6h, 24h or 7d")
	return cmd
}

func (a *App) functionMetrics(ctx context.Context, span string) error {
	c, err := a.client()
	if err != nil {
		return err
	}
	sel, err := a.selectTarget(ctx, c, scopeApp)
	if err != nil {
		return err
	}
	resp, err := c.GetFunctionMetricsWithResponse(ctx, sel.Project.Id, sel.Env, sel.App.Id,
		&api.GetFunctionMetricsParams{Range: &span})
	if err = client.Check(resp, err); err != nil {
		return err
	}
	m := resp.JSON200.Data
	if a.printer.Structured() {
		return a.printer.Data(m)
	}
	if m == nil || !m.Available {
		reason := "the server has none"
		if m != nil && m.Reason != nil {
			reason = *m.Reason
		}
		a.printer.Infof("No metrics of %s: %s.", sel.App.Name, reason)
		return nil
	}
	if t := m.Totals; t != nil {
		fmt.Fprintf(a.stdout, "Over %s: %d calls, %d failed, %d 4xx, %d 5xx; p50 %s, p95 %s, p99 %s\n", m.Range,
			t.Calls, t.Failed, t.Errors4xx, t.Errors5xx, millis(t.P50), millis(t.P95), millis(t.P99))
	}
	if m.ByOutcome != nil && len(*m.ByOutcome) > 0 {
		outcomes := *m.ByOutcome
		names := slices.Sorted(maps.Keys(outcomes))
		parts := make([]string, 0, len(names))
		for _, name := range names {
			parts = append(parts, fmt.Sprintf("%s %d", name, outcomes[name]))
		}
		fmt.Fprintf(a.stdout, "Outcomes: %s\n", strings.Join(parts, ", "))
	}
	paths := resolve.Deref(m.ByPath)
	if len(paths) == 0 {
		return nil
	}
	fmt.Fprintln(a.stdout)
	rows := make([][]string, 0, len(paths))
	for _, p := range paths {
		rows = append(rows, []string{p.Method, p.Path, strconv.Itoa(p.Calls), strconv.Itoa(p.Failed),
			strconv.Itoa(p.Errors4xx), strconv.Itoa(p.Errors5xx), millis(p.P50), millis(p.P95), millis(p.P99)})
	}
	return a.printer.Table([]string{"METHOD", "PATH", "CALLS", "FAILED", "4XX", "5XX", "P50", "P95", "P99"}, rows)
}

// millis is a duration the metrics give in milliseconds; - for none.
func millis(ms *float32) string {
	if ms == nil {
		return "-"
	}
	return strconv.FormatFloat(float64(*ms), 'f', -1, 32) + "ms"
}
