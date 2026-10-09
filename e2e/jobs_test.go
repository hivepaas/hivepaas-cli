//go:build e2e

package e2e

import (
	pathpkg "path"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// A job run now is waited for with its log; one that fails exits 8; its runs
// are tasks, each with its log.
func TestAJob(t *testing.T) {
	t.Parallel()
	p := project(t, "jobs")
	worker := p.app("worker")
	p.must("app", "create", "worker", "--image", busybox, "--command", "sh -c 'echo ready; exec sleep 3600'")
	path := worker.appPath()
	said := "said-" + runID
	for name, command := range map[string]string{"say": "echo " + said, "fail": "sh -c 'echo failing; exit 3'"} {
		call(t, "POST", path+"/sched-jobs", map[string]any{"name": name, "jobType": "container-command",
			"app": map[string]string{"id": pathpkg.Base(path)}, "command": map[string]string{"command": command}}, nil)
	}

	// The command runs in the app's running container: the deployed one, not
	// the placeholder it replaces, which runs while the image is pulled.
	eventually(t, deployWithin, func() error { return contains(worker.run("logs", "--tail", "50").stdout, "ready") })
	ls := worker.must("job", "ls").stdout
	assert.Contains(t, ls, "say")
	assert.Contains(t, ls, "fail")

	r := worker.must("job", "run", "say")
	assert.Contains(t, r.stderr, said)
	r = worker.run("job", "run", "fail")
	assert.Equal(t, 8, r.code, r.String())
	assert.Contains(t, r.stderr, "failing")

	// The app's tasks: the runs - one done, one failed - among what else ran for it.
	tasks := jsonOf[[]struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}](t, worker.must("task", "ls", "-o", "json"))
	statuses := map[string]int{}
	logged := false
	for _, task := range tasks {
		statuses[task.Status]++
		if task.Status == "done" && !logged {
			logged = strings.Contains(worker.must("logs", "--task", task.ID).stdout, said)
		}
	}
	assert.Equal(t, 1, statuses["failed"], "%v", tasks)
	assert.True(t, logged, "no done task logged %s: %v", said, tasks)

	worker.must("job", "disable", "say")
	worker.must("job", "enable", "say")
}
