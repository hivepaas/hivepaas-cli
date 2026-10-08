package cmd

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hivepaas/hivepaas-cli/internal/exitcode"
)

const tasksBody = `{"data":[
	{"id":"K2","type":"task:sched-job-exec","status":"failed","createdAt":"2026-10-08T14:00:00Z",
		"startedAt":"2026-10-08T14:00:01Z","endedAt":"2026-10-08T14:00:09Z","lastError":"exit status 3",
		"targetJob":{"id":"J1","name":"migrate"}},
	{"id":"K1","type":"task:app-deploy","status":"done","createdAt":"2026-10-07T09:00:00Z"}]}`

func TestTaskLsGetAndCancel(t *testing.T) {
	f := newFakeAPI(t)
	f.handle("GET "+appPath+"/tasks", func(w http.ResponseWriter, r *http.Request, _ int) {
		assert.Equal(t, "task:sched-job-exec", r.URL.Query().Get("type"), "the type as the server names it")
		_, _ = w.Write([]byte(tasksBody))
	})
	f.json("GET "+appPath+"/tasks/K2", http.StatusOK, `{"data":`+
		strings.TrimSuffix(strings.SplitN(strings.TrimPrefix(tasksBody, `{"data":[`), ",\n\t{\"id\":\"K1\"", 2)[0], "")+`}`)
	f.json("POST "+appPath+"/tasks/K2/cancel", http.StatusOK, `{"meta":null}`)

	r := f.run(args("task ls --type sched-job-exec " + shopAPI)...)
	require.Equal(t, exitcode.OK, r.code, r.stderr)
	assert.Regexp(t, `K2\s+sched-job-exec\s+failed\s+migrate\s+.*8s`, r.stdout)
	assert.Regexp(t, `K1\s+app-deploy\s+done\s+-\s`, r.stdout)

	r = f.run(args("task get K2 " + shopAPI)...)
	require.Equal(t, exitcode.OK, r.code, r.stderr)
	assert.Contains(t, r.stdout, "Task K2 of api (shop / production)\nType:    sched-job-exec\nStatus:  failed\n"+
		"What:    migrate\n")
	assert.Contains(t, r.stdout, "Error:   exit status 3\n")
	assert.Contains(t, r.stderr, "hivepaas logs --task K2 -p shop -e production -a api")

	r = f.run(args("task cancel K2 " + shopAPI)...)
	require.Equal(t, exitcode.OK, r.code, r.stderr)
	assert.Equal(t, 1, f.called("POST "+appPath+"/tasks/K2/cancel"))
}

func TestTaskCancelSaysWhichCancel(t *testing.T) {
	f := newFakeAPI(t)
	f.json("POST "+appPath+"/tasks/K1/cancel", http.StatusOK, `{"data":{"canceled":true}}`)
	f.json("POST "+appPath+"/tasks/K2/cancel", http.StatusOK, `{"data":{"canceled":false}}`)

	r := f.run(args("task cancel K1 " + shopAPI)...)
	assert.Contains(t, r.stderr, "Canceled task K1")
	r = f.run(args("task cancel K2 " + shopAPI)...)
	assert.Contains(t, r.stderr, "Canceling task K2 of api (shop / production): the server is stopping it.")
}

func TestLogsOfATask(t *testing.T) {
	f := newFakeAPI(t)
	f.logs("GET "+appPath+"/tasks/K2/logs", "running migrations", "done")

	r := f.run(args("logs --task K2 --no-timestamps " + shopAPI)...)

	require.Equal(t, exitcode.OK, r.code, r.stderr)
	assert.Equal(t, "running migrations\ndone\n", r.stdout)

	r = f.run(args("logs --task K2 --deployment D1 " + shopAPI)...)
	assert.Equal(t, exitcode.Usage, r.code)
}

const jobsBody = `{"data":[
	{"id":"J1","name":"migrate","jobType":"container-command","status":"active","updateVer":3,
		"schedule":{"initialTime":"","endTime":""}},
	{"id":"J2","name":"nightly-report","jobType":"container-command","status":"active","updateVer":5,
		"schedule":{"cronExpr":"0 2 * * *","initialTime":"","endTime":""},"nextRuns":["2999-01-01T02:00:00Z"]},
	{"id":"J3","name":"cleanup","jobType":"container-command","status":"disabled","updateVer":1,
		"schedule":{"interval":"1h","initialTime":"","endTime":""}}]}`

func TestJobLs(t *testing.T) {
	f := newFakeAPI(t)
	f.json("GET "+appPath+"/sched-jobs", http.StatusOK, jobsBody)

	r := f.run(args("job ls " + shopAPI)...)

	require.Equal(t, exitcode.OK, r.code, r.stderr)
	assert.Regexp(t, `migrate\s+container-command\s+-\s+active\s+-`, r.stdout)
	assert.Regexp(t, `nightly-report\s+container-command\s+0 2 \* \* \*\s+active\s+in \d+d`, r.stdout)
	assert.Regexp(t, `cleanup\s+container-command\s+every 1h\s+disabled\s+-`, r.stdout)
}

