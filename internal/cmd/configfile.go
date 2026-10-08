package cmd

import (
	"context"
	"encoding/base64"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/hivepaas/hivepaas-cli/internal/api"
	"github.com/hivepaas/hivepaas-cli/internal/client"
	"github.com/hivepaas/hivepaas-cli/internal/exitcode"
	"github.com/hivepaas/hivepaas-cli/internal/resolve"
)

func (a *App) configFileCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "config-file",
		Aliases: []string{"config-files"},
		Short:   "An app's config files: files HivePaaS keeps, mounted into its containers",
		Long: "An app's config files: an nginx.conf, a settings.yaml, kept by HivePaaS. A setting mount, made\n" +
			"in the dashboard, puts one at a path in the app's containers; a change reaches them on its\n" +
			"own, without a deployment.",
	}
	cmd.AddCommand(a.configFileLsCmd(), a.configFilePushCmd(), a.configFilePullCmd(), a.configFileRmCmd())
	return cmd
}

func (a *App) configFileLsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "ls",
		Short: "List an app's config files",
		Args:  usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, _, files, err := a.configFiles(cmd.Context())
			if err != nil {
				return err
			}
			if a.printer.Structured() {
				return a.printer.Data(files)
			}
			now := time.Now()
			rows := make([][]string, 0, len(files))
			for _, f := range files {
				rows = append(rows, storedRow(f.Name, f.Size, f.Base64, f.Inheritable, f.Inherited, f.UpdatedAt, now))
			}
			return a.printer.Table(storedColumns(colName), rows)
		},
	}
}

func (a *App) configFilePushCmd() *cobra.Command {
	var previews, noPreviews bool
	cmd := &cobra.Command{
		Use:   "push NAME FILE",
		Short: "Add a config file to an app, or change one, from a local file",
		Long: "Add a config file to an app, or change the one of that name, with FILE's content; - reads\n" +
			"stdin. A file that is not text is kept as it is. The app's pull request previews get it too\n" +
			"unless --no-previews.",
		Example: "  hivepaas config-file push nginx-conf ./nginx.conf",
		Args:    usageArgs(cobra.ExactArgs(2)), //nolint:mnd // NAME and FILE
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.configFilePush(cmd.Context(), args[0], args[1], previewsOf(previews, noPreviews))
		},
	}
	cmd.Flags().BoolVar(&previews, "previews", false, "the app's pull request previews get it too (default)")
	cmd.Flags().BoolVar(&noPreviews, "no-previews", false, "the app's pull request previews do not get it")
	cmd.MarkFlagsMutuallyExclusive("previews", "no-previews")
	return cmd
}

func (a *App) configFilePush(ctx context.Context, name, file string, previews *bool) error {
	var data []byte
	var err error
	if file == "-" {
		data, err = io.ReadAll(a.stdin)
	} else {
		data, err = os.ReadFile(file)
	}
	if err != nil {
		return exitcode.Wrap(exitcode.Usage, err)
	}
	content := valueOf(data)
	c, sel, files, err := a.configFiles(ctx)
	if err != nil {
		return err
	}
	current := ownConfigFile(files, name)
	if current == nil {
		resp, err := c.CreateAppConfigFileWithResponse(ctx, sel.Project.Id, sel.Env, sel.App.Id,
			api.ConfigfiledtoCreateConfigFileReq{Name: name, Content: content.text, Base64: content.base64,
				Inheritable: previews == nil || *previews})
		if err = client.Check(resp, err); err != nil {
			return err
		}
		if resp.JSON201 != nil {
			a.warnMeta(resp.JSON201.Meta)
		}
		a.printer.Successf("Added config file %s to %s.", name, sel.where())
		return nil
	}
	inheritable := current.Inheritable
	if previews != nil {
		inheritable = *previews
	}
	resp, err := c.UpdateAppConfigFileWithResponse(ctx, sel.Project.Id, sel.Env, sel.App.Id, current.Id,
		api.ConfigfiledtoUpdateConfigFileReq{Name: current.Name, Content: content.text, Base64: content.base64,
			Inheritable: inheritable, Default: current.Default != nil && *current.Default, UpdateVer: current.UpdateVer})
	if err = client.Check(resp, err); err != nil {
		return err
	}
	if resp.JSON200 != nil {
		a.warnMeta(resp.JSON200.Meta)
	}
	a.printer.Successf("Changed config file %s of %s.", name, sel.where())
	return nil
}

