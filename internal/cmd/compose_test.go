package cmd

import (
	"net/http"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hivepaas/hivepaas-cli/internal/exitcode"
)

const composeFile = `name: shop2
services:
  web:
    image: nginx:1.27
    env_file: app.env
    ports: ["8080:80"]
  db:
    image: postgres:17
    environment:
      POSTGRES_PASSWORD: ${DB_PASSWORD:?}
`

// reviewOf answers a validate: the files the compose file reads and its
// variables - and the plan once both are given.
const composeNeeds = `{"data":{"project":{"id":"","name":"shop2","key":"shop2","env":"production",
	"envKey":"production","newEnv":true,"fileName":"shop2"},
	"services":[{"name":"web","app":"web","image":"nginx:1.27","replicas":1,"mode":"replicated",
		"ports":[{"published":8080,"target":80,"protocol":"tcp","as":"domain","default":"domain",
			"domain":"web.shop2.example.com","suggested":"web.shop2.example.com"}]},
		{"name":"db","app":"db","image":"postgres:17","replicas":1,"mode":"replicated","ports":[]}],
	"variables":[{"name":"DB_PASSWORD","required":true,"given":false,"secret":true,"default":""}],
	"needs":[{"path":"app.env","as":"env_file","by":["web"],"given":false}],"profiles":[]}}`

const composePlanned = `{"data":{"project":{"id":"","name":"shop2","key":"shop2","env":"production",
	"envKey":"production","newEnv":true},
	"services":[{"name":"web","app":"web","image":"nginx:1.27","replicas":1,"mode":"replicated",
		"ports":[{"published":8080,"target":80,"protocol":"tcp","as":"domain","default":"domain",
			"domain":"web.shop2.example.com","suggested":"web.shop2.example.com"}]},
		{"name":"db","app":"db","image":"postgres:17","replicas":1,"mode":"replicated","ports":[]}],
	"variables":[{"name":"DB_PASSWORD","required":true,"given":true,"secret":true,"default":""}],
	"needs":[{"path":"app.env","as":"env_file","by":["web"],"given":true}],"profiles":[],
	"plan":{"planHash":"H1","summary":{"warning":1},"bundle":{},"nodes":[
		{"path":"projects/shop2","kind":"project","action":"create","selected":true,"deploy":false,"restart":false},
		{"path":"projects/shop2/envs/production/apps/web","kind":"app","action":"create","selected":true,
			"deploy":true,"restart":false,
			"issues":[{"code":"COMPOSE_HEALTHCHECK_DROPPED","path":"services.web.healthcheck","severity":"warning",
				"hint":"set it in the app's health checks"}]}]}}}`

func TestComposeUpMakesAProject(t *testing.T) {
	quickly(t)
	f := newFakeAPI(t)
	f.handle("POST /api/projects/from-compose/validate", func(w http.ResponseWriter, _ *http.Request, n int) {
		_, _ = w.Write([]byte([]string{composeNeeds, composePlanned}[min(n, 1)]))
	})
	f.json("POST /api/projects/from-compose/apply", http.StatusOK, `{"data":{"project":{"id":"P9"},
		"apps":[{"service":"web","app":"web","id":"A9"},{"service":"db","app":"db","id":"A8"}],
		"deployments":[{"appId":"A9","deploymentId":"D9"}]}}`)
	web := "/api/projects/P9/production/apps/A9"
	f.json("GET "+web+"/deployments/D9/status", http.StatusOK, `{"data":{"status":"done"}}`)
	f.json("GET "+web+"/deployments/D9", http.StatusOK, `{"data":{"id":"D9","status":"done",
		"startedAt":"2026-10-09T10:00:00Z","endedAt":"2026-10-09T10:00:20Z"}}`)
	f.logs("GET " + web + "/deployments/D9/logs")
	dir := dirWith(t, map[string]string{"compose.yaml": composeFile, "app.env": "LOG_LEVEL=info\n",
		".env": "TZ=UTC\n"})

	r := f.run("compose", "up", "-f", filepath.Join(dir, "compose.yaml"), "--var", "DB_PASSWORD=s3cret", "--yes")

	require.Equal(t, exitcode.OK, r.code, r.stderr)
	first := f.body("POST /api/projects/from-compose/validate", 0)
	assert.Equal(t, composeFile, first["compose"])
	assert.Equal(t, "TZ=UTC\n", first["dotEnv"])
	assert.Equal(t, true, first["deploy"])
	second := f.body("POST /api/projects/from-compose/validate", 1)
	assert.Contains(t, second["files"].(map[string]any), "app.env", "the file it reads, sent")
	assert.Equal(t, "s3cret", second["variables"].(map[string]any)["DB_PASSWORD"].(map[string]any)["value"])
	applied := f.body("POST /api/projects/from-compose/apply", 0)
	assert.Equal(t, "H1", applied["planHash"])
	assert.Equal(t, true, applied["acceptIssues"])
	for _, want := range []string{"shop2", "web", "nginx:1.27", "web.shop2.example.com", "postgres:17",
		"COMPOSE_HEALTHCHECK_DROPPED", "set it in the app's health checks"} {
		assert.Contains(t, r.stderr, want)
	}
	assert.Contains(t, r.stderr, "Deployed in 20s.")
}

