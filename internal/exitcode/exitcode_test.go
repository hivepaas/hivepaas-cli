package exitcode

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

type coded struct{ code int }

func (c coded) Error() string { return "coded" }
func (c coded) ExitCode() int { return c.code }

func TestOf(t *testing.T) {
	assert.Equal(t, OK, Of(nil))
	assert.Equal(t, Failure, Of(errors.New("plain")))
	assert.Equal(t, NotFound, Of(New(NotFound, "no app %q", "x")))
	assert.Equal(t, Auth, Of(fmt.Errorf("wrapped: %w", coded{Auth})))
	assert.Equal(t, Usage, Of(Wrap(Usage, coded{Auth})), "the outermost code is the one")
	assert.NoError(t, Wrap(Usage, nil))
}

func TestReported(t *testing.T) {
	err := Reported(Deployment)
	assert.True(t, IsReported(err))
	assert.True(t, IsReported(fmt.Errorf("deploying: %w", err)))
	assert.Equal(t, Deployment, Of(err))
	assert.False(t, IsReported(New(Deployment, "failed")))
}
