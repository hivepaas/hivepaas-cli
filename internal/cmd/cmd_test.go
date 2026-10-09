package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hivepaas/hivepaas-cli/internal/exitcode"
)

// appPath is the test app's path under the API: shop / production / api.
const appPath = "/api/projects/P1/production/apps/A1"

// fakeAPI is an installation with one project, shop, its environment
// production, and its app api. Each test adds the handlers it needs, and reads
// the requests it got.
type fakeAPI struct {
	t   *testing.T
	mux *http.ServeMux
	srv *httptest.Server

	mu    sync.Mutex
	calls []string
	// bodies are the bodies of the writes, by "METHOD path".
	bodies map[string][]string
}

func newFakeAPI(t *testing.T) *fakeAPI {
	t.Helper()
	return newFakeAPIWithApps(t, `[{"id":"A1","key":"api","name":"api","status":"active"}]`)
}

// newFakeAPIWithApps is newFakeAPI with production's apps as apps gives them.
func newFakeAPIWithApps(t *testing.T, apps string) *fakeAPI {
	t.Helper()
	f := &fakeAPI{t: t, mux: http.NewServeMux(), bodies: map[string][]string{}}
	f.json("GET /api/projects", http.StatusOK, `{"data":[{"id":"P1","key":"shop","name":"shop",
		"envs":[{"id":"E1","name":"production"}]}]}`)
	f.json("GET /api/projects/P1/production/apps", http.StatusOK, `{"data":`+apps+`}`)
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(body))
		call := r.Method + " " + r.URL.Path
		f.mu.Lock()
		f.calls = append(f.calls, call)
		if r.Method != http.MethodGet {
			f.bodies[call] = append(f.bodies[call], string(body))
		}
		f.mu.Unlock()
		f.mux.ServeHTTP(w, r)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

// json answers pattern with a fixed body.
func (f *fakeAPI) json(pattern string, status int, body string) {
	f.handle(pattern, func(w http.ResponseWriter, _ *http.Request, _ int) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	})
}

// handle answers pattern with h, told how many times it was called before.
func (f *fakeAPI) handle(pattern string, h func(w http.ResponseWriter, r *http.Request, n int)) {
	n := 0
	var mu sync.Mutex
	f.mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		mu.Lock()
		i := n
		n++
		mu.Unlock()
		h(w, r, i)
	})
}

func (f *fakeAPI) called(call string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	count := 0
	for _, c := range f.calls {
		if c == call {
			count++
		}
	}
	return count
}

func (f *fakeAPI) body(call string, i int) map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	require.Greater(f.t, len(f.bodies[call]), i, "no body %d of %s", i, call)
	var v map[string]any
	require.NoError(f.t, json.Unmarshal([]byte(f.bodies[call][i]), &v))
	return v
}

type result struct {
	stdout, stderr string
	code           int
}

// run runs the CLI against the fake API, with the key in the environment as a
// CI job gives it.
func (f *fakeAPI) run(args ...string) result {
	f.t.Helper()
	return f.runWithStdin("", args...)
}

// runWithStdin runs the CLI as run does, with stdin giving what a pipe would.
func (f *fakeAPI) runWithStdin(stdin string, args ...string) result {
	f.t.Helper()
	return f.runIn(context.Background(), stdin, args...)
}

// runIn runs the CLI as runWithStdin does, in ctx: its cancel is a Ctrl-C.
func (f *fakeAPI) runIn(ctx context.Context, stdin string, args ...string) result {
	f.t.Helper()
	env := map[string]string{
		"HIVEPAAS_CONFIG_DIR": f.t.TempDir(),
		"HIVEPAAS_URL":        f.srv.URL,
		"HIVEPAAS_API_KEY":    "KEYID:SECRET",
	}
	var stdout, stderr bytes.Buffer
	a := &App{
		stdin: strings.NewReader(stdin), stdout: &stdout, stderr: &stderr,
		getenv: func(k string) string { return env[k] },
	}
	code := a.Run(ctx, args)
	return result{stdout.String(), stderr.String(), code}
}

