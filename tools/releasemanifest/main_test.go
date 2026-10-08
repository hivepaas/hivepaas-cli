package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hivepaas/hivepaas-cli/internal/selfupdate"
)

// checksums is a goreleaser checksums.txt for version, without the platforms
// left out.
func checksums(t *testing.T, version string, leaveOut ...string) string {
	t.Helper()
	v, err := selfupdate.ParseVersion(version)
	require.NoError(t, err)
	var b strings.Builder
	for i, platform := range platforms {
		if !slices.Contains(leaveOut, platform) {
			fmt.Fprintf(&b, "%064x  %s\n", i+1, selfupdate.ArchiveFile(v, platform))
		}
	}
	path := filepath.Join(t.TempDir(), "checksums.txt")
	require.NoError(t, os.WriteFile(path, []byte(b.String()), 0o600))
	return path
}

func written(t *testing.T, file string) *selfupdate.Manifest {
	t.Helper()
	data, err := os.ReadFile(file)
	require.NoError(t, err)
	m, err := selfupdate.ParseManifest(data)
	require.NoError(t, err)
	return m
}

func TestAddReleases(t *testing.T) {
	file := filepath.Join(t.TempDir(), "release.json")

	require.NoError(t, run("v0.2.0", checksums(t, "v0.2.0"), "2026-10-20", file))
	m := written(t, file)
	assert.Equal(t, map[string]string{"stable": "v0.2.0"}, m.Channels)
	assert.Len(t, m.Releases["v0.2.0"].Archives, len(platforms))
	assert.Equal(t, selfupdate.Archive{File: "hivepaas_0.2.0_windows_arm64.zip", SHA256: fmt.Sprintf("%064x", 6)},
		m.Releases["v0.2.0"].Archives["windows_arm64"])

	require.NoError(t, run("v0.3.0-beta1", checksums(t, "v0.3.0-beta1"), "2026-10-25", file))
	m = written(t, file)
	assert.Equal(t, map[string]string{"stable": "v0.2.0", "beta": "v0.3.0-beta1"}, m.Channels)
	assert.Len(t, m.Releases, 2, "releases stay in the list")

	content, err := os.ReadFile(file)
	require.NoError(t, err)
	assert.True(t, strings.HasSuffix(string(content), "}\n"))
	assert.Contains(t, string(content), "\n  \"channels\": {\n", "indented, for review")
}

func TestAddRefuses(t *testing.T) {
	file := filepath.Join(t.TempDir(), "release.json")
	require.NoError(t, run("v0.3.0", checksums(t, "v0.3.0"), "2026-10-20", file))

	err := run("v0.2.1", checksums(t, "v0.2.1"), "2026-10-21", file)
	require.ErrorContains(t, err, "the stable channel is at v0.3.0, newer than v0.2.1")

	err = run("v0.4.0", checksums(t, "v0.4.0", "linux_arm64"), "2026-10-21", file)
	require.ErrorContains(t, err, "checksums.txt has no hivepaas_0.4.0_linux_arm64.tar.gz")

	err = run("v0.4.0", checksums(t, "v0.3.0"), "2026-10-21", file)
	require.ErrorContains(t, err, "is it the checksums.txt of v0.4.0?")

	require.Error(t, run("0.4.0", checksums(t, "v0.4.0"), "2026-10-21", file), "a tag has its v")
	require.Error(t, run("v0.4.0", checksums(t, "v0.4.0"), "21/10/2026", file))

	assert.Equal(t, map[string]string{"stable": "v0.3.0"}, written(t, file).Channels, "nothing was written")
}
