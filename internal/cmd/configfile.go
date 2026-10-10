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
	"github.com/hivepaas/hivepaas-cli/internal/exitcode"
)

func (a *App) configFileCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "config-file",
		Aliases: []string{"config-files"},
		Short:   "Config files: files HivePaaS keeps, mounted into apps' containers",
		Long: "Config files: an nginx.conf, a settings.yaml, kept by HivePaaS for an app, or - with --scope\n" +
			"env or --scope project - for its environment or its project, whose apps get them unless\n" +
			"--no-inheritable. A setting mount, made in the dashboard, puts one at a path in an app's\n" +
			"containers; a change reaches them on its own, without a deployment.",
	}
	a.addScopeFlag(cmd, "config files")
	cmd.AddCommand(a.configFileLsCmd(), a.configFilePushCmd(), a.configFilePullCmd(), a.configFileRmCmd())
	return cmd
}

func (a *App) configFileLsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "ls",
		Short: "List the config files",
		Args:  usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			st, files, err := a.configFiles(cmd.Context())
			if err != nil {
				return err
			}
			if a.printer.Structured() {
				return a.printer.Data(files)
			}
			now := time.Now()
			rows := make([][]string, 0, len(files))
			for _, f := range files {
				rows = append(rows, storedRow(f.Name, f.Size, f.Base64, f.Inheritable, st.owner(f.Inherited),
					f.UpdatedAt, now))
			}
			return a.printer.Table(st.columns(colName), rows)
		},
	}
}

func (a *App) configFilePushCmd() *cobra.Command {
	var share shareFlags
	cmd := &cobra.Command{
		Use:   "push NAME FILE",
		Short: "Add a config file, or change one, from a local file",
		Long: "Add a config file, or change the one of that name, with FILE's content; - reads stdin. A file\n" +
			"that is not text is kept as it is. It is shared below unless --no-inheritable: an app's with\n" +
			"its pull request previews, an environment's or a project's with their apps. One changed keeps\n" +
			"how it is shared unless told.",
		Example: "  hivepaas config-file push nginx-conf ./nginx.conf\n" +
			"  hivepaas config-file push ca-bundle ./ca.pem --scope project",
		Args: usageArgs(cobra.ExactArgs(2)), //nolint:mnd // NAME and FILE
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.configFilePush(cmd.Context(), args[0], args[1], share)
		},
	}
	share.add(cmd)
	return cmd
}

func (a *App) configFilePush(ctx context.Context, name, file string, share shareFlags) error {
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
	st, files, err := a.configFiles(ctx)
	if err != nil {
		return err
	}
	shared, err := share.shared(st.level)
	if err != nil {
		return err
	}
	current := ownConfigFile(files, name)
	if current == nil {
		meta, err := st.createConfigFile(ctx, api.ConfigfiledtoCreateConfigFileReq{Name: name, Content: content.text,
			Base64: content.base64, Inheritable: shared == nil || *shared})
		if err != nil {
			return err
		}
		a.warnMeta(meta)
		a.printer.Successf("Added config file %s to %s.", name, st.where())
		return nil
	}
	inheritable := current.Inheritable
	if shared != nil {
		inheritable = *shared
	}
	meta, err := st.updateConfigFile(ctx, current.Id, api.ConfigfiledtoUpdateConfigFileReq{Name: current.Name,
		Content: content.text, Base64: content.base64, Inheritable: inheritable,
		Default: current.Default != nil && *current.Default, UpdateVer: current.UpdateVer})
	if err != nil {
		return err
	}
	a.warnMeta(meta)
	a.printer.Successf("Changed config file %s of %s.", name, st.where())
	return nil
}

func (a *App) configFilePullCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "pull NAME [FILE]",
		Short: "Write a config file to a local file, or to stdout",
		Args:  usageArgs(cobra.RangeArgs(1, 2)), //nolint:mnd // NAME and FILE
		RunE: func(cmd *cobra.Command, args []string) error {
			st, files, err := a.configFiles(cmd.Context())
			if err != nil {
				return err
			}
			f := findConfigFile(files, args[0])
			if f == nil {
				return exitcode.New(exitcode.NotFound, "%s is not a config file of %s", args[0], st.where())
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
			a.printer.Successf("Wrote config file %s of %s to %s.", f.Name, st.where(), args[1])
			return nil
		},
	}
}

func (a *App) configFileRmCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rm NAME...",
		Short: "Remove config files",
		Args:  usageArgs(cobra.MinimumNArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			st, files, err := a.configFiles(ctx)
			if err != nil {
				return err
			}
			args = slices.Compact(slices.Sorted(slices.Values(args)))
			found := make([]*api.ConfigfiledtoConfigFileResp, 0, len(args))
			for _, name := range args {
				f := ownConfigFile(files, name)
				if f == nil {
					return exitcode.New(exitcode.NotFound, "%s is not a config file of %s", name, st.where())
				}
				found = append(found, f)
			}
			for i, f := range found {
				if err := st.deleteConfigFile(ctx, f.Id); err != nil {
					return a.removedSoFar(err, args[:i], st)
				}
			}
			a.printer.Successf("Removed %s from %s.", strings.Join(args, ", "), st.where())
			return nil
		},
	}
}

// configFiles are the store's config files, those it inherits among them,
// with the store they were read from.
func (a *App) configFiles(ctx context.Context) (*store, []api.ConfigfiledtoConfigFileResp, error) {
	st, err := a.openStore(ctx)
	if err != nil {
		return nil, nil, err
	}
	files, err := st.configFiles(ctx)
	if err != nil {
		return nil, nil, err
	}
	return st, files, nil
}

// ownConfigFile is the store's own config file name names, not one it inherits.
func ownConfigFile(files []api.ConfigfiledtoConfigFileResp, name string) *api.ConfigfiledtoConfigFileResp {
	for i, f := range files {
		if f.Name == name && (f.Inherited == nil || !*f.Inherited) {
			return &files[i]
		}
	}
	return nil
}

// findConfigFile is the config file name names: the store's own first, else one
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
