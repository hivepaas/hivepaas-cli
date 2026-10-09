package funccode

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hivepaas/hivepaas-cli/internal/exitcode"
)

func write(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for path, content := range files {
		full := filepath.Join(dir, filepath.FromSlash(path))
		require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
		require.NoError(t, os.WriteFile(full, []byte(content), 0o644))
	}
}

func paths(files []File) []string {
	out := make([]string, 0, len(files))
	for _, f := range files {
		out = append(out, f.Path)
	}
	return out
}

// A directory's code is its files, by path, but what a function never sends -
// libraries, git's, HivePaaS's own - and what its .gitignore ignores.
func TestCollectSendsTheCodeAndLeavesTheRest(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, map[string]string{
		"index.js": "export default () => ({})", "package.json": "{}", "lib/util.js": "x",
		".gitignore": "dist/\n*.log\n", "dist/out.js": "x", "app.log": "x",
		"node_modules/x/index.js": "x", ".git/config": "x", "__pycache__/a.pyc": "x",
		".venv/bin/python": "x", "venv/bin/python": "x", ".hivepaas/Dockerfile": "x",
		".hivepaas.json": "{}", ".DS_Store": "x", "sub/.DS_Store": "x",
	})
	require.NoError(t, os.Symlink(filepath.Join(dir, "index.js"), filepath.Join(dir, "link.js")))

	code, err := Collect(dir)

	require.NoError(t, err)
	assert.Equal(t, []string{".gitignore", "index.js", "lib/util.js", "package.json"}, paths(code.Files))
	assert.Equal(t, []string{"link.js"}, code.Links)
	assert.Equal(t, "export default () => ({})", code.Files[1].Content)
	assert.Equal(t, int64(len("dist/\n*.log\n")+len("export default () => ({})")+1+2), code.Size)
}

// What the server would refuse is said before anything is sent.
func TestCollectRefusesWhatTheServerWould(t *testing.T) {
	many := t.TempDir()
	files := map[string]string{}
	for i := range 101 {
		files[fmt.Sprintf("f/%03d.js", i)] = "x"
	}
	write(t, many, files)
	_, err := Collect(many)
	assert.Equal(t, exitcode.Invalid, exitcode.Of(err))
	assert.ErrorContains(t, err, "101 files")

	big := t.TempDir()
	write(t, big, map[string]string{"data.json": strings.Repeat("x", 900<<10), "more.json": strings.Repeat("y", 200<<10),
		"index.js": "x"})
	_, err = Collect(big)
	assert.Equal(t, exitcode.Invalid, exitcode.Of(err))
	assert.ErrorContains(t, err, "more than the 1 MB")
	assert.ErrorContains(t, err, "data.json (900 KB), more.json (200 KB)")

	binary := t.TempDir()
	write(t, binary, map[string]string{"index.js": "x", "logo.png": "\x89PNG\r\n\x1a\n\xff\xfe"})
	_, err = Collect(binary)
	assert.Equal(t, exitcode.Invalid, exitcode.Of(err))
	assert.ErrorContains(t, err, "logo.png is not text")

	spaced := t.TempDir()
	write(t, spaced, map[string]string{"index.js": "x", "my file.js": "x"})
	_, err = Collect(spaced)
	assert.Equal(t, exitcode.Invalid, exitcode.Of(err))
	assert.ErrorContains(t, err, `"my file.js"`)

	_, err = Collect(t.TempDir())
	assert.Equal(t, exitcode.Invalid, exitcode.Of(err))
	assert.ErrorContains(t, err, "no code")

	_, err = Collect(filepath.Join(t.TempDir(), "missing"))
	assert.Equal(t, exitcode.Usage, exitcode.Of(err))
}

// A file with a NUL byte is not text, as the server takes it; a .env is not
// code - a function's variables are its app's - and is left out, said.
func TestCollectLeavesOutDotEnvAndRefusesNUL(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, map[string]string{"index.js": "x", ".env": "SECRET=1", ".env.local": "SECRET=2",
		"lib/.env": "SECRET=3"})
	code, err := Collect(dir)
	require.NoError(t, err)
	assert.Equal(t, []string{"index.js"}, paths(code.Files))
	assert.Equal(t, []string{".env", ".env.local", "lib/.env"}, code.LeftOut)

	nul := t.TempDir()
	write(t, nul, map[string]string{"index.js": "x", "data.txt": "a\x00b"})
	_, err = Collect(nul)
	assert.Equal(t, exitcode.Invalid, exitcode.Of(err))
	assert.ErrorContains(t, err, "data.txt is not text")
}
