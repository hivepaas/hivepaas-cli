package funccode

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// A .gitignore is read as git reads one at the root of a repository.
func TestIgnoreReadsAGitignore(t *testing.T) {
	ig := ParseIgnore(`
# built
dist/
*.log
!keep.log
/secret.txt
docs/*.md
**/fixtures
a/**/b
\#hash
`)
	for path, dir := range map[string]bool{
		"dist": true, "src/dist": true, "app.log": false, "src/deep/app.log": false,
		"secret.txt": false, "docs/readme.md": false, "fixtures": true, "src/fixtures": true,
		"a/b": true, "a/x/y/b": true, "#hash": false,
	} {
		assert.True(t, ig.Match(path, dir), "%s is ignored", path)
	}
	for path, dir := range map[string]bool{
		"dist": false, "keep.log": false, "src/secret.txt": false, "docs/sub/readme.md": false,
		"src/index.js": false, "logs": true, "hash": false,
	} {
		assert.False(t, ig.Match(path, dir), "%s is kept", path)
	}
}

func TestIgnoreOfNothingIgnoresNothing(t *testing.T) {
	assert.False(t, ParseIgnore("").Match("index.js", false))
	var ig *Ignore
	assert.False(t, ig.Match("index.js", false))
}

// A backslash makes the character after it itself: \*.txt is the file *.txt.
func TestIgnoreEscapes(t *testing.T) {
	ig := ParseIgnore("\\*.txt\n")
	assert.True(t, ig.Match("*.txt", false))
	assert.False(t, ig.Match("a.txt", false))
}