// quickly makes the waiting for a deployment take no time.
func quickly(t *testing.T) {
	t.Helper()
	poll, grace, retry := pollInterval, logsGrace, logsRetry
	pollInterval, logsGrace, logsRetry = 10*time.Millisecond, time.Second, 10*time.Millisecond
	t.Cleanup(func() { pollInterval, logsGrace, logsRetry = poll, grace, retry })
}

// logs answers a deployment's log stream with lines, then ends it.
func (f *fakeAPI) logs(pattern string, lines ...string) {
	f.handle(pattern, func(w http.ResponseWriter, r *http.Request, _ int) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		frames := make([]map[string]string, 0, len(lines))
		for i, line := range lines {
			frames = append(frames, map[string]string{"type": "out", "data": line,
				"ts": time.Date(2026, 10, 8, 14, 2, i, 0, time.UTC).Format(time.RFC3339)})
		}
		data, _ := json.Marshal(frames)
		_ = conn.WriteMessage(websocket.BinaryMessage, data)
	})
}

const shopAPI = "-p shop -e production -a api"

func args(line string) []string { return strings.Fields(line) }

const envVarsV1 = `{"data":{
	"runtimeEnvVars":[
		{"key":"HIVEPAAS_APP_NAME","value":"api","isSystem":true,"isReadOnly":true,"isLiteral":true},
		{"key":"LOG_LEVEL","value":"info"}],
	"buildtimeEnvVars":[{"key":"NODE_ENV","value":"production"}],
	"sharedEnvVars":[],
	"inheritedRuntimeEnvVars":[{"key":"HIVEPAAS_PROJECT_NAME","value":"shop","isSystem":true}],
	"updateVer":%d}}`

