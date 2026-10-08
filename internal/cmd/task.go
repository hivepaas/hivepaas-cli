package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/hivepaas/hivepaas-cli/internal/api"
	"github.com/hivepaas/hivepaas-cli/internal/client"
	"github.com/hivepaas/hivepaas-cli/internal/exitcode"
	"github.com/hivepaas/hivepaas-cli/internal/output"
	"github.com/hivepaas/hivepaas-cli/internal/resolve"
)

// tasksShown is how many tasks `task ls` shows unless asked.
const tasksShown = 20

// taskTypePrefix is what the server's task types start with: task:sched-job-exec.
const taskTypePrefix = "task:"

// taskRef is a task and the app it is of.
type taskRef struct {
	sel *selection
	id  string
}

func (r taskRef) path() string {
	return fmt.Sprintf("/projects/%s/%s/apps/%s/tasks/%s", r.sel.Project.Id, r.sel.Env, r.sel.App.Id, r.id)
}

func (a *App) taskCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "task",
		Aliases: []string{"tasks"},
		Short:   "An app's tasks: its jobs' runs, and what else runs in the background for it",
	}
	cmd.AddCommand(a.taskLsCmd(), a.taskGetCmd(), a.taskCancelCmd())
	return cmd
}

func (a *App) taskLsCmd() *cobra.Command {
	var limit int
	var status, typ string
	cmd := &cobra.Command{
		Use:   "ls",
		Short: "List an app's tasks, the newest first",
		Args:  usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			if limit < 1 {
				return exitcode.New(exitcode.Usage, "--limit takes a number of tasks, not %d", limit)
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			sel, err := a.selectTarget(cmd.Context(), c, scopeApp)
			if err != nil {
				return err
			}
			params := &api.ListAppTaskParams{PageLimit: &limit}
			if status != "" {
				params.Status = &status
			}
			if typ != "" {
				params.Type = ptr(taskTypes(typ))
			}
			resp, err := c.ListAppTaskWithResponse(cmd.Context(), sel.Project.Id, sel.Env, sel.App.Id, params)
			if err = client.Check(resp, err); err != nil {
				return err
			}
			tasks := resolve.Deref(resp.JSON200.Data)
			if a.printer.Structured() {
				return a.printer.Data(tasks)
			}
			now := time.Now()
			rows := make([][]string, 0, len(tasks))
			for _, t := range tasks {
				rows = append(rows, []string{t.Id, taskType(t.Type), string(t.Status), taskWhat(t),
					output.Ago(t.CreatedAt, now), taskTook(&t)})
			}
			return a.printer.Table([]string{"ID", colType, colStatus, "WHAT", "CREATED", "TOOK"}, rows)
		},
	}
	cmd.Flags().IntVar(&limit, "limit", tasksShown, "how many to show")
	cmd.Flags().StringVar(&status, "status", "",
		"only these: not-started, in-progress, done, failed, canceled - several with commas")
	cmd.Flags().StringVar(&typ, "type", "", "only these types, such as sched-job-exec - several with commas")
	return cmd
}

func (a *App) taskGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "get TASK-ID",
		Short: "Show a task",
		Args:  usageArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			sel, err := a.selectTarget(cmd.Context(), c, scopeApp)
			if err != nil {
				return err
			}
			t, err := a.task(cmd.Context(), c, taskRef{sel: sel, id: args[0]})
			if err != nil {
				return err
			}
			if a.printer.Structured() {
				return a.printer.Data(t)
			}
			now := time.Now()
			fmt.Fprintf(a.stdout, "Task %s of %s\n", t.Id, sel.where())
			fmt.Fprintf(a.stdout, "Type:    %s\n", taskType(t.Type))
			fmt.Fprintf(a.stdout, "Status:  %s\n", t.Status)
			if what := taskWhat(*t); what != "-" {
				fmt.Fprintf(a.stdout, "What:    %s\n", what)
			}
			fmt.Fprintf(a.stdout, "Created: %s\n", output.Ago(t.CreatedAt, now))
			if textOf(t.StartedAt) != "" {
				fmt.Fprintf(a.stdout, "Started: %s\n", output.Ago(*t.StartedAt, now))
			}
			if textOf(t.EndedAt) != "" {
				fmt.Fprintf(a.stdout, "Took:    %s\n", taskTook(t))
			}
			if t.LastError != "" {
				fmt.Fprintf(a.stdout, "Error:   %s\n", strings.ReplaceAll(t.LastError, "\n", "\n         "))
			}
			a.printer.Infof("Its logs: hivepaas logs --task %s%s", t.Id, sel.flags())
			return nil
		},
	}
}

func (a *App) taskCancelCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "cancel TASK-ID",
		Short: "Cancel a task",
		Args:  usageArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			sel, err := a.selectTarget(cmd.Context(), c, scopeApp)
			if err != nil {
				return err
			}
			resp, err := c.CancelAppTaskWithResponse(cmd.Context(), sel.Project.Id, sel.Env, sel.App.Id, args[0])
			if err = client.Check(resp, err); err != nil {
				return err
			}
			// The spec gives the answer no type: {data: {canceled}}.
			var answer struct {
				Data struct {
					Canceled bool `json:"canceled"`
				} `json:"data"`
			}
			if json.Unmarshal(resp.Body, &answer) == nil && answer.Data.Canceled {
				a.printer.Successf("Canceled task %s of %s.", args[0], sel.where())
			} else {
				a.printer.Successf("Canceling task %s of %s: the server is stopping it.", args[0], sel.where())
			}
			return nil
		},
	}
}

