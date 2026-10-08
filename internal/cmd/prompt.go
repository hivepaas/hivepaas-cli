package cmd

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"

	"github.com/hivepaas/hivepaas-cli/internal/exitcode"
)

// interactive says the CLI may ask: stdin and stderr are terminals.
func (a *App) interactive() bool {
	in, inOK := a.stdin.(*os.File)
	errw, errOK := a.stderr.(*os.File)
	return inOK && errOK && term.IsTerminal(int(in.Fd())) && term.IsTerminal(int(errw.Fd())) //nolint:gosec
}

// prompt asks for a line, when it may; missing is the flag that gives it.
func (a *App) prompt(label, missing string) (string, error) {
	if !a.interactive() {
		return "", exitcode.New(exitcode.Usage, "%s is missing: give %s", strings.TrimSuffix(label, ": "), missing)
	}
	fmt.Fprint(a.stderr, label)
	line, err := bufio.NewReader(a.stdin).ReadString('\n')
	if err != nil && err != io.EOF {
		return "", fmt.Errorf("reading %s: %w", label, err)
	}
	return strings.TrimSpace(line), nil
}

// promptSecret asks for a secret without echoing it.
func (a *App) promptSecret(label, missing string) (string, error) {
	if !a.interactive() {
		return "", exitcode.New(exitcode.Usage, "%s is missing: give %s", strings.TrimSuffix(label, ": "), missing)
	}
	fmt.Fprint(a.stderr, label)
	in, _ := a.stdin.(*os.File)
	secret, err := term.ReadPassword(int(in.Fd())) //nolint:gosec
	fmt.Fprintln(a.stderr)
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", label, err)
	}
	return strings.TrimSpace(string(secret)), nil
}
