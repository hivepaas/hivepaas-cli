package cmd

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestTabCompletesJobsAndTasks(t *testing.T) {
	f := newFakeAPI(t)
	f.json("GET "+appPath+"/sched-jobs", http.StatusOK, jobsBody)
	f.json("GET "+appPath+"/tasks", http.StatusOK, tasksBody)

	r := f.run("__complete", "job", "run", "-p", "shop", "-e", "production", "-a", "api", "")
	assert.True(t, strings.HasPrefix(r.stdout, "migrate\tcontainer-command, active\nnightly-report\t"), r.stdout)

	r = f.run("__complete", "task", "cancel", "-p", "shop", "-e", "production", "-a", "api", "")
	assert.True(t, strings.HasPrefix(r.stdout, "K2\tsched-job-exec, failed, migrate\nK1\t"), r.stdout)
}
