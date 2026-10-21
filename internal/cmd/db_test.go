package cmd

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hivepaas/hivepaas-cli/internal/exitcode"
)

func TestDBMarkNamesTheMoment(t *testing.T) {
	f := newFakeAPI(t)
	f.json("POST "+appPath+"/managed/restore-points", http.StatusOK,
		`{"data":{"name":"before-v42","position":"0/3000060","at":"2026-10-18T14:00:00Z",
			"keepUntil":"2026-12-31T00:00:00Z","kept":true,"note":"migration 42"}}`)

	r := f.run(append(args("db mark before-v42 --keep-until 2026-12-31 "+shopAPI), "--note", "migration 42")...)

	require.Equal(t, exitcode.OK, r.code, r.stderr)
	assert.Contains(t, r.stderr, "Marked before-v42 at 0/3000060 on api")
	assert.Contains(t, r.stderr, "kept until 2026-12-31T00:00:00Z")
	written := f.body("POST "+appPath+"/managed/restore-points", 0)
	assert.Equal(t, "before-v42", written["name"])
	assert.Equal(t, "2026-12-31T00:00:00Z", written["keepUntil"], "a date is the start of its day, in UTC")
	assert.Equal(t, "migration 42", written["note"])
}

func TestDBMarkSaysWhatTheServerRefused(t *testing.T) {
	f := newFakeAPI(t)
	f.json("POST "+appPath+"/managed/restore-points", http.StatusBadRequest,
		`{"status":400,"code":"ERR_APP_MANAGED_RESTORE_POINT_NOT_ARCHIVED",
			"title":"The restore point was made, but the WAL it is in was not archived in time"}`)

	r := f.run(args("db mark late " + shopAPI)...)

	assert.NotEqual(t, exitcode.OK, r.code)
	assert.Contains(t, r.stderr, "not archived in time")
}

func TestDBMarkRefusesAKeepItCannotRead(t *testing.T) {
	f := newFakeAPI(t)

	r := f.run(args("db mark p --keep-until next-week " + shopAPI)...)

	assert.Equal(t, exitcode.Usage, r.code)
	assert.Contains(t, r.stderr, "is not a date such as 2026-12-31")
	assert.Zero(t, f.called("POST "+appPath+"/managed/restore-points"), "nothing asked of the server")
}
