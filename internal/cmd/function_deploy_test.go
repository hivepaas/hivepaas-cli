package cmd

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hivepaas/hivepaas-cli/internal/exitcode"
	"github.com/hivepaas/hivepaas-cli/internal/link"
)

// fakeFunctionDeployment answers a deployment D8 of api that is done.
func fakeFunctionDeployment(f *fakeAPI) {
	f.json("GET "+appPath+"/deployments/D8/status", http.StatusOK, `{"data":{"status":"done"}}`)
	f.json("GET "+appPath+"/deployments/D8", http.StatusOK, `{"data":{"id":"D8","status":"done",
		"startedAt":"2026-10-09T10:00:00Z","endedAt":"2026-10-09T10:00:12Z"}}`)
	f.logs("GET "+appPath+"/deployments/D8/logs", "1/1 tasks running")
}

// deploy sends the directory's code in place of the function's, whole, with
// the rest of its settings as they were, and says what changed.
func TestFunctionDeploySendsTheDirectory(t *testing.T) {
	quickly(t)
	f := newFakeAPI(t)
	f.json("GET "+appPath+"/deployment-settings", http.StatusOK, fnSettings)
	f.json("PUT "+appPath+"/deployment-settings", http.StatusOK, `{"data":{"deploymentId":"D8","taskId":"T8"}}`)
	fakeFunctionDeployment(f)
	dir := dirWith(t, map[string]string{"index.js": "new", "package.json": "{}", "new.js": "n"})

	r := f.run(append(args("function deploy --max-concurrency 8 "+shopAPI), dir)...)

	require.Equal(t, exitcode.OK, r.code, r.stderr)
	written := f.body("PUT "+appPath+"/deployment-settings", 0)
	assert.Equal(t, "function", written["activeMethod"])
	assert.InDelta(t, 3, written["updateVer"], 0)
	source := written["functionSource"].(map[string]any)
	assert.Equal(t, "node24", source["runtime"], "kept")
	assert.Equal(t, []any{"curl"}, source["systemPackages"], "kept")
	assert.Equal(t, map[string]any{"id": "R1"}, source["pushToRegistry"], "kept")
	assert.InDelta(t, 8, source["maxConcurrency"], 0)
	assert.Equal(t, []any{
		map[string]any{"path": "index.js", "content": "new"},
		map[string]any{"path": "new.js", "content": "n"},
		map[string]any{"path": "package.json", "content": "{}"},
	}, source["code"].(map[string]any)["inline"].(map[string]any)["files"])
	assert.Contains(t, r.stderr, "Code: index.js changed, new.js added, util.js removed")
	assert.Contains(t, r.stderr, "Max concurrency: 16 -> 8")
	assert.Contains(t, r.stderr, "Deployed in 12s.")
}

// The same code and settings: the function is deployed again.
func TestFunctionDeployOfTheSameCodeDeploysAgain(t *testing.T) {
	quickly(t)
	f := newFakeAPI(t)
	f.json("GET "+appPath+"/deployment-settings", http.StatusOK, fnSettings)
	f.json("POST "+appPath+"/deploy", http.StatusOK, `{"data":{"deploymentId":"D8","taskId":"T8"}}`)
	fakeFunctionDeployment(f)
	dir := dirWith(t, map[string]string{"index.js": "old", "package.json": "{}", "util.js": "u"})

	r := f.run(append(args("function deploy "+shopAPI), dir)...)

	require.Equal(t, exitcode.OK, r.code, r.stderr)
	assert.Zero(t, f.called("PUT "+appPath+"/deployment-settings"))
	assert.Equal(t, 1, f.called("POST "+appPath+"/deploy"))
}

// A directory linked to the function needs no flags, wherever it is run from.
func TestFunctionDeployReadsTheDirectorysLink(t *testing.T) {
	quickly(t)
	f := newFakeAPI(t)
	f.json("GET "+appPath+"/deployment-settings", http.StatusOK, fnSettings)
	f.json("PUT "+appPath+"/deployment-settings", http.StatusOK, `{"data":{"deploymentId":"D8"}}`)
	fakeFunctionDeployment(f)
	dir := dirWith(t, map[string]string{"index.js": "new", "package.json": "{}"})
	_, err := link.Write(dir, &link.Link{URL: f.srv.URL, Project: link.Named{ID: "P1", Name: "shop"},
		Env: "production", App: link.Named{ID: "A1", Name: "api"}})
	require.NoError(t, err)

	r := f.run("function", "deploy", dir)

	require.Equal(t, exitcode.OK, r.code, r.stderr)
	source := f.body("PUT "+appPath+"/deployment-settings", 0)["functionSource"].(map[string]any)
	files := source["code"].(map[string]any)["inline"].(map[string]any)["files"].([]any)
	assert.Len(t, files, 2, "the link file is not code")
}

// A function built from a repository takes the repository's flags, and no
// directory unless switched to inline code.
func TestFunctionDeployOfARepositorysCode(t *testing.T) {
	quickly(t)
	f := newFakeAPI(t)
	f.json("GET "+appPath+"/deployment-settings", http.StatusOK, fnRepoSettings)
	f.json("PUT "+appPath+"/deployment-settings", http.StatusOK, `{"data":{"deploymentId":"D8"}}`)
	fakeFunctionDeployment(f)
	dir := dirWith(t, map[string]string{"main.py": "def handler(req, ctx): pass"})

	r := f.run(append(args("function deploy "+shopAPI), dir)...)
	assert.Equal(t, exitcode.Usage, r.code)
	assert.Contains(t, r.stderr, "--use inline")

	r = f.run(args("function deploy --ref release --commit 0123456789abcdef0123456789abcdef01234567 " + shopAPI)...)
	require.Equal(t, exitcode.OK, r.code, r.stderr)
	code := f.body("PUT "+appPath+"/deployment-settings", 0)["functionSource"].(map[string]any)["code"].(map[string]any)
	assert.Equal(t, "release", code["repo"].(map[string]any)["repoRef"])
	assert.Equal(t, "fns/hello", code["dir"], "kept")

	r = f.run(append(args("function deploy --use inline "+shopAPI), dir)...)
	require.Equal(t, exitcode.OK, r.code, r.stderr)
	code = f.body("PUT "+appPath+"/deployment-settings", 1)["functionSource"].(map[string]any)["code"].(map[string]any)
	assert.Nil(t, code["repo"])
	assert.Empty(t, code["dir"])
	assert.NotNil(t, code["inline"])
}

// deploy of an app that is not a function names hivepaas deploy; hivepaas
// deploy with source flags on a function names function deploy.
func TestFunctionDeployAndDeployPointAtEachOther(t *testing.T) {
	f := newFakeAPI(t)
	f.handle("GET "+appPath+"/deployment-settings", func(w http.ResponseWriter, _ *http.Request, n int) {
		_, _ = w.Write([]byte([]string{deploymentSettings, fnSettings}[min(n, 1)]))
	})
	r := f.run(append(args("function deploy "+shopAPI), dirWith(t, map[string]string{"index.js": "x"}))...)
	assert.Equal(t, exitcode.Usage, r.code)
	assert.Contains(t, r.stderr, "api is not a function")
	assert.Contains(t, r.stderr, "hivepaas deploy")

	r = f.run(args("deploy --image nginx:1.30 " + shopAPI)...)
	assert.Equal(t, exitcode.Invalid, r.code)
	assert.Contains(t, r.stderr, "hivepaas function deploy")
}
