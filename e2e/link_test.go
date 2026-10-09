//go:build e2e

package e2e

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A directory linked to an app runs the app's commands without flags, and so
// does one below it; unlinked, it names no app.
func TestALinkedDirectory(t *testing.T) {
	t.Parallel()
	p := project(t, "link")
	p.must("app", "create", "web", "--image", busybox, "--command", "sh -c 'echo ready-one; exec sleep 3600'")
	dir := t.TempDir()
	here := cli{t: t}.in(dir)

	here.must("link", "-p", p.projectName(), "-e", "production", "-a", "web")
	here.must("deploy", "--command", "sh -c 'echo ready-two; exec sleep 3600'")
	eventually(t, deployWithin, func() error { return contains(here.run("logs", "--tail", "50").stdout, "ready-two") })
	below := filepath.Join(dir, "src", "deep")
	require.NoError(t, os.MkdirAll(below, 0o755))
	assert.Contains(t, here.in(below).must("deploy", "settings").stdout, busybox)

	here.must("unlink")
	r := here.run("ps")
	assert.Equal(t, 2, r.code, r.String())
	assert.Contains(t, r.stderr, "link")
}
