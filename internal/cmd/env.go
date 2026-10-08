package cmd

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/hivepaas/hivepaas-cli/internal/api"
	"github.com/hivepaas/hivepaas-cli/internal/carry"
	"github.com/hivepaas/hivepaas-cli/internal/client"
	"github.com/hivepaas/hivepaas-cli/internal/exitcode"
	"github.com/hivepaas/hivepaas-cli/internal/resolve"
)

// envKinds are the lists of variables an app keeps, as the commands name them.
const (
	kindRuntime = "runtime"
	kindBuild   = "build"
	kindShared  = "shared"
)

// envKindFlags choose a list: the runtime one unless --build or --shared.
type envKindFlags struct {
	build, shared bool
}

func (f *envKindFlags) add(cmd *cobra.Command, verb string) {
	cmd.Flags().BoolVar(&f.build, "build", false, verb+" the build-time variables")
	cmd.Flags().BoolVar(&f.shared, "shared", false, verb+" the shared variables")
	cmd.MarkFlagsMutuallyExclusive("build", "shared")
}

func (f *envKindFlags) kind() string {
	switch {
	case f.build:
		return kindBuild
	case f.shared:
		return kindShared
	}
	return kindRuntime
}

func (a *App) envCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "env", Short: "An app's environment variables"}
	cmd.AddCommand(a.envLsCmd(), a.envSetCmd(), a.envUnsetCmd(), a.envPullCmd())
	return cmd
}

func (a *App) envLsCmd() *cobra.Command {
	var all bool
	var kinds envKindFlags
	cmd := &cobra.Command{
		Use:   "ls",
		Short: "List an app's environment variables",
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
			vars, err := envVars(cmd.Context(), c, sel)
			if err != nil {
				return err
			}
			if a.printer.Structured() {
				return a.printer.Data(vars)
			}
			only := ""
			if kinds.build || kinds.shared {
				only = kinds.kind()
			}
			return a.printer.Table(envHeader(all), envRows(vars, only, all))
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "with those HivePaaS sets and those the app inherits")
	kinds.add(cmd, "only")
	return cmd
}

func envHeader(all bool) []string {
	if all {
		return []string{colKind, "KEY", "VALUE", "FROM"}
	}
	return []string{colKind, "KEY", "VALUE"}
}

// envRows are the variables of the lists, the runtime ones first: the app's
// own, and with all those HivePaaS sets and those it inherits.
func envRows(vars *api.AppsettingsdtoEnvVarsResp, only string, all bool) [][]string {
	type list struct {
		kind      string
		own       *[]api.BasedtoEnvVarResp
		inherited *[]api.BasedtoEnvVarResp
	}
	lists := []list{
		{kindRuntime, vars.RuntimeEnvVars, vars.InheritedRuntimeEnvVars},
		{kindBuild, vars.BuildtimeEnvVars, vars.InheritedBuildtimeEnvVars},
		{kindShared, vars.SharedEnvVars, nil},
	}
	var rows [][]string
	for _, l := range lists {
		if only != "" && l.kind != only {
			continue
		}
		for _, v := range resolve.Deref(l.own) {
			system := v.IsSystem != nil && *v.IsSystem
			switch {
			case !all && system:
			case !all:
				rows = append(rows, []string{l.kind, v.Key, v.Value})
			case system:
				rows = append(rows, []string{l.kind, v.Key, v.Value, "hivepaas"})
			default:
				rows = append(rows, []string{l.kind, v.Key, v.Value, "app"})
			}
		}
		if all {
			for _, v := range resolve.Deref(l.inherited) {
				rows = append(rows, []string{l.kind, v.Key, v.Value, "inherited"})
			}
		}
	}
	return rows
}