func (a *App) task(ctx context.Context, c *client.Client, ref taskRef) (*api.TaskdtoTaskResp, error) {
	resp, err := c.GetAppTaskWithResponse(ctx, ref.sel.Project.Id, ref.sel.Env, ref.sel.App.Id, ref.id)
	if err = client.Check(resp, err); err != nil {
		return nil, err
	}
	if resp.JSON200 == nil || resp.JSON200.Data == nil {
		return nil, exitcode.New(exitcode.Server, "the server answered no task %s", ref.id)
	}
	return resp.JSON200.Data, nil
}

// waitForTask waits for a task to finish, following its log on stderr, and
// answers the finished task, or ctx's error when ctx ends first. A failure the
// server will retry is said, and waited out.
func (a *App) waitForTask(ctx context.Context, c *client.Client, ref taskRef) (*api.TaskdtoTaskResp, error) {
	said := ""
	err := a.follow(ctx, c.BaseURL+ref.path()+"/logs", "the task's logs", c.Target,
		func(ctx context.Context) (bool, error) {
			finished, retrying, err := a.taskFinished(ctx, c, ref)
			if retrying != nil && textOf(retrying.RetryAt) != said {
				said = textOf(retrying.RetryAt)
				a.printer.Infof("The run failed: %s. It is tried again %s.", retrying.LastError,
					output.In(said, time.Now()))
			}
			return finished, err
		})
	if err != nil {
		return nil, err
	}
	return a.task(ctx, c, ref)
}

// taskFinished reads whether a task has finished: done, canceled, or failed
// with no retry to come. A failed one the server will run again has not: it
// answers that task too.
func (a *App) taskFinished(ctx context.Context, c *client.Client, ref taskRef) (
	finished bool, retrying *api.TaskdtoTaskResp, _ error,
) {
	resp, err := c.GetAppTaskStatusWithResponse(ctx, ref.sel.Project.Id, ref.sel.Env, ref.sel.App.Id, ref.id)
	err = client.Check(resp, err)
	if err == nil && (resp.JSON200 == nil || resp.JSON200.Data == nil) {
		err = exitcode.New(exitcode.Server, "the server answered no status for task %s", ref.id)
	}
	if err != nil {
		return false, nil, err
	}
	status := resp.JSON200.Data.Status
	if status != api.TaskStatusFailed {
		return taskOver(status), nil, nil
	}
	t, err := a.task(ctx, c, ref)
	if err != nil {
		return false, nil, err
	}
	if retryPending(t) {
		return false, t, nil
	}
	return true, nil, nil
}

// retryPending says a failed task is to run again: the server sets when.
func retryPending(t *api.TaskdtoTaskResp) bool {
	at, err := time.Parse(time.RFC3339, textOf(t.RetryAt))
	return t.Status == api.TaskStatusFailed && err == nil && at.Year() > 1
}

func taskOver(status api.BaseTaskStatus) bool {
	switch status { //nolint:exhaustive // the others have not finished
	case api.TaskStatusDone, api.TaskStatusFailed, api.TaskStatusCanceled:
		return true
	}
	return false
}

// taskOutcome says how a task ended, and exits 8 when it did not succeed.
func (a *App) taskOutcome(t *api.TaskdtoTaskResp, ref taskRef) error {
	took := taskTook(t)
	switch t.Status { //nolint:exhaustive // waitForTask answers a finished task
	case api.TaskStatusDone:
		a.printer.Successf("Done in %s.", took)
		return nil
	case api.TaskStatusCanceled:
		a.printer.Errorf("The task was canceled after %s.", took)
	default:
		reason := ""
		if t.LastError != "" {
			reason = ": " + t.LastError
		}
		a.printer.Errorf("The task failed after %s%s", took, reason)
	}
	a.printer.Infof("Its logs: hivepaas logs --task %s%s", t.Id, ref.sel.flags())
	return exitcode.Reported(exitcode.Deployment)
}

// taskType is a task's type as a person reads it: sched-job-exec.
func taskType(t api.BaseTaskType) string {
	return strings.TrimPrefix(string(t), taskTypePrefix)
}

// taskTypes are the types --type names, as the server takes them.
func taskTypes(list string) string {
	parts := strings.Split(list, ",")
	for i, part := range parts {
		part = strings.TrimSpace(part)
		if !strings.HasPrefix(part, taskTypePrefix) {
			part = taskTypePrefix + part
		}
		parts[i] = part
	}
	return strings.Join(parts, ",")
}

// taskWhat is what a task acts on: the job it runs.
func taskWhat(t api.TaskdtoTaskResp) string {
	if t.TargetJob != nil && t.TargetJob.Name != "" {
		return t.TargetJob.Name
	}
	return "-"
}

// taskTook is how long a task ran, as the server timed it; - when it has not
// ended.
func taskTook(t *api.TaskdtoTaskResp) string {
	if textOf(t.StartedAt) == "" || textOf(t.EndedAt) == "" {
		return "-"
	}
	start, err1 := time.Parse(time.RFC3339, *t.StartedAt)
	end, err2 := time.Parse(time.RFC3339, *t.EndedAt)
	if err1 != nil || err2 != nil {
		return "?"
	}
	return end.Sub(start).Round(time.Second).String()
}
