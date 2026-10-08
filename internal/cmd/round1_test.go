package cmd

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hivepaas/hivepaas-cli/internal/exitcode"
)

func TestDotenvRoundTrip(t *testing.T) {
	pairs, err := parseDotenv(`# the app's settings
export LOG_LEVEL=debug
EMPTY=
SPACED =  value with spaces   # and a comment
URL=postgres://u:p@db:5432/app?sslmode=disable
SINGLE='kept as it is: $HOME \n'
DOUBLE="line one\nline two \"quoted\""
MULTI="first
second"
LOG_LEVEL=info
`)
	require.NoError(t, err)
	assert.Equal(t, [][2]string{
		{"LOG_LEVEL", "info"},
		{"EMPTY", ""},
		{"SPACED", "value with spaces"},
		{"URL", "postgres://u:p@db:5432/app?sslmode=disable"},
		{"SINGLE", `kept as it is: $HOME \n`},
		{"DOUBLE", "line one\nline two \"quoted\""},
		{"MULTI", "first\nsecond"},
	}, pairs, "the last of a key wins, in the place of its first")

	again, err := parseDotenv(formatDotenv(pairs))
	require.NoError(t, err)
	assert.Equal(t, pairs, again, "what env pull writes, env set --file reads back")
	assert.Equal(t, "A=plain\nB='has space'\nC=\"it's\\nhere\"\n",
		formatDotenv([][2]string{{"A", "plain"}, {"B", "has space"}, {"C", "it's\nhere"}}))

	for _, bad := range []string{"NOEQUALS", "1KEY=x", `Q="not closed`, `Q="x" trailing`} {
		_, err = parseDotenv(bad)
		assert.Error(t, err, bad)
	}
}

const envVarsWithInherited = `{"data":{
	"inheritedRuntimeEnvVars":[{"key":"HIVEPAAS_PROJECT_NAME","value":"shop","isSystem":true},
		{"key":"TZ","value":"UTC"},{"key":"LOG_LEVEL","value":"warn"}],
	"sharedEnvVars":[{"key":"HIVEPAAS_HOST","value":"api","isSystem":true},{"key":"REGION","value":"eu"}],
	"runtimeEnvVars":[{"key":"LOG_LEVEL","value":"debug"},
		{"key":"DATABASE_URL","value":"${db.HIVEPAAS_URL}"},
		{"key":"TEMPLATE","value":"${not expanded}","isLiteral":true}],
	"buildtimeEnvVars":[{"key":"NODE_ENV","value":"production"}],
	"updateVer":3}}`

func TestEnvPull(t *testing.T) {
	f := newFakeAPI(t)
	f.json("GET "+appPath+"/env-vars", http.StatusOK, envVarsWithInherited)

	r := f.run(args("env pull " + shopAPI)...)
	require.Equal(t, exitcode.OK, r.code, r.stderr)
	assert.Equal(t, "TZ=UTC\nLOG_LEVEL=debug\nREGION=eu\nDATABASE_URL='${db.HIVEPAAS_URL}'\n"+
		"TEMPLATE='${not expanded}'\n", r.stdout,
		"inherited, shared, runtime, the app's own over what it inherits; HivePaaS's own left out")
	assert.Contains(t, r.stderr, "DATABASE_URL names other variables")
	assert.NotContains(t, r.stderr, "TEMPLATE names", "a literal value names nothing")

	r = f.run(args("env pull --build " + shopAPI)...)
	require.Equal(t, exitcode.OK, r.code, r.stderr)
	assert.Equal(t, "REGION=eu\nNODE_ENV=production\n", r.stdout)

	file := filepath.Join(t.TempDir(), ".env")
	r = f.run(append(args("env pull "+shopAPI), file)...)
	require.Equal(t, exitcode.OK, r.code, r.stderr)
	info, err := os.Stat(file)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "it holds secrets")

	r = f.run(append(args("env pull "+shopAPI), file)...)
	assert.Equal(t, exitcode.Usage, r.code)
	assert.Contains(t, r.stderr, "exists: give --force")
	r = f.run(append(args("env pull --force "+shopAPI), file)...)
	assert.Equal(t, exitcode.OK, r.code, r.stderr)
}

