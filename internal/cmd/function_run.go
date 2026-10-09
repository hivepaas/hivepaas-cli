package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
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
// the libraries are installed first. A variable for the tests.
var runTimeout = 20 * time.Minute

// A test run's outcomes the CLI tells apart: the function answered, its
// libraries could not be installed.
const (
	outcomeOK              = "ok"
	outcomeLibrariesFailed = "libraries-failed"
)

// runBodyMax is the largest request body a run takes.
const runBodyMax = 1 << 20

// librariesLogShown is how many of a failed install's last lines are said.
const librariesLogShown = 30

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
	a.printer.Infof("Running %s with the code in %s, %d files ...", sel.App.Name, dirWords(dir), len(files))
	resp, err := c.TestRunFunction(ctx, sel.Project.Id, sel.Env, sel.App.Id, api.AppdtoTestRunFunctionReq{
		Code: &api.AppsettingsdtoFunctionInlineCodeReq{Files: &files}, Request: request,
	})
	if err != nil {
		return a.runStopped(ctx, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return a.runStopped(ctx, err)
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

// runStopped is a run the CLI stopped waiting for: Ctrl-C, its timeout, or a
// server that went away.
func (a *App) runStopped(ctx context.Context, err error) error {
	var netErr net.Error
	switch {
	case ctx.Err() != nil:
		a.printer.Infof("")
		a.printer.Infof("Stopped waiting for the run.")
		return exitcode.Reported(exitcode.Interrupted)
	case errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()):
		return exitcode.New(exitcode.Timeout, "the run took more than %s", runTimeout)
	}
	return client.Unreachable(err)
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
	body := []byte(flags.data)
	switch {
	case flags.data == "@-":
		var err error
		if body, err = io.ReadAll(stdin); err != nil {
			return nil, fmt.Errorf("reading stdin: %w", err)
		}
	case strings.HasPrefix(flags.data, "@"):
		var err error
		if body, err = os.ReadFile(flags.data[1:]); err != nil {
			return nil, exitcode.New(exitcode.Usage, "--data: %v", err)
		}
	}
	// The API takes the body as text: other bytes would arrive changed.
	if !utf8.Valid(body) {
		return nil, exitcode.New(exitcode.Usage, "--data is not text: a run's request body is sent as text")
	}
	if len(body) > runBodyMax {
		return nil, exitcode.New(exitcode.Usage, "--data is %s, more than the 1 MB a run's request takes",
			sizeOf(int64(len(body))))
	}
	req.Body = string(body)
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
	switch {
	case run.Outcome == outcomeLibrariesFailed:
		a.printer.Errorf("Its libraries could not be installed:")
		lines := strings.Split(strings.TrimRight(run.LibrariesLog, "\n"), "\n")
		a.printer.Infof("%s", strings.Join(lines[max(0, len(lines)-librariesLogShown):], "\n"))
	case run.LibrariesBuilt:
		a.printer.Infof("Installed its libraries first.")
		if a.debug && run.LibrariesLog != "" {
			a.printer.Infof("%s", strings.TrimRight(run.LibrariesLog, "\n"))
		}
	}
	answered := run.Outcome == outcomeOK
	if answered {
		a.printer.Infof("%d %s in %s", run.Status, http.StatusText(run.Status), durationWords(run.DurationMs))
	}
	if lines := logLines(run.Logs); len(lines) > 0 {
		a.printer.Infof("%s", a.printer.DimErr("Logs:"))
		a.printer.Infof("%s", strings.Join(lines, "\n"))
		if run.LogsTruncated {
			a.printer.Warnf("its logs were cut at 1 MB")
		}
	}
	if !a.printer.Structured() && answered {
		body := run.Body
		// At a terminal what follows on stderr starts a line of its own.
		if len(body) > 0 && !bytes.HasSuffix(body, []byte("\n")) && isTerminal(a.stdout) {
			body = append(body, '\n')
		}
		if _, err := a.stdout.Write(body); err != nil {
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
			"same versions.", strings.Join(paths, " and "), dirWords(dir))
		return nil
	}
	code := make([]funccode.File, 0, len(files))
	for _, f := range files {
		code = append(code, funccode.File{Path: f.Path, Content: f.Content})
	}
	if err := writeCode(dir, code, true); err != nil {
		return err
	}
	a.printer.Successf("Wrote %s into %s.", strings.Join(paths, " and "), dirWords(dir))
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

// durationWords is how long a call took, as people say it: 0.4 ms, 12 ms, 1.25s.
func durationWords(ms float64) string {
	const second, precision = 1000, 10 * time.Millisecond
	switch {
	case ms < 1:
		return fmt.Sprintf("%.1f ms", ms)
	case ms < second:
		return fmt.Sprintf("%.0f ms", ms)
	}
	return time.Duration(ms * float64(time.Millisecond)).Round(precision).String()
}

// runtimeLine is a line the runtime writes of a call: a message the handler
// logged ("log"), or the call itself ("invocation").
type runtimeLine struct {
	HP  string `json:"hp"`
	Msg string `json:"msg"`
}

// logLines are a run's logs as they are said: the handler's messages, its own
// lines as they are, and not the line of the call, whose status is said.
func logLines(logs string) []string {
	if strings.TrimSpace(logs) == "" {
		return nil
	}
	var lines []string
	for line := range strings.SplitSeq(strings.TrimRight(logs, "\n"), "\n") {
		var rt runtimeLine
		if strings.HasPrefix(line, `{"hp":`) && json.Unmarshal([]byte(line), &rt) == nil {
			switch rt.HP {
			case "log":
				lines = append(lines, rt.Msg)
				continue
			case "invocation":
				continue
			}
		}
		lines = append(lines, line)
	}
	return lines
}