// A job run now is waited for as a deployment is: its logs followed, its end
// said, exit 8 when it fails.
func TestJobRun(t *testing.T) {
	quickly(t)
	f := newFakeAPI(t)
	f.json("GET "+appPath+"/sched-jobs", http.StatusOK, jobsBody)
	f.json("POST "+appPath+"/sched-jobs/J1/exec", http.StatusOK, `{"data":{"task":{"id":"K3"}}}`)
	f.handle("GET "+appPath+"/tasks/K3/status", func(w http.ResponseWriter, _ *http.Request, n int) {
		_, _ = w.Write([]byte(`{"data":{"status":"` + []string{"in-progress", "done"}[min(n, 1)] + `"}}`))
	})
	f.json("GET "+appPath+"/tasks/K3", http.StatusOK, `{"data":{"id":"K3","status":"done",
		"startedAt":"2026-10-08T14:00:00Z","endedAt":"2026-10-08T14:00:03Z"}}`)
	f.logs("GET "+appPath+"/tasks/K3/logs", "applied 2 migrations")

	r := f.run(args("job run Migrate " + shopAPI)...)

	require.Equal(t, exitcode.OK, r.code, r.stderr)
	assert.Contains(t, r.stderr, "Running migrate on api (shop / production), task K3")
	assert.Contains(t, r.stderr, "applied 2 migrations")
	assert.Contains(t, r.stderr, "Done in 3s.")

	f.json("POST "+appPath+"/sched-jobs/J2/exec", http.StatusOK, `{"data":{"task":{"id":"K4"}}}`)
	f.json("GET "+appPath+"/tasks/K4/status", http.StatusOK, `{"data":{"status":"failed"}}`)
	f.json("GET "+appPath+"/tasks/K4", http.StatusOK, `{"data":{"id":"K4","status":"failed",
		"startedAt":"2026-10-08T14:00:00Z","endedAt":"2026-10-08T14:00:05Z","lastError":"exit status 3"}}`)
	f.logs("GET " + appPath + "/tasks/K4/logs")

	r = f.run(args("job run nightly-report " + shopAPI)...)
	assert.Equal(t, exitcode.Deployment, r.code)
	assert.Contains(t, r.stderr, "The task failed after 5s: exit status 3")
	assert.Contains(t, r.stderr, "hivepaas logs --task K4")

	r = f.run(args("job run nope " + shopAPI)...)
	assert.Equal(t, exitcode.NotFound, r.code)
	assert.Contains(t, r.stderr, `no scheduled job "nope" of api (shop / production). There: migrate, `+
		"nightly-report, cleanup")
}

// A run that failed and that the server will retry is waited for: its end is
// the retry's.
func TestJobRunWaitsOutARetry(t *testing.T) {
	quickly(t)
	f := newFakeAPI(t)
	f.json("GET "+appPath+"/sched-jobs", http.StatusOK, jobsBody)
	f.json("POST "+appPath+"/sched-jobs/J1/exec", http.StatusOK, `{"data":{"task":{"id":"K5"}}}`)
	f.handle("GET "+appPath+"/tasks/K5/status", func(w http.ResponseWriter, _ *http.Request, n int) {
		_, _ = w.Write([]byte(`{"data":{"status":"` + []string{"failed", "failed", "done"}[min(n, 2)] + `"}}`))
	})
	f.handle("GET "+appPath+"/tasks/K5", func(w http.ResponseWriter, _ *http.Request, n int) {
		if n < 2 {
			_, _ = w.Write([]byte(`{"data":{"id":"K5","status":"failed","retryAt":"2026-10-08T14:00:30Z",
				"lastError":"connection refused","config":{"retry":0,"maxRetry":2,"priority":"default"}}}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":{"id":"K5","status":"done","retryAt":"0001-01-01T00:00:00Z",
			"startedAt":"2026-10-08T14:00:30Z","endedAt":"2026-10-08T14:00:32Z"}}`))
	})
	f.logs("GET " + appPath + "/tasks/K5/logs")

	r := f.run(args("job run migrate " + shopAPI)...)

	require.Equal(t, exitcode.OK, r.code, r.stderr)
	assert.Contains(t, r.stderr, "The run failed: connection refused. It is tried again")
	assert.Contains(t, r.stderr, "Done in 2s.")
}

func TestJobRunRefusesADisabledJob(t *testing.T) {
	f := newFakeAPI(t)
	f.json("GET "+appPath+"/sched-jobs", http.StatusOK, jobsBody)

	r := f.run(args("job run cleanup " + shopAPI)...)

	assert.Equal(t, exitcode.Invalid, r.code)
	assert.Contains(t, r.stderr, "cleanup is disabled: hivepaas job enable cleanup")
	assert.Zero(t, f.called("POST "+appPath+"/sched-jobs/J3/exec"))
}

func TestJobEnableAndDisable(t *testing.T) {
	f := newFakeAPI(t)
	f.json("GET "+appPath+"/sched-jobs", http.StatusOK, jobsBody)
	f.json("PUT "+appPath+"/sched-jobs/J1/status", http.StatusOK, `{"meta":null}`)

	r := f.run(args("job disable migrate " + shopAPI)...)
	require.Equal(t, exitcode.OK, r.code, r.stderr)
	assert.Equal(t, map[string]any{"status": "disabled", "updateVer": float64(3), "default": nil,
		"expireAt": nil, "inheritable": nil}, f.body("PUT "+appPath+"/sched-jobs/J1/status", 0))
	assert.Contains(t, r.stderr, "Disabled migrate on api (shop / production).")

	r = f.run(args("job enable migrate " + shopAPI)...)
	require.Equal(t, exitcode.OK, r.code, r.stderr)
	assert.Contains(t, r.stderr, "migrate is enabled already.")
	assert.Equal(t, 1, f.called("PUT "+appPath+"/sched-jobs/J1/status"), "nothing written for a job as asked")
}
