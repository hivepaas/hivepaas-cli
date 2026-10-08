package cmd

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/hivepaas/hivepaas-cli/internal/api"
	"github.com/hivepaas/hivepaas-cli/internal/client"
	"github.com/hivepaas/hivepaas-cli/internal/exitcode"
	"github.com/hivepaas/hivepaas-cli/internal/output"
	"github.com/hivepaas/hivepaas-cli/internal/resolve"
	"github.com/hivepaas/hivepaas-cli/internal/stream"
)

type logsFlags struct {
	follow       bool
	since        string
	tail         int
	noTimestamps bool
	deployment   string

	// The stored logs: read when one of these is given.
	history   bool
	search    string
	regex     bool
	matchCase bool
	levels    []string
	until     string
}

// stored says the logs asked for are the stored ones, which can be searched.
func (f logsFlags) stored() bool {
	return f.history || f.search != "" || len(f.levels) > 0 || f.until != ""
}

// durationPattern is a period as the server reads one: 90s, 10m, 1h30m, 2d.
var durationPattern = regexp.MustCompile(`^(\d+(\.\d+)?(ns|us|µs|ms|s|m|h|d|w))+$`)

func (a *App) logsCmd() *cobra.Command {
	var flags logsFlags
	cmd := &cobra.Command{
		Use:   "logs",
		Short: "Show an app's logs, or a deployment's",
		Long: "Show an app's logs, or with --deployment a deployment's.\n\n" +
			"With -f the command follows them until Ctrl-C, and opens the stream again when it drops.\n" +
			"With -o json each line is a JSON object of its own: {type, data, ts}.\n\n" +
			"--search, --level and --history read the stored logs instead, those of containers that no\n" +
			"longer exist too: the last hour unless --since says otherwise, the newest lines first cut.",
		Example: "  hivepaas logs -f --since 10m\n" +
			"  hivepaas logs --search timeout --level error,warn --since 1d\n" +
			"  hivepaas logs --deployment 01JA2C7W... -f",
		Args: usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.logs(cmd.Context(), flags)
		},
	}
	cmd.Flags().BoolVarP(&flags.follow, "follow", "f", false, "follow the logs as they are written")
	cmd.Flags().StringVar(&flags.since, "since", "", "from a time ago, such as 10m or 2h, or from an RFC 3339 time")
	cmd.Flags().IntVar(&flags.tail, "tail", 0, "the last so many lines: the server's default for an app is 1000")
	cmd.Flags().BoolVar(&flags.noTimestamps, "no-timestamps", false, "leave out each line's time")
	cmd.Flags().StringVar(&flags.deployment, "deployment", "", "a deployment's logs, by its id")
	cmd.Flags().BoolVar(&flags.history, "history", false, "the stored logs, without a search")
	cmd.Flags().StringVar(&flags.search, "search", "", "stored lines with this text, from the start of a word")
	cmd.Flags().BoolVar(&flags.regex, "regex", false, "--search is a regular expression")
	cmd.Flags().BoolVar(&flags.matchCase, "match-case", false, "--search tells upper from lower case")
	cmd.Flags().StringSliceVar(&flags.levels, "level", nil,
		"stored lines of these levels: trace, debug, info, warn, error, fatal, panic")
	cmd.Flags().StringVar(&flags.until, "until", "", "stored lines before this RFC 3339 time")
	return cmd
}

