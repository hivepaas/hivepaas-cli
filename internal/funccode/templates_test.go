package funccode

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Each runtime's starter code is there, with the file its entrypoint names.
func TestEachRuntimeHasItsStarterCode(t *testing.T) {
	for runtime, entry := range map[string]string{"node24": "index.js", "bun1": "index.ts", "python313": "main.py",
		"go127": "handler.go"} {
		files, entrypoint, err := Template(runtime, false)
		require.NoError(t, err, runtime)
		assert.Empty(t, entrypoint, "the runtime's default: %s", runtime)
		assert.Contains(t, paths(files), entry, runtime)
	}
	files, _, _ := Template("go127", false)
	assert.Contains(t, files[1].Content, "func Handle(ctx context.Context, req *hivepaas.Request)")
	assert.Contains(t, files[1].Content, "\n\tname := req.Query.Get(\"name\")\n", "tabs, as gofmt writes them")

	files, entrypoint, err := Template("node24", true)
	require.NoError(t, err)
	assert.Equal(t, "index.ts", entrypoint)
	assert.Equal(t, []string{"package.json", "index.ts"}, paths(files))
	assert.Contains(t, files[1].Content, "ctx.log(`${req.method} ${req.path}`)")

	_, _, err = Template("bun1", true)
	assert.ErrorContains(t, err, "--typescript is for node24")
	_, _, err = Template("ruby", false)
	assert.ErrorContains(t, err, "node24, bun1, python313, go127")
}

// With no --entrypoint, the runtime's default file is the server's to fill; the
// one index file there is otherwise.
func TestEntrypointFindsTheIndexFile(t *testing.T) {
	ts := []File{{Path: "package.json"}, {Path: "index.ts"}}
	assert.Equal(t, "index.ts", Entrypoint("node24", ts))
	assert.Empty(t, Entrypoint("bun1", ts), "bun1's default")
	assert.Empty(t, Entrypoint("node24", []File{{Path: "index.js"}, {Path: "index.ts"}}))
	assert.Empty(t, Entrypoint("node24", []File{{Path: "index.mjs"}, {Path: "index.ts"}}), "two: the server says")
	assert.Empty(t, Entrypoint("python313", []File{{Path: "app.py"}}))
	assert.Empty(t, Entrypoint("go127", []File{{Path: "handler.go"}}))
}
