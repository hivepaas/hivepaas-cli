package selfupdate_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hivepaas/hivepaas-cli/internal/selfupdate"
	"github.com/hivepaas/hivepaas-cli/internal/selfupdate/selfupdatetest"
)

func TestTheEmbeddedKeysAreHivepaasReleaseKeys(t *testing.T) {
	keys, err := selfupdate.EmbeddedKeys()
	require.NoError(t, err)
	assert.Contains(t, keys, "2026_ed")
	assert.Contains(t, keys, "2026_ml")
}

// testdata/release.signed.json was signed by hivepaas's own tool -
// `releasesign sign -context cli` - with the keys in testdata/keys: what that
// tool signs, the CLI opens.
func TestOpenWhatHivepaasReleasesignSigned(t *testing.T) {
	keys, err := selfupdate.LoadKeys(os.DirFS("testdata/keys"))
	require.NoError(t, err)
	signed, err := os.ReadFile("testdata/release.signed.json")
	require.NoError(t, err)
	want, err := os.ReadFile("testdata/release.json")
	require.NoError(t, err)

	got, err := selfupdate.Open(keys, signed)
	require.NoError(t, err)
	assert.Equal(t, want, got)
	_, err = selfupdate.ParseManifest(got)
	require.NoError(t, err)
}

// The same list signed as a release.json - the server's context - is no list of
// the CLI's releases.
func TestOpenRefusesTheServersContext(t *testing.T) {
	keys, err := selfupdate.LoadKeys(os.DirFS("testdata/keys"))
	require.NoError(t, err)
	signed, err := os.ReadFile("testdata/server-context.signed.json")
	require.NoError(t, err)

	_, err = selfupdate.Open(keys, signed)
	assert.ErrorIs(t, err, selfupdate.ErrSignature)
}

func TestOpenRefusesWhatTheKeysDidNotSign(t *testing.T) {
	signer := selfupdatetest.NewSigner(t)
	keys := signer.Keys(t)
	data := []byte(`{"channels":{},"releases":{}}`)
	signed := signer.Sign(t, data, selfupdate.Context)

	opened, err := selfupdate.Open(keys, signed)
	require.NoError(t, err)
	assert.Equal(t, data, opened)

	other := selfupdatetest.NewSigner(t)
	_, err = selfupdate.Open(keys, other.Sign(t, data, selfupdate.Context))
	require.ErrorIs(t, err, selfupdate.ErrSignature, "signed by keys the CLI does not trust")

	embedded, err := selfupdate.EmbeddedKeys()
	require.NoError(t, err)
	_, err = selfupdate.Open(embedded, signed)
	require.ErrorIs(t, err, selfupdate.ErrSignature, "the release keys did not sign it")

	var env map[string]any
	require.NoError(t, json.Unmarshal(signed, &env))
	env["signatures"] = env["signatures"].([]any)[:1]
	oneSig, _ := json.Marshal(env)
	_, err = selfupdate.Open(keys, oneSig)
	require.ErrorIs(t, err, selfupdate.ErrSignature, "one algorithm is not enough")

	tampered := bytes.Replace(signed, []byte(`"payload":"`), []byte(`"payload":"e30K`), 1)
	_, err = selfupdate.Open(keys, tampered)
	require.ErrorIs(t, err, selfupdate.ErrSignature, "a changed payload")
}

func TestVersions(t *testing.T) {
	ordered := []string{"v0.1.0", "v0.2.0-alpha1", "v0.2.0-beta1", "v0.2.0-beta2", "v0.2.0-beta10", "v0.2.0",
		"v0.2.1", "v0.10.0", "v1.0.0"}
	for i := range ordered {
		for j := range ordered {
			a, err := selfupdate.ParseVersion(ordered[i])
			require.NoError(t, err)
			b, err := selfupdate.ParseVersion(ordered[j])
			require.NoError(t, err)
			assert.Equal(t, sign(i-j), a.Compare(b), "%s vs %s", ordered[i], ordered[j])
		}
	}
	v, err := selfupdate.ParseVersion("0.3.0-beta1")
	require.NoError(t, err)
	assert.Equal(t, "v0.3.0-beta1", v.String(), "the CLI's own version has no v")
	assert.True(t, v.IsPrerelease())
	assert.Equal(t, selfupdate.ChannelBeta, selfupdate.ChannelOf(v))

	for _, bad := range []string{"dev", "v1.2", "v1.2.3-beta.1", "v1.2.3-rc", "1.2.3.4", ""} {
		_, err = selfupdate.ParseVersion(bad)
		assert.Error(t, err, bad)
	}
}