func (a *App) logs(ctx context.Context, flags logsFlags) error {
	if a.printer.Format == output.YAML {
		return exitcode.New(exitcode.Usage, "logs are written as JSON lines with -o json; there is no YAML for a stream")
	}
	if flags.stored() {
		return a.storedLogs(ctx, flags)
	}
	query, err := logsQuery(flags)
	if err != nil {
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

	l := &stream.Logs{Query: query, Header: client.Headers(c.Target)}
	if flags.deployment != "" {
		ref := deploymentRef{sel: sel, id: flags.deployment}
		l.URL = c.BaseURL + ref.path() + "/logs"
		if flags.follow {
			// A deployment's stream ends with it, and before it starts.
			l.Again = func(ctx context.Context) bool {
				resp, err := c.GetAppDeploymentStatusWithResponse(ctx, sel.Project.Id, sel.Env, sel.App.Id, ref.id)
				return client.Check(resp, err) == nil && resp.JSON200 != nil && resp.JSON200.Data != nil &&
					!isOver(resp.JSON200.Data.Status)
			}
		}
	} else {
		l.URL = fmt.Sprintf("%s/projects/%s/%s/apps/%s/logs", c.BaseURL, sel.Project.Id, sel.Env, sel.App.Id)
		// The times are asked for always: a stream opened again starts from the
		// last one. --no-timestamps only leaves them out of what is written.
		l.Query.Set("timestamps", "true")
		if flags.follow {
			l.Again = func(context.Context) bool { return true }
		}
	}

	err = l.Run(ctx, func(frame api.TasklogLogFrame) { a.logLine(frame, flags.noTimestamps) })
	if errors.Is(err, context.Canceled) && ctx.Err() != nil {
		return exitcode.Reported(exitcode.Interrupted)
	}
	return err
}

func logsQuery(flags logsFlags) (url.Values, error) {
	query := url.Values{}
	if flags.follow {
		query.Set("follow", "true")
	}
	if flags.tail < 0 {
		return nil, exitcode.New(exitcode.Usage, "--tail takes a number of lines, not %d", flags.tail)
	}
	if flags.tail > 0 {
		query.Set("tail", strconv.Itoa(flags.tail))
	}
	switch since := strings.TrimSpace(flags.since); {
	case since == "":
	case durationPattern.MatchString(since):
		query.Set("duration", since)
	default:
		t, err := time.Parse(time.RFC3339, since)
		if err != nil {
			return nil, exitcode.New(exitcode.Usage, "--since takes a period, such as 10m or 2h, or an RFC 3339 "+
				"time such as 2026-10-08T14:00:00Z, not %q", since)
		}
		query.Set("since", t.UTC().Format(time.RFC3339))
	}
	return query, nil
}

// logLine writes a line of a log on stdout: the line itself, or with -o json
// the frame.
func (a *App) logLine(frame api.TasklogLogFrame, noTimestamps bool) {
	if a.printer.Structured() {
		data, _ := output.Marshal(frame)
		fmt.Fprintln(a.stdout, string(data))
		return
	}
	data := strings.TrimRight(frame.Data, "\r\n")
	if noTimestamps {
		fmt.Fprintln(a.stdout, data)
		return
	}
	stamp := frame.Ts
	if ts, err := time.Parse(time.RFC3339Nano, frame.Ts); err == nil {
		stamp = ts.UTC().Format(time.RFC3339)
	}
	fmt.Fprintf(a.stdout, "%s  %s\n", a.printer.Dim(stamp), data)
}

// storedLogs reads the app's stored logs: searched, by level, those of
// containers gone too. The newest lines of the period come, oldest first; when
// there are more, it says how to read the ones before.
func (a *App) storedLogs(ctx context.Context, flags logsFlags) error {
	switch {
	case flags.follow:
		return exitcode.New(exitcode.Usage, "-f follows the live logs: --search, --level and --history read the stored ones")
	case flags.deployment != "":
		return exitcode.New(exitcode.Usage, "a deployment's logs are not searched: leave out --search, --level and --history")
	case flags.tail < 0:
		return exitcode.New(exitcode.Usage, "--tail takes a number of lines, not %d", flags.tail)
	}
	params := &api.GetAppLogHistoryParams{}
	now := time.Now()
	if flags.since != "" {
		start, err := sinceTime(flags.since, now)
		if err != nil {
			return err
		}
		params.Start = &start
	}
	if flags.until != "" {
		end, err := time.Parse(time.RFC3339Nano, flags.until)
		if err != nil {
			return exitcode.New(exitcode.Usage, "--until takes an RFC 3339 time such as 2026-10-08T14:00:00Z, not %q",
				flags.until)
		}
		params.End = ptr(end.UTC().Format(time.RFC3339Nano))
	}
	if flags.tail > 0 {
		params.Limit = &flags.tail
	}
	if flags.search != "" {
		params.Search, params.Regex, params.MatchCase = &flags.search, &flags.regex, &flags.matchCase
	}
	if len(flags.levels) > 0 {
		params.Levels = ptr(strings.Join(flags.levels, ","))
	}

	c, err := a.client()
	if err != nil {
		return err
	}
	sel, err := a.selectTarget(ctx, c, scopeApp)
	if err != nil {
		return err
	}
	resp, err := c.GetAppLogHistoryWithResponse(ctx, sel.Project.Id, sel.Env, sel.App.Id, params)
	if err = client.Check(resp, err); err != nil {
		return err
	}
	data := resp.JSON200.Data
	for _, frame := range resolve.Deref(data.Logs) {
		a.logLine(frame, flags.noTimestamps)
	}
	if data.Truncated && data.NextEnd != nil {
		a.printer.Infof("Older lines match too: --until %s shows them.", *data.NextEnd)
	}
	return nil
}

// sinceTime is the start --since names: a period ago, or a time.
func sinceTime(since string, now time.Time) (string, error) {
	since = strings.TrimSpace(since)
	if d, err := time.ParseDuration(since); err == nil {
		return now.Add(-d).UTC().Format(time.RFC3339), nil
	}
	if m := dayPattern.FindStringSubmatch(since); m != nil {
		days, _ := strconv.Atoi(m[1])
		return now.AddDate(0, 0, -days).UTC().Format(time.RFC3339), nil
	}
	t, err := time.Parse(time.RFC3339, since)
	if err != nil {
		return "", exitcode.New(exitcode.Usage, "--since takes a period, such as 10m, 2h or 3d, or an RFC 3339 "+
			"time such as 2026-10-08T14:00:00Z, not %q", since)
	}
	return t.UTC().Format(time.RFC3339), nil
}

// dayPattern is a period of days, which time.ParseDuration does not read: 3d.
var dayPattern = regexp.MustCompile(`^(\d+)d$`)
