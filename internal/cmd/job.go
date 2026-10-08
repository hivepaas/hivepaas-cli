package cmd

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/hivepaas/hivepaas-cli/internal/api"
	"github.com/hivepaas/hivepaas-cli/internal/client"
	"github.com/hivepaas/hivepaas-cli/internal/exitcode"
	"github.com/hivepaas/hivepaas-cli/internal/output"
	"github.com/hivepaas/hivepaas-cli/internal/resolve"
)

// defaultJobTimeout is how long `job run` waits for the run.
const defaultJobTimeout = 30 * time.Minute

func (a *App) jobCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "job",
		Aliases: []string{"jobs"},
		Short:   "An app's scheduled jobs: list them, run one now, turn one on or off",
	}
	cmd.AddCommand(a.jobLsCmd(), a.jobRunCmd(), a.jobStatusCmd("enable", api.SettingStatusActive),
		a.jobStatusCmd("disable", api.SettingStatusDisabled))
	return cmd
}

func (a *App) jobLsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "ls",
		Short: "List an app's scheduled jobs",
		Args:  usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			sel, err := a.selectTarget(cmd.Context(), c, scopeApp)
			if err != nil {
				return err
			}
			jobs, err := schedJobs(cmd.Context(), c, sel)
			if err != nil {
				return err
			}
			if a.printer.Structured() {
				return a.printer.Data(jobs)
			}
			now := time.Now()
			rows := make([][]string, 0, len(jobs))
			for _, j := range jobs {
				next := "-"
				if runs := resolve.Deref(j.NextRuns); len(runs) > 0 && j.Status == api.SettingStatusActive {
					next = output.In(runs[0], now)
				}
				rows = append(rows, []string{j.Name, string(j.JobType), scheduleWords(j.Schedule), string(j.Status), next})
			}
			return a.printer.Table([]string{colName, colType, "SCHEDULE", colStatus, "NEXT RUN"}, rows)
		},
	}
}

func (a *App) jobRunCmd() *cobra.Command {
	var noWait bool
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "run JOB",
		Short: "Run a scheduled job now, and wait for it while following its logs",
		Long: "Run one of the app's scheduled jobs now, by its name or id, besides its schedule. The command\n" +
			"waits for the run, following its logs on stderr; Ctrl-C stops the waiting, not the run:\n" +
			"`hivepaas task cancel` cancels it. A run that fails exits 8.",
		Example: "  hivepaas job run migrate\n  hivepaas job run nightly-report --no-wait",
		Args:    usageArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.jobRun(cmd.Context(), args[0], noWait, timeout)
		},
	}
	cmd.Flags().BoolVar(&noWait, "no-wait", false, "start the run and return")
	cmd.Flags().DurationVar(&timeout, "timeout", defaultJobTimeout, "how long to wait for the run")
	return cmd
}

func (a *App) jobRun(ctx context.Context, name string, noWait bool, timeout time.Duration) error {
	c, err := a.client()
	if err != nil {
		return err
	}
	sel, err := a.selectTarget(ctx, c, scopeApp)
	if err != nil {
		return err
	}
	job, err := a.schedJob(ctx, c, sel, name)
	if err != nil {
		return err
	}
	if job.Status != api.SettingStatusActive {
		// The server takes the run, and its queue cancels it: say so first.
		return exitcode.New(exitcode.Invalid, "%s is %s: hivepaas job enable %s%s turns it on", job.Name, job.Status,
			shellWord(job.Name), sel.flags())
	}
	resp, err := c.ExecuteAppSchedJobWithResponse(ctx, sel.Project.Id, sel.Env, sel.App.Id, job.Id,
		api.SchedjobdtoExecuteSchedJobReq{})
	if err = client.Check(resp, err); err != nil {
		return err
	}
	if resp.JSON200 == nil || resp.JSON200.Data == nil {
		return exitcode.New(exitcode.Server, "the server answered no task for the run of %s", job.Name)
	}
	ref := taskRef{sel: sel, id: resp.JSON200.Data.Task.Id}
	a.printer.Infof("Running %s on %s, task %s", job.Name, sel.where(), ref.id)
	if noWait {
		a.printer.Infof("  Follow it:  hivepaas logs --task %s -f%s", ref.id, sel.flags())
		if a.printer.Structured() {
			return a.printer.Data(resp.JSON200.Data)
		}
		return nil
	}
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	task, err := a.waitForTask(waitCtx, c, ref)
	if err != nil {
		return a.stoppedWaitingForTask(ctx, err, ref, timeout)
	}
	if a.printer.Structured() {
		if err = a.printer.Data(task); err != nil {
			return err
		}
	}
	return a.taskOutcome(task, ref)
}

