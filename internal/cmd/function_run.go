package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/hivepaas/hivepaas-cli/internal/api"
	"github.com/hivepaas/hivepaas-cli/internal/client"
	"github.com/hivepaas/hivepaas-cli/internal/exitcode"
	"github.com/hivepaas/hivepaas-cli/internal/funccode"
)

// runTimeout is how long a test run may take: a call may take 15 minutes, and
// the libraries are installed first.
const runTimeout = 20 * time.Minute

// outcomeOK is a test run's outcome when the function answered.
const outcomeOK = "ok"

type functionRunFlags struct {
	method, path, data string
	query, headers     []string
	fail, saveLock     bool
	list               bool
}

func (a *App) functionRunCmd() *cobra.Command {
	var flags functionRunFlags
	cmd := &cobra.Command{
		Use:   "run [DIR]",
		Short: "Call a function once with a directory's code, before it is deployed",
		Long: "Call a function once with DIR's code - . by default - not saved nor deployed, in a throwaway\n" +
			"container on a build node, with the function's variables and secrets: the dashboard's Test run.\n" +
			"The body it answered goes to stdout, as it is; its status, how long it took and what it logged\n" +
			"go to stderr. It exits 0 when the function answered, whatever its status - as curl does, and\n" +
			"with --fail, 8 for a status of 400 or more - and 8 when it did not answer.",
		Example: "  hivepaas function run --query name=Ada\n" +
			"  hivepaas function run ./hello --method POST --data @order.json --header 'Content-Type: " +
			"application/json' | jq .",
		Args: usageArgs(cobra.MaximumNArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.functionRun(cmd.Context(), args, flags)
		},
	}
	f := cmd.Flags()
	f.StringVar(&flags.method, "method", http.MethodGet, "the request's method")
	f.StringVar(&flags.path, "path", "/", "the request's path")
	f.StringArrayVar(&flags.query, "query", nil, "a query parameter, K=V; repeat for more")
	f.StringArrayVar(&flags.headers, "header", nil, "a header, 'K: V'; repeat for more")
	f.StringVar(&flags.data, "data", "", "the request's body: text, @FILE, or @- for stdin")
	f.BoolVar(&flags.fail, "fail", false, "exit 8 for a status of 400 or more")
	f.BoolVar(&flags.saveLock, "save-lock", false, "write the lock files the run made into the directory")
	f.BoolVar(&flags.list, "list", false, "show the files that would be sent, and send nothing")
	return cmd
}

// testRunAnswer is a test run's answer. The API types its body as an array of
// numbers; it is JSON's base64, which []byte reads.
type testRunAnswer struct {
	Outcome        string              `json:"outcome"`
	Status         int                 `json:"status"`
	Headers        map[string][]string `json:"headers"`
	Body           []byte              `json:"body"`
	BodyTruncated  bool                `json:"bodyTruncated"`
	RequestID      string              `json:"requestId"`
	DurationMs     float64             `json:"durationMs"`
	Logs           string              `json:"logs"`
	LogsTruncated  bool                `json:"logsTruncated"`
	Error          string              `json:"error"`
	ExitCode       int                 `json:"exitCode"`
	LibrariesBuilt bool                `json:"librariesBuilt"`
	LibrariesLog   string              `json:"librariesLog"`
	LockFiles      []funccodeFile      `json:"lockFiles"`
}

type funccodeFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

