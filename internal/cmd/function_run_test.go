package cmd

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hivepaas/hivepaas-cli/internal/exitcode"
)

const testRunPath = appPath + "/function/test-run"

// run sends the directory's code and the request, and gives the body on stdout
// and the rest on stderr.
func TestFunctionRun(t *testing.T) {
	f := newFakeAPI(t)
	f.json("POST "+testRunPath, http.StatusOK, `{"data":{"outcome":"ok","status":200,
		"headers":{"content-type":["application/json"]},"body":"eyJoZWxsbyI6IkFkYSJ9","durationMs":12.4,
		"logs":"GET /hello\n","exitCode":0,"librariesBuilt":true,
		"lockFiles":[{"path":"package-lock.json","content":"{\"lockfileVersion\":3}"}]}}`)
	dir := dirWith(t, map[string]string{"index.js": "export default () => ({})", "package.json": "{}"})

	r := f.run(append(args("function run --path /hello --query name=Ada --header X-Trace:abc --data hi "+shopAPI),
		dir)...)

	require.Equal(t, exitcode.OK, r.code, r.stderr)
	assert.JSONEq(t, `{"hello":"Ada"}`, r.stdout)
	assert.Contains(t, r.stderr, "200")
	assert.Contains(t, r.stderr, "GET /hello")
	assert.Contains(t, r.stderr, "package-lock.json")
	assert.Contains(t, r.stderr, "--save-lock")
	assert.NoFileExists(t, filepath.Join(dir, "package-lock.json"))
	body := f.body("POST "+testRunPath, 0)
	req := body["request"].(map[string]any)
	assert.Equal(t, "GET", req["method"])
	assert.Equal(t, "/hello", req["path"])
	assert.Equal(t, map[string]any{"name": []any{"Ada"}}, req["query"])
	assert.Equal(t, map[string]any{"X-Trace": []any{"abc"}}, req["headers"])
	assert.Equal(t, "hi", req["body"])
	assert.Len(t, body["code"].(map[string]any)["files"], 2)

	r = f.run(append(args("function run --save-lock "+shopAPI), dir)...)
	require.Equal(t, exitcode.OK, r.code, r.stderr)
	assert.JSONEq(t, `{"lockfileVersion":3}`, readFile(t, filepath.Join(dir, "package-lock.json")))
}

// A function that did not answer exits 8; one that answered an error status
// does only with --fail, as curl.
func TestFunctionRunExitsAsCurl(t *testing.T) {
	f := newFakeAPI(t)
	f.handle("POST "+testRunPath, func(w http.ResponseWriter, _ *http.Request, n int) {
		_, _ = w.Write([]byte([]string{
			`{"data":{"outcome":"ok","status":500,"body":"","logs":"","exitCode":0,"librariesBuilt":false}}`,
			`{"data":{"outcome":"ok","status":500,"body":"","logs":"","exitCode":0,"librariesBuilt":false}}`,
			`{"data":{"outcome":"not-loaded","error":"index.js has no default export","logs":"",
				"exitCode":1,"librariesBuilt":false}}`,
		}[min(n, 2)]))
	})
	dir := dirWith(t, map[string]string{"index.js": "x"})

	assert.Equal(t, exitcode.OK, f.run(append(args("function run "+shopAPI), dir)...).code)
	assert.Equal(t, exitcode.Deployment, f.run(append(args("function run --fail "+shopAPI), dir)...).code)
	r := f.run(append(args("function run "+shopAPI), dir)...)
	assert.Equal(t, exitcode.Deployment, r.code)
	assert.Contains(t, r.stderr, "not-loaded")
	assert.Contains(t, r.stderr, "index.js has no default export")
}

// -o json gives the whole answer, the body as text.
func TestFunctionRunAsJSON(t *testing.T) {
	f := newFakeAPI(t)
	f.json("POST "+testRunPath, http.StatusOK, `{"data":{"outcome":"ok","status":200,
		"body":"eyJoZWxsbyI6IkFkYSJ9","logs":"","exitCode":0,"librariesBuilt":false}}`)
	dir := dirWith(t, map[string]string{"index.js": "x"})

	r := f.run(append(args("function run -o json "+shopAPI), dir)...)

	require.Equal(t, exitcode.OK, r.code, r.stderr)
	var out map[string]any
	require.NoError(t, json.Unmarshal([]byte(r.stdout), &out))
	assert.Equal(t, `{"hello":"Ada"}`, out["body"])
	assert.Equal(t, "ok", out["outcome"])
}

// The runtime's log lines are read: what the handler logged is its message,
// and the line of the call itself is the status said already.
func TestFunctionRunReadsTheRuntimesLogLines(t *testing.T) {
	f := newFakeAPI(t)
	f.json("POST "+testRunPath, http.StatusOK, `{"data":{"outcome":"ok","status":200,"body":"","durationMs":0.4,
		"logs":"{\"hp\":\"log\",\"requestId\":\"r1\",\"msg\":\"GET /hello\"}\nprinted as it is\n`+
		`{\"hp\":\"invocation\",\"requestId\":\"r1\",\"method\":\"GET\",\"path\":\"/\",\"status\":200}\n",
		"exitCode":0,"librariesBuilt":false}}`)
	dir := dirWith(t, map[string]string{"index.js": "x"})

	r := f.run(append(args("function run "+shopAPI), dir)...)

	require.Equal(t, exitcode.OK, r.code, r.stderr)
	assert.Contains(t, r.stderr, "200 OK in 0.4 ms")
	assert.Contains(t, r.stderr, "GET /hello\n")
	assert.Contains(t, r.stderr, "printed as it is")
	assert.NotContains(t, r.stderr, `"hp"`)
}

func TestDurationWords(t *testing.T) {
	assert.Equal(t, "0.4 ms", durationWords(0.435))
	assert.Equal(t, "12 ms", durationWords(12.4))
	assert.Equal(t, "1.25s", durationWords(1250))
}
