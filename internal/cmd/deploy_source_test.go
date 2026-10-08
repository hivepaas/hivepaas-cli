package cmd

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hivepaas/hivepaas-cli/internal/exitcode"
)

// deployedBy answers what waiting for deployment id reads: it is done.
func (f *fakeAPI) deployedBy(id string) {
	f.json("GET "+appPath+"/deployments/"+id+"/status", http.StatusOK, `{"data":{"status":"done"}}`)
	f.json("GET "+appPath+"/deployments/"+id, http.StatusOK, `{"data":{"id":"`+id+`","status":"done",
		"startedAt":"2026-10-08T14:02:10Z","endedAt":"2026-10-08T14:02:34Z"}}`)
	f.logs("GET "+appPath+"/deployments/"+id+"/logs", "Building")
}

func repoOf(t *testing.T, written map[string]any) map[string]any {
	t.Helper()
	repo, ok := written["repoSource"].(map[string]any)
	require.True(t, ok, "a repository source is written: %v", written)
	return repo
}

// One write of every change, which deploys them: the CLI waits for that
// deployment, and starts no other.
func TestDeployACommitOfAnotherRef(t *testing.T) {
	quickly(t)
	f := newFakeAPI(t)
	f.json("GET "+appPath+"/deployment-settings", http.StatusOK, repoSettings)
	f.json("PUT "+appPath+"/deployment-settings", http.StatusOK, `{"data":{"deploymentId":"D1","taskId":"T1"}}`)
	f.deployedBy("D1")

	r := f.run(args("deploy --ref release --commit " + sha + " --no-auto-deploy " + shopAPI)...)

	require.Equal(t, exitcode.OK, r.code, r.stderr)
	assert.Zero(t, f.called("POST "+appPath+"/deploy"))
	assert.Equal(t, 1, f.called("PUT "+appPath+"/deployment-settings"))
	written := f.body("PUT "+appPath+"/deployment-settings", 0)
	repo := repoOf(t, written)
	assert.Equal(t, "release", repo["repoRef"])
	assert.Equal(t, sha, repo["commitHash"])
	assert.Equal(t, false, repo["autoDeploy"])
	assert.Equal(t, map[string]any{"id": "G1"}, repo["credentials"], "the rest as it was")
	assert.Equal(t, "serve", written["command"])
	assert.InDelta(t, 7, written["updateVer"], 0)
	assert.Contains(t, r.stderr, "Ref: main -> release\nCommit: none -> "+sha+"\nAuto-deploy: on -> off\n")
	assert.NotContains(t, r.stderr, "Warning")
	assert.Contains(t, r.stderr, "Deployed in 24s.")
}

func TestDeployACommitThatAPushDeploysToo(t *testing.T) {
	f := newFakeAPI(t)
	f.json("GET "+appPath+"/deployment-settings", http.StatusOK, repoSettings)
	f.json("PUT "+appPath+"/deployment-settings", http.StatusOK, `{"data":{"deploymentId":"D1"}}`)

	r := f.run(args("deploy --commit " + sha + " --no-wait " + shopAPI)...)

	require.Equal(t, exitcode.OK, r.code, r.stderr)
	assert.Contains(t, r.stderr, "Warning: api also deploys each push to main: this commit may be deployed twice")
}

// Credentials are named as the dashboard lists them for the environment.
func TestDeployNamesItsCredentials(t *testing.T) {
	f := newFakeAPI(t)
	f.json("GET "+appPath+"/deployment-settings", http.StatusOK, repoSettings)
	f.json("PUT "+appPath+"/deployment-settings", http.StatusOK, `{"data":{"deploymentId":"D1"}}`)
	f.json("GET /api/projects/P1/production/registry-auth", http.StatusOK, `{"data":[
		{"id":"R1","name":"ghcr","kind":"generic","address":"ghcr.io"},
		{"id":"R2","name":"Docker Hub","kind":"docker-hub","address":"docker.io"}]}`)
	f.json("GET /api/projects/P1/production/git-credentials", http.StatusOK,
		`{"data":[{"id":"G2","name":"deploy-key","kind":"ssh-key"}]}`)

	r := f.run("deploy", "--push-to", "docker hub", "--git-credential", "deploy-key", "--no-wait",
		"-p", "shop", "-e", "production", "-a", "api")

	require.Equal(t, exitcode.OK, r.code, r.stderr)
	repo := repoOf(t, f.body("PUT "+appPath+"/deployment-settings", 0))
	assert.Equal(t, map[string]any{"id": "R2"}, repo["pushToRegistry"])
	assert.Equal(t, map[string]any{"id": "G2"}, repo["credentials"])
	assert.Contains(t, r.stderr, "Git credential: acme-app -> deploy-key\nPush to: ghcr -> Docker Hub\n")

	r = f.run(args("deploy --push-to None --no-wait " + shopAPI)...)
	require.Equal(t, exitcode.OK, r.code, r.stderr)
	written := f.body("PUT "+appPath+"/deployment-settings", 1)
	assert.Equal(t, map[string]any{"id": ""}, repoOf(t, written)["pushToRegistry"], "none, in any case, is none")

	r = f.run(args("deploy --push-to nope --no-wait " + shopAPI)...)
	assert.Equal(t, exitcode.NotFound, r.code)
	assert.Contains(t, r.stderr, `no registry credential "nope" in shop / production. There: ghcr, Docker Hub`)
	assert.Equal(t, 2, f.called("PUT "+appPath+"/deployment-settings"), "nothing written for a name not found")
}