// What it cannot go on without is said before anything is made: a variable
// with no value, a blocking issue, a script that did not say --yes.
func TestComposeUpStopsBeforeMaking(t *testing.T) {
	f := newFakeAPI(t)
	f.handle("POST /api/projects/from-compose/validate", func(w http.ResponseWriter, _ *http.Request, n int) {
		if n == 2 {
			_, _ = w.Write([]byte(composePlanned))
			return
		}
		_, _ = w.Write([]byte(composeNeeds))
	})
	dir := dirWith(t, map[string]string{"compose.yaml": composeFile, "app.env": "LOG_LEVEL=info\n"})
	file := filepath.Join(dir, "compose.yaml")

	r := f.run("compose", "up", "-f", file, "--yes")
	assert.Equal(t, exitcode.Usage, r.code)
	assert.Contains(t, r.stderr, "DB_PASSWORD")

	r = f.run("compose", "up", "-f", file, "--var", "DB_PASSWORD=x")
	assert.Equal(t, exitcode.Usage, r.code, "no terminal to ask at")
	assert.Contains(t, r.stderr, "--yes")
	assert.Zero(t, f.called("POST /api/projects/from-compose/apply"))
}

func TestComposeUpRefusesABlockedPlan(t *testing.T) {
	f := newFakeAPI(t)
	f.json("POST /api/projects/from-compose/validate", http.StatusOK, `{"data":{"project":{"name":"x",
		"env":"production","newEnv":true},"services":[],"variables":[],"needs":[],"profiles":[],
		"plan":{"planHash":"H2","summary":{"blocked":1},"bundle":{},"nodes":[{"path":"projects/x/envs/production/apps/web",
			"kind":"app","action":"create","selected":true,"deploy":false,"restart":false,
			"issues":[{"code":"COMPOSE_PRIVILEGED","path":"services.web.privileged","severity":"blocked"}]}]}}}`)
	dir := dirWith(t, map[string]string{"docker-compose.yml": "services:\n  web:\n    image: x\n"})

	r := f.run("compose", "up", "-f", filepath.Join(dir, "docker-compose.yml"), "--yes")

	assert.Equal(t, exitcode.Invalid, r.code)
	assert.Contains(t, r.stderr, "COMPOSE_PRIVILEGED")
	assert.Zero(t, f.called("POST /api/projects/from-compose/apply"))
}

// With -p the services go into a project's environment, made when missing.
func TestComposeUpIntoAProject(t *testing.T) {
	f := newFakeAPI(t)
	f.json("POST /api/projects/P1/from-compose/validate", http.StatusOK, `{"data":{"project":{"id":"P1",
		"name":"shop","env":"staging","newEnv":true},"services":[],"variables":[],"needs":[],"profiles":[],
		"plan":{"planHash":"H3","summary":{},"bundle":{},"nodes":[]}}}`)
	f.json("POST /api/projects/P1/from-compose/apply", http.StatusOK, `{"data":{"project":{"id":"P1"},"apps":[],
		"deployments":[]}}`)
	dir := dirWith(t, map[string]string{"compose.yml": "services:\n  web:\n    image: x\n"})

	r := f.run("compose", "up", "-f", filepath.Join(dir, "compose.yml"), "-p", "shop", "-e", "staging",
		"--no-deploy", "--yes")

	require.Equal(t, exitcode.OK, r.code, r.stderr)
	project := f.body("POST /api/projects/P1/from-compose/validate", 0)["project"].(map[string]any)
	assert.Equal(t, "staging", project["env"])
	assert.Equal(t, true, project["newEnv"])
	assert.Equal(t, false, f.body("POST /api/projects/P1/from-compose/apply", 0)["deploy"])
}
