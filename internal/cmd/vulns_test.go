package cmd

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hivepaas/hivepaas-cli/internal/exitcode"
)

// imageScanBody is api's last scan: alpine:3.10, past its support, with a
// fixable CRITICAL, an unfixed HIGH, a fixable MEDIUM, and a fixable HIGH an
// ignore of the project sets aside.
const imageScanBody = `{"data":{"enabled":true,"scannedAt":"2026-10-15T02:00:00Z",
	"summary":{"critical":{"total":1,"fixable":1},"high":{"total":1,"fixable":0},"medium":{"total":1,"fixable":1},
		"low":{"total":0,"fixable":0},"unknown":{"total":0,"fixable":0},"ignored":1},
	"images":[{"imageId":"sha256:aa","imageRef":"alpine:3.10","osFamily":"alpine","osName":"3.10.9","osEosl":true,
		"status":"done","scannedAt":"2026-10-15T02:00:00Z","updatedAt":"2026-10-15T02:00:00Z",
		"summary":{"critical":{"total":1,"fixable":1},"high":{"total":1,"fixable":0},"medium":{"total":1,"fixable":1},
			"low":{"total":0,"fixable":0},"unknown":{"total":0,"fixable":0},"ignored":1}}],
	"findings":[
		{"imageId":"sha256:aa","vulnId":"CVE-2021-36159","pkgName":"apk-tools","pkgType":"alpine",
			"installedVersion":"2.10.6-r0","fixedVersion":"2.10.7-r0","severity":"CRITICAL","title":"libfetch reads past"},
		{"imageId":"sha256:aa","vulnId":"CVE-2-HIGH","pkgName":"busybox","pkgType":"alpine",
			"installedVersion":"1.30.1-r5","severity":"HIGH"},
		{"imageId":"sha256:aa","vulnId":"CVE-3-MED","pkgName":"zlib","pkgType":"alpine",
			"installedVersion":"1.2.11-r1","fixedVersion":"1.2.12-r0","severity":"MEDIUM"}],
	"ignored":[
		{"imageId":"sha256:aa","vulnId":"CVE-4-IGN","pkgName":"musl","pkgType":"alpine",
			"installedVersion":"1.1.22-r3","fixedVersion":"1.1.22-r4","severity":"HIGH",
			"ignore":{"id":"I1","scope":"project","reason":"never fed outside input","setBy":"ada","inheritable":true}}]}}`

func TestAppVulnsListsTheFixableByDefault(t *testing.T) {
	f := newFakeAPI(t)
	f.json("GET "+appPath+"/image-scan", http.StatusOK, imageScanBody)

	r := f.run(args("app vulns " + shopAPI)...)

	require.Equal(t, exitcode.OK, r.code, r.stderr)
	assert.Contains(t, r.stdout, "alpine:3.10")
	assert.Contains(t, r.stdout, "alpine 3.10.9 (unsupported)")
	assert.Contains(t, r.stdout, "CVE-2021-36159")
	assert.Contains(t, r.stdout, "2.10.7-r0")
	assert.Contains(t, r.stdout, "CVE-3-MED")
	assert.NotContains(t, r.stdout, "CVE-2-HIGH", "no fix is out for it")
	assert.NotContains(t, r.stdout, "CVE-4-IGN", "an ignore sets it aside")
	assert.Contains(t, r.stderr, "Counts are fixable/all")
}

func TestAppVulnsNarrowedAndWidened(t *testing.T) {
	f := newFakeAPI(t)
	f.json("GET "+appPath+"/image-scan", http.StatusOK, imageScanBody)

	r := f.run(args("app vulns --severity critical,HIGH --all --ignored " + shopAPI)...)

	require.Equal(t, exitcode.OK, r.code, r.stderr)
	assert.Contains(t, r.stdout, "CVE-2-HIGH")
	assert.NotContains(t, r.stdout, "CVE-3-MED")
	assert.Contains(t, r.stdout, "CVE-4-IGN")
	assert.Contains(t, r.stdout, "its project")
	assert.Contains(t, r.stdout, "never fed outside input (ada)")

	r = f.run(args("app vulns --severity severe " + shopAPI)...)
	assert.Equal(t, exitcode.Usage, r.code)
	assert.Contains(t, r.stderr, `--severity "severe"`)
}

// -o json prints the API's data, with the findings the flags list.
func TestAppVulnsAsJSON(t *testing.T) {
	f := newFakeAPI(t)
	f.json("GET "+appPath+"/image-scan", http.StatusOK, imageScanBody)

	r := f.run(args("app vulns -o json --severity critical " + shopAPI)...)

	require.Equal(t, exitcode.OK, r.code, r.stderr)
	var out struct {
		Enabled  bool             `json:"enabled"`
		Findings []map[string]any `json:"findings"`
		Ignored  []map[string]any `json:"ignored"`
		Images   []map[string]any `json:"images"`
	}
	require.NoError(t, json.Unmarshal([]byte(r.stdout), &out), r.stdout)
	assert.True(t, out.Enabled)
	require.Len(t, out.Findings, 1)
	assert.Equal(t, "CVE-2021-36159", out.Findings[0]["vulnId"])
	assert.Empty(t, out.Ignored, "listed with --ignored only")
	assert.Len(t, out.Images, 1)
}

// --fail-on fails on a fixable finding of that severity or worse, whatever
// the list shows, and not on one an ignore sets aside.
func TestAppVulnsFailOn(t *testing.T) {
	f := newFakeAPI(t)
	f.json("GET "+appPath+"/image-scan", http.StatusOK, imageScanBody)

	r := f.run(args("app vulns --fail-on critical --severity low " + shopAPI)...)
	assert.Equal(t, exitcode.Vulnerable, r.code)
	assert.Contains(t, r.stderr, "api runs 1 fixable critical vulnerability that no ignore sets aside")

	r = f.run(args("app vulns --fail-on medium " + shopAPI)...)
	assert.Equal(t, exitcode.Vulnerable, r.code)
	assert.Contains(t, r.stderr, "2 fixable medium or worse vulnerabilities")

	r = f.run(args("app vulns --fail-on unknown " + shopAPI)...)
	assert.Equal(t, exitcode.Usage, r.code)
}

