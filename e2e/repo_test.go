//go:build e2e

package e2e

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The repository the dashboard's env/up.sh serves inside dind, where the backend
// reaches it: main has two commits, each a Dockerfile printing which it is;
// develop is at the first. Their hashes are these on every env.
const (
	shopRepo     = "git://127.0.0.1/e2e/shop.git"
	firstCommit  = "2010c034663e785fac6e0d81eb55232d5abfcc0b"
	secondCommit = "860f1025bb11e0171549d86ac75c002cad5caa56"
)

type deployment struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Output struct {
		CommitHash string `json:"commitHash"`
	} `json:"output"`
}

// latestDeployment is the app's newest deployment.
func latestDeployment(c cli) deployment {
	c.t.Helper()
	deployments := jsonOf[[]deployment](c.t, c.must("deploy", "ls", "-o", "json"))
	require.NotEmpty(c.t, deployments)
	return deployments[0]
}

// An app built from a repository: at a commit, then following its branch, then
// another branch - each deployment built from the commit it names.
func TestAnAppFromARepository(t *testing.T) {
	t.Parallel()
	p := project(t, "repo")
	shop := p.app("shop")

	p.must("app", "create", "shop", "--repo", shopRepo, "--ref", "main", "--commit", firstCommit,
		"--dockerfile", "Dockerfile")
	assert.Equal(t, firstCommit, latestDeployment(shop).Output.CommitHash)
	eventually(t, deployWithin, func() error {
		return contains(shop.run("logs", "--tail", "50").stdout, "built-from-commit-1")
	})

	shop.must("deploy", "--commit", "none")
	assert.Equal(t, secondCommit, latestDeployment(shop).Output.CommitHash, "main's head")
	eventually(t, deployWithin, func() error {
		return contains(shop.run("logs", "--tail", "50").stdout, "built-from-commit-2")
	})

	shop.must("deploy", "--ref", "develop")
	latest := latestDeployment(shop)
	assert.Equal(t, firstCommit, latest.Output.CommitHash, "develop's head")
	assert.Equal(t, "done", latest.Status)
	assert.Contains(t, shop.must("deploy", "settings").stdout, "develop")
}

// A preview is refused while the app's previews are off; on, it builds the
// branch the app deploys from, runs as an app of its own, and is removed.
func TestAPreview(t *testing.T) {
	t.Parallel()
	p := project(t, "preview")
	shop := p.app("shop")
	p.must("app", "create", "shop", "--repo", shopRepo, "--ref", "main", "--commit", firstCommit,
		"--dockerfile", "Dockerfile")
	path := shop.appPath()

	previewsOn(t, path, false)
	r := shop.run("preview", "create", "--ref", "develop")
	assert.Equal(t, 6, r.code, r.String())
	assert.Contains(t, r.stderr, "Feature Settings")
	previewsOn(t, path, true)

	// No --ref: the branch the app deploys from, main, at its head.
	shop.must("preview", "create")
	previews := jsonOf[[]struct {
		Name string `json:"name"`
	}](t, shop.must("preview", "ls", "-o", "json"))
	require.Len(t, previews, 1)
	preview := p.app(previews[0].Name)
	eventually(t, deployWithin, func() error {
		return contains(preview.run("logs", "--tail", "50").stdout, "built-from-commit-2")
	})

	shop.must("preview", "rm", previews[0].Name, "--yes")
	left := jsonOf[[]struct {
		Name string `json:"name"`
	}](t, shop.must("preview", "ls", "-o", "json"))
	assert.Empty(t, left, fmt.Sprint(left))
}

// previewsOn turns the app's previews on or off, as its Feature Settings do.
func previewsOn(t *testing.T, path string, on bool) {
	t.Helper()
	var features struct {
		Data struct {
			UpdateVer int `json:"updateVer"`
		} `json:"data"`
	}
	call(t, http.MethodGet, path+"/feature-settings", nil, &features)
	call(t, http.MethodPut, path+"/feature-settings", map[string]any{"updateVer": features.Data.UpdateVer,
		"previewSettings": map[string]any{"enabled": on, "appsToClone": []any{}, "commands": []any{}}}, nil)
}
