package cmd

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hivepaas/hivepaas-cli/internal/exitcode"
)

const previewsBody = `{"data":[{"id":"V1","key":"api-pr-12","name":"api-pr-12","status":"active",
	"updatedAt":"2026-10-09T10:00:00Z"}]}`

// fakeDoneTask answers task id of api as done after 3s, with a line logged.
func fakeDoneTask(f *fakeAPI, id, logged string) {
	f.json("GET "+appPath+"/tasks/"+id+"/status", http.StatusOK, `{"data":{"status":"done"}}`)
	f.json("GET "+appPath+"/tasks/"+id, http.StatusOK, `{"data":{"id":"`+id+`","status":"done",
		"startedAt":"2026-10-09T10:00:00Z","endedAt":"2026-10-09T10:00:03Z"}}`)
	f.logs("GET "+appPath+"/tasks/"+id+"/logs", logged)
}

func TestPreviewLs(t *testing.T) {
	f := newFakeAPI(t)
	f.json("GET "+appPath+"/previews", http.StatusOK, previewsBody)

	r := f.run(args("preview ls " + shopAPI)...)

	require.Equal(t, exitcode.OK, r.code, r.stderr)
	assert.Contains(t, r.stdout, "api-pr-12")
}

// create asks what the server can do first, says the secrets withheld, then
// makes the preview and waits for the task that makes it.
func TestPreviewCreate(t *testing.T) {
	quickly(t)
	f := newFakeAPI(t)
	f.json("POST "+appPath+"/previews/prepare", http.StatusOK, `{"data":{"enabled":true,"canCloneDbApps":true,
		"canSkipCloningDbApps":true,"repoURL":"https://github.com/acme/api.git",
		"withheldSecrets":[{"name":"STRIPE_KEY","envVars":["STRIPE_KEY"]}]}}`)
	f.json("POST "+appPath+"/previews", http.StatusCreated, `{"data":{"id":"T5"}}`)
	fakeDoneTask(f, "T5", "Created preview api-feat-x")

	r := f.run(args("preview create --ref feat/x --subdomain feat-x --no-db " + shopAPI)...)

	require.Equal(t, exitcode.OK, r.code, r.stderr)
	body := f.body("POST "+appPath+"/previews", 0)
	assert.Equal(t, "feat/x", body["repoRef"])
	assert.Equal(t, "feat-x", body["customSubdomain"])
	assert.Equal(t, false, body["cloneDbApps"])
	assert.Equal(t, false, body["noStart"])
	assert.Contains(t, r.stderr, "STRIPE_KEY")
	assert.Contains(t, r.stderr, "Created preview api-feat-x")
	assert.Contains(t, r.stderr, "Done in 3s.")
}

func TestPreviewCreateRefusedWhenPreviewsAreOff(t *testing.T) {
	f := newFakeAPI(t)
	f.json("POST "+appPath+"/previews/prepare", http.StatusOK, `{"data":{"enabled":false,"canCloneDbApps":false,
		"canSkipCloningDbApps":false,"canListBranches":false,"canListPullRequests":false,"repoURL":""}}`)

	r := f.run(args("preview create " + shopAPI)...)

	assert.Equal(t, exitcode.Invalid, r.code)
	assert.Contains(t, r.stderr, "Feature Settings")
	assert.Zero(t, f.called("POST "+appPath+"/previews"))
}

// rm deletes one of the app's previews, and nothing that is not one.
func TestPreviewRm(t *testing.T) {
	f := newFakeAPI(t)
	f.json("GET "+appPath+"/previews", http.StatusOK, previewsBody)
	f.json("DELETE /api/projects/P1/production/apps/V1", http.StatusOK, `{}`)

	r := f.run(args("preview rm api " + shopAPI + " --yes")...)
	assert.Equal(t, exitcode.NotFound, r.code, "api is the app, not a preview of it")
	assert.Zero(t, f.called("DELETE /api/projects/P1/production/apps/V1"))
	assert.Zero(t, f.called("DELETE "+appPath))

	r = f.run(args("preview rm api-pr-12 " + shopAPI)...)
	assert.Equal(t, exitcode.Usage, r.code, "no terminal to ask at")

	r = f.run(args("preview rm api-pr-12 --yes " + shopAPI)...)
	require.Equal(t, exitcode.OK, r.code, r.stderr)
	assert.Equal(t, 1, f.called("DELETE /api/projects/P1/production/apps/V1"))
}
