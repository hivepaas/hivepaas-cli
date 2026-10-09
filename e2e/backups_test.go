//go:build e2e

package e2e

import (
	"fmt"
	"net/http"
	pathpkg "path"
	"regexp"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// An app's volume backed up by its data backup job, the snapshot listed, read
// and restored - the app finding its data again - then deleted.
func TestADataBackup(t *testing.T) {
	t.Parallel()
	// Made before the project, so removed after it: the app mounts them.
	data := volume(t, "e2e-data-"+runID, "")
	store := volume(t, "e2e-backups-"+runID, "/hp/e2e-backups-"+runID)
	var repo struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	call(t, http.MethodPost, "settings/backup-repos", map[string]any{"name": "e2e-backups-" + runID,
		"engine": "kopia", "volume": map[string]string{"id": store}, "password": "E2e-backup-Pass1",
		"inheritable": true}, &repo)
	t.Cleanup(func() {
		if err := tryCall(http.MethodDelete, "settings/backup-repos/"+repo.Data.ID, nil, nil); err != nil {
			t.Logf("deleting the backup repository: %v", err)
		}
	})

	p := project(t, "backup")
	keeper := p.app("keeper")
	p.must("app", "create", "keeper")
	path := keeper.appPath()
	var storage struct {
		Data struct {
			UpdateVer int `json:"updateVer"`
		} `json:"data"`
	}
	call(t, http.MethodGet, path+"/storage-settings", nil, &storage)
	call(t, http.MethodPut, path+"/storage-settings", map[string]any{"updateVer": storage.Data.UpdateVer,
		"mounts": []map[string]any{{"type": "volume", "source": data, "target": "/data",
			"volumeOptions": map[string]any{"subpath": "", "noCopy": false}}}}, nil)
	mark := "mark-" + runID
	keeper.must("deploy", "--image", busybox, "--command",
		fmt.Sprintf(`sh -c 'echo %s > /data/mark; echo ready; exec sleep 3600'`, mark))
	// The deployed container, not the placeholder it replaces, has written the
	// mark.
	eventually(t, deployWithin, func() error { return contains(keeper.run("logs", "--tail", "50").stdout, "ready") })

	var mounts struct {
		Data struct {
			Mounts []struct {
				VolumeID string `json:"volumeId"`
			} `json:"mounts"`
		} `json:"data"`
	}
	call(t, http.MethodGet, path+"/storage-settings", nil, &mounts)
	require.Len(t, mounts.Data.Mounts, 1)
	require.NotEmpty(t, mounts.Data.Mounts[0].VolumeID, "the mount is of the app's own directory")
	call(t, http.MethodPost, path+"/sched-jobs", map[string]any{"name": "keep", "jobType": "data-backup",
		"app": map[string]string{"id": pathpkg.Base(path)}, "dataBackup": map[string]any{"source": "volume",
			"sourceVolume":     map[string]string{"id": mounts.Data.Mounts[0].VolumeID},
			"targetRepository": map[string]string{"id": repo.Data.ID}}}, nil)

	keeper.must("backup", "run")
	snapshots := jsonOf[[]struct {
		ShortID string `json:"shortId"`
	}](t, keeper.must("backup", "ls", "-o", "json"))
	require.Len(t, snapshots, 1)
	id := snapshots[0].ShortID
	assert.Contains(t, keeper.must("backup", "ls").stdout, "keep")
	assert.Contains(t, keeper.must("backup", "files", id).stdout, "mark")
	t.Run("a file of it downloaded", func(t *testing.T) {
		r := keeper.run("backup", "download", id, "mark", "-O", "-")
		if assert.Zero(t, r.code, r.String()) {
			assert.Equal(t, mark+"\n", r.stdout)
		}
	})

	// From here each container says what it found, with a name of its own,
	// then changes it: one started after the restore finds the mark again.
	keeper.must("deploy", "--command",
		`sh -c 'echo "found-$(cat /data/mark)-in-$(cat /proc/sys/kernel/random/uuid)"; echo changed > /data/mark; `+
			`exec sleep 3600'`)
	found := regexp.MustCompile(`found-` + mark + `-in-([\w-]+)`)
	var before string
	eventually(t, deployWithin, func() error {
		m := found.FindStringSubmatch(keeper.run("logs", "--tail", "200").stdout)
		if m == nil {
			return fmt.Errorf("no %s found yet", mark)
		}
		before = m[1]
		return nil
	})

	r := keeper.must("backup", "restore", id, "--yes")
	assert.Contains(t, r.stderr, "the app stopped during it")
	eventually(t, deployWithin, func() error {
		for _, m := range found.FindAllStringSubmatch(keeper.run("logs", "--tail", "200").stdout, -1) {
			if m[1] != before {
				return nil
			}
		}
		return fmt.Errorf("no container after %s found %s", before, mark)
	})

	keeper.must("backup", "rm", id, "--yes")
	assert.NotContains(t, keeper.must("backup", "ls").stdout, id)
}

// volume makes a volume of the installation's - a directory of the node, bound,
// when given one - and deletes it when the test ends.
func volume(t *testing.T, name, directory string) string {
	t.Helper()
	body := map[string]any{"name": name, "driver": "local", "nodeId": "current", "inheritable": true}
	if directory != "" {
		body["bindOptions"] = map[string]string{"directory": directory}
	}
	var made struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	call(t, http.MethodPost, "cluster/volumes", body, &made)
	t.Cleanup(func() {
		// Docker refuses one still in use: a project's containers go a little
		// after the project does.
		deadline := time.Now().Add(time.Minute)
		for {
			err := tryCall(http.MethodDelete, "cluster/volumes/"+made.Data.ID, nil, nil)
			if err == nil || time.Now().After(deadline) {
				if err != nil {
					t.Logf("deleting volume %s: %v", name, err)
				}
				return
			}
			time.Sleep(2 * time.Second)
		}
	})
	return made.Data.ID
}
