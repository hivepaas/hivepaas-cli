package cmd

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hivepaas/hivepaas-cli/internal/exitcode"
	"github.com/hivepaas/hivepaas-cli/internal/link"
)

// fnSettings are the deployment settings of api, a function with inline code.
const fnSettings = `{"data":{"activeMethod":"function","updateVer":3,"command":"","functionSource":{
	"runtime":"node24","contract":"v1","entrypoint":{"file":"index.js","handler":"default"},
	"code":{"inline":{"files":[{"path":"index.js","content":"old"},{"path":"package.json","content":"{}"},
		{"path":"util.js","content":"u"}]}},
	"systemPackages":["curl"],"timeout":"30s","maxConcurrency":16,"maxBodySize":"6MB",
	"pushToRegistry":{"id":"R1","name":"ghcr"}}}}`

// fnRepoSettings are those of api built from a repository.
const fnRepoSettings = `{"data":{"activeMethod":"function","updateVer":5,"functionSource":{
	"runtime":"python313","contract":"v1","entrypoint":{"file":"main.py","handler":"handler"},
	"code":{"dir":"fns/hello","repo":{"repoType":"git","repoURL":"https://github.com/acme/fns.git",
		"repoRef":"main","commitHash":"","autoDeploy":true}},
	"systemPackages":[],"timeout":"30s","maxConcurrency":16,"maxBodySize":"6MB"}}}`

// dirWith is a temporary directory holding files.
func dirWith(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for path, content := range files {
		full := filepath.Join(dir, filepath.FromSlash(path))
		require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
		require.NoError(t, os.WriteFile(full, []byte(content), 0o644))
	}
	return dir
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(data)
}

// init writes the runtime's starter code, and overwrites nothing unless told.
func TestFunctionInitWritesTheStarterCode(t *testing.T) {
	f := newFakeAPI(t)
	dir := filepath.Join(t.TempDir(), "hello")

	r := f.run("function", "init", dir, "--runtime", "python313")
	require.Equal(t, exitcode.OK, r.code, r.stderr)
	assert.Contains(t, readFile(t, filepath.Join(dir, "main.py")), "def handler(req, ctx):")
	assert.Contains(t, r.stderr, "hivepaas function create")

	require.NoError(t, os.WriteFile(filepath.Join(dir, "main.py"), []byte("mine"), 0o644))
	r = f.run("function", "init", dir, "--runtime", "python313")
	assert.Equal(t, exitcode.Failure, r.code)
	assert.Contains(t, r.stderr, "main.py")
	assert.Equal(t, "mine", readFile(t, filepath.Join(dir, "main.py")), "nothing overwritten")

	r = f.run("fn", "init", dir, "--runtime", "python313", "--force")
	require.Equal(t, exitcode.OK, r.code, r.stderr)
	assert.Contains(t, readFile(t, filepath.Join(dir, "main.py")), "def handler(req, ctx):")

	ts := t.TempDir()
	r = f.run("function", "init", ts, "--runtime", "node24", "--typescript")
	require.Equal(t, exitcode.OK, r.code, r.stderr)
	assert.FileExists(t, filepath.Join(ts, "index.ts"))
	assert.NoFileExists(t, filepath.Join(ts, "index.js"))

	r = f.run("function", "init", t.TempDir())
	assert.Equal(t, exitcode.Usage, r.code, "no terminal to ask at")
	assert.Contains(t, r.stderr, "--runtime")
	assert.Zero(t, len(f.calls), "init asks the server nothing")
}

// get shows a function's settings; ls lists the environment's functions.
func TestFunctionGetAndLs(t *testing.T) {
	f := newFakeAPIWithApps(t, `[{"id":"A1","key":"api","name":"api","status":"active"},
		{"id":"F1","key":"hello","name":"hello","status":"active","category":"function"}]`)
	f.json("GET "+appPath+"/deployment-settings", http.StatusOK, fnSettings)

	r := f.run(args("function get " + shopAPI)...)
	require.Equal(t, exitcode.OK, r.code, r.stderr)
	for _, want := range []string{"node24", "index.js", "default", "3 files", "curl", "30s", "16", "6MB", "ghcr"} {
		assert.Contains(t, r.stdout, want)
	}

	r = f.run(args("function ls -p shop -e production")...)
	require.Equal(t, exitcode.OK, r.code, r.stderr)
	assert.Contains(t, r.stdout, "hello")
	assert.NotContains(t, r.stdout, "api", "an app that is not a function")
}