// stoppedWaitingForTask says the run goes on without the CLI, and how to pick it
// up again; any other error is the error.
func (a *App) stoppedWaitingForTask(ctx context.Context, err error, ref taskRef, timeout time.Duration) error {
	code := exitcode.Timeout
	switch {
	case ctx.Err() != nil:
		code = exitcode.Interrupted
		a.printer.Infof("")
		a.printer.Infof("Stopped waiting.")
	case errors.Is(err, context.DeadlineExceeded):
		a.printer.Warnf("stopped waiting after %s.", timeout)
	default:
		return err
	}
	a.printer.Infof("Task %s of %s is still running on the server.", ref.id, ref.sel.App.Name)
	a.printer.Infof("  Follow it:  hivepaas logs --task %s -f%s", ref.id, ref.sel.flags())
	a.printer.Infof("  Cancel it:  hivepaas task cancel %s%s", ref.id, ref.sel.flags())
	return exitcode.Reported(code)
}

func (a *App) jobStatusCmd(verb string, status api.BaseSettingStatus) *cobra.Command {
	return &cobra.Command{
		Use: verb + " JOB",
		Short: map[string]string{"enable": "Turn a scheduled job on: it runs on its schedule again",
			"disable": "Turn a scheduled job off: it does not run on its schedule"}[verb],
		Args: usageArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			sel, err := a.selectTarget(cmd.Context(), c, scopeApp)
			if err != nil {
				return err
			}
			job, err := a.schedJob(cmd.Context(), c, sel, args[0])
			if err != nil {
				return err
			}
			if job.Status == status {
				a.printer.Infof("%s is %sd already.", job.Name, verb)
				return nil
			}
			resp, err := c.UpdateAppSchedJobStatusWithResponse(cmd.Context(), sel.Project.Id, sel.Env, sel.App.Id,
				job.Id, api.SchedjobdtoUpdateSchedJobStatusReq{Status: &status, UpdateVer: job.UpdateVer})
			if err = client.Check(resp, err); err != nil {
				return err
			}
			a.printer.Successf("%s %s on %s.", strings.ToUpper(verb[:1])+verb[1:]+"d", job.Name, sel.where())
			return nil
		},
	}
}

// schedJobs are an app's scheduled jobs.
func schedJobs(ctx context.Context, c *client.Client, sel *selection) ([]api.SchedjobdtoSchedJobResp, error) {
	var all []api.SchedjobdtoSchedJobResp
	for offset := 0; ; offset += jobsPage {
		resp, err := c.ListAppSchedJobWithResponse(ctx, sel.Project.Id, sel.Env, sel.App.Id,
			&api.ListAppSchedJobParams{PageOffset: ptr(offset), PageLimit: ptr(jobsPage)})
		if err = client.Check(resp, err); err != nil {
			return nil, err
		}
		if resp.JSON200 == nil {
			return all, nil
		}
		page := resolve.Deref(resp.JSON200.Data)
		all = append(all, page...)
		if len(page) < jobsPage {
			return all, nil
		}
	}
}

// jobsPage is one page of an app's jobs.
const jobsPage = 200

// schedJob is the app's job input names, by id or name.
func (a *App) schedJob(ctx context.Context, c *client.Client, sel *selection, input string) (
	*api.SchedjobdtoSchedJobResp, error,
) {
	jobs, err := schedJobs(ctx, c, sel)
	if err != nil {
		return nil, err
	}
	job, err := resolve.Setting("scheduled job", input, " of "+sel.where(), jobs,
		func(j api.SchedjobdtoSchedJobResp) (string, string) { return j.Id, j.Name })
	if err != nil {
		return nil, err
	}
	return &job, nil
}

// scheduleWords says when a job runs: its cron expression, every so long, or
// only when run.
func scheduleWords(s *api.SchedjobdtoScheduleResp) string {
	switch {
	case s == nil:
		return "-"
	case textOf(s.CronExpr) != "":
		return *s.CronExpr
	case textOf(s.Interval) != "":
		return "every " + *s.Interval
	}
	return "-"
}
