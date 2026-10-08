package cmd

import (
	"cmp"
	"context"
	"slices"
	"strconv"
	"time"

	"github.com/spf13/cobra"

	"github.com/hivepaas/hivepaas-cli/internal/api"
	"github.com/hivepaas/hivepaas-cli/internal/client"
	"github.com/hivepaas/hivepaas-cli/internal/output"
	"github.com/hivepaas/hivepaas-cli/internal/resolve"
)

func (a *App) psCmd() *cobra.Command {
	var state string
	cmd := &cobra.Command{
		Use:   "ps [APP]",
		Short: "List an app's replicas: where each runs, its state, and why one failed",
		Long: "List the Swarm tasks of an app's service: each replica's current task and the ones before\n" +
			"it, the node it is on, its container, and the error of one that failed.",
		Example: "  hivepaas ps\n  hivepaas ps api --state running",
		Args:    usageArgs(cobra.MaximumNArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 {
				a.app = args[0]
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			sel, err := a.selectTarget(cmd.Context(), c, scopeApp)
			if err != nil {
				return err
			}
			tasks, err := serviceTasks(cmd.Context(), c, sel, state)
			if err != nil {
				return err
			}
			if a.printer.Structured() {
				return a.printer.Data(tasks)
			}
			if len(tasks) == 0 {
				a.printer.Infof("%s runs no replica.", sel.where())
				return nil
			}
			now := time.Now()
			rows := make([][]string, 0, len(tasks))
			for _, t := range tasks {
				rows = append(rows, psRow(t, now))
			}
			return a.printer.Table([]string{"REPLICA", "STATE", "DESIRED", "NODE", "CONTAINER", "SINCE", "ERROR"}, rows)
		},
	}
	cmd.Flags().StringVar(&state, "state", "",
		"only the tasks in these states, such as running or failed - several with commas")
	return cmd
}

// serviceTasks are an app's Swarm tasks, by replica, the newest of each first.
func serviceTasks(ctx context.Context, c *client.Client, sel *selection, state string) (
	[]api.AppsettingsdtoServiceTaskResp, error,
) {
	params := &api.GetAppServiceTasksParams{}
	if state != "" {
		params.State = &state
	}
	resp, err := c.GetAppServiceTasksWithResponse(ctx, sel.Project.Id, sel.Env, sel.App.Id, params)
	if err = client.Check(resp, err); err != nil {
		return nil, err
	}
	var tasks []api.AppsettingsdtoServiceTaskResp
	if resp.JSON200 != nil {
		tasks = resolve.Deref(resp.JSON200.Data)
	}
	slices.SortStableFunc(tasks, func(x, y api.AppsettingsdtoServiceTaskResp) int {
		if bySlot := cmp.Compare(x.Slot, y.Slot); bySlot != 0 {
			return bySlot
		}
		return cmp.Compare(taskTime(y), taskTime(x))
	})
	return tasks, nil
}

func psRow(t api.AppsettingsdtoServiceTaskResp, now time.Time) []string {
	replica, node, state, container, since, problem := strconv.Itoa(t.Slot), "-", "-", "-", "-", ""
	if t.Slot == 0 {
		replica = "-" // a global service's task is one per node
	}
	if t.Node != nil {
		node = cmp.Or(t.Node.Hostname, t.Node.Name, short(t.Node.Id))
	}
	if s := t.Status; s != nil {
		state = string(s.State)
		if s.ContainerStatus != nil && s.ContainerStatus.ContainerId != "" {
			container = short(s.ContainerStatus.ContainerId)
		}
		if s.Timestamp != "" {
			since = output.Ago(s.Timestamp, now)
		}
		problem = s.Err
	}
	return []string{replica, state, string(t.DesiredState), node, container, since, problem}
}

func taskTime(t api.AppsettingsdtoServiceTaskResp) string {
	if t.Status == nil {
		return ""
	}
	return t.Status.Timestamp
}
