package cmd

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/hivepaas/hivepaas-cli/internal/client"
	"github.com/hivepaas/hivepaas-cli/internal/exitcode"
)

// maxBody is how much of a response the api command reads.
const maxBody = 64 << 20

var apiMethods = []string{
	http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete,
	http.MethodHead, http.MethodOptions,
}

func (a *App) apiCmd() *cobra.Command {
	var data string
	cmd := &cobra.Command{
		Use:   "api METHOD PATH",
		Short: "Call any endpoint of the API, with the context's URL and key",
		Long: "Call any endpoint of the API, as the spec documents it: PATH is under /api.\n\n" +
			"{project}, {env} and {app} in PATH are filled in from the flags or the directory's link.\n" +
			"-d sends a JSON body: the text itself, @FILE, or @- for stdin. The response's body goes to\n" +
			"stdout as it came, JSON indented, or YAML with -o yaml.",
		Example: "  hivepaas api GET /projects/{project}/{env}/apps/{app}/routing-settings\n" +
			"  hivepaas api PUT /projects/{project}/{env}/apps/{app}/health-check-settings -d @health.json",
		Args: usageArgs(cobra.ExactArgs(2)), //nolint:mnd
		RunE: func(cmd *cobra.Command, args []string) error {
			method := strings.ToUpper(args[0])
			if !slices.Contains(apiMethods, method) {
				return exitcode.New(exitcode.Usage, "%s is not an HTTP method: %s", args[0],
					strings.Join(apiMethods, ", "))
			}
			body, err := a.apiBody(cmd.Context(), data)
			if err != nil {
				return err
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			path, err := a.apiPath(cmd, c, args[1])
			if err != nil {
				return err
			}
			resp, err := c.Raw(cmd.Context(), method, path, body)
			if err != nil {
				return err
			}
			defer resp.Body.Close()
			payload, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
			if err != nil {
				return exitcode.Wrap(exitcode.Server, fmt.Errorf("reading the response: %w", err))
			}
			if err = client.CheckStatus(resp.StatusCode, payload); err != nil {
				return err
			}
			if len(bytes.TrimSpace(payload)) == 0 {
				return nil
			}
			return a.printer.JSON(payload)
		},
	}
	cmd.Flags().StringVarP(&data, "data", "d", "", "the request's JSON body: the text, @FILE, or @- for stdin")
	return cmd
}

// apiBody is the body -d gives: none, the text itself, a file's or stdin's.
func (a *App) apiBody(ctx context.Context, data string) (io.Reader, error) {
	switch {
	case data == "":
		return nil, nil
	case data == "@-":
		content, err := a.readStdin(ctx, "the body")
		if err != nil {
			return nil, err
		}
		return strings.NewReader(content), nil
	case strings.HasPrefix(data, "@"):
		content, err := os.ReadFile(data[1:])
		if err != nil {
			return nil, exitcode.Wrap(exitcode.Usage, fmt.Errorf("reading the body: %w", err))
		}
		return bytes.NewReader(content), nil
	}
	return strings.NewReader(data), nil
}

// apiPath is PATH under the API, with {project}, {env} and {app} filled in
// from the target, as far as it names them.
func (a *App) apiPath(cmd *cobra.Command, c *client.Client, path string) (string, error) {
	path = "/" + strings.TrimPrefix(strings.TrimPrefix(path, "/api/"), "/")
	need := scope(-1)
	for i, placeholder := range []string{"{project}", "{env}", "{app}"} {
		if strings.Contains(path, placeholder) {
			need = scope(i)
		}
	}
	if need < 0 {
		return path, nil
	}
	sel, err := a.selectTarget(cmd.Context(), c, need)
	if err != nil {
		return "", err
	}
	path = strings.ReplaceAll(path, "{project}", url.PathEscape(sel.Project.Id))
	if need >= scopeEnv {
		path = strings.ReplaceAll(path, "{env}", url.PathEscape(sel.Env))
	}
	if need >= scopeApp {
		path = strings.ReplaceAll(path, "{app}", url.PathEscape(sel.App.Id))
	}
	return path, nil
}
