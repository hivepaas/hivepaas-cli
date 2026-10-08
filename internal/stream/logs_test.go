package stream

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hivepaas/hivepaas-cli/internal/api"
	"github.com/hivepaas/hivepaas-cli/internal/client"
	"github.com/hivepaas/hivepaas-cli/internal/exitcode"
)

func frame(ts, data string) api.TasklogLogFrame {
	return api.TasklogLogFrame{Type: "out", Data: data, Ts: ts}
}

// logServer answers each opening of the stream with the next of batches, then
// closes it, as the API does at the end of a stream. It records the queries.
type logServer struct {
	mu      sync.Mutex
	batches [][]api.TasklogLogFrame
	queries []string
	headers []http.Header
}

func (s *logServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.queries = append(s.queries, r.URL.RawQuery)
	s.headers = append(s.headers, r.Header.Clone())
	var batch []api.TasklogLogFrame
	if len(s.batches) > 0 {
		batch, s.batches = s.batches[0], s.batches[1:]
	}
	s.mu.Unlock()
	conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	data, _ := json.Marshal(batch)
	_ = conn.WriteMessage(websocket.BinaryMessage, data)
}

func TestRunReadsTheStreamToItsEnd(t *testing.T) {
	srv := &logServer{batches: [][]api.TasklogLogFrame{{
		frame("2026-10-08T14:02:11Z", "Pulling"), frame("2026-10-08T14:02:19Z", "Updating"),
	}}}
	ts := httptest.NewServer(srv)
	defer ts.Close()

	var got []string
	l := &Logs{URL: ts.URL + "/api/logs", Query: map[string][]string{"follow": {"true"}},
		Header: http.Header{"Hivepaas-Cli": {"dev; api-level=2"}}}
	err := l.Run(context.Background(), func(f api.TasklogLogFrame) { got = append(got, f.Data) })

	require.NoError(t, err)
	assert.Equal(t, []string{"Pulling", "Updating"}, got)
	assert.Equal(t, []string{"follow=true"}, srv.queries)
	assert.Equal(t, "dev; api-level=2", srv.headers[0].Get("HivePaaS-CLI"), "the headers go on the upgrade")
}

// A stream opened again starts at the last line's second, and what it gives
// again is left out.
func TestRunOpensTheStreamAgainFromTheLastLine(t *testing.T) {
	srv := &logServer{batches: [][]api.TasklogLogFrame{
		{frame("2026-10-08T14:02:11.100Z", "one"), frame("2026-10-08T14:02:12.500Z", "two")},
		{
			frame("2026-10-08T14:02:11.100Z", "one"), frame("2026-10-08T14:02:12.500Z", "two"),
			frame("2026-10-08T14:02:12.500Z", "two and a half"), frame("2026-10-08T14:02:13Z", "three"),
		},
	}}
	ts := httptest.NewServer(srv)
	defer ts.Close()

	opened := 0
	var got []string
	l := &Logs{
		URL: ts.URL, Query: map[string][]string{"tail": {"100"}, "follow": {"true"}},
		Again: func(context.Context) bool { opened++; return opened < 2 },
		Retry: time.Millisecond,
	}
	err := l.Run(context.Background(), func(f api.TasklogLogFrame) { got = append(got, f.Data) })

	require.NoError(t, err)
	assert.Equal(t, []string{"one", "two", "two and a half", "three"}, got)
	require.Len(t, srv.queries, 2)
	assert.Equal(t, "follow=true&tail=100", srv.queries[0])
	assert.Equal(t, "follow=true&since=2026-10-08T14%3A02%3A12Z", srv.queries[1],
		"since the last line, without the tail the first opening asked for")
}

func TestRunGivesTheAPIsRefusal(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"status":404,"code":"ERR_APP_NOT_FOUND","detail":"App 'x' is not found"}`))
	}))
	defer ts.Close()

	opened := 0
	l := &Logs{URL: ts.URL, Again: func(context.Context) bool { opened++; return true }, Retry: time.Millisecond}
	err := l.Run(context.Background(), func(api.TasklogLogFrame) {})

	var apiErr *client.APIError
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, "ERR_APP_NOT_FOUND", apiErr.Info.Code)
	assert.Equal(t, exitcode.NotFound, exitcode.Of(err))
	assert.Zero(t, opened, "a refusal is not tried again")
}

func TestRunGivesUpOnAServerItCannotReach(t *testing.T) {
	ts := httptest.NewServer(http.NotFoundHandler())
	url := ts.URL
	ts.Close()

	l := &Logs{URL: url, Again: func(context.Context) bool { return true }, Retry: time.Millisecond}
	err := l.Run(context.Background(), func(api.TasklogLogFrame) {})

	require.Error(t, err)
	assert.Equal(t, exitcode.Server, exitcode.Of(err))
}

func TestRunEndsWithItsContext(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		_, _, _ = conn.ReadMessage() // held open until the client goes
	}))
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	l := &Logs{URL: ts.URL, Again: func(context.Context) bool { return true }}
	err := l.Run(ctx, func(api.TasklogLogFrame) {})

	assert.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestDedupe(t *testing.T) {
	var d dedupe
	assert.True(t, d.fresh(frame("2026-10-08T14:00:01Z", "a")))
	assert.True(t, d.fresh(frame("2026-10-08T14:00:00Z", "older, first time"))) // a first stream drops nothing
	assert.True(t, d.fresh(frame("2026-10-08T14:00:01Z", "b")))
	d.reopen()

	assert.Equal(t, "2026-10-08T14:00:01Z", d.since().Format(time.RFC3339))
	assert.False(t, d.fresh(frame("2026-10-08T14:00:00Z", "older, first time")))
	assert.False(t, d.fresh(frame("2026-10-08T14:00:01Z", "a")))
	assert.True(t, d.fresh(frame("2026-10-08T14:00:01Z", "c")), "a new line of the same second")
	assert.True(t, d.fresh(frame("2026-10-08T14:00:02Z", "d")))
	assert.True(t, d.fresh(frame("", "no time")), "a line without a time is never dropped")
}
