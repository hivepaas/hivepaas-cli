package cmd

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hivepaas/hivepaas-cli/internal/exitcode"
)

const snapshotsBody = `{"data":[
	{"id":"S1","shortId":"a1b2c3d4","snapshotId":"a1b2c3d4e5f6","time":"2026-10-09T10:00:00Z","sizeBytes":2097152,
		"paths":["/data"],"tags":["daily"],"repo":{"id":"R1","name":"nightly"},
		"app":{"id":"A1","name":"api"},
		"job":{"id":"J9","name":"backup-data","sourceVolumeId":"V2","sourceVolumeSubpath":"uploads"}},
	{"id":"S2","shortId":"e5f6a7b8","snapshotId":"e5f6a7b8c9d0","time":"2026-10-08T10:00:00Z","sizeBytes":1024,
		"paths":["/data"],"tags":[],"repo":{"id":"R2","name":"offsite"},"app":{"id":"A1","name":"api"}}],
	"repos":[{"id":"R1","name":"nightly"},{"id":"R2","name":"offsite"}]}`

func TestBackupLs(t *testing.T) {
	f := newFakeAPI(t)
	f.json("GET "+appPath+"/backup-snapshots", http.StatusOK, snapshotsBody)

	r := f.run(args("backup ls " + shopAPI)...)
	require.Equal(t, exitcode.OK, r.code, r.stderr)
	for _, want := range []string{"a1b2c3d4", "e5f6a7b8", "2.0 MB", "backup-data", "nightly", "daily"} {
		assert.Contains(t, r.stdout, want)
	}

	r = f.run(args("backup ls --repo offsite " + shopAPI)...)
	require.Equal(t, exitcode.OK, r.code, r.stderr)
	assert.Contains(t, r.stdout, "e5f6a7b8")
	assert.NotContains(t, r.stdout, "a1b2c3d4")
}

// files lists what a snapshot holds; download writes one file of it.
func TestBackupFilesAndDownload(t *testing.T) {
	f := newFakeAPI(t)
	f.json("GET "+appPath+"/backup-snapshots", http.StatusOK, snapshotsBody)
	f.handle("GET "+appPath+"/backup-snapshots/S1/entries", func(w http.ResponseWriter, r *http.Request, _ int) {
		assert.Equal(t, "uploads", r.URL.Query().Get("path"))
		_, _ = w.Write([]byte(`{"data":[{"name":"logo.png","sizeBytes":2048},{"name":"docs","dir":true,
			"sizeBytes":4096}]}`))
	})
	f.handle("GET "+appPath+"/backup-snapshots/S1/download", func(w http.ResponseWriter, r *http.Request, _ int) {
		assert.Equal(t, "uploads/logo.png", r.URL.Query().Get("path"))
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write([]byte("PNGDATA"))
	})

	r := f.run(args("backup files a1b2c3d4 uploads " + shopAPI)...)
	require.Equal(t, exitcode.OK, r.code, r.stderr)
	assert.Contains(t, r.stdout, "logo.png")
	assert.Contains(t, r.stdout, "docs/")

	out := filepath.Join(t.TempDir(), "logo.png")
	r = f.run(append(args("backup download a1b2c3d4 uploads/logo.png "+shopAPI+" -O"), out)...)
	require.Equal(t, exitcode.OK, r.code, r.stderr)
	data, err := os.ReadFile(out)
	require.NoError(t, err)
	assert.Equal(t, "PNGDATA", string(data))

	r = f.run(args("backup download a1b2c3d4 uploads/logo.png -O - " + shopAPI)...)
	require.Equal(t, exitcode.OK, r.code, r.stderr)
	assert.Equal(t, "PNGDATA", r.stdout)

	r = f.run(args("backup files nope " + shopAPI)...)
	assert.Equal(t, exitcode.NotFound, r.code)
}

// restore writes a snapshot back where its job backed up, asked first, and
// waits for the task.
func TestBackupRestore(t *testing.T) {
	quickly(t)
	f := newFakeAPI(t)
	f.json("GET "+appPath+"/backup-snapshots", http.StatusOK, snapshotsBody)
	f.json("POST "+appPath+"/backup-snapshots/S1/restore", http.StatusOK, `{"data":{"task":{"id":"T7"}}}`)
	fakeDoneTask(f, "T7", "restored 12 files")

	r := f.run(args("backup restore a1b2c3d4 " + shopAPI)...)
	assert.Equal(t, exitcode.Usage, r.code, "no terminal to ask at")
	assert.Zero(t, f.called("POST "+appPath+"/backup-snapshots/S1/restore"))

	r = f.run(args("backup restore a1b2c3d4 --stop-app --yes " + shopAPI)...)
	require.Equal(t, exitcode.OK, r.code, r.stderr)
	body := f.body("POST "+appPath+"/backup-snapshots/S1/restore", 0)
	assert.Equal(t, map[string]any{"id": "A1"}, body["targetApp"])
	assert.Equal(t, map[string]any{"id": "V2"}, body["volume"])
	assert.Equal(t, "uploads", body["subpath"])
	assert.Equal(t, "replace", body["mode"])
	assert.Equal(t, true, body["stopApp"])
	assert.Contains(t, r.stderr, "restored 12 files")
	assert.Contains(t, r.stderr, "Done in 3s.")

	r = f.run(args("backup restore a1b2c3d4 --mode merge --yes " + shopAPI)...)
	assert.Equal(t, exitcode.Usage, r.code)
}

// rm deletes a snapshot, asked first.
func TestBackupRm(t *testing.T) {
	f := newFakeAPI(t)
	f.json("GET "+appPath+"/backup-snapshots", http.StatusOK, snapshotsBody)
	f.json("DELETE "+appPath+"/backup-snapshots/S2", http.StatusOK, `{}`)

	assert.Equal(t, exitcode.Usage, f.run(args("backup rm e5f6a7b8 "+shopAPI)...).code)
	r := f.run(args("backup rm e5f6a7b8 --yes " + shopAPI)...)
	require.Equal(t, exitcode.OK, r.code, r.stderr)
	assert.Equal(t, 1, f.called("DELETE "+appPath+"/backup-snapshots/S2"))
}

// run runs the app's data backup job; with several, it is named.
func TestBackupRun(t *testing.T) {
	quickly(t)
	f := newFakeAPI(t)
	f.handle("GET "+appPath+"/sched-jobs", func(w http.ResponseWriter, _ *http.Request, n int) {
		jobs := `{"id":"J9","name":"backup-data","jobType":"data-backup","status":"active"},
			{"id":"J1","name":"migrate","jobType":"container-command","status":"active"}`
		if n >= 2 {
			jobs += `,{"id":"J8","name":"backup-db","jobType":"data-backup","status":"active"}`
		}
		_, _ = w.Write([]byte(`{"data":[` + jobs + `]}`))
	})
	f.json("POST "+appPath+"/sched-jobs/J9/exec", http.StatusOK, `{"data":{"task":{"id":"T9"}}}`)
	fakeDoneTask(f, "T9", "snapshot a1b2c3d4 saved")

	r := f.run(args("backup run " + shopAPI)...)
	require.Equal(t, exitcode.OK, r.code, r.stderr)
	assert.Contains(t, r.stderr, "snapshot a1b2c3d4 saved")

	r = f.run(args("backup run " + shopAPI)...)
	assert.Equal(t, exitcode.Usage, r.code)
	assert.Contains(t, r.stderr, "backup-data")
	assert.Contains(t, r.stderr, "backup-db")
}
