//go:build e2e

package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A function begun from the starter code, made and served at its domain, its
// code pulled back; changed, called once before it is deployed, then deployed.
func TestAFunction(t *testing.T) {
	t.Parallel()
	p := project(t, "function")
	dir := t.TempDir()
	code := filepath.Join(dir, "hello")
	p.in(dir).must("function", "init", "hello", "--runtime", "node24")

	domain := "hello-" + runID + ".localhost"
	page := "https://" + domain + "/?name=Grace"
	p.in(dir).must("function", "create", "hello", "hello", "--runtime", "node24", "--domain", domain)
	assert.Contains(t, p.must("function", "ls").stdout, "hello")
	eventually(t, deployWithin, func() error { return answers(page, `"hello":"Grace"`) })

	fn := p.app("hello")
	assert.Contains(t, fn.must("function", "get").stdout, "node24")
	fn.must("function", "metrics")
	pulled := t.TempDir()
	fn.in(pulled).must("function", "pull", ".")
	for _, name := range []string{"index.js", "package.json"} {
		want, err := os.ReadFile(filepath.Join(code, name))
		require.NoError(t, err)
		got, err := os.ReadFile(filepath.Join(pulled, name))
		require.NoError(t, err, name)
		assert.Equal(t, string(want), string(got), name)
	}

	index := filepath.Join(code, "index.js")
	source, err := os.ReadFile(index)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(index, []byte(strings.Replace(string(source), "hello: name", "hi: name", 1)),
		0o600))
	r := fn.in(code).must("function", "run", "--query", "name=Ada")
	assert.Contains(t, r.stdout, `"hi":"Ada"`)
	require.NoError(t, answers(page, `"hello":"Grace"`), "the deployed function, unchanged by a run")

	fn.in(code).must("function", "deploy")
	eventually(t, deployWithin, func() error { return answers(page, `"hi":"Grace"`) })
}
