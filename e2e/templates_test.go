//go:build e2e

package e2e

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// A template from the store deployed: its volume named by a parameter, its app
// made, deployed and running what the template asks.
func TestATemplate(t *testing.T) {
	t.Parallel()
	// Made before the project, so removed after it: the app's data is on it.
	name := "e2e-template-data-" + runID
	volume(t, name, "")
	p := project(t, "template")

	assert.Contains(t, p.must("template", "ls", "--search", "redis").stdout, "redis")
	p.must("template", "deploy", "redis", "--name", "cache", "--param", "dataVolume="+name)

	cache := p.app("cache")
	assert.Contains(t, p.must("app", "ls").stdout, "cache")
	eventually(t, deployWithin, func() error {
		return contains(cache.run("logs", "--tail", "100").stdout, "Ready to accept connections")
	})
}
