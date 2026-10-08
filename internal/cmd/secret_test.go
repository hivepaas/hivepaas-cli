package cmd

import (
	"encoding/base64"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hivepaas/hivepaas-cli/internal/exitcode"
)

const secretsBody = `{"data":[
	{"id":"S1","key":"STRIPE_KEY","value":"","secretMasked":true,"base64":false,"inheritable":true,"default":false,
		"updateVer":4,"size":32,"updatedAt":"2026-10-08T14:00:00Z"},
	{"id":"S9","key":"SHARED_TOKEN","value":"","secretMasked":true,"inherited":true,"inheritable":true,"updateVer":1}]}`

// A secret the app has is changed, keeping whether its previews get it; one it
// has not is added; and one it inherits is not touched - the app gets its own.
func TestSecretSet(t *testing.T) {
	f := newFakeAPI(t)
	f.json("GET "+appPath+"/secrets", http.StatusOK, secretsBody)
	f.json("PUT "+appPath+"/secrets/S1", http.StatusOK, `{"meta":null}`)
	f.json("POST "+appPath+"/secrets", http.StatusCreated, `{"data":{"id":"S2"}}`)

	r := f.run(args("secret set STRIPE_KEY=sk_live_2 SHARED_TOKEN=mine " + shopAPI)...)

	require.Equal(t, exitcode.OK, r.code, r.stderr)
	assert.Equal(t, map[string]any{"key": "STRIPE_KEY", "value": "sk_live_2", "base64": false, "inheritable": true,
		"default": false, "updateVer": float64(4)}, f.body("PUT "+appPath+"/secrets/S1", 0))
	assert.Equal(t, map[string]any{"key": "SHARED_TOKEN", "value": "mine", "base64": false, "inheritable": true,
		"default": false}, f.body("POST "+appPath+"/secrets", 0))
	assert.Contains(t, r.stderr, "Changed STRIPE_KEY, added SHARED_TOKEN on api (shop / production).")
}

// A value off the command line: from stdin, or a file kept as it is.
func TestSecretSetFromStdinOrAFile(t *testing.T) {
	f := newFakeAPI(t)
	f.json("GET "+appPath+"/secrets", http.StatusOK, `{"data":[]}`)
	f.json("POST "+appPath+"/secrets", http.StatusCreated, `{"data":{"id":"S3"}}`)

	r := f.runWithStdin("tok-123\n", args("secret set API_TOKEN --no-previews "+shopAPI)...)
	require.Equal(t, exitcode.OK, r.code, r.stderr)
	assert.Equal(t, map[string]any{"key": "API_TOKEN", "value": "tok-123", "base64": false, "inheritable": false,
		"default": false}, f.body("POST "+appPath+"/secrets", 0), "the line's end is not the value's")

	keystore := filepath.Join(t.TempDir(), "keystore.jks")
	require.NoError(t, os.WriteFile(keystore, []byte{0xfe, 0xed, 0x00, 0x02}, 0o600))
	r = f.run("secret", "set", "KEYSTORE", "--file", keystore, "-p", "shop", "-e", "production", "-a", "api")
	require.Equal(t, exitcode.OK, r.code, r.stderr)
	written := f.body("POST "+appPath+"/secrets", 1)
	assert.Equal(t, true, written["base64"])
	assert.Equal(t, base64.StdEncoding.EncodeToString([]byte{0xfe, 0xed, 0x00, 0x02}), written["value"])

	r = f.run("secret", "set", "A=1", "--file", keystore, "-p", "shop", "-e", "production", "-a", "api")
	assert.Equal(t, exitcode.Usage, r.code)
}

// The server can save a change and fail to bring it to the apps: it says so in
// meta.warning, and so does the CLI.
func TestSecretSetSaysTheServersWarning(t *testing.T) {
	f := newFakeAPI(t)
	f.json("GET "+appPath+"/secrets", http.StatusOK, secretsBody)
	f.json("PUT "+appPath+"/secrets/S1", http.StatusOK,
		`{"meta":{"warning":"Secret updated successfully, but failed to apply changes to apps"}}`)

	r := f.run(args("secret set STRIPE_KEY=sk_live_3 " + shopAPI)...)

	require.Equal(t, exitcode.OK, r.code, r.stderr)
	assert.Contains(t, r.stderr, "Warning: Secret updated successfully, but failed to apply changes to apps")
}

// Bytes from stdin that are not text are kept to the last one.
func TestSecretSetKeepsABinaryValueWhole(t *testing.T) {
	f := newFakeAPI(t)
	f.json("GET "+appPath+"/secrets", http.StatusOK, `{"data":[]}`)
	f.json("POST "+appPath+"/secrets", http.StatusCreated, `{"data":{"id":"S3"}}`)

	r := f.runWithStdin(string([]byte{0xfe, 0xed, 0x0a}), args("secret set BLOB "+shopAPI)...)

	require.Equal(t, exitcode.OK, r.code, r.stderr)
	assert.Equal(t, base64.StdEncoding.EncodeToString([]byte{0xfe, 0xed, 0x0a}),
		f.body("POST "+appPath+"/secrets", 0)["value"])
}

