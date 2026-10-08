// Package cmd is the CLI's commands.
package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/hivepaas/hivepaas-cli/internal/client"
	"github.com/hivepaas/hivepaas-cli/internal/config"
	"github.com/hivepaas/hivepaas-cli/internal/exitcode"
	"github.com/hivepaas/hivepaas-cli/internal/output"
	"github.com/hivepaas/hivepaas-cli/internal/selfupdate"
)

// App is what every command shares: the global flags, the configuration, and
// where it reads and writes.
type App struct {
	contextName string
	project     string
	env         string
	app         string
	format      string
	noColor     bool
	debug       bool
	ignoreLink  bool
	// completing says the CLI answers the shell's Tab: it asks nothing.
	completing bool

	stdin  io.Reader
	stdout io.Writer
	stderr io.Writer
	getenv func(string) string

	dir     string
	cfg     *config.Config
	secrets *config.Secrets
	printer *output.Printer

	// newUpdater makes the updater of `update`, and executable finds the binary
	// it replaces; nil for the real ones.
	newUpdater func() (*selfupdate.Updater, error)
	executable func() (string, error)
}

// Execute runs the CLI and answers its exit code.
func Execute() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// The first Ctrl-C asks the command to stop, and to say what it leaves going -
	// a deployment that goes on on the server. The next one is the shell's again,
	// and ends the CLI at once, whatever it is waiting for.
	go func() {
		<-ctx.Done()
		stop()
	}()
	app := &App{stdin: os.Stdin, stdout: os.Stdout, stderr: os.Stderr, getenv: os.Getenv}
	return app.Run(ctx, os.Args[1:])
}

// Run runs the CLI with args.
func (a *App) Run(ctx context.Context, args []string) int {
	root := a.rootCmd() //nolint:contextcheck // cobra hands each command the context: cmd.Context()
	root.SetArgs(args)
	root.SetIn(a.stdin)
	root.SetOut(a.stdout)
	root.SetErr(a.stderr)
	notice := a.startNotice(ctx, root, args)
	err := root.ExecuteContext(ctx)
	if err != nil {
		a.reportError(err)
	}
	if line := notice(); line != "" {
		fmt.Fprintln(a.stderr, line)
	}
	return exitcode.Of(err)
}

func (a *App) rootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "hivepaas",
		Short:         "The command-line client of a HivePaaS installation",
		SilenceErrors: true,
		SilenceUsage:  true,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			return a.init()
		},
	}
	flags := root.PersistentFlags()
	flags.StringVar(&a.contextName, "context", "", "the installation to use, instead of the current one")
	flags.StringVarP(&a.project, "project", "p", "", "project, by name, key or id")
	flags.StringVarP(&a.env, "env", "e", "", "environment, by name")
	flags.StringVarP(&a.app, "app", "a", "", "app, by name, key or id")
	flags.StringVarP(&a.format, "output", "o", output.Table, "table, json or yaml")
	flags.BoolVar(&a.noColor, "no-color", false, "no colors")
	flags.BoolVar(&a.debug, "debug", false, "log each request to stderr")
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		return exitcode.Wrap(exitcode.Usage, err)
	})

	root.AddCommand(
		a.versionCmd(), a.updateCmd(),
		a.loginCmd(), a.logoutCmd(), a.whoamiCmd(), a.contextCmd(),
		a.projectsCmd(), a.appsCmd(), a.linkCmd(), a.unlinkCmd(),
		a.deployCmd(), a.logsCmd(), a.restartCmd(), a.envCmd(), a.templatesCmd(), a.openCmd(), a.apiCmd(),
	)
	a.registerCompletions(root)
	return root
}

// init reads the configuration once the flags are parsed.
func (a *App) init() error {
	printer, err := output.New(a.stdout, a.stderr, a.format, a.noColor)
	if err != nil {
		return exitcode.Wrap(exitcode.Usage, err)
	}
	a.printer = printer
	if a.getenv("HIVEPAAS_DEBUG") != "" {
		a.debug = true
	}
	if a.dir, err = config.Dir(a.getenv); err != nil {
		return err
	}
	if a.cfg, err = config.Load(a.dir); err != nil {
		return err
	}
	a.secrets = config.NewSecrets(a.dir)
	return nil
}

// target is the installation the command talks to.
func (a *App) target() (*config.Target, error) {
	target, err := config.ResolveTarget(a.cfg, a.secrets, a.contextName, a.getenv)
	if errors.Is(err, config.ErrNotLoggedIn) {
		return nil, exitcode.Wrap(exitcode.Auth, err)
	}
	if err != nil {
		return nil, exitcode.Wrap(exitcode.Usage, err)
	}
	return target, nil
}

// client is a client of the installation the command talks to.
func (a *App) client() (*client.Client, error) {
	target, err := a.target()
	if err != nil {
		return nil, err
	}
	return a.clientOf(target)
}

func (a *App) clientOf(target *config.Target) (*client.Client, error) {
	opts := client.Options{
		Warn:          func(message string) { a.printer.Warnf("%s", message) },
		UpdateCommand: a.updateCommand(),
	}
	if a.debug {
		opts.Debug = a.stderr
	}
	return client.New(target, opts)
}

// reportError says what went wrong: the API's error itself with -o json, for
// a script to read, and a line for a person otherwise.
func (a *App) reportError(err error) {
	if exitcode.IsReported(err) {
		return
	}
	var apiErr *client.APIError
	if a.printer != nil && a.printer.Structured() && errors.As(err, &apiErr) {
		data, _ := json.MarshalIndent(apiErr.Info, "", "  ")
		fmt.Fprintln(a.stderr, string(data))
		return
	}
	if a.printer != nil {
		a.printer.Errorf("%s", err)
	} else {
		fmt.Fprintln(a.stderr, "Error:", err)
	}
	if exitcode.Of(err) == exitcode.CLIOutdated {
		fmt.Fprintf(a.stderr, "Update it: %s\n", a.updateCommand())
	}
}

// updateCommand is what updates this CLI: hivepaas update, or the package
// manager's command when one installed it.
func (a *App) updateCommand() string {
	path, err := a.exe()
	if err != nil {
		return "hivepaas update"
	}
	return selfupdate.UpdateCommand(path)
}

// usageArgs checks a command's arguments as cobra does, and makes a mistake a
// usage error.
func usageArgs(check cobra.PositionalArgs) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		return exitcode.Wrap(exitcode.Usage, check(cmd, args))
	}
}
