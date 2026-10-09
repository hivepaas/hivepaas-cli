//go:build e2e

package e2e

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// What is set is taken away again - a variable, a secret from a file, a config
// file, a domain, an environment, an app with its data - and nothing else goes
// with it.
func TestTakingAway(t *testing.T) {
	t.Parallel()
	p := project(t, "edits")
	web := p.app("web")
	p.must("app", "create", "web", "--image", busybox, "--command", "sleep 3600")
	dir := files(t, map[string]string{"cert.pem": "-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n",
		"nginx.conf": "worker_processes 1;\n"})
	here := web.in(dir)

	web.must("env", "set", "KEEP=1", "DROP=2")
	web.must("env", "unset", "DROP")
	here.must("env", "pull", "app.env")
	pulled, err := os.ReadFile(filepath.Join(dir, "app.env"))
	require.NoError(t, err)
	assert.Contains(t, string(pulled), "KEEP=1")
	assert.NotContains(t, string(pulled), "DROP")

	here.must("secret", "set", "CERT", "--file", "cert.pem")
	here.must("secret", "set", "OTHER=x")
	assert.Contains(t, web.must("secret", "ls").stdout, "CERT")
	web.must("secret", "rm", "CERT")
	secrets := web.must("secret", "ls").stdout
	assert.NotContains(t, secrets, "CERT")
	assert.Contains(t, secrets, "OTHER")

	here.must("config-file", "push", "nginx", "nginx.conf")
	web.must("config-file", "rm", "nginx")
	assert.NotContains(t, web.must("config-file", "ls").stdout, "nginx")

	domain := "edits-" + runID + ".localhost"
	web.must("domain", "add", domain, "--port", "80", "--no-force-https")
	assert.Contains(t, web.must("open", "--print").stdout, domain)
	web.must("domain", "rm", domain)
	assert.NotContains(t, web.must("domain", "ls").stdout, domain)

	p.must("project", "env", "add", "staging")
	assert.Contains(t, p.must("project", "get", p.projectName()).stdout, "staging")
	p.must("project", "env", "rm", "staging", "--yes")
	assert.NotContains(t, p.must("project", "get", p.projectName()).stdout, "staging")

	web.must("app", "delete", "--yes", "--remove-storage")
	assert.NotContains(t, p.must("app", "ls").stdout, "web")
}