func (a *App) functionRun(ctx context.Context, args []string, flags functionRunFlags) error {
	request, err := flags.request(a.stdin)
	if err != nil {
		return err
	}
	dir := dirArg(args)
	code, err := a.collectCode(dir)
	if err != nil {
		return err
	}
	if flags.list {
		return a.listCode(code)
	}
	if len(args) > 0 {
		if err = a.linkFromDir(dir); err != nil {
			return err
		}
	}
	c, err := a.clientWithTimeout(runTimeout)
	if err != nil {
		return err
	}
	sel, err := a.selectTarget(ctx, c, scopeApp)
	if err != nil {
		return err
	}
	files := make([]api.AppsettingsdtoFunctionFileReq, 0, len(code.Files))
	for _, f := range code.Files {
		files = append(files, api.AppsettingsdtoFunctionFileReq{Path: f.Path, Content: f.Content})
	}
	a.printer.Infof("Running %s with %s's code, %d files ...", sel.App.Name, dir, len(files))
	resp, err := c.TestRunFunction(ctx, sel.Project.Id, sel.Env, sel.App.Id, api.AppdtoTestRunFunctionReq{
		Code: &api.AppsettingsdtoFunctionInlineCodeReq{Files: &files}, Request: request,
	})
	if err != nil {
		return client.Check(&struct{ HTTPResponse *http.Response }{}, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return exitcode.Wrap(exitcode.Server, fmt.Errorf("reading the run's answer: %w", err))
	}
	if err = client.CheckStatus(resp.StatusCode, data); err != nil {
		return err
	}
	var answer struct {
		Data *testRunAnswer `json:"data"`
	}
	if err = json.Unmarshal(data, &answer); err != nil || answer.Data == nil {
		return exitcode.New(exitcode.Server, "the server answered no run")
	}
	return a.ranWith(answer.Data, dir, flags)
}

// request is the request the flags give.
func (flags functionRunFlags) request(stdin io.Reader) (*api.AppdtoTestRunRequestReq, error) {
	req := &api.AppdtoTestRunRequestReq{Method: strings.ToUpper(flags.method), Path: flags.path}
	if !strings.HasPrefix(req.Path, "/") {
		req.Path = "/" + req.Path
	}
	if len(flags.query) > 0 {
		query := map[string][]string{}
		for _, pair := range flags.query {
			key, value, ok := strings.Cut(pair, "=")
			if !ok || key == "" {
				return nil, exitcode.New(exitcode.Usage, "--query takes K=V, not %q", pair)
			}
			query[key] = append(query[key], value)
		}
		req.Query = &query
	}
	if len(flags.headers) > 0 {
		headers := map[string][]string{}
		for _, line := range flags.headers {
			key, value, ok := strings.Cut(line, ":")
			if key = strings.TrimSpace(key); !ok || key == "" {
				return nil, exitcode.New(exitcode.Usage, "--header takes 'K: V', not %q", line)
			}
			headers[key] = append(headers[key], strings.TrimSpace(value))
		}
		req.Headers = &headers
	}
	switch {
	case flags.data == "@-":
		body, err := io.ReadAll(stdin)
		if err != nil {
			return nil, fmt.Errorf("reading stdin: %w", err)
		}
		req.Body = string(body)
	case strings.HasPrefix(flags.data, "@"):
		body, err := os.ReadFile(flags.data[1:])
		if err != nil {
			return nil, exitcode.New(exitcode.Usage, "--data: %v", err)
		}
		req.Body = string(body)
	default:
		req.Body = flags.data
	}
	return req, nil
}

// ranWith says what a run brought back: the body on stdout, the rest on
// stderr, and exits as curl does.
func (a *App) ranWith(run *testRunAnswer, dir string, flags functionRunFlags) error {
	if a.printer.Structured() {
		if err := a.printer.Data(runOutput(run)); err != nil {
			return err
		}
	}
	if run.LibrariesBuilt {
		a.printer.Infof("Installed its libraries first.")
		if a.debug && run.LibrariesLog != "" {
			a.printer.Infof("%s", strings.TrimRight(run.LibrariesLog, "\n"))
		}
	}
	answered := run.Outcome == outcomeOK
	if answered {
		a.printer.Infof("%d %s in %s", run.Status, http.StatusText(run.Status), durationWords(run.DurationMs))
	}
	if logs := strings.TrimRight(run.Logs, "\n"); logs != "" {
		a.printer.Infof("%s", a.printer.DimErr("Logs:"))
		a.printer.Infof("%s", logs)
		if run.LogsTruncated {
			a.printer.Warnf("its logs were cut at 1 MB")
		}
	}
	if !a.printer.Structured() && answered {
		if _, err := a.stdout.Write(run.Body); err != nil {
			return fmt.Errorf("writing the body: %w", err)
		}
		if run.BodyTruncated {
			a.printer.Warnf("the body was cut at 1 MB")
		}
	}
	if err := a.lockFiles(run.LockFiles, dir, flags.saveLock); err != nil {
		return err
	}
	switch {
	case !answered:
		reason := run.Outcome
		if run.Error != "" {
			reason += ": " + run.Error
		}
		a.printer.Errorf("The function did not answer: %s", reason)
		return exitcode.Reported(exitcode.Deployment)
	case flags.fail && run.Status >= http.StatusBadRequest:
		a.printer.Errorf("It answered %d %s.", run.Status, http.StatusText(run.Status))
		return exitcode.Reported(exitcode.Deployment)
	}
	return nil
}

// lockFiles says the lock files a run made, or with save writes them into dir.
func (a *App) lockFiles(files []funccodeFile, dir string, save bool) error {
	if len(files) == 0 {
		return nil
	}
	paths := make([]string, 0, len(files))
	for _, f := range files {
		paths = append(paths, f.Path)
	}
	if !save {
		a.printer.Infof("The run made %s: --save-lock writes it into %s, so that each deployment installs the "+
			"same versions.", strings.Join(paths, " and "), dir)
		return nil
	}
	code := make([]funccode.File, 0, len(files))
	for _, f := range files {
		code = append(code, funccode.File{Path: f.Path, Content: f.Content})
	}
	if err := writeCode(dir, code, true); err != nil {
		return err
	}
	a.printer.Successf("Wrote %s into %s.", strings.Join(paths, " and "), filepath.Clean(dir))
	return nil
}

// runOutput is a run as -o json gives it: the body as text when it is UTF-8,
// else as base64.
func runOutput(run *testRunAnswer) any {
	type output struct {
		*testRunAnswer
		Body       *string `json:"body,omitempty"`
		BodyBase64 []byte  `json:"bodyBase64,omitempty"`
	}
	out := output{testRunAnswer: run}
	if utf8.Valid(run.Body) {
		out.Body = ptr(string(run.Body))
	} else {
		out.BodyBase64 = run.Body
	}
	return out
}

// durationWords is how long a call took, as people say it.
func durationWords(ms float64) string {
	return (time.Duration(ms * float64(time.Millisecond))).Round(time.Millisecond).String()
}