func sign(d int) int {
	switch {
	case d < 0:
		return -1
	case d > 0:
		return 1
	}
	return 0
}

func TestManifestIsCheckedWhole(t *testing.T) {
	good, err := os.ReadFile("testdata/release.json")
	require.NoError(t, err)
	m, err := selfupdate.ParseManifest(good)
	require.NoError(t, err)

	stable, _ := m.Newest(selfupdate.ChannelStable)
	beta, _ := m.Newest(selfupdate.ChannelBeta)
	assert.Equal(t, "v0.2.0", stable.String())
	assert.Equal(t, "v0.3.0-beta1", beta.String())

	m.Channels[selfupdate.ChannelStable] = "v0.4.0"
	m.Releases["v0.4.0"] = m.Releases["v0.2.0"]
	_, err = selfupdate.ParseManifest(mustJSON(t, m))
	require.Error(t, err, "v0.4.0's files are v0.2.0's")

	m.Releases["v0.4.0"] = selfupdate.Release{Archives: map[string]selfupdate.Archive{
		"darwin_arm64": {File: "hivepaas_0.4.0_darwin_arm64.tar.gz", SHA256: strings.Repeat("a", 64)},
	}}
	m2, err := selfupdate.ParseManifest(mustJSON(t, m))
	require.NoError(t, err)
	beta, _ = m2.Newest(selfupdate.ChannelBeta)
	assert.Equal(t, "v0.4.0", beta.String(), "a stable release supersedes the betas before it")
	assert.Equal(t, []string{"v0.4.0", "v0.3.0-beta1", "v0.2.0"}, names(m2.Versions()))

	for name, change := range map[string]func(*selfupdate.Manifest){
		"a beta on stable":     func(m *selfupdate.Manifest) { m.Channels["stable"] = "v0.3.0-beta1" },
		"an unlisted release":  func(m *selfupdate.Manifest) { m.Channels["beta"] = "v9.0.0-beta1" },
		"an unknown channel":   func(m *selfupdate.Manifest) { m.Channels["nightly"] = "v0.2.0" },
		"a path for a file":    func(m *selfupdate.Manifest) { setFile(m, "../../etc/passwd") },
		"no SHA-256":           func(m *selfupdate.Manifest) { setSum(m, "abc") },
		"a version written v2": func(m *selfupdate.Manifest) { m.Releases["0.2.0"] = m.Releases["v0.2.0"] },
	} {
		m, err := selfupdate.ParseManifest(good)
		require.NoError(t, err)
		change(m)
		_, err = selfupdate.ParseManifest(mustJSON(t, m))
		assert.Error(t, err, name)
	}
}

func setFile(m *selfupdate.Manifest, file string) {
	a := m.Releases["v0.2.0"].Archives["darwin_arm64"]
	a.File = file
	m.Releases["v0.2.0"].Archives["darwin_arm64"] = a
}

func setSum(m *selfupdate.Manifest, sum string) {
	a := m.Releases["v0.2.0"].Archives["darwin_arm64"]
	a.SHA256 = sum
	m.Releases["v0.2.0"].Archives["darwin_arm64"] = a
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	data, err := json.Marshal(v)
	require.NoError(t, err)
	return data
}

func names(versions []selfupdate.Version) []string {
	out := make([]string, 0, len(versions))
	for _, v := range versions {
		out = append(out, v.String())
	}
	return out
}

func publish(t *testing.T) (*selfupdatetest.Publication, *selfupdate.Updater) {
	t.Helper()
	signer := selfupdatetest.NewSigner(t)
	pub := selfupdatetest.Publish(t, signer, map[string]string{"stable": "v0.3.0"},
		map[string][]byte{"v0.2.0": []byte("binary 0.2.0"), "v0.3.0": []byte("binary 0.3.0")})
	return pub, pub.Updater(signer.Keys(t))
}

