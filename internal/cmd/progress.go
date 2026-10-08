package cmd

import (
	"fmt"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/term"
)

// progressEvery is how often a copy's progress is said.
const progressEvery = 500 * time.Millisecond

// progress counts what r gives, and at a terminal says it on stderr - so many
// MB, of total when it is known, at what rate - until done.
func (a *App) progress(r io.Reader, total int64) (io.Reader, func()) {
	errw, ok := a.stderr.(*os.File)
	if !ok || !term.IsTerminal(int(errw.Fd())) { //nolint:gosec // a file descriptor fits an int
		return r, func() {}
	}
	counted := &countingReader{r: r}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Go(func() {
		started := time.Now()
		ticker := time.NewTicker(progressEvery)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				fmt.Fprint(a.stderr, "\r\x1b[K")
				return
			case <-ticker.C:
				n := counted.n.Load()
				rate := float64(n) / time.Since(started).Seconds()
				line := "  " + megabytes(n)
				if total > 0 {
					line += " of " + megabytes(total)
				}
				fmt.Fprintf(a.stderr, "\r\x1b[K%s, %s/s", line, megabytes(int64(rate)))
			}
		}
	})
	var once sync.Once
	return counted, func() { once.Do(func() { close(stop); wg.Wait() }) }
}

type countingReader struct {
	r io.Reader
	n atomic.Int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n.Add(int64(n))
	return n, err //nolint:wrapcheck // the reader's own
}

func megabytes(n int64) string {
	const mb = 1 << 20
	return fmt.Sprintf("%.1f MB", float64(n)/mb)
}