func TestSecretLsAndRm(t *testing.T) {
	f := newFakeAPI(t)
	f.json("GET "+appPath+"/secrets", http.StatusOK, secretsBody)
	f.json("DELETE "+appPath+"/secrets/S1", http.StatusOK, `{"meta":null}`)

	r := f.run(args("secret ls " + shopAPI)...)
	require.Equal(t, exitcode.OK, r.code, r.stderr)
	assert.Regexp(t, `STRIPE_KEY\s+32 B\s+text\s+yes\s+this app\s`, r.stdout)
	assert.Regexp(t, `SHARED_TOKEN\s+-\s+text\s+yes\s+inherited`, r.stdout)
	assert.NotContains(t, r.stdout, "sk_", "no value is listed")

	r = f.run(args("secret rm STRIPE_KEY SHARED_TOKEN " + shopAPI)...)
	assert.Equal(t, exitcode.NotFound, r.code, "an inherited secret is not the app's to remove")
	assert.Zero(t, f.called("DELETE "+appPath+"/secrets/S1"), "none is removed when one is not found")

	r = f.run(args("secret rm STRIPE_KEY " + shopAPI)...)
	require.Equal(t, exitcode.OK, r.code, r.stderr)
	assert.Equal(t, 1, f.called("DELETE "+appPath+"/secrets/S1"))
}

// An app with more secrets than one page holds: all of them are read.
func TestSecretsAreReadPageByPage(t *testing.T) {
	f := newFakeAPI(t)
	f.handle("GET "+appPath+"/secrets", func(w http.ResponseWriter, r *http.Request, _ int) {
		assert.Equal(t, "500", r.URL.Query().Get("pageLimit"))
		count := 500
		if r.URL.Query().Get("pageOffset") == "500" {
			count = 1
		}
		items := make([]string, 0, count)
		for i := range count {
			items = append(items, `{"id":"S`+r.URL.Query().Get("pageOffset")+"-"+strconv.Itoa(i)+
				`","key":"K`+r.URL.Query().Get("pageOffset")+"_"+strconv.Itoa(i)+`","updateVer":1}`)
		}
		_, _ = w.Write([]byte(`{"data":[` + strings.Join(items, ",") + `]}`))
	})
	f.json("DELETE "+appPath+"/secrets/S500-0", http.StatusOK, `{"meta":null}`)

	r := f.run(args("secret rm K500_0 K500_0 " + shopAPI)...)

	require.Equal(t, exitcode.OK, r.code, r.stderr)
	assert.Equal(t, 1, f.called("DELETE "+appPath+"/secrets/S500-0"), "the one on the second page, once")
}

const configFilesBody = `{"data":[
	{"id":"F1","name":"nginx-conf","content":"server {}\n","base64":false,"inheritable":true,"updateVer":2,"size":10},
	{"id":"F2","name":"logo","content":"/u0AAg==","base64":true,"inheritable":false,"updateVer":1,"size":4}]}`

func TestConfigFilePushAndPull(t *testing.T) {
	f := newFakeAPI(t)
	f.json("GET "+appPath+"/config-files", http.StatusOK, configFilesBody)
	f.json("PUT "+appPath+"/config-files/F1", http.StatusOK, `{"meta":null}`)
	f.json("POST "+appPath+"/config-files", http.StatusCreated, `{"data":{"id":"F3"}}`)
	dir := t.TempDir()
	conf := filepath.Join(dir, "nginx.conf")
	require.NoError(t, os.WriteFile(conf, []byte("server { listen 80; }\n"), 0o600))

	r := f.run("config-file", "push", "nginx-conf", conf, "-p", "shop", "-e", "production", "-a", "api")
	require.Equal(t, exitcode.OK, r.code, r.stderr)
	assert.Equal(t, map[string]any{"name": "nginx-conf", "content": "server { listen 80; }\n", "base64": false,
		"inheritable": true, "default": false, "updateVer": float64(2)}, f.body("PUT "+appPath+"/config-files/F1", 0))
	assert.Contains(t, r.stderr, "Changed config file nginx-conf of api (shop / production).")

	r = f.runWithStdin("a: 1\n", args("config-file push settings - "+shopAPI)...)
	require.Equal(t, exitcode.OK, r.code, r.stderr)
	assert.Equal(t, "settings", f.body("POST "+appPath+"/config-files", 0)["name"])

	r = f.run(args("config-file pull nginx-conf " + shopAPI)...)
	require.Equal(t, exitcode.OK, r.code, r.stderr)
	assert.Equal(t, "server {}\n", r.stdout)

	out := filepath.Join(dir, "logo.png")
	r = f.run("config-file", "pull", "logo", out, "-p", "shop", "-e", "production", "-a", "api")
	require.Equal(t, exitcode.OK, r.code, r.stderr)
	data, err := os.ReadFile(out)
	require.NoError(t, err)
	assert.Equal(t, []byte{0xfe, 0xed, 0x00, 0x02}, data, "a binary file as it was")

	r = f.run(args("config-file pull nope " + shopAPI)...)
	assert.Equal(t, exitcode.NotFound, r.code)
}