func TestFetchAndBinary(t *testing.T) {
	pub, u := publish(t)
	m, err := u.Fetch(context.Background())
	require.NoError(t, err)
	v, _ := m.Newest(selfupdate.ChannelStable)
	archive := m.Releases[v.String()].Archives[selfupdate.Platform(runtime.GOOS, runtime.GOARCH)]

	binary, err := u.Binary(context.Background(), v, archive)
	require.NoError(t, err)
	assert.Equal(t, "binary 0.3.0", string(binary))

	pub.Replace("/download/v0.3.0/"+archive.File, selfupdatetest.TarGz(t, "hivepaas", []byte("something else")))
	_, err = u.Binary(context.Background(), v, archive)
	assert.ErrorIs(t, err, selfupdate.ErrChecksum, "an archive other than the signed one")
}

func TestReplace(t *testing.T) {
	for _, goos := range []string{"linux", "windows"} {
		dir := t.TempDir()
		path := filepath.Join(dir, "hivepaas")
		require.NoError(t, os.WriteFile(path, []byte("old"), 0o751))

		require.NoError(t, selfupdate.Replace(path, []byte("new"), goos))

		content, err := os.ReadFile(path)
		require.NoError(t, err)
		assert.Equal(t, "new", string(content), goos)
		info, err := os.Stat(path)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o751), info.Mode().Perm(), "the old binary's mode")
		entries, _ := os.ReadDir(dir)
		if goos == "windows" {
			assert.Len(t, entries, 2, "the old one beside it, until RemoveOld")
			selfupdate.RemoveOld(path)
		}
		entries, _ = os.ReadDir(dir)
		assert.Len(t, entries, 1, "no file left behind")
	}
}

func TestManagedBy(t *testing.T) {
	assert.Equal(t, "brew upgrade hivepaas", selfupdate.ManagedBy("/opt/homebrew/Cellar/hivepaas/0.2.0/bin/hivepaas"))
	assert.Equal(t, "brew upgrade hivepaas",
		selfupdate.ManagedBy("/home/linuxbrew/.linuxbrew/Cellar/hivepaas/0.2.0/bin/hivepaas"))
	assert.Equal(t, "scoop update hivepaas",
		selfupdate.ManagedBy(`C:\Users\me\scoop\apps\hivepaas\current\hivepaas.exe`))
	assert.Empty(t, selfupdate.ManagedBy("/usr/local/bin/hivepaas"))
	assert.Equal(t, "hivepaas update", selfupdate.UpdateCommand("/usr/local/bin/hivepaas"))
}

func TestNotifier(t *testing.T) {
	pub, u := publish(t)
	now := time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)
	current, _ := selfupdate.ParseVersion("0.2.0")
	n := &selfupdate.Notifier{Dir: t.TempDir(), Current: current, Updater: u, Command: "hivepaas update",
		Now: func() time.Time { return now }}

	want := "A new release of hivepaas is out: 0.3.0 (this is 0.2.0). Update it: hivepaas update"
	assert.Equal(t, want, n.Start(context.Background())())
	assert.Equal(t, 1, pub.Requests("/list"))

	now = now.Add(23 * time.Hour)
	assert.Equal(t, want, n.Start(context.Background())(), "from what it found, without asking again")
	assert.Equal(t, 1, pub.Requests("/list"))

	now = now.Add(2 * time.Hour)
	n.Current, _ = selfupdate.ParseVersion("0.3.0")
	assert.Empty(t, n.Start(context.Background())(), "the newest says nothing")
	assert.Equal(t, 2, pub.Requests("/list"))
}

func TestNotifierIsSilentWhenTheListCannotBeRead(t *testing.T) {
	pub, u := publish(t)
	pub.Replace("/list", []byte("not signed"))
	now := time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)
	current, _ := selfupdate.ParseVersion("0.2.0")
	n := &selfupdate.Notifier{Dir: t.TempDir(), Current: current, Updater: u, Command: "hivepaas update",
		Now: func() time.Time { return now }}

	assert.Empty(t, n.Start(context.Background())())
	assert.Empty(t, n.Start(context.Background())())
	assert.Equal(t, 1, pub.Requests("/list"), "a failed reading is not tried again before tomorrow")
}