func (a *App) configFilePullCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "pull NAME [FILE]",
		Short: "Write an app's config file to a local file, or to stdout",
		Args:  usageArgs(cobra.RangeArgs(1, 2)), //nolint:mnd // NAME and FILE
		RunE: func(cmd *cobra.Command, args []string) error {
			_, sel, files, err := a.configFiles(cmd.Context())
			if err != nil {
				return err
			}
			f := findConfigFile(files, args[0])
			if f == nil {
				return exitcode.New(exitcode.NotFound, "%s is not a config file of %s", args[0], sel.where())
			}
			data := []byte(f.Content)
			if f.Base64 {
				if data, err = base64.StdEncoding.DecodeString(f.Content); err != nil {
					return exitcode.New(exitcode.Server, "config file %s: %v", f.Name, err)
				}
			}
			if len(args) == 1 || args[1] == "-" {
				_, err = a.stdout.Write(data)
				return err //nolint:wrapcheck // stdout
			}
			if err = os.WriteFile(args[1], data, 0o644); err != nil { //nolint:gosec,mnd // a file as touch makes one
				return exitcode.Wrap(exitcode.Failure, err)
			}
			a.printer.Successf("Wrote config file %s of %s to %s.", f.Name, sel.where(), args[1])
			return nil
		},
	}
}

func (a *App) configFileRmCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rm NAME...",
		Short: "Remove config files of an app",
		Args:  usageArgs(cobra.MinimumNArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			c, sel, files, err := a.configFiles(ctx)
			if err != nil {
				return err
			}
			args = slices.Compact(slices.Sorted(slices.Values(args)))
			found := make([]*api.ConfigfiledtoConfigFileResp, 0, len(args))
			for _, name := range args {
				f := ownConfigFile(files, name)
				if f == nil {
					return exitcode.New(exitcode.NotFound, "%s is not a config file of %s", name, sel.where())
				}
				found = append(found, f)
			}
			for i, f := range found {
				resp, err := c.DeleteAppConfigFileWithResponse(ctx, sel.Project.Id, sel.Env, sel.App.Id, f.Id)
				if err = client.Check(resp, err); err != nil {
					return a.removedSoFar(err, args[:i], sel)
				}
			}
			a.printer.Successf("Removed %s from %s.", strings.Join(args, ", "), sel.where())
			return nil
		},
	}
}

// configFiles are the app's config files, those of its project and env among
// them, with the client and the app they were read with.
func (a *App) configFiles(ctx context.Context) (
	*client.Client, *selection, []api.ConfigfiledtoConfigFileResp, error,
) {
	c, err := a.client()
	if err != nil {
		return nil, nil, nil, err
	}
	sel, err := a.selectTarget(ctx, c, scopeApp)
	if err != nil {
		return nil, nil, nil, err
	}
	var files []api.ConfigfiledtoConfigFileResp
	for offset := 0; ; offset += storedPage {
		resp, err := c.ListAppConfigFileWithResponse(ctx, sel.Project.Id, sel.Env, sel.App.Id, page(offset))
		if err = client.Check(resp, err); err != nil {
			return nil, nil, nil, err
		}
		if resp.JSON200 == nil {
			return c, sel, files, nil
		}
		items := resolve.Deref(resp.JSON200.Data)
		files = append(files, items...)
		if len(items) < storedPage {
			return c, sel, files, nil
		}
	}
}

// ownConfigFile is the app's own config file name names, not one it inherits.
func ownConfigFile(files []api.ConfigfiledtoConfigFileResp, name string) *api.ConfigfiledtoConfigFileResp {
	for i, f := range files {
		if f.Name == name && (f.Inherited == nil || !*f.Inherited) {
			return &files[i]
		}
	}
	return nil
}

// findConfigFile is the config file name names: the app's own first, else one
// it inherits.
func findConfigFile(files []api.ConfigfiledtoConfigFileResp, name string) *api.ConfigfiledtoConfigFileResp {
	if f := ownConfigFile(files, name); f != nil {
		return f
	}
	for i, f := range files {
		if f.Name == name {
			return &files[i]
		}
	}
	return nil
}