func TestEnvSetFromAFile(t *testing.T) {
	f := newFakeAPI(t)
	f.json("GET "+appPath+"/env-vars", http.StatusOK, strings.Replace(envVarsV1, "%d", "1", 1))
	f.json("PUT "+appPath+"/env-vars", http.StatusOK, `{"meta":null}`)
	file := filepath.Join(t.TempDir(), ".env")
	require.NoError(t, os.WriteFile(file, []byte("LOG_LEVEL=warn\nFEATURE_X=on\n"), 0o600))

	r := f.run(append(args("env set --file "+file+" "+shopAPI), "FEATURE_X=off")...)

	require.Equal(t, exitcode.OK, r.code, r.stderr)
	assert.Equal(t, []any{
		map[string]any{"key": "LOG_LEVEL", "value": "warn", "isLiteral": false},
		map[string]any{"key": "FEATURE_X", "value": "off", "isLiteral": false},
	}, f.body("PUT "+appPath+"/env-vars", 0)["runtimeEnvVars"], "an argument beside the file wins")

	r = f.run(args("env set " + shopAPI)...)
	assert.Equal(t, exitcode.Usage, r.code)
}

const (
	deploymentD2 = `{"id":"D2","status":"failed","createdAt":"2026-10-08T14:00:00Z",
		"startedAt":"2026-10-08T14:00:01Z","endedAt":"2026-10-08T14:00:42Z",
		"trigger":{"source":"repo-webhook","sourceUser":{"username":"tien"}},
		"output":{"commitHashShort":"a1b2c3d","commitTitle":"Fix the login page","error":"build failed"}}`
	deploymentD1 = `{"id":"D1","status":"done","createdAt":"2026-10-07T09:00:00Z",
		"startedAt":"2026-10-07T09:00:01Z","endedAt":"2026-10-07T09:00:25Z","trigger":{"source":"api"},
		"settings":{"activeMethod":"image","imageSource":{"image":"ghcr.io/acme/api:1.4.2"}}}`
	deploymentsList = `{"data":[` + deploymentD2 + `,` + deploymentD1 + `]}`
)

func TestDeployLsAndGet(t *testing.T) {
	f := newFakeAPI(t)
	f.handle("GET "+appPath+"/deployments", func(w http.ResponseWriter, r *http.Request, _ int) {
		assert.Equal(t, "5", r.URL.Query().Get("pageLimit"))
		assert.Equal(t, "failed", r.URL.Query().Get("status"))
		_, _ = w.Write([]byte(deploymentsList))
	})
	f.json("GET "+appPath+"/deployments/D2", http.StatusOK, `{"data":`+deploymentD2+`}`)

	r := f.run(args("deploy ls --limit 5 --status failed " + shopAPI)...)
	require.Equal(t, exitcode.OK, r.code, r.stderr)
	lines := strings.Split(strings.TrimSpace(r.stdout), "\n")
	require.Len(t, lines, 3)
	assert.Regexp(t, `^ID\s+STATUS\s+BY\s+CREATED\s+TOOK\s+SOURCE$`, lines[0])
	assert.Regexp(t, `^D2\s+failed\s+push \(tien\)\s+.+?\s+41s\s+a1b2c3d Fix the login page$`, lines[1])
	assert.Regexp(t, `^D1\s+done\s+api\s+.+?\s+24s\s+ghcr.io/acme/api:1.4.2$`, lines[2])

	r = f.run(args("deploy get D2 " + shopAPI)...)
	require.Equal(t, exitcode.OK, r.code, r.stderr)
	assert.Contains(t, r.stdout, "Deployment D2 of api (shop / production)\nStatus:  failed\nBy:      push (tien)\n")
	assert.Contains(t, r.stdout, "Error:   build failed\n")
	assert.Contains(t, r.stderr, "hivepaas logs --deployment D2 -p shop -e production -a api")
}

func TestAppStopAndStart(t *testing.T) {
	f := newFakeAPI(t)
	f.json("POST "+appPath+"/running-status", http.StatusOK, `{"meta":null}`)

	r := f.run(args("app stop " + shopAPI)...)
	require.Equal(t, exitcode.OK, r.code, r.stderr)
	assert.Contains(t, r.stderr, "Stopped api (shop / production).")
	assert.Equal(t, map[string]any{"running": false}, f.body("POST "+appPath+"/running-status", 0))

	r = f.run(args("app start api -p shop -e production")...)
	require.Equal(t, exitcode.OK, r.code, r.stderr)
	assert.Equal(t, map[string]any{"running": true}, f.body("POST "+appPath+"/running-status", 1))
}

