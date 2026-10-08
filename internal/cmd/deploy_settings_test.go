package cmd

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hivepaas/hivepaas-cli/internal/exitcode"
)

func TestDeploySettings(t *testing.T) {
	f := newFakeAPI(t)
	f.json("GET "+appPath+"/deployment-settings", http.StatusOK, repoSettings)

	r := f.run(args("deploy settings " + shopAPI)...)

	require.Equal(t, exitcode.OK, r.code, r.stderr)
	assert.Equal(t, "Method:              repository\n"+
		"Repository:          https://github.com/acme/api.git\n"+
		"Ref:                 main\n"+
		"Commit:              follows main\n"+
		"Git credential:      acme-app\n"+
		"Dockerfile:          Dockerfile in the repository\n"+
		"Submodules:          off\n"+
		"LFS:                 off\n"+
		"Push to:             ghcr\n"+
		"Auto-deploy:         on\n"+
		"Command:             serve\n", r.stdout)

	r = f.run(args("deploy settings -o json " + shopAPI)...)
	require.Equal(t, exitcode.OK, r.code, r.stderr)
	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(r.stdout), &got), "the API's data")
	assert.Equal(t, "repo", got["activeMethod"])
}