// Settings as asked already are not written: the app is deployed as it is, with
// the flags only that call takes.
func TestDeployWithNothingToChange(t *testing.T) {
	f := newFakeAPI(t)
	f.json("GET "+appPath+"/deployment-settings", http.StatusOK, repoSettings)
	f.json("POST "+appPath+"/deploy", http.StatusOK, `{"data":{"deploymentId":"D2","taskId":"T2"}}`)

	r := f.run(args("deploy --ref main --no-cache --change-id pr-12 --no-wait " + shopAPI)...)

	require.Equal(t, exitcode.OK, r.code, r.stderr)
	assert.Zero(t, f.called("PUT "+appPath+"/deployment-settings"))
	assert.Contains(t, r.stderr, "The deployment settings are as asked already.")
	assert.Equal(t, map[string]any{"noCache": true, "changeId": "pr-12"}, f.body("POST "+appPath+"/deploy", 0))
}

// --no-cache and --change-id are the deploy call's: a change deploys without
// them, so the two do not go together.
func TestDeployRefusesNoCacheWithAChange(t *testing.T) {
	f := newFakeAPI(t)
	f.json("GET "+appPath+"/deployment-settings", http.StatusOK, repoSettings)

	r := f.run(args("deploy --ref release --no-cache " + shopAPI)...)

	assert.Equal(t, exitcode.Usage, r.code)
	assert.Contains(t, r.stderr, "--no-cache cannot go with a change of the settings")
	assert.Zero(t, f.called("PUT "+appPath+"/deployment-settings"))
	assert.Zero(t, f.called("POST "+appPath+"/deploy"))
}

func TestDeployAnImageOnAnAppBuiltFromSource(t *testing.T) {
	f := newFakeAPI(t)
	f.json("GET "+appPath+"/deployment-settings", http.StatusOK, repoSettings)
	f.json("PUT "+appPath+"/deployment-settings", http.StatusOK, `{"data":{"deploymentId":"D1"}}`)

	r := f.run(args("deploy --image nginx:1.30 " + shopAPI)...)
	assert.Equal(t, exitcode.Invalid, r.code)
	assert.Contains(t, r.stderr, "--use image switches it")
	assert.Zero(t, f.called("PUT "+appPath+"/deployment-settings"))

	r = f.run(args("deploy --use image --image nginx:1.30 --no-wait " + shopAPI)...)
	require.Equal(t, exitcode.OK, r.code, r.stderr)
	written := f.body("PUT "+appPath+"/deployment-settings", 0)
	assert.Equal(t, "image", written["activeMethod"])
	assert.Equal(t, "https://github.com/acme/api.git", repoOf(t, written)["repoURL"], "the repository is kept")
}

func TestDeployADockerfileGivenInline(t *testing.T) {
	f := newFakeAPI(t)
	f.json("GET "+appPath+"/deployment-settings", http.StatusOK, repoSettings)
	f.json("PUT "+appPath+"/deployment-settings", http.StatusOK, `{"data":{"deploymentId":"D1"}}`)
	file := filepath.Join(t.TempDir(), "Dockerfile.prod")
	require.NoError(t, os.WriteFile(file, []byte("FROM scratch\n"), 0o600))

	r := f.run("deploy", "--dockerfile-inline", file, "--no-wait", "-p", "shop", "-e", "production", "-a", "api")

	require.Equal(t, exitcode.OK, r.code, r.stderr)
	assert.Equal(t, map[string]any{"source": "manual", "path": "Dockerfile", "content": "FROM scratch\n"},
		repoOf(t, f.body("PUT "+appPath+"/deployment-settings", 0))["dockerfile"])

	r = f.run(args("deploy --dockerfile-inline /nope/Dockerfile " + shopAPI)...)
	assert.Equal(t, exitcode.Usage, r.code)
}

// Two credentials a name matches are told apart by id: they have no key.
func TestDeployNamesOneCredentialOfTwo(t *testing.T) {
	f := newFakeAPI(t)
	f.json("GET "+appPath+"/deployment-settings", http.StatusOK, repoSettings)
	f.json("GET /api/projects/P1/production/registry-auth", http.StatusOK, `{"data":[
		{"id":"R1","name":"ghcr"},{"id":"R3","name":"GHCR"}]}`)

	r := f.run(args("deploy --push-to Ghcr " + shopAPI)...)

	assert.Equal(t, exitcode.Usage, r.code)
	assert.Contains(t, r.stderr, `registry credential "Ghcr" in shop / production names more than one - give its id: `+
		"ghcr R1, GHCR R3")
}