// An app that is not a function is said to be one.
func TestFunctionCommandsRefuseAnAppThatIsNotOne(t *testing.T) {
	f := newFakeAPI(t)
	f.json("GET "+appPath+"/deployment-settings", http.StatusOK, deploymentSettings)

	r := f.run(args("function get " + shopAPI)...)
	assert.Equal(t, exitcode.Usage, r.code)
	assert.Contains(t, r.stderr, "api is not a function")
}

// pull writes the code kept on the server, and overwrites nothing unless told.
func TestFunctionPull(t *testing.T) {
	f := newFakeAPI(t)
	f.json("GET "+appPath+"/deployment-settings", http.StatusOK, fnSettings)
	dir := dirWith(t, map[string]string{"package.json": "{}"})

	r := f.run(append(args("function pull "+shopAPI), dir)...)
	require.Equal(t, exitcode.OK, r.code, r.stderr)
	assert.Equal(t, "old", readFile(t, filepath.Join(dir, "index.js")))
	assert.Equal(t, "u", readFile(t, filepath.Join(dir, "util.js")))

	require.NoError(t, os.WriteFile(filepath.Join(dir, "index.js"), []byte("mine"), 0o644))
	r = f.run(append(args("function pull "+shopAPI), dir)...)
	assert.Equal(t, exitcode.Failure, r.code)
	assert.Contains(t, r.stderr, "index.js")
	assert.Equal(t, "mine", readFile(t, filepath.Join(dir, "index.js")))

	r = f.run(append(args("function pull --force "+shopAPI), dir)...)
	require.Equal(t, exitcode.OK, r.code, r.stderr)
	assert.Equal(t, "old", readFile(t, filepath.Join(dir, "index.js")))
}

func TestFunctionPullOfARepositorysCodeNamesIt(t *testing.T) {
	f := newFakeAPI(t)
	f.json("GET "+appPath+"/deployment-settings", http.StatusOK, fnRepoSettings)

	r := f.run(append(args("function pull "+shopAPI), t.TempDir())...)
	assert.Equal(t, exitcode.Usage, r.code)
	assert.Contains(t, r.stderr, "https://github.com/acme/fns.git")
	assert.Contains(t, r.stderr, "fns/hello")
}

// metrics shows a function's calls, or says why there are none.
func TestFunctionMetrics(t *testing.T) {
	f := newFakeAPI(t)
	f.handle("GET "+appPath+"/function-metrics", func(w http.ResponseWriter, r *http.Request, n int) {
		assert.Equal(t, "1h", r.URL.Query().Get("range"))
		if n == 0 {
			_, _ = w.Write([]byte(`{"data":{"available":true,"range":"1h","start":"2026-10-09T10:00:00Z",
				"end":"2026-10-09T11:00:00Z","stepSeconds":60,"clamped":false,
				"totals":{"calls":120,"failed":3,"errors4xx":2,"errors5xx":1,"p50":12.5,"p95":80,"p99":210},
				"byOutcome":{"ok":117,"error":3},
				"byPath":[{"method":"GET","path":"/hello","calls":100,"failed":1,"errors4xx":0,"errors5xx":1,
					"p50":11,"p95":70,"p99":200}]}}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":{"available":false,"reason":"no-metrics-backend","range":"1h",
			"start":"2026-10-09T10:00:00Z","end":"2026-10-09T11:00:00Z","stepSeconds":60,"clamped":false}}`))
	})

	r := f.run(args("function metrics --range 1h " + shopAPI)...)
	require.Equal(t, exitcode.OK, r.code, r.stderr)
	for _, want := range []string{"120", "80", "/hello", "ok", "117"} {
		assert.Contains(t, r.stdout, want)
	}

	r = f.run(args("function metrics --range 1h " + shopAPI)...)
	require.Equal(t, exitcode.OK, r.code, r.stderr)
	assert.Contains(t, r.stderr, "no-metrics-backend")
}

// A directory with no link of its own leaves the target to the working
// directory's link, as every command: pull writes into a new directory.
func TestFunctionPullIntoANewDirectoryReadsTheWorkingDirectorysLink(t *testing.T) {
	f := newFakeAPI(t)
	f.json("GET "+appPath+"/deployment-settings", http.StatusOK, fnSettings)
	linked := t.TempDir()
	_, err := link.Write(linked, &link.Link{URL: f.srv.URL, Project: link.Named{ID: "P1", Name: "shop"},
		Env: "production", App: link.Named{ID: "A1", Name: "api"}})
	require.NoError(t, err)
	t.Chdir(linked)

	r := f.run("function", "pull", filepath.Join(t.TempDir(), "new"))

	require.Equal(t, exitcode.OK, r.code, r.stderr)
}
