// Package exitcode is the CLI's exit codes, a contract with the scripts and CI
// jobs that run it: docs/superpowers/specs/2026-10-08-cli-design.md, section 7.
package exitcode

import (
	"errors"
	"fmt"
)

const (
	OK          = 0
	Failure     = 1
	Usage       = 2
	Auth        = 3
	Forbidden   = 4
	NotFound    = 5
	Invalid     = 6
	Server      = 7
	Deployment  = 8
	Timeout     = 9
	CLIOutdated = 10
	Interrupted = 130
)

// Error is an error that says how the CLI exits.
type Error struct {
	Code int
	Err  error
	// Reported says the command has told the person already: the CLI exits
	// with Code and prints nothing more.
	Reported bool
}

func (e *Error) Error() string { return e.Err.Error() }
func (e *Error) Unwrap() error { return e.Err }

// ExitCode is Code: the outermost code an error carries is the one it exits
// with.
func (e *Error) ExitCode() int { return e.Code }

// New is an error exiting with code.
func New(code int, format string, args ...any) error {
	return &Error{Code: code, Err: fmt.Errorf(format, args...)} //nolint:err113
}

// Wrap is err, exiting with code.
func Wrap(code int, err error) error {
	if err == nil {
		return nil
	}
	return &Error{Code: code, Err: err}
}

// Reported is an exit with code after the command has said what happened.
func Reported(code int) error {
	return &Error{Code: code, Err: fmt.Errorf("exit %d", code), Reported: true} //nolint:err113
}

// IsReported says the command has told the person about err already.
func IsReported(err error) bool {
	var exitErr *Error
	return errors.As(err, &exitErr) && exitErr.Reported
}

// Of is the code an error exits with: the outermost it carries, or Failure.
func Of(err error) int {
	if err == nil {
		return OK
	}
	var coded interface{ ExitCode() int }
	if errors.As(err, &coded) {
		return coded.ExitCode()
	}
	return Failure
}
