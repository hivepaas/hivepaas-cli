package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hivepaas/hivepaas-cli/internal/exitcode"
	"github.com/hivepaas/hivepaas-cli/internal/selfupdate"
	"github.com/hivepaas/hivepaas-cli/internal/selfupdate/selfupdatetest"
	"github.com/hivepaas/hivepaas-cli/internal/version"
)

// releases publishes v0.1.0, v0.2.0 and v0.3.0, stable, and v0.4.0-beta1.
type releases struct {
	t      *testing.T
	pub    *selfupdatetest.Publication
	keys   selfupdate.Keys
	binary string // the installed binary, which update replaces
}

func newReleases(t *testing.T) *releases {
	t.Helper()
	signer := selfupdatetest.NewSigner(t)
	pub := selfupdatetest.Publish(t, signer, map[string]string{"stable": "v0.3.0", "beta": "v0.4.0-beta1"},
		map[string][]byte{
			"v0.1.0": []byte("hivepaas 0.1.0"), "v0.2.0": []byte("hivepaas 0.2.0"),
			"v0.3.0": []byte("hivepaas 0.3.0"), "v0.4.0-beta1": []byte("hivepaas 0.4.0-beta1"),
		})
	binary := filepath.Join(t.TempDir(), "bin", "hivepaas")
	require.NoError(t, os.MkdirAll(filepath.Dir(binary), 0o755))
	require.NoError(t, os.WriteFile(binary, []byte("hivepaas installed"), 0o755))
	return &releases{t: t, pub: pub, keys: signer.Keys(t), binary: binary}
}

// run runs the CLI as version current, installed at r.binary.
func (r *releases) run(current string, args ...string) result {
	r.t.Helper()
	was := version.Version
	version.Version = current
	defer func() { version.Version = was }()
	env := map[string]string{"HIVEPAAS_CONFIG_DIR": r.t.TempDir()}
	var stdout, stderr bytes.Buffer
	a := &App{
		stdin: strings.NewReader(""), stdout: &stdout, stderr: &stderr,
		getenv:     func(k string) string { return env[k] },
		newUpdater: func() (*selfupdate.Updater, error) { return r.pub.Updater(r.keys), nil },
		executable: func() (string, error) { return r.binary, nil },
	}
	code := a.Run(context.Background(), args)
	return result{stdout.String(), stderr.String(), code}
}

func (r *releases) installed() string {
	r.t.Helper()
	content, err := os.ReadFile(r.binary)
	require.NoError(r.t, err)
	return string(content)
}

func TestUpdateInstallsTheNewestRelease(t *testing.T) {
	r := newReleases(t)

	res := r.run("0.2.0", "update", "--check")
	require.Equal(t, exitcode.OK, res.code, res.stderr)
	assert.Contains(t, res.stderr, "hivepaas 0.3.0 is out (this is 0.2.0): hivepaas update installs it.")
	assert.Equal(t, "hivepaas installed", r.installed(), "--check changes nothing")

	res = r.run("0.2.0", "update")
	require.Equal(t, exitcode.OK, res.code, res.stderr)
	assert.Equal(t, "hivepaas 0.3.0", r.installed())
	assert.Contains(t, res.stderr, "Updated hivepaas 0.2.0 -> 0.3.0 ("+r.binary+").")

	res = r.run("0.3.0", "update")
	require.Equal(t, exitcode.OK, res.code, res.stderr)
	assert.Contains(t, res.stderr, "hivepaas 0.3.0 is the newest release.")
}

func TestUpdateFollowsTheChannel(t *testing.T) {
	r := newReleases(t)

	res := r.run("0.3.0-beta2", "update")
	require.Equal(t, exitcode.OK, res.code, res.stderr)
	assert.Equal(t, "hivepaas 0.4.0-beta1", r.installed(), "a beta follows beta")

	res = r.run("0.3.0", "update", "--channel", "beta", "--check", "-o", "json")
	require.Equal(t, exitcode.OK, res.code, res.stderr)
	var check map[string]any
	require.NoError(t, json.Unmarshal([]byte(res.stdout), &check))
	assert.Equal(t, map[string]any{"current": "0.3.0", "newest": "v0.4.0-beta1", "newer": true}, check)
}

func TestUpdateToAVersion(t *testing.T) {
	r := newReleases(t)

	res := r.run("0.3.0", "update", "--version", "v0.1.0")
	require.Equal(t, exitcode.OK, res.code, res.stderr)
	assert.Equal(t, "hivepaas 0.1.0", r.installed(), "--version may go back")

	res = r.run("0.3.0", "update", "--version", "v9.0.0")
	assert.Equal(t, exitcode.NotFound, res.code)
	assert.Contains(t, res.stderr, "v9.0.0 is not a release of the CLI: v0.4.0-beta1, v0.3.0, v0.2.0, v0.1.0")

	res = r.run("dev", "update")
	assert.Equal(t, exitcode.Usage, res.code, "a development build is given its release")
	res = r.run("dev", "update", "--version", "v0.2.0")
	require.Equal(t, exitcode.OK, res.code, res.stderr)
	assert.Equal(t, "hivepaas 0.2.0", r.installed())
}

func TestUpdateInstallsNothingItCannotTrust(t *testing.T) {
	r := newReleases(t)
	file := selfupdate.ArchiveFile(selfupdate.Version{Major: 0, Minor: 3}, selfupdate.Platform(runtime.GOOS,
		runtime.GOARCH))
	r.pub.Replace("/download/v0.3.0/"+file, selfupdatetest.TarGz(t, "hivepaas", []byte("not the release")))

	res := r.run("0.2.0", "update")
	assert.Equal(t, exitcode.Failure, res.code)
	assert.Contains(t, res.stderr, "its SHA-256 differs; nothing was changed")
	assert.Equal(t, "hivepaas installed", r.installed())

	r.keys = selfupdatetest.NewSigner(t).Keys(t) // the list is signed by keys the CLI does not trust
	res = r.run("0.2.0", "update")
	assert.Equal(t, exitcode.Failure, res.code)
	assert.Contains(t, res.stderr, "not validly signed")
	assert.Equal(t, "hivepaas installed", r.installed())
}

func TestUpdateLeavesAPackageManagersBinary(t *testing.T) {
	r := newReleases(t)
	r.binary = filepath.Join(t.TempDir(), "Cellar", "hivepaas", "0.2.0", "bin", "hivepaas")

	res := r.run("0.2.0", "update")
	assert.Equal(t, exitcode.Failure, res.code)
	assert.Contains(t, res.stderr, "update it with brew upgrade hivepaas")

	res = r.run("0.2.0", "update", "--check")
	require.Equal(t, exitcode.OK, res.code, res.stderr)
	assert.Contains(t, res.stderr, "hivepaas 0.3.0 is out (this is 0.2.0): brew upgrade hivepaas installs it.")
}

func TestAnOutdatedCLIIsToldHowToUpdate(t *testing.T) {
	f := newFakeAPI(t)
	f.json("POST "+appPath+"/restart", 426, `{"status":426,"code":"ERR_CLI_OUTDATED",
		"detail":"This server takes changes only from a newer HivePaaS CLI"}`)

	r := f.run(args("restart " + shopAPI)...)

	assert.Equal(t, exitcode.CLIOutdated, r.code)
	assert.Contains(t, r.stderr, "Update it: hivepaas update")
}