func TestAppVulnsFailOnPassesAClearApp(t *testing.T) {
	f := newFakeAPI(t)
	f.json("GET "+appPath+"/image-scan", http.StatusOK, `{"data":{"enabled":true,"scannedAt":"2026-10-15T02:00:00Z",
		"summary":{"critical":{"total":0,"fixable":0},"high":{"total":1,"fixable":0},"medium":{"total":0,"fixable":0},
			"low":{"total":0,"fixable":0},"unknown":{"total":0,"fixable":0},"ignored":0},
		"images":[],"findings":[{"imageId":"sha256:aa","vulnId":"CVE-2-HIGH","pkgName":"busybox","pkgType":"alpine",
			"installedVersion":"1.30.1-r5","severity":"HIGH"}],"ignored":[]}}`)

	r := f.run(args("app vulns --fail-on high " + shopAPI)...)
	assert.Equal(t, exitcode.OK, r.code, "a HIGH no fix is out for does not fail it: %s", r.stderr)
}

func TestAppVulnsOfAnAppNeverScanned(t *testing.T) {
	f := newFakeAPI(t)
	f.json("GET "+appPath+"/image-scan", http.StatusOK, `{"data":{"enabled":false,"summary":null,
		"images":[],"findings":[],"ignored":[]}}`)

	r := f.run(args("app vulns " + shopAPI)...)
	require.Equal(t, exitcode.OK, r.code, r.stderr)
	assert.Contains(t, r.stderr, "image scanning is switched off on the server")
	assert.Contains(t, r.stderr, "api's images were never scanned: hivepaas app scan -p shop -e production -a api")

	r = f.run(args("app vulns --fail-on critical " + shopAPI)...)
	assert.Equal(t, exitcode.Failure, r.code, "nothing to tell is not a pass")
	assert.Contains(t, r.stderr, "--fail-on cannot tell")
}

// A server older than image scanning answers the path a bare 404, where an
// app it does not have has an ErrorInfo: the CLI tells them apart.
func TestAppVulnsOnAServerWithoutImageScanning(t *testing.T) {
	f := newFakeAPI(t)
	f.handle("GET "+appPath+"/image-scan", func(w http.ResponseWriter, _ *http.Request, _ int) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("not found"))
	})
	f.json("GET /api/sessions/me", http.StatusOK,
		`{"data":{"user":{"email":"ada@example.com"},"server":{"version":"v1.0.0-beta4","apiLevel":2}}}`)

	r := f.run(args("app vulns " + shopAPI)...)
	assert.Equal(t, exitcode.NotFound, r.code)
	assert.Contains(t, r.stderr, "HivePaaS v1.0.0-beta4 has no image scanning: it comes with a newer release")

	g := newFakeAPI(t)
	g.json("GET "+appPath+"/image-scan", http.StatusNotFound,
		`{"status":404,"code":"ERR_APP_NOT_FOUND","detail":"app not found"}`)
	r = g.run(args("app vulns " + shopAPI)...)
	assert.Equal(t, exitcode.NotFound, r.code)
	assert.Contains(t, r.stderr, "app not found")
	assert.Zero(t, g.called("GET /api/sessions/me"))
}

func TestAppScanWaitsThenLists(t *testing.T) {
	quickly(t)
	f := newFakeAPI(t)
	f.json("POST "+appPath+"/image-scan", http.StatusOK, `{"data":{"task":{"id":"K7"}}}`)
	f.handle("GET "+appPath+"/tasks/K7/status", func(w http.ResponseWriter, _ *http.Request, n int) {
		_, _ = w.Write([]byte(`{"data":{"status":"` + []string{"in-progress", "done"}[min(n, 1)] + `"}}`))
	})
	f.json("GET "+appPath+"/tasks/K7", http.StatusOK, `{"data":{"id":"K7","status":"done",
		"startedAt":"2026-10-15T02:00:00Z","endedAt":"2026-10-15T02:00:04Z"}}`)
	f.logs("GET " + appPath + "/tasks/K7/logs")
	f.json("GET "+appPath+"/image-scan", http.StatusOK, imageScanBody)

	r := f.run(args("app scan --wait --fail-on critical " + shopAPI)...)

	assert.Equal(t, exitcode.Vulnerable, r.code, r.stderr)
	assert.Contains(t, r.stderr, "Scanning the images of api (shop / production), task K7")
	assert.Contains(t, r.stderr, "Done in 4s.")
	assert.Contains(t, r.stdout, "CVE-2021-36159")
}

func TestAppScanWithoutWait(t *testing.T) {
	f := newFakeAPI(t)
	f.json("POST "+appPath+"/image-scan", http.StatusOK, `{"data":{"task":{"id":"K8"}}}`)

	r := f.run(args("app scan " + shopAPI)...)
	require.Equal(t, exitcode.OK, r.code, r.stderr)
	assert.Contains(t, r.stderr, "task K8")
	assert.Contains(t, r.stderr, "hivepaas app vulns -p shop -e production -a api")
	assert.Zero(t, f.called("GET "+appPath+"/image-scan"))

	r = f.run(args("app scan --fail-on high " + shopAPI)...)
	assert.Equal(t, exitcode.Usage, r.code, "a gate needs the scan's end")
	assert.Equal(t, 1, f.called("POST "+appPath+"/image-scan"), "and nothing is asked")
}
