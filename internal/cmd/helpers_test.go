package cmd

import (
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hivepaas/hivepaas-cli/internal/api"
	"github.com/hivepaas/hivepaas-cli/internal/exitcode"
)

func TestImageChange(t *testing.T) {
	assert.Equal(t, "ghcr.io/acme/api:1.4.2 -> 1.4.3", imageChange("ghcr.io/acme/api:1.4.2", "ghcr.io/acme/api:1.4.3"))
	assert.Equal(t, "nginx:1.30 -> redis:8", imageChange("nginx:1.30", "redis:8"))
	assert.Equal(t, "reg:5000/api -> reg:5000/api:2", imageChange("reg:5000/api", "reg:5000/api:2"),
		"a registry's port is not a tag")
	assert.Equal(t, "api@sha256:ab -> api:2", imageChange("api@sha256:ab", "api:2"))
}

func TestShellWord(t *testing.T) {
	assert.Equal(t, "shop", shellWord("shop"))
	assert.Equal(t, "my_app-2", shellWord("my_app-2"))
	assert.Equal(t, "'Project A'", shellWord("Project A"))
	assert.Equal(t, `'it'\''s'`, shellWord("it's"))
	assert.Equal(t, "''", shellWord(""))
}

func TestSelectionFlagsLeaveOutWhatTheLinkNames(t *testing.T) {
	sel := &selection{
		Project: &api.ProjectdtoProjectResp{Key: "shop", Name: "Shop"},
		Env:     "production",
		App:     &api.AppdtoAppResp{Key: "api", Name: "API"},
	}
	assert.Equal(t, " -p shop -e production -a api", sel.flags())
	sel.linked.project, sel.linked.env = true, true
	assert.Equal(t, " -a api", sel.flags())
	sel.linked.app = true
	assert.Empty(t, sel.flags())
}

func TestKeyValues(t *testing.T) {
	pairs, err := keyValues([]string{"A=1", "B=x=y", "A=2", "C="})
	require.NoError(t, err)
	assert.Equal(t, [][2]string{{"B", "x=y"}, {"A", "2"}, {"C", ""}}, pairs)

	_, err = keyValues([]string{"=1"})
	assert.Equal(t, exitcode.Usage, exitcode.Of(err))
	_, err = keyValues([]string{"NOVALUE"})
	assert.Equal(t, exitcode.Usage, exitcode.Of(err))
}

func TestLogsQuery(t *testing.T) {
	q, err := logsQuery(logsFlags{follow: true, since: "10m", tail: 50})
	require.NoError(t, err)
	assert.Equal(t, url.Values{"follow": {"true"}, "duration": {"10m"}, "tail": {"50"}}, q)

	q, err = logsQuery(logsFlags{since: "2026-10-08T21:00:00+07:00"})
	require.NoError(t, err)
	assert.Equal(t, url.Values{"since": {"2026-10-08T14:00:00Z"}}, q)

	for _, since := range []string{"2d", "1h30m"} {
		q, err = logsQuery(logsFlags{since: since})
		require.NoError(t, err)
		assert.Equal(t, since, q.Get("duration"))
	}
	_, err = logsQuery(logsFlags{since: "yesterday"})
	assert.Equal(t, exitcode.Usage, exitcode.Of(err))
	_, err = logsQuery(logsFlags{tail: -1})
	assert.Equal(t, exitcode.Usage, exitcode.Of(err))
}

func TestEnvRows(t *testing.T) {
	vars := &api.AppsettingsdtoEnvVarsResp{
		RuntimeEnvVars: &[]api.BasedtoEnvVarResp{
			{Key: "HIVEPAAS_APP_NAME", Value: "api", IsSystem: ptr(true)}, {Key: "LOG_LEVEL", Value: "info"},
		},
		BuildtimeEnvVars:        &[]api.BasedtoEnvVarResp{{Key: "NODE_ENV", Value: "production"}},
		InheritedRuntimeEnvVars: &[]api.BasedtoEnvVarResp{{Key: "HIVEPAAS_PROJECT_NAME", Value: "shop"}},
	}
	assert.Equal(t, [][]string{{"runtime", "LOG_LEVEL", "info"}, {"build", "NODE_ENV", "production"}},
		envRows(vars, "", false))
	assert.Equal(t, [][]string{{"build", "NODE_ENV", "production"}}, envRows(vars, kindBuild, false))
	assert.Equal(t, [][]string{
		{"runtime", "HIVEPAAS_APP_NAME", "api", "hivepaas"},
		{"runtime", "LOG_LEVEL", "info", "app"},
		{"runtime", "HIVEPAAS_PROJECT_NAME", "shop", "inherited"},
		{"build", "NODE_ENV", "production", "app"},
	}, envRows(vars, "", true))
}
