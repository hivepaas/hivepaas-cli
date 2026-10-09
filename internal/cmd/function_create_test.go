package cmd

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hivepaas/hivepaas-cli/internal/exitcode"
	"github.com/hivepaas/hivepaas-cli/internal/link"
)

const fnCreatePath = "/api/projects/P1/production/apps/function"

// fakeCreatedFunction answers the creation of hello, F1, and its deployment D7.
func fakeCreatedFunction(f *fakeAPI) {
	hello := "/api/projects/P1/production/apps/F1"
	f.json("POST "+fnCreatePath, http.StatusCreated, `{"data":{"id":"F1","deploymentId":"D7","taskId":"T7"}}`)
	f.json("GET "+hello, http.StatusOK, `{"data":{"id":"F1","key":"hello","name":"hello"}}`)
	f.json("GET "+hello+"/deployments/D7/status", http.StatusOK, `{"data":{"status":"done"}}`)
	f.json("GET "+hello+"/deployments/D7", http.StatusOK, `{"data":{"id":"D7","status":"done",
		"startedAt":"2026-10-09T10:00:00Z","endedAt":"2026-10-09T10:00:41Z"}}`)
	f.logs("GET "+hello+"/deployments/D7/logs", "Building function-runtime-node24", "1/1 tasks running")
}

// A function made from a directory's code: its files and its settings go in
// one request, which deploys it; the directory is linked to it.
func TestFunctionCreateFromADirectory(t *testing.T) {
	quickly(t)
	f := newFakeAPI(t)
	fakeCreatedFunction(f)
	dir := dirWith(t, map[string]string{"index.js": "export default () => ({})", "package.json": "{}",
		"node_modules/x/index.js": "x"})

	r := f.run(append(args("function create hello -p shop -e production --runtime node24 --call-timeout 10s "+
		"--max-concurrency 4 --packages curl,jq --domain Hello.Example.com"), dir)...)

	require.Equal(t, exitcode.OK, r.code, r.stderr)
	body := f.body("POST "+fnCreatePath, 0)
	assert.Equal(t, "hello", body["name"])
	assert.Equal(t, "active", body["status"])
	assert.Equal(t, "Hello.Example.com", body["domain"], "the server normalizes it")
	source := body["source"].(map[string]any)
	assert.Equal(t, "node24", source["runtime"])
	assert.Equal(t, "10s", source["timeout"])
	assert.InDelta(t, 4, source["maxConcurrency"], 0)
	assert.Equal(t, []any{"curl", "jq"}, source["systemPackages"])
	code := source["code"].(map[string]any)
	assert.Nil(t, code["repo"])
	assert.Equal(t, []any{
		map[string]any{"path": "index.js", "content": "export default () => ({})"},
		map[string]any{"path": "package.json", "content": "{}"},
	}, code["inline"].(map[string]any)["files"])
	assert.Contains(t, r.stderr, "Created function hello")
	assert.Contains(t, r.stderr, "Building function-runtime-node24")
	assert.Contains(t, r.stderr, "Deployed in 41s.")

	l, err := link.Find(dir)
	require.NoError(t, err)
	require.NotNil(t, l, "the directory is linked")
	assert.Equal(t, "F1", l.App.ID)
}

// Code from init --typescript has its entrypoint found: index.ts.
func TestFunctionCreateFindsTheTypeScriptEntrypoint(t *testing.T) {
	quickly(t)
	f := newFakeAPI(t)
	fakeCreatedFunction(f)
	dir := t.TempDir()
	require.Equal(t, exitcode.OK, f.run("function", "init", dir, "--runtime", "node24", "--typescript").code)

	r := f.run(append(args("function create hello -p shop -e production --runtime node24 --no-wait --no-link"),
		dir)...)

	require.Equal(t, exitcode.OK, r.code, r.stderr)
	source := f.body("POST "+fnCreatePath, 0)["source"].(map[string]any)
	assert.Equal(t, "index.ts", source["entrypoint"].(map[string]any)["file"])
	assert.Contains(t, r.stderr, "Entrypoint: index.ts")
	l, _ := link.Find(dir)
	assert.Nil(t, l, "--no-link")
}

// A function built from a repository: no directory, nothing linked.
func TestFunctionCreateFromARepository(t *testing.T) {
	f := newFakeAPI(t)
	fakeCreatedFunction(f)
	f.json("GET /api/projects/P1/production/git-credentials", http.StatusOK,
		`{"data":[{"id":"G2","name":"deploy-key","kind":"ssh-key"}]}`)

	r := f.run(args("function create hello -p shop -e production --runtime python313 --no-wait " +
		"--repo https://github.com/acme/fns.git --ref main --path fns/hello --git-credential deploy-key")...)

	require.Equal(t, exitcode.OK, r.code, r.stderr)
	code := f.body("POST "+fnCreatePath, 0)["source"].(map[string]any)["code"].(map[string]any)
	assert.Nil(t, code["inline"])
	assert.Equal(t, "fns/hello", code["dir"])
	repo := code["repo"].(map[string]any)
	assert.Equal(t, "git", repo["repoType"])
	assert.Equal(t, "https://github.com/acme/fns.git", repo["repoURL"])
	assert.Equal(t, "main", repo["repoRef"])
	assert.Equal(t, map[string]any{"id": "G2"}, repo["credentials"])
}