func (a *App) envSetCmd() *cobra.Command {
	var kinds envKindFlags
	var literal bool
	var file string
	cmd := &cobra.Command{
		Use:   "set [KEY=VALUE...]",
		Short: "Set environment variables of an app",
		Long: "Set environment variables of an app: the runtime ones, or with --build or --shared the\n" +
			"build-time or shared ones. A value may name others, ${db.HIVEPAAS_URL}, unless --literal.\n\n" +
			"--file sets those of a .env file, - for stdin; KEY=VALUE arguments beside it win.",
		Example: "  hivepaas env set LOG_LEVEL=debug FEATURE_X=on\n" +
			"  hivepaas env set --file .env.production\n" +
			"  hivepaas env set --build NODE_ENV=production",
		Args: usageArgs(cobra.ArbitraryArgs),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 && file == "" {
				return exitcode.New(exitcode.Usage, "give KEY=VALUE, or --file with a .env file")
			}
			pairs, err := a.envFile(cmd.Context(), file)
			if err != nil {
				return err
			}
			given, err := keyValues(args)
			if err != nil {
				return err
			}
			for _, pair := range given {
				pairs = appendPair(pairs, pair[0], pair[1])
			}
			kind := kinds.kind()
			return a.changeEnvVars(cmd.Context(), kind, func(_ *api.AppsettingsdtoEnvVarsResp,
				list *[]api.BasedtoEnvVarReq,
			) (string, error) {
				var changed, added []string
				for _, pair := range pairs {
					v := api.BasedtoEnvVarReq{Key: pair[0], Value: pair[1], IsLiteral: literal}
					i := slices.IndexFunc(*list, func(e api.BasedtoEnvVarReq) bool { return e.Key == v.Key })
					switch {
					case i < 0:
						*list = append(*list, v)
						added = append(added, v.Key)
					case (*list)[i] != v:
						(*list)[i] = v
						changed = append(changed, v.Key)
					}
				}
				return summary(changed, added), nil
			})
		},
	}
	kinds.add(cmd, "set")
	cmd.Flags().BoolVar(&literal, "literal", false, "take the values as they are, without expanding ${...}")
	cmd.Flags().StringVar(&file, "file", "", "set the variables of this .env file; - reads stdin")
	return cmd
}

// envFile is what a .env file sets: none without one.
func (a *App) envFile(ctx context.Context, file string) ([][2]string, error) {
	var content string
	switch file {
	case "":
		return nil, nil
	case "-":
		var err error
		if content, err = a.readStdin(ctx, "the .env file"); err != nil {
			return nil, err
		}
	default:
		data, err := os.ReadFile(file)
		if err != nil {
			return nil, exitcode.Wrap(exitcode.Usage, fmt.Errorf("reading %s: %w", file, err))
		}
		content = string(data)
	}
	pairs, err := parseDotenv(content)
	if err != nil {
		return nil, exitcode.New(exitcode.Usage, "%s: %s", firstOf(strings.TrimPrefix(file, "-"), "stdin"), err)
	}
	return pairs, nil
}

func (a *App) envPullCmd() *cobra.Command {
	var build, force bool
	cmd := &cobra.Command{
		Use:   "pull [FILE]",
		Short: "Write an app's environment variables as a .env file, to run it on this machine",
		Long: "Write the variables an app runs with as a .env file: those it inherits, its shared ones and\n" +
			"its runtime ones, or with --build its build-time ones - the app's own over what it inherits.\n" +
			"Those HivePaaS sets, which describe the app where it runs, are left out. A value naming\n" +
			"others, ${db.HIVEPAAS_URL}, is written as it is: only the server expands it.\n\n" +
			"Without FILE it writes to stdout. A FILE that exists is replaced only with --force; the file\n" +
			"is made readable by you alone, for it holds secrets.",
		Example: "  hivepaas env pull .env.local\n" +
			"  hivepaas env pull -a worker > worker.env",
		Args: usageArgs(cobra.MaximumNArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			sel, err := a.selectTarget(cmd.Context(), c, scopeApp)
			if err != nil {
				return err
			}
			vars, err := envVars(cmd.Context(), c, sel)
			if err != nil {
				return err
			}
			pairs, references := pulled(vars, build)
			for _, key := range references {
				a.printer.Warnf("%s names other variables, which only the server expands: it is written as it is", key)
			}
			content := formatDotenv(pairs)
			if len(args) == 0 {
				_, err = fmt.Fprint(a.stdout, content)
				return err //nolint:wrapcheck
			}
			if err = writeSecretFile(args[0], content, force); err != nil {
				return err
			}
			a.printer.Successf("Wrote %d variables of %s to %s.", len(pairs), sel.where(), args[0])
			return nil
		},
	}
	cmd.Flags().BoolVar(&build, "build", false, "the build-time variables instead of the runtime ones")
	cmd.Flags().BoolVar(&force, "force", false, "replace FILE when it exists")
	return cmd
}

