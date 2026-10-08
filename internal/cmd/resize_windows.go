//go:build windows

package cmd

import (
	"context"
	"time"

	"github.com/hivepaas/hivepaas-cli/internal/stream"
)

// sizePoll is how often the console's size is looked at: Windows has no signal
// for a resize.
const sizePoll = 500 * time.Millisecond

// watchSize gives the console's size whenever it changes.
func watchSize(ctx context.Context, size func() (stream.Size, bool)) <-chan stream.Size {
	sizes := make(chan stream.Size, 1)
	go func() {
		defer close(sizes)
		last, _ := size()
		ticker := time.NewTicker(sizePoll)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if s, ok := size(); ok && s != last {
					last = s
					select {
					case sizes <- s:
					default:
					}
				}
			}
		}
	}()
	return sizes
}