func TestLogsSearchTheStoredLogs(t *testing.T) {
	f := newFakeAPI(t)
	f.handle("GET "+appPath+"/logs/history", func(w http.ResponseWriter, r *http.Request, _ int) {
		q := r.URL.Query()
		assert.Equal(t, "timeout", q.Get("search"))
		assert.Equal(t, "error,warn", q.Get("levels"))
		assert.Equal(t, "50", q.Get("limit"))
		assert.NotEmpty(t, q.Get("start"), "--since 2d is a start")
		_, _ = w.Write([]byte(`{"data":{"logs":[
			{"type":"err","data":"db timeout","ts":"2026-10-08T14:00:00Z"},
			{"type":"warn","data":"slow: timeout soon","ts":"2026-10-08T14:00:05Z"}],
			"truncated":true,"nextEnd":"2026-10-08T13:59:59.999999999Z"}}`))
	})

	r := f.run(args("logs --search timeout --level error,warn --since 2d --tail 50 " + shopAPI)...)

	require.Equal(t, exitcode.OK, r.code, r.stderr)
	assert.Equal(t, "2026-10-08T14:00:00Z  db timeout\n2026-10-08T14:00:05Z  slow: timeout soon\n", r.stdout)
	assert.Contains(t, r.stderr, "Older lines match too: --until 2026-10-08T13:59:59.999999999Z shows them.")

	r = f.run(args("logs -f --search timeout " + shopAPI)...)
	assert.Equal(t, exitcode.Usage, r.code, "the stored logs are not followed")
}

func TestOpenPrintsTheAddresses(t *testing.T) {
	f := newFakeAPI(t)
	f.json("GET "+appPath, http.StatusOK, `{"data":{"id":"A1","key":"api","name":"api",
		"accessLinks":["https://api.shop.example.com","javascript:alert(1)","http://10.0.0.5:8080"]}}`)

	r := f.run(args("open --print " + shopAPI)...)

	require.Equal(t, exitcode.OK, r.code, r.stderr)
	assert.Equal(t, "https://api.shop.example.com\nhttp://10.0.0.5:8080\n", r.stdout, "web addresses only")
}

// Tab offers what the installation has, by key, with the name beside it.
func TestTabCompletes(t *testing.T) {
	f := newFakeAPI(t)
	f.json("GET /api/app-templates", http.StatusOK,
		`{"data":[{"name":"postgres","title":"PostgreSQL"},{"name":"redis","title":"Redis"}]}`)

	r := f.run("__complete", "app", "get", "-p", "shop", "-e", "production", "")
	require.Equal(t, exitcode.OK, r.code, r.stderr)
	assert.True(t, strings.HasPrefix(r.stdout, "api\tapi\n:4\n"), r.stdout)

	r = f.run("__complete", "app", "ls", "-p", "")
	assert.True(t, strings.HasPrefix(r.stdout, "shop\tshop\n:4\n"), r.stdout)

	r = f.run("__complete", "app", "ls", "-p", "shop", "-e", "")
	assert.True(t, strings.HasPrefix(r.stdout, "production\n:4\n"), r.stdout)

	r = f.run("__complete", "template", "deploy", "")
	assert.True(t, strings.HasPrefix(r.stdout, "postgres\tPostgreSQL\nredis\tRedis\n:4\n"), r.stdout)

	r = f.run("__complete", "app", "get", "")
	assert.True(t, strings.HasPrefix(r.stdout, ":4\n"), "with no project named, nothing - and no question")
}

func TestDeployLsJSONIsTheAPIs(t *testing.T) {
	f := newFakeAPI(t)
	f.json("GET "+appPath+"/deployments", http.StatusOK, deploymentsList)

	r := f.run(args("deploy ls -o json " + shopAPI)...)

	require.Equal(t, exitcode.OK, r.code, r.stderr)
	var got []map[string]any
	require.NoError(t, json.Unmarshal([]byte(r.stdout), &got))
	assert.Len(t, got, 2)
	assert.Equal(t, "D2", got[0]["id"])
}
