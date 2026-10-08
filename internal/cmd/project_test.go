package cmd

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hivepaas/hivepaas-cli/internal/exitcode"
)

func TestProjectCreate(t *testing.T) {
	f := newFakeAPI(t)
	f.json("POST /api/projects", http.StatusCreated, `{"data":{"id":"P2"}}`)

	r := f.run("project", "create", "blog")
	require.Equal(t, exitcode.OK, r.code, r.stderr)
	assert.Equal(t, map[string]any{"name": "blog", "note": "", "status": "active", "tags": []any{},
		"owner": map[string]any{"id": ""}, "envs": []any{
			map[string]any{"name": "development", "color": "#a855f7"},
			map[string]any{"name": "production", "color": "#84cc16"},
		}}, f.body("POST /api/projects", 0),
		"the envs the dashboard's form starts with; the server gives it to the key's user")
	assert.Contains(t, r.stderr, "Created project blog, with development, production.")

	r = f.run("project", "create", "shop2", "--envs", "staging,production")
	require.Equal(t, exitcode.OK, r.code, r.stderr)
	assert.Equal(t, []any{map[string]any{"name": "staging", "color": "#64748b"},
		map[string]any{"name": "production", "color": "#84cc16"}}, f.body("POST /api/projects", 1)["envs"])
}

// An env is added by writing the project back with it: everything else as it
// was, its owner too.
func TestProjectEnvAddAndRm(t *testing.T) {
	f := newFakeAPI(t)
	f.json("GET /api/projects/P1", http.StatusOK, `{"data":{"id":"P1","key":"shop","name":"shop","note":"the shop",
		"status":"active","tags":["web"],"owner":{"id":"U1","username":"tien"},"updateVer":6,
		"envs":[{"id":"E1","name":"production","color":"#84cc16"}]}}`)
	f.json("PUT /api/projects/P1", http.StatusOK, `{"meta":null}`)
	f.json("DELETE /api/projects/P1/production", http.StatusOK, `{"meta":null}`)

	r := f.run(args("project env add staging -p shop")...)
	require.Equal(t, exitcode.OK, r.code, r.stderr)
	assert.Equal(t, map[string]any{"name": "shop", "note": "the shop", "status": "active", "tags": []any{"web"},
		"owner": map[string]any{"id": "U1"}, "updateVer": float64(6), "envs": []any{
			map[string]any{"name": "production", "color": "#84cc16"},
			map[string]any{"name": "staging", "color": "#64748b"},
		}}, f.body("PUT /api/projects/P1", 0))

	r = f.run(args("project env add production -p shop")...)
	assert.Equal(t, exitcode.Invalid, r.code)

	r = f.run(args("project env rm production -p shop")...)
	assert.Equal(t, exitcode.Usage, r.code, "a script says --yes")
	assert.Zero(t, f.called("DELETE /api/projects/P1/production"))

	r = f.run(args("project env rm production -p shop --yes --remove-storage")...)
	require.Equal(t, exitcode.OK, r.code, r.stderr)
	assert.Contains(t, r.stderr, "Deleted environment production of shop, with its apps, and the data on the volumes.")
}

func TestProjectDelete(t *testing.T) {
	f := newFakeAPI(t)
	f.handle("DELETE /api/projects/P1", func(w http.ResponseWriter, r *http.Request, _ int) {
		assert.Equal(t, "false", r.URL.Query().Get("removeStorage"))
		_, _ = w.Write([]byte(`{"meta":null}`))
	})

	r := f.run(args("project delete shop")...)
	assert.Equal(t, exitcode.Usage, r.code)

	r = f.run(args("project delete shop --yes")...)
	require.Equal(t, exitcode.OK, r.code, r.stderr)
	assert.Contains(t, r.stderr, "Deleted project shop, with production and their apps.")
}