// A change made in the dashboard between the read and the write makes the
// write fail: the CLI reads again, changes that, and writes it.
func TestEnvSetWritesAgainOverAChangeMadeMeanwhile(t *testing.T) {
	f := newFakeAPI(t)
	f.handle("GET "+appPath+"/env-vars", func(w http.ResponseWriter, _ *http.Request, n int) {
		_, _ = w.Write([]byte(strings.Replace(envVarsV1, "%d", []string{"1", "2"}[min(n, 1)], 1)))
	})
	f.handle("PUT "+appPath+"/env-vars", func(w http.ResponseWriter, _ *http.Request, n int) {
		if n == 0 {
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"status":409,"code":"ERR_UPDATE_VER_MISMATCHED","detail":"changed meanwhile"}`))
			return
		}
		_, _ = w.Write([]byte(`{"meta":{"warning":"applied to 0 of 1 tasks"}}`))
	})

	r := f.run(args("env set LOG_LEVEL=debug FEATURE_X=on " + shopAPI)...)

	require.Equal(t, exitcode.OK, r.code, r.stderr)
	assert.Contains(t, r.stderr, "Changed LOG_LEVEL, added FEATURE_X (runtime) on api (shop / production).")
	assert.Contains(t, r.stderr, "Warning: applied to 0 of 1 tasks")
	assert.Equal(t, 2, f.called("PUT "+appPath+"/env-vars"))
	written := f.body("PUT "+appPath+"/env-vars", 1)
	assert.InDelta(t, 2, written["updateVer"], 0, "the second write is of what the second read gave")
	assert.Equal(t, []any{
		map[string]any{"key": "LOG_LEVEL", "value": "debug", "isLiteral": false},
		map[string]any{"key": "FEATURE_X", "value": "on", "isLiteral": false},
	}, written["runtimeEnvVars"], "the app's own, changed, without those HivePaaS sets")
	assert.Equal(t, []any{map[string]any{"key": "NODE_ENV", "value": "production", "isLiteral": false}},
		written["buildtimeEnvVars"], "the other lists as they were")
}

func TestEnvUnset(t *testing.T) {
	f := newFakeAPI(t)
	f.json("GET "+appPath+"/env-vars", http.StatusOK, strings.Replace(envVarsV1, "%d", "1", 1))
	f.json("PUT "+appPath+"/env-vars", http.StatusOK, `{"meta":null}`)

	r := f.run(args("env unset HIVEPAAS_APP_NAME " + shopAPI)...)
	assert.Equal(t, exitcode.Invalid, r.code)
	assert.Contains(t, r.stderr, "HIVEPAAS_APP_NAME is set by HivePaaS")

	r = f.run(args("env unset NOPE " + shopAPI)...)
	assert.Equal(t, exitcode.NotFound, r.code)
	assert.Zero(t, f.called("PUT "+appPath+"/env-vars"), "nothing is written when a key is not there")

	r = f.run(args("env unset --build NODE_ENV " + shopAPI)...)
	require.Equal(t, exitcode.OK, r.code, r.stderr)
	assert.Equal(t, []any{}, f.body("PUT "+appPath+"/env-vars", 0)["buildtimeEnvVars"])
}

const deploymentSettings = `{"data":{"activeMethod":"image",
	"imageSource":{"image":"ghcr.io/acme/api:1.4.2","registryAuth":{"id":"R1","name":"ghcr"}},
	"image":{"repoName":"shop-api","tagPrefix":"prd"},"command":"serve","updateVer":4}}`

// The deployment settings changed deploy the app: the CLI waits for that
// deployment, and does not start another.
func TestDeployAnImage(t *testing.T) {
	quickly(t)
	f := newFakeAPI(t)
	f.json("GET "+appPath+"/deployment-settings", http.StatusOK, deploymentSettings)
	f.json("PUT "+appPath+"/deployment-settings", http.StatusOK, `{"data":{"deploymentId":"D1","taskId":"T1"}}`)
	f.json("GET "+appPath+"/deployments/D1/status", http.StatusOK, `{"data":{"status":"done"}}`)
	f.json("GET "+appPath+"/deployments/D1", http.StatusOK, `{"data":{"id":"D1","status":"done",
		"startedAt":"2026-10-08T14:02:10Z","endedAt":"2026-10-08T14:02:34Z"}}`)
	f.logs("GET "+appPath+"/deployments/D1/logs", "Pulling ghcr.io/acme/api:1.4.3", "1/1 tasks running")

	r := f.run(args("deploy --image ghcr.io/acme/api:1.4.3 -o json " + shopAPI)...)

	require.Equal(t, exitcode.OK, r.code, r.stderr)
	assert.Zero(t, f.called("POST "+appPath+"/deploy"))
	written := f.body("PUT "+appPath+"/deployment-settings", 0)
	assert.Equal(t, map[string]any{"enabled": false, "image": "ghcr.io/acme/api:1.4.3",
		"registryAuth": map[string]any{"id": "R1"}}, written["imageSource"])
	assert.Equal(t, "serve", written["command"])
	assert.InDelta(t, 4, written["updateVer"], 0)
	assert.Contains(t, r.stderr, "Image: ghcr.io/acme/api:1.4.2 -> 1.4.3")
	assert.Contains(t, r.stderr, "Pulling ghcr.io/acme/api:1.4.3")
	assert.Contains(t, r.stderr, "Deployed in 24s.")
	var out map[string]any
	require.NoError(t, json.Unmarshal([]byte(r.stdout), &out), "stdout is the deployment only")
	assert.Equal(t, "D1", out["id"])
}

func TestDeployThatFailsExits8(t *testing.T) {
	quickly(t)
	f := newFakeAPI(t)
	f.json("POST "+appPath+"/deploy", http.StatusOK, `{"data":{"deploymentId":"D2","taskId":"T2"}}`)
	f.handle("GET "+appPath+"/deployments/D2/status", func(w http.ResponseWriter, _ *http.Request, n int) {
		_, _ = w.Write([]byte(`{"data":{"status":"` + []string{"in-progress", "failed"}[min(n, 1)] + `"}}`))
	})
	f.json("GET "+appPath+"/deployments/D2", http.StatusOK, `{"data":{"id":"D2","status":"failed",
		"startedAt":"2026-10-08T14:02:10Z","endedAt":"2026-10-08T14:02:51Z","output":{"error":"health check failed"}}}`)
	f.logs("GET "+appPath+"/deployments/D2/logs", "Updating the service")

	r := f.run(args("deploy " + shopAPI)...)

	assert.Equal(t, exitcode.Deployment, r.code)
	assert.Contains(t, r.stderr, "Deployment failed after 41s: health check failed")
	assert.Contains(t, r.stderr, "hivepaas logs --deployment D2 -p shop -e production -a api")
	assert.NotContains(t, r.stderr, "Error: exit", "the exit is not reported twice")
}

func TestDeployTimesOutLeavingTheDeploymentRunning(t *testing.T) {
	quickly(t)
	f := newFakeAPI(t)
	f.json("POST "+appPath+"/deploy", http.StatusOK, `{"data":{"deploymentId":"D3","taskId":"T3"}}`)
	f.json("GET "+appPath+"/deployments/D3/status", http.StatusOK, `{"data":{"status":"in-progress"}}`)
	f.logs("GET " + appPath + "/deployments/D3/logs")

	r := f.run(args("deploy --timeout 100ms " + shopAPI)...)

	assert.Equal(t, exitcode.Timeout, r.code)
	assert.Contains(t, r.stderr, "Deployment D3 of api is still running on the server.")
	assert.Contains(t, r.stderr, "Cancel it:  hivepaas deploy cancel D3 -p shop -e production -a api")
	assert.Zero(t, f.called("POST "+appPath+"/deployments/D3/cancel"), "waiting no more cancels nothing")
}

func TestDeployCancelTheRunningOne(t *testing.T) {
	f := newFakeAPI(t)
	f.handle("GET "+appPath+"/deployments", func(w http.ResponseWriter, r *http.Request, _ int) {
		assert.Equal(t, "in-progress,not-started", r.URL.Query().Get("status"))
		_, _ = w.Write([]byte(`{"data":[{"id":"D4","status":"in-progress"}]}`))
	})
	f.json("POST "+appPath+"/deployments/D4/cancel", http.StatusOK, `{"data":{"canceled":false}}`)

	r := f.run(args("deploy cancel " + shopAPI)...)

	require.Equal(t, exitcode.OK, r.code, r.stderr)
	assert.Contains(t, r.stderr, "Canceling deployment D4 of api (shop / production)")
}

func TestAPIFillsInTheTarget(t *testing.T) {
	f := newFakeAPI(t)
	f.json("GET "+appPath+"/routing-settings", http.StatusOK, `{"data":{"zeta":1,"alpha":"<b>"}}`)
	f.json("GET /api/nope", http.StatusForbidden, `{"status":403,"code":"ERR_FORBIDDEN","detail":"Forbidden"}`)

	r := f.run(args("api get /projects/{project}/{env}/apps/{app}/routing-settings " + shopAPI)...)
	require.Equal(t, exitcode.OK, r.code, r.stderr)
	assert.Equal(t, "{\n  \"data\": {\n    \"zeta\": 1,\n    \"alpha\": \"<b>\"\n  }\n}\n", r.stdout,
		"the body as it came, indented, its keys in their order")

	r = f.run("api", "GET", "/nope", "-o", "json")
	assert.Equal(t, exitcode.Forbidden, r.code)
	assert.JSONEq(t, `{"status":403,"code":"ERR_FORBIDDEN","detail":"Forbidden","title":""}`, r.stderr)
}

func TestTemplatesDeployAsksAboutDataLeftBehind(t *testing.T) {
	f := newFakeAPI(t)
	f.json("GET /api/app-templates/postgres", http.StatusOK, `{"data":{"name":"postgres","title":"PostgreSQL",
		"versions":[{"name":"18","default":true}],
		"parameters":[{"name":"dataVolume","type":"volume"},{"name":"replicaOf","type":"app","optional":true},
			{"name":"port","type":"int","optional":true}]}}`)
	f.json("GET /api/projects/P1/cluster-volumes", http.StatusOK,
		`{"data":[{"id":"V1","name":"default"},{"id":"V2","name":"fast"}]}`)
	f.json("POST /api/projects/P1/production/apps/from-template/preflight", http.StatusOK, `{"data":{
		"storage":[{"app":"orders-db","appKey":"orders_db","isDatabase":true,"path":"shop/prd/orders-db",
			"volume":{"id":"V2","name":"fast"}}]}}`)
	f.json("POST /api/projects/P1/production/apps/from-template", http.StatusCreated,
		`{"data":{"app":{"id":"A9"},"deployment":{"id":"D9"}}}`)

	r := f.run(args("template deploy postgres --name orders-db -p shop -e production " +
		"--param dataVolume=fast --param replicaOf=api --param port=6432")...)

	assert.Equal(t, exitcode.Invalid, r.code)
	assert.Contains(t, r.stderr, "orders-db (a database): shop/prd/orders-db on volume fast")
	assert.Contains(t, r.stderr, "give --keep-storage to start on that data, or --reset-storage")
	assert.Zero(t, f.called("POST /api/projects/P1/production/apps/from-template"))
	asked := f.body("POST /api/projects/P1/production/apps/from-template/preflight", 0)
	assert.Equal(t, map[string]any{"dataVolume": "V2", "replicaOf": "api", "port": float64(6432)}, asked["params"],
		"a volume by its id, an app by its key, a number as one")

	r = f.run(args("template deploy postgres --name orders-db -p shop -e production --param dataVolume=fast " +
		"--reset-storage --no-wait")...)
	require.Equal(t, exitcode.OK, r.code, r.stderr)
	assert.Equal(t, true, f.body("POST /api/projects/P1/production/apps/from-template", 0)["resetStorage"])
	assert.Contains(t, r.stderr, "Created orders-db (PostgreSQL 18) in shop / production.")
}

func TestTemplatesDeployNeedsAVolumeWhenThereAreSeveral(t *testing.T) {
	f := newFakeAPI(t)
	f.json("GET /api/app-templates/postgres", http.StatusOK, `{"data":{"name":"postgres","title":"PostgreSQL",
		"parameters":[{"name":"dataVolume","title":"Data volume","type":"volume"}]}}`)
	f.json("GET /api/projects/P1/cluster-volumes", http.StatusOK,
		`{"data":[{"id":"V1","name":"default"},{"id":"V2","name":"fast"}]}`)

	r := f.run(args("template deploy postgres -p shop -e production")...)

	assert.Equal(t, exitcode.Usage, r.code)
	assert.Contains(t, r.stderr, "which volume for dataVolume (Data volume)? Give --param dataVolume=NAME, "+
		"of: default, fast")
}

func TestListsShowKeys(t *testing.T) {
	f := newFakeAPI(t)

	r := f.run("project", "ls")
	require.Equal(t, exitcode.OK, r.code, r.stderr)
	assert.Equal(t, "NAME  KEY   ENVS        STATUS\nshop  shop  production  \n", r.stdout)

	r = f.run(args("app ls -p shop -e production")...)
	require.Equal(t, exitcode.OK, r.code, r.stderr)
	assert.Equal(t, "NAME  KEY  KIND  STATUS  UPDATED\napi   api  -     active  \n", r.stdout)
}

// The nouns are singular, as gh's; the plural, as fly's, does the same.
func TestThePluralIsAnAlias(t *testing.T) {
	f := newFakeAPI(t)
	for _, pair := range [][2]string{
		{"project ls", "projects ls"},
		{"app ls -p shop -e production", "apps ls -p shop -e production"},
		{"app get api -p shop -e production -o json", "apps get api -p shop -e production -o json"},
	} {
		singular, plural := f.run(args(pair[0])...), f.run(args(pair[1])...)
		assert.Equal(t, singular, plural, pair[1])
	}
	f.json("GET /api/app-templates", 200, `{"data":[{"name":"postgres","title":"PostgreSQL"}]}`)
	assert.Equal(t, f.run("template", "ls"), f.run("templates", "ls"))
	r := f.run("contexts", "ls")
	assert.Equal(t, exitcode.OK, r.code, r.stderr)
}