// pulled are the variables an app runs with - or is built with - as pairs: the
// inherited ones, then its shared ones, then its runtime or build-time ones,
// each over those before it; HivePaaS's own left out. references are the keys
// whose values name other variables.
func pulled(vars *api.AppsettingsdtoEnvVarsResp, build bool) (pairs [][2]string, references []string) {
	lists := []*[]api.BasedtoEnvVarResp{vars.InheritedRuntimeEnvVars, vars.SharedEnvVars, vars.RuntimeEnvVars}
	if build {
		lists = []*[]api.BasedtoEnvVarResp{vars.InheritedBuildtimeEnvVars, vars.SharedEnvVars, vars.BuildtimeEnvVars}
	}
	for _, list := range lists {
		for _, v := range resolve.Deref(list) {
			if v.IsSystem != nil && *v.IsSystem {
				continue
			}
			pairs = appendPair(pairs, v.Key, v.Value)
		}
	}
	for _, pair := range pairs {
		if strings.Contains(pair[1], "${") && !isLiteral(vars, pair[0]) {
			references = append(references, pair[0])
		}
	}
	return pairs, references
}

// isLiteral says the variable key, as the app's own lists have it, is not
// expanded.
func isLiteral(vars *api.AppsettingsdtoEnvVarsResp, key string) bool {
	literal := false
	for _, list := range []*[]api.BasedtoEnvVarResp{vars.InheritedRuntimeEnvVars, vars.InheritedBuildtimeEnvVars,
		vars.SharedEnvVars, vars.RuntimeEnvVars, vars.BuildtimeEnvVars} {
		for _, v := range resolve.Deref(list) {
			if v.Key == key {
				literal = v.IsLiteral != nil && *v.IsLiteral
			}
		}
	}
	return literal
}

