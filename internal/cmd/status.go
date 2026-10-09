package cmd

import (
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

func (a *App) statusCmd() *cobra.Command {
	var fail bool
	cmd := &cobra.Command{
		Use:   "status",
		Short: "What needs attention on the installation: apps not running, nodes down",
		Long: "What Home's \"Needs attention\" lists, for the whole installation: an app that is not running or\n" +
			"keeps restarting, a node that is down or over-committed - with where, since when and what is\n" +
			"known. --fail exits 1 when anything is listed, for a script that checks.",
		Args: usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			resp, err := c.GetHomeAttentionWithResponse(cmd.Context())
			if err = client.Check(resp, err); err != nil {
				return err
			}
			var items []api.HomedtoAttentionItemResp
			if resp.JSON200 != nil && resp.JSON200.Data != nil {
				items = resolve.Deref(resp.JSON200.Data.Items)
			}
			if err = a.showAttention(items); err != nil {
				return err
			}
			if fail && len(items) > 0 {
				return exitcode.Reported(exitcode.Failure)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&fail, "fail", false, "exit 1 when anything needs attention")
	return cmd
}

func (a *App) showAttention(items []api.HomedtoAttentionItemResp) error {
	if a.printer.Structured() {
		return a.printer.Data(items)
	}
	if len(items) == 0 {
		a.printer.Successf("Nothing needs attention.")
		return nil
	}
	now := time.Now()
	rows := make([][]string, 0, len(items))
	for _, item := range items {
		since := "-"
		if item.Since != nil {
			since = output.Ago(*item.Since, now)
		}
		rows = append(rows, []string{item.Severity, item.Kind, attentionWhere(item), since, attentionDetail(item)})
	}
	return a.printer.Table([]string{"SEVERITY", "KIND", "WHERE", "SINCE", "DETAIL"}, rows)
}

// attentionWhere is where an item is: an app's project, env and name, or what
// the item is about - a node.
func attentionWhere(item api.HomedtoAttentionItemResp) string {
	if item.Project != nil && item.App != nil {
		return item.Project.Name + " / " + textOf(item.Env) + " / " + item.App.Name
	}
	return item.Subject
}

// attentionDetail is what is known of an item: replicas, restarts, the last
// error, a node's state, its memory.
func attentionDetail(item api.HomedtoAttentionItemResp) string {
	var parts []string
	if item.Desired != nil {
		parts = append(parts, fmt.Sprintf("%d of %d running", deref(item.Running), *item.Desired))
	}
	if item.Restarts != nil {
		parts = append(parts, fmt.Sprintf("%d restarts", *item.Restarts))
	}
	if item.NodeState != nil {
		parts = append(parts, *item.NodeState)
	}
	if item.MemoryLimitsBytes != nil && item.MemoryTotalBytes != nil {
		parts = append(parts, fmt.Sprintf("memory asked %s of %s", sizeOf(int64(*item.MemoryLimitsBytes)),
			sizeOf(int64(*item.MemoryTotalBytes))))
	}
	if item.LastError != nil && *item.LastError != "" {
		parts = append(parts, *item.LastError)
	}
	if len(parts) == 0 {
		return "-"
	}
	return strings.Join(parts, "; ")
}

// deref is what p points to, or the zero value.
func deref[T any](p *T) T {
	var zero T
	if p == nil {
		return zero
	}
	return *p
}
