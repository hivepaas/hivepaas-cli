//go:build e2e

package e2e

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// alpine:3.10 is past its support, and its apk-tools has CVE-2021-36159,
// CRITICAL, fixed in 2.10.7-r0: a finding the DB will not drop.
const (
	oldAlpine = "alpine:3.10"
	oldCVE    = "CVE-2021-36159"
)

// imageScanSettings are the installation's image scanning settings, as the
// API answers and takes them.
type imageScanSettings struct {
	UpdateVer        int            `json:"updateVer"`
	Enabled          bool           `json:"enabled"`
	Volume           *idRef         `json:"volume"`
	DBRepository     string         `json:"dbRepository"`
	JavaDBRepository string         `json:"javaDbRepository"`
	Schedule         map[string]any `json:"schedule"`
	Notification     map[string]any `json:"notification"`
	CPULimit         float64        `json:"cpuLimit"`
	MemoryLimit      string         `json:"memoryLimit"`
}

type idRef struct {
	ID string `json:"id"`
}

// switchScanning switches image scanning on with a volume, or off.
func switchScanning(t *testing.T, enabled bool, volumeID string) error {
	t.Helper()
	var current struct {
		Data imageScanSettings `json:"data"`
	}
	if err := tryCall(http.MethodGet, "system/settings/image-scan", nil, &current); err != nil {
		return err
	}
	s := current.Data
	volume := map[string]string{"id": volumeID}
	if volumeID == "" && s.Volume != nil {
		volume["id"] = s.Volume.ID
	}
	return tryCall(http.MethodPut, "system/settings/image-scan", map[string]any{
		"updateVer": s.UpdateVer, "enabled": enabled, "volume": volume,
		"dbRepository": s.DBRepository, "javaDbRepository": s.JavaDBRepository,
		"schedule": map[string]any{"interval": s.Schedule["interval"]}, "notification": s.Notification,
		"cpuLimit": s.CPULimit, "memoryLimit": s.MemoryLimit,
	}, nil)
}

// scanning switches image scanning on with a volume of the test's, on the node
// HivePaaS runs on, and off again when the test ends, then deletes the volume.
func scanning(t *testing.T) {
	t.Helper()
	var made struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	call(t, http.MethodPost, "cluster/volumes", map[string]any{"name": "e2e-trivy-" + runID, "driver": "local",
		"nodeId": "current", "inheritable": true}, &made)
	require.NoError(t, switchScanning(t, true, made.Data.ID))
	t.Cleanup(func() {
		if err := switchScanning(t, false, ""); err != nil {
			t.Logf("switching image scanning off: %v", err)
		}
		// Docker refuses a volume a container still has: the scan's goes a
		// little after it ends.
		for range 30 {
			err := tryCall(http.MethodDelete, "cluster/volumes/"+made.Data.ID, nil, nil)
			if err == nil || !strings.Contains(err.Error(), ": 409 ") {
				return
			}
			time.Sleep(2 * time.Second)
		}
	})
}

// Image scanning as a CI job runs it: deploy, scan and wait, and fail on what
// the app runs - until an ignore sets it aside.
func TestImageScanGate(t *testing.T) {
	t.Parallel()
	scanning(t)
	p := project(t, "scan")
	old := p.app("old")
	p.must("app", "create", "old", "--image", oldAlpine, "--command", "sleep 3600")

	// The first scan on a volume fetches Trivy's DB; one that came before it
	// had it failed, and is asked again.
	var r result
	for range 2 {
		if r = old.run("app", "scan", "--wait", "--fail-on", "critical"); r.code == 11 {
			break
		}
	}
	require.Equal(t, 11, r.code, r.String())
	assert.Contains(t, r.stdout, oldCVE)
	assert.Contains(t, r.stdout, "(unsupported)")
	assert.Contains(t, r.stderr, "old runs 1 fixable critical vulnerability that no ignore sets aside")

	scan := jsonOf[struct {
		Findings []struct {
			VulnID       string `json:"vulnId"`
			FixedVersion string `json:"fixedVersion"`
		} `json:"findings"`
	}](t, old.must("app", "vulns", "--severity", "critical", "-o", "json"))
	require.NotEmpty(t, scan.Findings)
	assert.Equal(t, oldCVE, scan.Findings[0].VulnID)
	assert.Equal(t, "2.10.7-r0", scan.Findings[0].FixedVersion)

	call(t, http.MethodPost, old.appPath()+"/vulnerability-ignores", map[string]any{"vulnId": oldCVE,
		"package": "apk-tools", "reason": "apk never runs in the container", "expireAt": nil}, nil)
	r = old.run("app", "vulns", "--fail-on", "critical")
	assert.Zero(t, r.code, "an ignore sets it aside: %s", r)
	r = old.must("app", "vulns", "--severity", "critical", "--ignored")
	assert.Contains(t, r.stdout, "apk never runs in the container")
	assert.Contains(t, r.stdout, "this app")
}
