package cmd

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"sync"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/hivepaas/hivepaas-cli/internal/client"
	"github.com/hivepaas/hivepaas-cli/internal/exitcode"
	"github.com/hivepaas/hivepaas-cli/internal/stream"
)

func (a *App) execCmd() *cobra.Command {
	var shell string
	cmd := &cobra.Command{
		Use:     "exec [APP]",
		Aliases: []string{"ssh"},
		Short:   "Open a shell in an app's container",
		Long: "Open a shell in an app's container: sh, or the one --shell names, in one of its running\n" +
			"containers. At a terminal the keys go to the shell as they are typed, Ctrl-C included;\n" +
			"exit, or Ctrl-D, ends it. Piped in, the input is typed into the shell, which ends with it.\n\n" +
			"The key needs the execute action, and the app's terminal must be on (its settings).",
		Example: "  hivepaas exec\n" +
			"  hivepaas exec worker --shell bash\n" +
			"  echo 'df -h' | hivepaas exec db",
		Args: usageArgs(cobra.MaximumNArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 {
				a.app = args[0]
			}
			return a.exec(cmd.Context(), shell)
		},
	}
	cmd.Flags().StringVar(&shell, "shell", "", "sh, bash, zsh or fish: sh when left out")
	return cmd
}

func (a *App) exec(ctx context.Context, shell string) error {
	c, err := a.client()
	if err != nil {
		return err
	}
	sel, err := a.selectTarget(ctx, c, scopeApp)
	if err != nil {
		return err
	}

	query := url.Values{}
	if shell != "" {
		query.Set("shell", shell)
	}
	session := stream.Session{In: a.stdin, Out: a.stdout}
	in, isTerminal := a.stdin.(*os.File)
	isTerminal = isTerminal && term.IsTerminal(int(in.Fd())) //nolint:gosec // a file descriptor fits an int
	sizeOf := func() (stream.Size, bool) {
		out, ok := a.stdout.(*os.File)
		if !ok {
			return stream.Size{}, false
		}
		w, h, err := term.GetSize(int(out.Fd()))                                        //nolint:gosec
		return stream.Size{Width: uint(max(w, 0)), Height: uint(max(h, 0))}, err == nil //nolint:gosec
	}
	if size, ok := sizeOf(); ok {
		query.Set("w", strconv.FormatUint(uint64(size.Width), 10))
		query.Set("h", strconv.FormatUint(uint64(size.Height), 10))
	}

	sessionCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	if isTerminal {
		session.Resize = watchSize(sessionCtx, sizeOf)
	}
	var restore sync.Once
	restoreTerminal := func() {}
	defer restore.Do(func() { restoreTerminal() })
	session.Opened = func(containerID, _ string) {
		a.printer.Infof("%s", a.printer.DimErr(fmt.Sprintf("Shell in %s, container %s. exit or Ctrl-D ends it.",
			sel.where(), short(containerID))))
		if isTerminal {
			// From here the keys are the shell's: Ctrl-C goes to it, not to the CLI.
			if state, err := term.MakeRaw(int(in.Fd())); err == nil { //nolint:gosec
				restoreTerminal = func() { _ = term.Restore(int(in.Fd()), state) } //nolint:gosec
			}
		}
	}

	t := &stream.Terminal{
		URL:    fmt.Sprintf("%s/projects/%s/%s/apps/%s/terminal", c.BaseURL, sel.Project.Id, sel.Env, sel.App.Id),
		Query:  query,
		Header: client.Headers(c.Target),
	}
	err = t.Run(sessionCtx, session)
	restore.Do(func() { restoreTerminal() }) // before anything more is written
	cancel()
	if errors.Is(err, context.Canceled) && ctx.Err() != nil {
		return exitcode.Reported(exitcode.Interrupted)
	}
	return err
}

// short is a container's id as docker shows it: its first twelve characters.
func short(id string) string {
	if len(id) > shortID {
		return id[:shortID]
	}
	return id
}

const shortID = 12
