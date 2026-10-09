//go:build e2e

package e2e

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// A compose file made a project: refused without a terminal while a variable
// has no value, then made with --var - the file it reads sent with it - and
// its service deployed.
func TestComposeUp(t *testing.T) {
	t.Parallel()
	name := "e2e-compose-" + runID
	dir := files(t, map[string]string{
		"compose.yaml": "name: " + name + `
services:
  web:
    image: ` + busybox + `
    command: ["sh", "-c", "echo ready-$$GREETING-$$LEVEL; exec sleep 3600"]
    env_file: app.env
    environment:
      GREETING: ${GREETING:?}
`,
		"app.env": "LEVEL=info\n",
	})
	c := cli{t: t}.in(dir)
	t.Cleanup(func() {
		if r := c.run("project", "delete", name, "--yes", "--remove-storage"); r.code != 0 && r.code != 5 {
			t.Logf("deleting project %s:\n%s", name, r)
		}
	})

	r := c.run("compose", "up", "--yes")
	assert.Equal(t, 2, r.code, r.String())
	assert.Contains(t, r.stderr, "GREETING")
	assert.Equal(t, 5, c.run("project", "get", name).code, "nothing made")

	r = c.must("compose", "up", "--var", "GREETING=hi", "--yes")
	assert.Contains(t, r.stderr, "web")
	web := cli{t: t, flags: []string{"-p", name, "-e", "production", "-a", "web"}}
	assert.Contains(t, web.must("app", "ls").stdout, "web")
	eventually(t, deployWithin, func() error {
		return contains(web.run("logs", "--tail", "50").stdout, "ready-hi-info")
	})
}
