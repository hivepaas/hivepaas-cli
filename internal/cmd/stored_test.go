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

const (
	envPath     = "/api/projects/P1/production"
	projectPath = "/api/projects/P1"
)

// An environment's secrets: one it has is changed, keeping whether its apps
// get it; one it has not is added, not for its apps when told; one it
// inherits from the project is not its to change, and it gets its own.
func TestSecretSetAtAnEnvironment(t *testing.T) {
	f := newFakeAPI(t)
	f.json("GET "+envPath+"/secrets", http.StatusOK, `{"data":[
		{"id":"S1","key":"DATABASE_URL","inheritable":true,"default":false,"updateVer":3},
		{"id":"S9","key":"SHARED","inherited":true,"inheritable":true,"updateVer":1}]}`)
	f.json("PUT "+envPath+"/secrets/S1", http.StatusOK, `{"meta":null}`)
	f.json("POST "+envPath+"/secrets", http.StatusCreated, `{"data":{"id":"S2"}}`)

	r := f.run(args("secret set DATABASE_URL=postgres://db --scope env -p shop -e production")...)
	require.Equal(t, exitcode.OK, r.code, r.stderr)
	assert.Equal(t, map[string]any{"key": "DATABASE_URL", "value": "postgres://db", "base64": false,
		"inheritable": true, "default": false, "updateVer": float64(3)}, f.body("PUT "+envPath+"/secrets/S1", 0))
	assert.Contains(t, r.stderr, "Changed DATABASE_URL on the environment shop / production.")

	r = f.run(args("secret set SHARED=mine --scope env --no-inheritable -p shop -e production")...)
	require.Equal(t, exitcode.OK, r.code, r.stderr)
	assert.Equal(t, map[string]any{"key": "SHARED", "value": "mine", "base64": false, "inheritable": false,
		"default": false}, f.body("POST "+envPath+"/secrets", 0))
	assert.Zero(t, f.called("GET "+appPath+"/secrets"), "no app is asked for")
}

// A project's secret is for its apps unless told; --previews is an app's,
// and --scope says app, env or project.
func TestSecretSetAtAProject(t *testing.T) {
	f := newFakeAPI(t)
	f.json("GET "+projectPath+"/secrets", http.StatusOK, `{"data":[]}`)
	f.json("POST "+projectPath+"/secrets", http.StatusCreated, `{"data":{"id":"S3"}}`)

	r := f.run(args("secret set SENTRY_DSN=https://sentry --scope project -p shop")...)
	require.Equal(t, exitcode.OK, r.code, r.stderr)
	assert.Equal(t, map[string]any{"key": "SENTRY_DSN", "value": "https://sentry", "base64": false,
		"inheritable": true, "default": false}, f.body("POST "+projectPath+"/secrets", 0))
	assert.Contains(t, r.stderr, "Added SENTRY_DSN on the project shop.")

	r = f.run(args("secret set A=1 --scope project --no-previews -p shop")...)
	assert.Equal(t, exitcode.Usage, r.code)
	assert.Contains(t, r.stderr, "--inheritable or --no-inheritable")

	r = f.run(args("secret ls --scope team -p shop")...)
	assert.Equal(t, exitcode.Usage, r.code)
	assert.Equal(t, 1, f.called("POST "+projectPath+"/secrets"), "nothing more was written")
}

// A project's config files: listed as its own and for its apps, pushed,
// pulled and removed there.
func TestConfigFilesOfAProject(t *testing.T) {
	f := newFakeAPI(t)
	f.json("GET "+projectPath+"/config-files", http.StatusOK, `{"data":[
		{"id":"F1","name":"ca-bundle","content":"-----BEGIN CERTIFICATE-----\n","base64":false,
			"inheritable":true,"updateVer":2,"size":28}]}`)
	f.json("PUT "+projectPath+"/config-files/F1", http.StatusOK, `{"meta":null}`)
	f.json("POST "+projectPath+"/config-files", http.StatusCreated, `{"data":{"id":"F2"}}`)
	f.json("DELETE "+projectPath+"/config-files/F1", http.StatusOK, `{"meta":null}`)

	r := f.run(args("config-file ls --scope project -p shop")...)
	require.Equal(t, exitcode.OK, r.code, r.stderr)
	assert.Regexp(t, `NAME\s+SIZE\s+TYPE\s+FOR APPS\s+OF`, r.stdout)
	assert.Regexp(t, `ca-bundle\s+28 B\s+text\s+yes\s+this project`, r.stdout)

	r = f.run(args("config-file pull ca-bundle --scope project -p shop")...)
	require.Equal(t, exitcode.OK, r.code, r.stderr)
	assert.Equal(t, "-----BEGIN CERTIFICATE-----\n", r.stdout)

	conf := filepath.Join(t.TempDir(), "settings.yaml")
	require.NoError(t, os.WriteFile(conf, []byte("level: info\n"), 0o600))
	r = f.run("config-file", "push", "settings", conf, "--scope", "project", "--no-inheritable", "-p", "shop")
	require.Equal(t, exitcode.OK, r.code, r.stderr)
	assert.Equal(t, map[string]any{"name": "settings", "content": "level: info\n", "base64": false,
		"inheritable": false, "default": false}, f.body("POST "+projectPath+"/config-files", 0))
	assert.Contains(t, r.stderr, "Added config file settings to the project shop.")

	r = f.run(args("config-file rm ca-bundle --scope project -p shop")...)
	require.Equal(t, exitcode.OK, r.code, r.stderr)
	assert.Equal(t, 1, f.called("DELETE "+projectPath+"/config-files/F1"))
}
