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
