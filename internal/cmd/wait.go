package cmd

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/hivepaas/hivepaas-cli/internal/api"
	"github.com/hivepaas/hivepaas-cli/internal/client"
	"github.com/hivepaas/hivepaas-cli/internal/exitcode"
	"github.com/hivepaas/hivepaas-cli/internal/stream"
)

// The waiting's pace; the tests make it quicker.
var (
	// pollInterval is how often a deployment's status is read.
	pollInterval = 2 * time.Second
	// logsGrace is how long the log stream of a finished deployment is given to
	// deliver its last lines.
	logsGrace = 3 * time.Second
	// logsRetry is how long to wait before opening a log stream again.
	logsRetry = 2 * time.Second
)

// waitFor waits for a deployment to finish, reading its status every two
// seconds, and with logs, following its log on stderr. It answers the finished
// deployment, or ctx's error when ctx ends first.
func (a *App) waitFor(ctx context.Context, c *client.Client, ref deploymentRef, logs bool) (
	*api.AppdeploymentdtoDeploymentResp, error,
) {
	var over atomic.Bool
	logsCtx, stopLogs := context.WithCancel(ctx)
	defer stopLogs()
	logsDone := make(chan struct{})
	if logs {
		go func() {
			defer close(logsDone)
			l := &stream.Logs{
				URL:    c.BaseURL + ref.path() + "/logs",
				Query:  url.Values{"follow": {"true"}},
				Header: client.Headers(c.Target),
				// The stream ends with the deployment, and before it starts: it is
				// opened again until the deployment is over.
				Again: func(context.Context) bool { return !over.Load() },
				Retry: logsRetry,
			}
			err := l.Run(logsCtx, a.deployLine)
			if err != nil && logsCtx.Err() == nil {
				a.printer.Warnf("the deployment's logs: %s", err)
			}
		}()
	} else {
		close(logsDone)
	}

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		resp, err := c.GetAppDeploymentStatusWithResponse(ctx, ref.sel.Project.Id, ref.sel.Env, ref.sel.App.Id, ref.id)
		err = client.Check(resp, err)
		if err == nil && (resp.JSON200 == nil || resp.JSON200.Data == nil) {
			err = exitcode.New(exitcode.Server, "the server answered no status for deployment %s", ref.id)
		}
		switch {
		case ctx.Err() != nil:
			stopLogs()
			<-logsDone
			return nil, ctx.Err() //nolint:wrapcheck // the caller tells an interrupt from a timeout
		case err != nil && !transient(err):
			stopLogs()
			<-logsDone
			return nil, err
		case err == nil && isOver(resp.JSON200.Data.Status):
			over.Store(true)
			select {
			case <-logsDone:
			case <-time.After(logsGrace):
			case <-ctx.Done():
			}
			stopLogs()
			<-logsDone
			return a.deployment(ctx, c, ref)
		}
		select {
		case <-ctx.Done():
		case <-ticker.C:
		}
	}
}

func (a *App) deployment(ctx context.Context, c *client.Client, ref deploymentRef) (
	*api.AppdeploymentdtoDeploymentResp, error,
) {
	resp, err := c.GetAppDeploymentWithResponse(ctx, ref.sel.Project.Id, ref.sel.Env, ref.sel.App.Id, ref.id)
	if err = client.Check(resp, err); err != nil {
		return nil, err
	}
	return resp.JSON200.Data, nil
}

func isOver(status api.BaseDeploymentStatus) bool {
	switch status { //nolint:exhaustive // the others have not finished
	case api.DeploymentStatusDone, api.DeploymentStatusFailed, api.DeploymentStatusCanceled:
		return true
	}
	return false
}

// deployLine writes a line of a deployment's log on stderr, indented under the
// command's own lines, with the time it was written.
func (a *App) deployLine(frame api.TasklogLogFrame) {
	stamp := "        "
	if ts, err := time.Parse(time.RFC3339Nano, frame.Ts); err == nil && !ts.IsZero() {
		stamp = ts.Local().Format(time.TimeOnly)
	}
	for line := range strings.SplitSeq(strings.TrimRight(frame.Data, "\r\n"), "\n") {
		fmt.Fprintf(a.stderr, "  %s  %s\n", a.printer.DimErr(stamp), strings.TrimRight(line, "\r"))
	}
}
