//go:build !windows

package cmd

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/hivepaas/hivepaas-cli/internal/stream"
)

// watchSize gives the terminal's size whenever the window is resized: SIGWINCH.
func watchSize(ctx context.Context, size func() (stream.Size, bool)) <-chan stream.Size {
	sizes := make(chan stream.Size, 1)
	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	go func() {
		defer signal.Stop(winch)
		defer close(sizes)
		for {
			select {
			case <-ctx.Done():
				return
			case <-winch:
				if s, ok := size(); ok {
					select {
					case sizes <- s:
					default: // one waiting already: it is read before this one would be
					}
				}
			}
		}
	}()
	return sizes
}
