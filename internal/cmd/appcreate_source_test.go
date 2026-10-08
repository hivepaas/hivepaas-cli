package cmd

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hivepaas/hivepaas-cli/internal/exitcode"
)

// An app created from its repository: the names it gives are found before the
// app is created, and the repository written last, which deploys it.
func TestAppCreateFromARepository(t *testing.T) {
	f := newFakeAPI(t)
	web := "/api/projects/P1/production/apps/A9"
	f.json("GET /api/projects/P1/production/git-credentials", http.StatusOK,
		`{"data":[{"id":"G2","name":"deploy-key","kind":"ssh-key"}]}`)
	f.json("POST /api/projects/P1/production/apps", http.StatusCreated, `{"data":{"id":"A9"}}`)
	f.json("GET "+web, http.StatusOK, `{"data":{"id":"A9","key":"web","name":"web"}}`)
	f.json("GET "+web+"/deployment-settings", http.StatusOK, `{"data":{"activeMethod":"","updateVer":0}}`)
	f.json("PUT "+web+"/deployment-settings", http.StatusOK, `{"data":{"deploymentId":"D9"}}`)

	r := f.run(args("app create web -p shop -e production --repo https://github.com/acme/web.git --ref main " +
		"--git-credential nope --no-wait")...)
	assert.Equal(t, exitcode.NotFound, r.code)
	assert.Zero(t, f.called("POST /api/projects/P1/production/apps"), "nothing created for a name not found")

	r = f.run(args("app create web -p shop -e production --repo https://github.com/acme/web.git --ref main " +
		"--git-credential deploy-key --no-wait")...)
	require.Equal(t, exitcode.OK, r.code, r.stderr)
	written := f.body("PUT "+web+"/deployment-settings", 0)
	assert.Equal(t, "repo", written["activeMethod"])
	repo := repoOf(t, written)
	assert.Equal(t, "https://github.com/acme/web.git", repo["repoURL"])
	assert.Equal(t, "main", repo["repoRef"])
	assert.Equal(t, map[string]any{"id": "G2"}, repo["credentials"])
	assert.Contains(t, r.stderr, "Deploying web (shop / production), deployment D9")
}

// Flags no app could deploy with create no app.
func TestAppCreateChecksItsFlagsFirst(t *testing.T) {
	f := newFakeAPI(t)
	f.json("POST /api/projects/P1/production/apps", http.StatusCreated, `{"data":{"id":"A9"}}`)

	r := f.run(args("app create web -p shop -e production --image nginx:1.30 --ref main")...)
	assert.Equal(t, exitcode.Usage, r.code)
	r = f.run(args("app create web -p shop -e production --command serve")...)
	assert.Equal(t, exitcode.Invalid, r.code)
	r = f.run(args("app create web -p shop -e production --repo https://github.com/acme/web.git --scan-path x")...)
	assert.Equal(t, exitcode.Invalid, r.code)

	assert.Zero(t, f.called("POST /api/projects/P1/production/apps"))
}
