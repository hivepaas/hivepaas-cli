package cmd

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hivepaas/hivepaas-cli/internal/exitcode"
)

// status lists what needs attention, where, and what is known of it.
func TestStatusListsWhatNeedsAttention(t *testing.T) {
	f := newFakeAPI(t)
	f.handle("GET /api/home/attention", func(w http.ResponseWriter, _ *http.Request, n int) {
		if n > 1 {
			_, _ = w.Write([]byte(`{"data":{"items":[]}}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":{"items":[
			{"kind":"app-not-running","severity":"critical","scope":"app","subject":"api","canAct":true,
				"project":{"id":"P1","name":"shop"},"env":"production","app":{"id":"A1","name":"api"},
				"running":0,"desired":2,"lastError":"task: non-zero exit (1)","since":"2026-10-09T10:00:00Z"},
			{"kind":"node-down","severity":"warning","scope":"cluster","subject":"worker-2","canAct":false,
				"nodeState":"down"}]}}`))
	})

	r := f.run("status")
	require.Equal(t, exitcode.OK, r.code, r.stderr)
	for _, want := range []string{"critical", "app-not-running", "shop / production / api", "0 of 2 running",
		"task: non-zero exit (1)", "warning", "node-down", "worker-2", "down"} {
		assert.Contains(t, r.stdout, want)
	}

	r = f.run("status", "--fail")
	assert.Equal(t, exitcode.Failure, r.code, "something needs attention")

	r = f.run("status", "--fail")
	assert.Equal(t, exitcode.OK, r.code, r.stderr)
	assert.Contains(t, r.stderr, "Nothing needs attention.")
}
