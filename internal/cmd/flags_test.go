package cmd

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/hivepaas/hivepaas-cli/internal/exitcode"
)

// Flags that exclude each other are a usage error, whichever command has them.
func TestExclusiveFlagsExit2(t *testing.T) {
	f := newFakeAPI(t)
	for _, line := range []string{
		"deploy --lfs --no-lfs " + shopAPI,
		"app scale --replicas 2 --min 1 " + shopAPI,
	} {
		r := f.run(args(line)...)
		assert.Equal(t, exitcode.Usage, r.code, line)
		assert.True(t, strings.Contains(r.stderr, "none of the others can be"), r.stderr)
	}
}
