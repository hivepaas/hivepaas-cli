//go:build e2e

package e2e

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A secret reaches the container through a variable naming it, and is never
// shown; a config file mounted into it is changed there without a deployment.
func TestSecretsAndConfigFiles(t *testing.T) {
	t.Parallel()
	p := project(t, "secrets")
	app := p.app("conf")
	p.must("app", "create", "conf", "--image", busybox, "--command", `sh -c 'echo "token=$TOKEN"; exec sleep 3600'`)
	path := app.appPath()

	secret := "s3cr3t-" + runID
	app.with(secret).must("secret", "set", "TOKEN")
	ls := app.must("secret", "ls").stdout
	assert.Contains(t, ls, "TOKEN")
	assert.NotContains(t, ls, secret)
	app.must("env", "set", "TOKEN=${secrets.TOKEN}")

	dir := files(t, map[string]string{"conf.txt": "level=info\n"})
	app.in(dir).must("config-file", "push", "app-conf", "conf.txt")
	assert.Contains(t, app.must("config-file", "ls").stdout, "app-conf")
	var configs struct {
		Data []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"data"`
	}
	call(t, "GET", path+"/config-files", nil, &configs)
	require.Len(t, configs.Data, 1)
	call(t, "POST", path+"/setting-mounts", map[string]any{
		"name": "app-conf", "source": map[string]string{"id": configs.Data[0].ID},
		"files": []map[string]string{{"path": "/etc/app/conf.txt", "part": "content"}},
	}, nil)

	app.must("restart")
	eventually(t, deployWithin, func() error {
		return contains(app.run("logs", "--tail", "50").stdout, "token="+secret)
	})
	eventually(t, deployWithin, func() error {
		return contains(app.with("cat /etc/app/conf.txt\nexit\n").run("exec").stdout, "level=info")
	})

	require.NoError(t, os.WriteFile(filepath.Join(dir, "conf.txt"), []byte("level=debug\n"), 0o600))
	app.in(dir).must("config-file", "push", "app-conf", "conf.txt")
	eventually(t, 2*time.Minute, func() error {
		return contains(app.with("cat /etc/app/conf.txt\nexit\n").run("exec").stdout, "level=debug")
	})
	assert.Equal(t, "level=debug\n", app.must("config-file", "pull", "app-conf", "-").stdout)
}

// A project's secret and an environment's config file reach the apps that use
// them, which list them as inherited. One kept from the apps is not theirs to
// use.
func TestSecretsAndConfigFilesOfAProjectAndAnEnvironment(t *testing.T) {
	t.Parallel()
	p := project(t, "shared")
	app := p.app("shared")
	p.must("app", "create", "shared", "--image", busybox, "--command", `sh -c 'echo "token=$TOKEN"; exec sleep 3600'`)

	secret := "shared-" + runID
	p.with(secret).must("secret", "set", "TOKEN", "--scope", "project")
	p.must("secret", "set", "KEPT=x", "--scope", "project", "--no-inheritable")
	ls := p.must("secret", "ls", "--scope", "project").stdout
	assert.Regexp(t, `TOKEN\s+\S+ B\s+text\s+yes\s+this project`, ls)
	assert.Regexp(t, `KEPT\s+\S+ B\s+text\s+no\s+this project`, ls)
	assert.NotContains(t, ls, secret)
	own := app.must("secret", "ls").stdout
	assert.Regexp(t, `TOKEN\s+.*inherited`, own)
	assert.NotContains(t, own, "KEPT")
	app.must("env", "set", "TOKEN=${secrets.TOKEN}")
	assert.NotZero(t, app.run("env", "set", "OTHER=${secrets.KEPT}").code, "a secret kept from the apps")

	dir := files(t, map[string]string{"shared.conf": "region=eu\n"})
	p.in(dir).must("config-file", "push", "shared-conf", "shared.conf", "--scope", "env")
	assert.Regexp(t, `shared-conf\s+.*inherited`, app.must("config-file", "ls").stdout)
	assert.Equal(t, "region=eu\n", app.must("config-file", "pull", "shared-conf", "-").stdout)

	app.must("restart")
	eventually(t, deployWithin, func() error {
		return contains(app.run("logs", "--tail", "50").stdout, "token="+secret)
	})

	p.must("secret", "rm", "KEPT", "--scope", "project")
	p.must("config-file", "rm", "shared-conf", "--scope", "env")
	assert.NotContains(t, app.must("config-file", "ls").stdout, "shared-conf")
	assert.NotContains(t, p.must("secret", "ls", "--scope", "project").stdout, "KEPT")
}