// writeSecretFile writes content to path, readable by its owner alone, and
// never over a file that is there unless force.
func writeSecretFile(path, content string, force bool) error {
	flags := os.O_WRONLY | os.O_CREATE | os.O_EXCL
	if force {
		flags = os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	}
	f, err := os.OpenFile(path, flags, secretFileMode)
	if errors.Is(err, fs.ErrExist) {
		return exitcode.New(exitcode.Usage, "%s exists: give --force to replace it", path)
	}
	if err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	if _, err = f.WriteString(content); err != nil {
		_ = f.Close()
		return fmt.Errorf("writing %s: %w", path, err)
	}
	if err = f.Chmod(secretFileMode); err != nil {
		_ = f.Close()
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return f.Close() //nolint:wrapcheck
}

// secretFileMode is a file of secrets: its owner's alone.
const secretFileMode = 0o600

func (a *App) envUnsetCmd() *cobra.Command {
	var kinds envKindFlags
	cmd := &cobra.Command{
		Use:   "unset KEY...",
		Short: "Remove environment variables of an app",
		Args:  usageArgs(cobra.MinimumNArgs(1)),
		RunE: func(cmd *cobra.Command, keys []string) error {
			kind := kinds.kind()
			return a.changeEnvVars(cmd.Context(), kind, func(vars *api.AppsettingsdtoEnvVarsResp,
				list *[]api.BasedtoEnvVarReq,
			) (string, error) {
				for _, key := range keys {
					i := slices.IndexFunc(*list, func(e api.BasedtoEnvVarReq) bool { return e.Key == key })
					if i < 0 {
						if isSystemVar(vars, kind, key) {
							return "", exitcode.New(exitcode.Invalid, "%s is set by HivePaaS: it cannot be removed", key)
						}
						return "", exitcode.New(exitcode.NotFound, "there is no %s variable %s", kind, key)
					}
					*list = slices.Delete(*list, i, i+1)
				}
				return "removed " + strings.Join(keys, ", "), nil
			})
		},
	}
	kinds.add(cmd, "remove from")
	return cmd
}

// changeEnvVars reads an app's variables, changes the list of kind, and writes
// them back under their updateVer. A change made meanwhile makes the write
// fail: the variables are read, and changed, once more. change answers what it
// did, nothing when there was nothing to do.
func (a *App) changeEnvVars(ctx context.Context, kind string,
	change func(vars *api.AppsettingsdtoEnvVarsResp, list *[]api.BasedtoEnvVarReq) (string, error),
) error {
	c, err := a.client()
	if err != nil {
		return err
	}
	sel, err := a.selectTarget(ctx, c, scopeApp)
	if err != nil {
		return err
	}
	for attempt := 0; ; attempt++ {
		vars, err := envVars(ctx, c, sel)
		if err != nil {
			return err
		}
		req, err := carry.EnvVars(vars)
		if err != nil {
			return err
		}
		list := map[string]*[]api.BasedtoEnvVarReq{
			kindRuntime: req.RuntimeEnvVars, kindBuild: req.BuildtimeEnvVars, kindShared: req.SharedEnvVars,
		}[kind]
		did, err := change(vars, list)
		if err != nil {
			return err
		}
		if did == "" {
			a.printer.Infof("Nothing to change on %s.", sel.where())
			return nil
		}
		resp, err := c.UpdateAppEnvVarsWithResponse(ctx, sel.Project.Id, sel.Env, sel.App.Id, *req)
		err = client.Check(resp, err)
		var apiErr *client.APIError
		if attempt == 0 && errors.As(err, &apiErr) && apiErr.Info.Code == errUpdateVerMismatched {
			continue
		}
		if err != nil {
			return err
		}
		a.printer.Successf("%s (%s) on %s.", capitalize(did), kind, sel.where())
		if resp.JSON200 != nil && resp.JSON200.Meta != nil && resp.JSON200.Meta.Warning != nil {
			a.printer.Warnf("%s", *resp.JSON200.Meta.Warning)
		}
		return nil
	}
}

func envVars(ctx context.Context, c *client.Client, sel *selection) (*api.AppsettingsdtoEnvVarsResp, error) {
	resp, err := c.GetAppEnvVarsWithResponse(ctx, sel.Project.Id, sel.Env, sel.App.Id)
	if err = client.Check(resp, err); err != nil {
		return nil, err
	}
	if resp.JSON200 == nil || resp.JSON200.Data == nil {
		return nil, exitcode.New(exitcode.Server, "the server answered no environment variables")
	}
	return resp.JSON200.Data, nil
}

func isSystemVar(vars *api.AppsettingsdtoEnvVarsResp, kind, key string) bool {
	list := map[string]*[]api.BasedtoEnvVarResp{
		kindRuntime: vars.RuntimeEnvVars, kindBuild: vars.BuildtimeEnvVars, kindShared: vars.SharedEnvVars,
	}[kind]
	return slices.ContainsFunc(resolve.Deref(list), func(v api.BasedtoEnvVarResp) bool {
		return v.Key == key && v.IsSystem != nil && *v.IsSystem
	})
}

// keyValues are KEY=VALUE arguments as pairs, the last of a key repeated.
func keyValues(args []string) ([][2]string, error) {
	var pairs [][2]string
	for _, arg := range args {
		key, value, found := strings.Cut(arg, "=")
		key = strings.TrimSpace(key)
		if !found || key == "" {
			return nil, exitcode.New(exitcode.Usage, "%q is not KEY=VALUE", arg)
		}
		pairs = slices.DeleteFunc(pairs, func(p [2]string) bool { return p[0] == key })
		pairs = append(pairs, [2]string{key, value})
	}
	return pairs, nil
}

// summary says what a set did: changed LOG_LEVEL, added FEATURE_X.
func summary(changed, added []string) string {
	var parts []string
	if len(changed) > 0 {
		parts = append(parts, "changed "+strings.Join(changed, ", "))
	}
	if len(added) > 0 {
		parts = append(parts, "added "+strings.Join(added, ", "))
	}
	return strings.Join(parts, ", ")
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
