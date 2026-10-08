package cmd

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"

	"github.com/hivepaas/hivepaas-cli/internal/exitcode"
)

// interactive says the CLI may ask: stdin and stderr are terminals.
func (a *App) interactive() bool {
	if a.completing {
		return false
	}
	in, inOK := a.stdin.(*os.File)
	errw, errOK := a.stderr.(*os.File)
	return inOK && errOK && term.IsTerminal(int(in.Fd())) && term.IsTerminal(int(errw.Fd())) //nolint:gosec
}

// prompt asks for a line, when it may; missing is the flag that gives it.
func (a *App) prompt(ctx context.Context, label, missing string) (string, error) {
	if !a.interactive() {
		return "", exitcode.New(exitcode.Usage, "%s is missing: give %s", strings.TrimSuffix(label, ": "), missing)
	}
	fmt.Fprint(a.stderr, label)
	line, err := a.read(ctx, func() (string, error) {
		line, err := bufio.NewReader(a.stdin).ReadString('\n')
		if err != nil && err != io.EOF {
			return "", fmt.Errorf("reading %s: %w", label, err)
		}
		return line, nil
	}, nil)
	return strings.TrimSpace(line), err
}

// promptSecret asks for a secret without echoing it.
func (a *App) promptSecret(ctx context.Context, label, missing string) (string, error) {
	if !a.interactive() {
		return "", exitcode.New(exitcode.Usage, "%s is missing: give %s", strings.TrimSuffix(label, ": "), missing)
	}
	fmt.Fprint(a.stderr, label)
	fd := int(a.stdin.(*os.File).Fd()) //nolint:forcetypeassert,gosec // interactive says it is a terminal
	// ReadPassword turns the echo off until it returns: a Ctrl-C that leaves it
	// waiting must turn it back on, or the shell after the CLI shows nothing typed.
	state, err := term.GetState(fd)
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", label, err)
	}
	secret, err := a.read(ctx, func() (string, error) {
		secret, err := term.ReadPassword(fd)
		if err != nil {
			return "", fmt.Errorf("reading %s: %w", label, err)
		}
		return string(secret), nil
	}, func() { _ = term.Restore(fd, state) })
	if !exitcode.IsReported(err) {
		fmt.Fprintln(a.stderr) // the Enter the terminal did not echo
	}
	return strings.TrimSpace(secret), err
}

// readStdin is all of stdin: a secret or a body given through a pipe.
func (a *App) readStdin(ctx context.Context, what string) (string, error) {
	return a.read(ctx, func() (string, error) {
		data, err := io.ReadAll(a.stdin)
		if err != nil {
			return "", fmt.Errorf("reading %s from stdin: %w", what, err)
		}
		return string(data), nil
	}, nil)
}

// read waits for what read reads from the terminal, or for Ctrl-C: a read does
// not end on its own when the context does, and a CLI waiting on one would not
// stop. restore, when given, puts the terminal back as it was.
func (a *App) read(ctx context.Context, read func() (string, error), restore func()) (string, error) {
	type answer struct {
		text string
		err  error
	}
	answered := make(chan answer, 1)
	go func() {
		text, err := read()
		answered <- answer{text, err}
	}()
	select {
	case got := <-answered:
		return got.text, got.err
	case <-ctx.Done():
		if restore != nil {
			restore()
		}
		fmt.Fprintln(a.stderr)
		return "", exitcode.Reported(exitcode.Interrupted)
	}
}
