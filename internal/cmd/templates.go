package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/hivepaas/hivepaas-cli/internal/api"
	"github.com/hivepaas/hivepaas-cli/internal/client"
	"github.com/hivepaas/hivepaas-cli/internal/exitcode"
	"github.com/hivepaas/hivepaas-cli/internal/resolve"
)

// templateSorts are --sort's words and the orders the API takes.
var templateSorts = map[string]string{
	"name": "name", "popular": "-stars", "trending": "-trending", "new": "-added",
}

const templatesPage = 1000

func (a *App) templatesCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "template", Aliases: []string{"templates"}, Short: "The template store"}
	var sort, search, category string
	ls := &cobra.Command{
		Use:   "ls",
		Short: "List the templates",
		Args:  usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			order, ok := templateSorts[sort]
			if !ok {
				return exitcode.New(exitcode.Usage, "--sort takes name, popular, trending or new, not %q", sort)
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			params := &api.ListAppTemplatesParams{Sort: &order, PageLimit: ptr(templatesPage)}
			if search != "" {
				params.Search = &search
			}
			if category != "" {
				params.Category = &category
			}
			resp, err := c.ListAppTemplatesWithResponse(cmd.Context(), params)
			if err = client.Check(resp, err); err != nil {
				return err
			}
			templates := resolve.Deref(resp.JSON200.Data)
			if a.printer.Structured() {
				return a.printer.Data(templates)
			}
			rows := make([][]string, 0, len(templates))
			for _, t := range templates {
				rows = append(rows, []string{t.Name, t.Title, stars(t.Stars), versionNames(t.Versions)})
			}
			return a.printer.Table([]string{colName, "TITLE", "STARS", "VERSIONS"}, rows)
		},
	}
	ls.Flags().StringVar(&sort, "sort", "name", "name, popular, trending or new")
	ls.Flags().StringVar(&search, "search", "", "match name, title, tagline, tags and aliases")
	ls.Flags().StringVar(&category, "category", "", "a category, such as databases or databases/sql")
	cmd.AddCommand(ls, a.templatesDeployCmd())
	return cmd
}

func stars(n int) string {
	switch {
	case n == 0:
		return "-"
	case n >= 1000: //nolint:mnd
		return fmt.Sprintf("%.1fk", float64(n)/1000) //nolint:mnd
	default:
		return fmt.Sprint(n)
	}
}

func versionNames(versions *[]api.ApptemplatedtoAppTemplateVersionResp) string {
	list := resolve.Deref(versions)
	names := make([]string, 0, len(list))
	for _, v := range list {
		if !v.Deprecated {
			names = append(names, v.Name)
		}
	}
	return strings.Join(names, ", ")
}