// Nothing is created from code the server would refuse, nor without a runtime.
func TestFunctionCreateChecksFirst(t *testing.T) {
	f := newFakeAPI(t)
	fakeCreatedFunction(f)

	r := f.run(append(args("function create hello -p shop -e production --runtime node24"), t.TempDir())...)
	assert.Equal(t, exitcode.Invalid, r.code)
	assert.Contains(t, r.stderr, "no code")

	dir := dirWith(t, map[string]string{"index.js": "x"})
	r = f.run(append(args("function create hello -p shop -e production"), dir)...)
	assert.Equal(t, exitcode.Usage, r.code)
	r = f.run(append(args("function create hello -p shop -e production --runtime ruby"), dir)...)
	assert.Equal(t, exitcode.Usage, r.code)
	r = f.run(append(args("function create hello -p shop -e production --runtime node24 --call-timeout 20m"),
		dir)...)
	assert.Equal(t, exitcode.Usage, r.code)
	r = f.run(append(args("function create hello -p shop -e production --runtime node24 --repo https://x.git"),
		dir)...)
	assert.Equal(t, exitcode.Usage, r.code, "a directory and a repository")

	assert.Zero(t, f.called("POST "+fnCreatePath))
}

// --list shows what would be sent, and sends nothing.
func TestFunctionListShowsTheFiles(t *testing.T) {
	f := newFakeAPI(t)
	dir := dirWith(t, map[string]string{"index.js": "12345", "lib/a.js": "1", "dist/x.js": "x", ".gitignore": "dist/"})

	r := f.run(append(args("function create hello -p shop -e production --runtime node24 --list"), dir)...)

	require.Equal(t, exitcode.OK, r.code, r.stderr)
	assert.Contains(t, r.stdout, "index.js")
	assert.Contains(t, r.stdout, "lib/a.js")
	assert.NotContains(t, r.stdout, "dist/x.js")
	assert.Empty(t, f.calls, "--list asks the server nothing")

	r = f.run(append(args("function create hello -p shop -e production --runtime node24 --list -o json"), dir)...)
	require.Equal(t, exitcode.OK, r.code, r.stderr)
	var listed []map[string]any
	require.NoError(t, json.Unmarshal([]byte(r.stdout), &listed))
	assert.Len(t, listed, 3)
}

// --list is for a directory's code: with a repository's it is refused, and
// nothing is made.
func TestFunctionListOfARepositorysCodeIsRefused(t *testing.T) {
	f := newFakeAPI(t)
	fakeCreatedFunction(f)
	f.json("GET "+appPath+"/deployment-settings", http.StatusOK, fnRepoSettings)

	r := f.run(args("function create x -p shop -e production --repo https://github.com/acme/fns.git --list")...)
	assert.Equal(t, exitcode.Usage, r.code)
	assert.Zero(t, f.called("POST "+fnCreatePath))

	r = f.run(args("function deploy --list " + shopAPI)...)
	assert.Equal(t, exitcode.Usage, r.code)
	assert.Zero(t, f.called("PUT "+appPath+"/deployment-settings"))
	assert.Zero(t, f.called("POST "+appPath+"/deploy"))
}

// The handler's file is among the files sent, or nothing is: the deployment
// would fail on it.
func TestFunctionCodeWithoutItsEntrypointIsRefused(t *testing.T) {
	f := newFakeAPI(t)
	fakeCreatedFunction(f)
	f.json("GET "+appPath+"/deployment-settings", http.StatusOK, fnSettings)

	py := dirWith(t, map[string]string{"app.py": "def handler(req, ctx): pass"})
	r := f.run(append(args("function create x -p shop -e production --runtime python313"), py)...)
	assert.Equal(t, exitcode.Invalid, r.code)
	assert.Contains(t, r.stderr, "main.py")

	typo := dirWith(t, map[string]string{"index.js": "x"})
	r = f.run(append(args("function create x -p shop -e production --runtime node24 --entrypoint indx.js"), typo)...)
	assert.Equal(t, exitcode.Invalid, r.code)
	assert.Contains(t, r.stderr, "indx.js")

	ignored := dirWith(t, map[string]string{"index.js": "x", ".gitignore": "index.js\n"})
	r = f.run(append(args("function create x -p shop -e production --runtime node24"), ignored)...)
	assert.Equal(t, exitcode.Invalid, r.code)
	assert.Contains(t, r.stderr, ".gitignore")
	assert.Zero(t, f.called("POST "+fnCreatePath))

	renamed := dirWith(t, map[string]string{"main.js": "x"})
	r = f.run(append(args("function deploy "+shopAPI), renamed)...)
	assert.Equal(t, exitcode.Invalid, r.code, "the stored entrypoint, index.js")
	assert.Zero(t, f.called("PUT "+appPath+"/deployment-settings"))
}
