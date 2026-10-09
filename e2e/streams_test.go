//go:build e2e

package e2e

import (
	"fmt"
	"net/http"
	pathpkg "path"
	"regexp"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A log followed shows its lines as they are written, until Ctrl-C; a
// deployment held by its pre-deployment command is canceled, the app running
// on; a run waited for past --timeout exits 9 and goes on, until canceled.
func TestFollowingWaitingAndCanceling(t *testing.T) {
	t.Parallel()
	p := project(t, "streams")
	ticker := p.app("ticker")
	p.must("app", "create", "ticker", "--image", busybox, "--command",
		`sh -c 'i=0; while true; do i=$((i+1)); echo tick-$i; sleep 1; done'`)
	eventually(t, deployWithin, func() error { return contains(ticker.run("logs", "--tail", "5").stdout, "tick-") })

	r := ticker.interrupted(8*time.Second, "logs", "-f", "--tail", "1")
	assert.Equal(t, 130, r.code, r.String())
	assert.GreaterOrEqual(t, len(regexp.MustCompile(`tick-\d+`).FindAllString(r.stdout, -1)), 4, r.String())

	ticker.must("deploy", "--pre-deploy", "sleep 300", "--no-wait")
	var deployments []struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	eventually(t, time.Minute, func() error {
		deployments = jsonOf[[]struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		}](t, ticker.must("deploy", "ls", "-o", "json"))
		if len(deployments) == 0 || deployments[0].Status != "in-progress" {
			return fmt.Errorf("no deployment in progress: %v", deployments)
		}
		return nil
	})
	ticker.must("deploy", "cancel")
	eventually(t, time.Minute, func() error {
		return contains(ticker.must("deploy", "get", deployments[0].ID).stdout, "canceled")
	})
	require.NoError(t, running(ticker, 1), "the app runs on in the container it ran")

	path := ticker.appPath()
	call(t, http.MethodPost, path+"/sched-jobs", map[string]any{"name": "slow", "jobType": "container-command",
		"app": map[string]string{"id": pathpkg.Base(path)}, "command": map[string]string{"command": "sleep 300"}}, nil)
	r = ticker.run("job", "run", "slow", "--timeout", "5s")
	assert.Equal(t, 9, r.code, r.String())
	var inProgress string
	for _, task := range jsonOf[[]struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}](t, ticker.must("task", "ls", "-o", "json")) {
		if task.Status == "in-progress" {
			inProgress = task.ID
		}
	}
	require.NotEmpty(t, inProgress, "the run goes on after the waiting stopped")
	ticker.must("task", "cancel", inProgress)
	eventually(t, time.Minute, func() error {
		return contains(ticker.must("task", "get", inProgress).stdout, "canceled")
	})
}
