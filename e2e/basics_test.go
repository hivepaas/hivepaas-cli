//go:build e2e

package e2e

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// Who the key acts as and where, what the installation's state is, and how
// the CLI exits when it cannot go on.
func TestAskingAndFailing(t *testing.T) {
	t.Parallel()
	c := cli{t: t}

	assert.Contains(t, c.must("whoami").stdout, env.username)
	assert.Contains(t, c.must("version").stdout, "API level")
	if r := c.run("status"); r.code != 0 {
		t.Errorf("status:\n%s", r)
	}

	p := project(t, "basics")
	r := p.run("app", "get", "nothing-here")
	assert.Equal(t, 5, r.code, "an app that is not there\n%s", r)
	r = p.run("app", "create", "x", "--replicas", "2")
	assert.Equal(t, 2, r.code, "a flag app create does not take\n%s", r)
	r = cli{t: t}.run("project", "delete", p.projectName())
	assert.Equal(t, 2, r.code, "no terminal to ask at, and no --yes\n%s", r)
	assert.Contains(t, r.stderr, "--yes")
	p.must("project", "get", p.projectName())
}
