package cmd

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hivepaas/hivepaas-cli/internal/exitcode"
)

const serviceTasksBody = `{"data":[
	{"id":"T2","slot":2,"desiredState":"running","node":{"id":"N2","refId":"swarm-n2","hostname":"worker-2"},
		"status":{"state":"running","timestamp":"2026-10-08T14:00:00Z","containerStatus":{"containerId":"c2c2c2c2c2c2c2c2"}}},
	{"id":"T1a","slot":1,"desiredState":"shutdown","node":{"id":"N1","refId":"swarm-n1","hostname":"worker-1"},
		"status":{"state":"failed","timestamp":"2026-10-08T13:00:00Z","err":"task: non-zero exit (1)",
			"containerStatus":{"containerId":"c1old"}}},
	{"id":"T1b","slot":1,"desiredState":"running","node":{"id":"N1","refId":"swarm-n1","hostname":"worker-1"},
		"status":{"state":"running","timestamp":"2026-10-08T13:01:00Z","containerStatus":{"containerId":"c1c1c1c1c1c1c1c1"}}}
	]}`

// Each replica's tasks together, the newest first, and why the one that failed
// did.
func TestPsListsTheReplicas(t *testing.T) {
	f := newFakeAPI(t)
	f.handle("GET "+appPath+"/service-tasks", func(w http.ResponseWriter, r *http.Request, n int) {
		if n == 1 {
			assert.Equal(t, "running", r.URL.Query().Get("state"))
		}
		_, _ = w.Write([]byte(serviceTasksBody))
	})

	r := f.run(args("ps " + shopAPI)...)

	require.Equal(t, exitcode.OK, r.code, r.stderr)
	lines := strings.Split(strings.TrimSpace(r.stdout), "\n")
	require.Len(t, lines, 4)
	assert.Regexp(t, `^REPLICA\s+STATE\s+DESIRED\s+NODE\s+CONTAINER\s+SINCE\s+ERROR$`, lines[0])
	assert.Regexp(t, `^1\s+running\s+running\s+worker-1\s+c1c1c1c1c1c1\s`, lines[1])
	assert.Regexp(t, `^1\s+failed\s+shutdown\s+worker-1\s+c1old\s+.*task: non-zero exit \(1\)$`, lines[2])
	assert.Regexp(t, `^2\s+running\s+running\s+worker-2\s+c2c2c2c2c2c2\s`, lines[3])

	r = f.run(args("ps --state running " + shopAPI)...)
	require.Equal(t, exitcode.OK, r.code, r.stderr)
}
