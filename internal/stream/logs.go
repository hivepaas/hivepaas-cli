// Package stream is the API's websocket streams: an app's logs and a
// deployment's.
package stream

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/gorilla/websocket"

	"github.com/hivepaas/hivepaas-cli/internal/api"
	"github.com/hivepaas/hivepaas-cli/internal/client"
	"github.com/hivepaas/hivepaas-cli/internal/exitcode"
)

const (
	// defaultRetry is how long Run waits before opening a stream again.
	defaultRetry = 2 * time.Second
	// maxFailures is how many times in a row the stream may fail to open before
	// Run gives up.
	maxFailures = 5
	// maxErrorBody is how much of a refused upgrade's body is read.
	maxErrorBody = 64 << 10
)

// errNotLogs is a message that is not log lines: reading the stream again
// would read it again.
var errNotLogs = errors.New("the log stream sent something other than log lines")

// Logs is a log stream: each message is a JSON array of tasklog.LogFrame.
type Logs struct {
	// URL is the stream's address: https://paas.example.com/api/.../logs.
	URL    string
	Query  url.Values
	Header http.Header
	// Again says whether to open the stream once more after it ends: a dropped
	// connection, a session the server closed. The new one starts from the time
	// of the last line the old one gave, and the lines seen already are left
	// out. Nil never opens it again.
	Again func(ctx context.Context) bool
	// Retry is how long to wait before opening it again: two seconds when zero.
	Retry time.Duration
	// Dialer opens the connection; websocket.DefaultDialer when nil.
	Dialer *websocket.Dialer
}

// Run reads the stream and gives each line to emit, until the stream ends and
// Again does not open it again, or ctx is done.
func (l *Logs) Run(ctx context.Context, emit func(api.TasklogLogFrame)) error {
	var seen dedupe
	failures := 0
	for {
		opened, err := l.read(ctx, seen.since(), func(frame api.TasklogLogFrame) {
			if seen.fresh(frame) {
				emit(frame)
			}
		})
		switch {
		case ctx.Err() != nil:
			return ctx.Err() //nolint:wrapcheck // the caller's own
		case err != nil && !retryable(err):
			return err
		case opened:
			failures = 0
		default:
			failures++
		}
		if l.Again == nil || !l.Again(ctx) {
			if failures > 0 {
				return err
			}
			return nil
		}
		if failures >= maxFailures {
			return err
		}
		seen.reopen()
		select {
		case <-ctx.Done():
			return ctx.Err() //nolint:wrapcheck
		case <-time.After(l.retry()):
		}
	}
}

func (l *Logs) retry() time.Duration {
	if l.Retry > 0 {
		return l.Retry
	}
	return defaultRetry
}

// read opens the stream once and reads it to its end. It answers whether it
// opened, and why it could not.
func (l *Logs) read(ctx context.Context, since time.Time, emit func(api.TasklogLogFrame)) (bool, error) {
	conn, err := l.dial(ctx, since)
	if err != nil {
		return false, err
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			// The server ends a stream by closing the connection: that is its end,
			// not a failure.
			return true, nil
		}
		var frames []api.TasklogLogFrame
		if err = json.Unmarshal(data, &frames); err != nil {
			return true, fmt.Errorf("%w: %w", errNotLogs, err)
		}
		for _, frame := range frames {
			emit(frame)
		}
	}
}

func (l *Logs) dial(ctx context.Context, since time.Time) (*websocket.Conn, error) {
	target, err := url.Parse(l.URL)
	if err != nil {
		return nil, fmt.Errorf("the log stream's address: %w", err)
	}
	switch target.Scheme {
	case "https":
		target.Scheme = "wss"
	case "http":
		target.Scheme = "ws"
	}
	query := url.Values{}
	for key, values := range l.Query {
		query[key] = values
	}
	if !since.IsZero() {
		// The lines since the last one seen: they replace what the first opening
		// asked for, the last so many or those of a period.
		query.Set("since", since.UTC().Truncate(time.Second).Format(time.RFC3339))
		query.Del("tail")
		query.Del("duration")
	}
	target.RawQuery = query.Encode()

	dialer := l.Dialer
	if dialer == nil {
		dialer = websocket.DefaultDialer
	}
	conn, resp, err := dialer.DialContext(ctx, target.String(), l.Header)
	if err == nil {
		return conn, nil
	}
	if resp != nil {
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusSwitchingProtocols {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
			if apiErr := client.CheckStatus(resp.StatusCode, body); apiErr != nil {
				return nil, apiErr
			}
		}
	}
	return nil, exitcode.Wrap(exitcode.Server, fmt.Errorf("opening the log stream: %w", err))
}

// retryable says whether opening the stream again may go better: not when the
// server refused the request itself.
func retryable(err error) bool {
	var apiErr *client.APIError
	if errors.As(err, &apiErr) {
		return apiErr.Status >= http.StatusInternalServerError
	}
	return !errors.Is(err, errNotLogs)
}

// dedupe leaves out the lines a reopened stream gives again. A stream opened
// anew starts at the second of the last line seen, or at the start of a
// deployment's log: what is older than that line is dropped, and so are the
// lines of its very time seen already.
type dedupe struct {
	latest   time.Time
	atLatest map[string]bool
	// cut and atCut are latest and atLatest when the stream was last reopened.
	cut   time.Time
	atCut map[string]bool
}

func (d *dedupe) since() time.Time { return d.cut }

func (d *dedupe) reopen() {
	d.cut, d.atCut = d.latest, d.atLatest
}

func (d *dedupe) fresh(frame api.TasklogLogFrame) bool {
	ts, err := time.Parse(time.RFC3339Nano, frame.Ts)
	if err != nil || ts.IsZero() {
		return true // a line without a time cannot be placed, so it is not dropped
	}
	key := string(frame.Type) + "\x00" + frame.Data
	if !d.cut.IsZero() && (ts.Before(d.cut) || ts.Equal(d.cut) && d.atCut[key]) {
		return false
	}
	switch {
	case ts.After(d.latest):
		d.latest = ts
		d.atLatest = map[string]bool{key: true}
	case ts.Equal(d.latest):
		d.atLatest[key] = true
	}
	return true
}
