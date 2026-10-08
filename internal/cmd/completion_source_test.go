package cmd

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestTabCompletesCredentials(t *testing.T) {
	f := newFakeAPI(t)
	f.json("GET /api/projects/P1/production/registry-auth", http.StatusOK,
		`{"data":[{"id":"R1","name":"ghcr","kind":"generic","address":"ghcr.io"}]}`)
	f.json("GET /api/projects/P1/production/git-credentials", http.StatusOK,
		`{"data":[{"id":"G2","name":"deploy-key","kind":"ssh-key"}]}`)

	r := f.run("__complete", "deploy", "-p", "shop", "-e", "production", "--push-to", "")
	assert.True(t, strings.HasPrefix(r.stdout, "none\tno credential\nghcr\tgeneric, ghcr.io\n:4\n"), r.stdout)

	r = f.run("__complete", "deploy", "-p", "shop", "-e", "production", "--git-credential", "")
	assert.True(t, strings.HasPrefix(r.stdout, "none\tno credential\ndeploy-key\tssh-key\n:4\n"), r.stdout)

	r = f.run("__complete", "deploy", "--use", "")
	assert.True(t, strings.HasPrefix(r.stdout, "image\nrepo\n:4\n"), r.stdout)
}
