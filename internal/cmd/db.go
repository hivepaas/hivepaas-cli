package cmd

import (
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/hivepaas/hivepaas-cli/internal/api"
	"github.com/hivepaas/hivepaas-cli/internal/client"
	"github.com/hivepaas/hivepaas-cli/internal/exitcode"
)

// markTimeout is how long mark waits for its answer: the server answers once
// the WAL the point is in is archived, which it waits twice the database's
// archive timeout for.
const markTimeout = 5 * time.Minute

func (a *App) dbCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "db",
		Short: "A managed database: the moments it can be restored to",
		Long: "A managed database, made from a managed template: its engine archives it, and it can be\n" +
			"restored into a new app to any moment its archive reaches. The dashboard restores it.",
	}
	cmd.AddCommand(a.dbMarkCmd())
	return cmd
}

func (a *App) dbMarkCmd() *cobra.Command {
	var keepUntil, note string
	cmd := &cobra.Command{
		Use:   "mark NAME",
		Short: "Name this moment of a managed database, to restore to it exactly",
		Long: "Name this moment of a managed database: a restore point, which a restore into a new app\n" +
			"recovers to exactly. It is listed once the WAL it is in is archived. It can be restored to\n" +
			"for as long as retention keeps the base backup before it - about retainFull nights - or\n" +
			"until --keep-until, at most 90 days ahead, which holds retention back and costs the\n" +
			"storage of everything archived from it on. A name is letters, digits, '.', '_' and '-',\n" +
			"at most 63, unique in the app; hp- is HivePaaS's own.",
		Example: "  hivepaas db mark before-v42 -a shop-db\n" +
			"  hivepaas db mark q4-release --keep-until 2026-12-31 --note \"before the Q4 release\"",
		Args: usageArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			req := api.AppmanageddtoCreateRestorePointReq{Name: strings.TrimSpace(args[0])}
			if note != "" {
				req.Note = &note
			}
			if keepUntil != "" {
				at, err := parseKeepUntil(keepUntil)
				if err != nil {
					return err
				}
				req.KeepUntil = &at
			}
			c, err := a.clientWithTimeout(markTimeout)
			if err != nil {
				return err
			}
			sel, err := a.selectTarget(cmd.Context(), c, scopeApp)
			if err != nil {
				return err
			}
			resp, err := c.AppManagedRestorePointCreateWithResponse(cmd.Context(), sel.Project.Id, sel.Env,
				sel.App.Id, req)
			if err = client.Check(resp, err); err != nil {
				return err
			}
			if resp.JSON200 == nil || resp.JSON200.Data == nil {
				return exitcode.New(exitcode.Server, "the server answered no restore point")
			}
			point := resp.JSON200.Data
			kept := ""
			if point.KeepUntil != nil {
				kept = ", kept until " + *point.KeepUntil
			}
			a.printer.Successf("Marked %s at %s on %s%s.", point.Name, point.Lsn, sel.where(), kept)
			return nil
		},
	}
	cmd.Flags().StringVar(&keepUntil, "keep-until", "",
		"hold retention back for the point until then: a date (2026-12-31) or an RFC 3339 time")
	cmd.Flags().StringVar(&note, "note", "", "what the moment is, for whoever restores to it")
	return cmd
}

// parseKeepUntil reads a date, as the start of that day in UTC, or an RFC 3339
// time.
func parseKeepUntil(value string) (string, error) {
	if at, err := time.Parse(time.DateOnly, value); err == nil {
		return at.UTC().Format(time.RFC3339), nil
	}
	if at, err := time.Parse(time.RFC3339, value); err == nil {
		return at.UTC().Format(time.RFC3339), nil
	}
	return "", exitcode.New(exitcode.Usage, "--keep-until %s is not a date such as 2026-12-31, nor an RFC 3339 time",
		value)
}
