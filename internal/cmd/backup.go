package cmd

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/hivepaas/hivepaas-cli/internal/api"
	"github.com/hivepaas/hivepaas-cli/internal/client"
	"github.com/hivepaas/hivepaas-cli/internal/exitcode"
	"github.com/hivepaas/hivepaas-cli/internal/output"
	"github.com/hivepaas/hivepaas-cli/internal/resolve"
)

// snapshotsListed is how many of an app's snapshots are read, the newest first.
const snapshotsListed = 100

// defaultRestoreTimeout is how long restore waits for its task.
const defaultRestoreTimeout = 2 * time.Hour

func (a *App) backupCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "backup",
		Aliases: []string{"backups"},
		Short:   "An app's data backups: snapshots of its volumes, and what restores them",
		Long: "An app's data backups: snapshots of its volumes, made by its data backup jobs into backup\n" +
			"repositories. A snapshot is given by its id or short id, as ls shows them.",
	}
	cmd.AddCommand(a.backupLsCmd(), a.backupRunCmd(), a.backupFilesCmd(), a.backupDownloadCmd(),
		a.backupRestoreCmd(), a.backupRmCmd())
	return cmd
}

// snapshotList is a page of the snapshots the selected app sees, the newest
// first, of total; and the repositories they are in.
type snapshotList struct {
	items []api.BackupsnapshotdtoBackupSnapshotResp
	repos []api.SettingsBaseSettingResp
	total int
}

// snapshots are the newest of the snapshots the selected app sees: those
// search finds - by short id or description - in repo, when given.
func snapshots(ctx context.Context, c *client.Client, sel *selection, search, repo string) (*snapshotList, error) {
	params := &api.ListAppBackupSnapshotParams{PageLimit: ptr(snapshotsListed)}
	if search != "" {
		params.Search = &search
	}
	if repo != "" {
		params.Repo = &[]string{repo}
	}
	resp, err := c.ListAppBackupSnapshotWithResponse(ctx, sel.Project.Id, sel.Env, sel.App.Id, params)
	if err = client.Check(resp, err); err != nil {
		return nil, err
	}
	list := &snapshotList{}
	if resp.JSON200 == nil {
		return list, nil
	}
	list.items, list.repos = resolve.Deref(resp.JSON200.Data), resolve.Deref(resp.JSON200.Repos)
	slices.SortStableFunc(list.items, func(x, y api.BackupsnapshotdtoBackupSnapshotResp) int {
		return strings.Compare(y.Time, x.Time)
	})
	list.total = len(list.items)
	if m := resp.JSON200.Meta; m != nil && m.Page != nil {
		list.total = max(list.total, m.Page.Total)
	}
	return list, nil
}

// shortIDLen is how long a snapshot's short id is: the start of the
// repository's id of it.
const shortIDLen = 8

// snapshot is the snapshot of the selected app input names, however old: its
// short id or the repository's id of it, which the server looks for, or the
// record's id.
func snapshot(ctx context.Context, c *client.Client, sel *selection, input string) (
	*api.BackupsnapshotdtoBackupSnapshotResp, error,
) {
	searches := []string{input}
	if len(input) > shortIDLen {
		searches = append(searches, input[:shortIDLen])
	}
	for _, search := range searches {
		list, err := snapshots(ctx, c, sel, search, "")
		if err != nil {
			return nil, err
		}
		for i := range list.items {
			if s := &list.items[i]; s.Id == input || s.ShortId == input || s.SnapshotId == input {
				return s, nil
			}
		}
	}
	notFound := exitcode.New(exitcode.NotFound, "%s has no snapshot %s: hivepaas backup ls%s lists them",
		sel.App.Name, input, sel.flags())
	resp, err := c.GetAppBackupSnapshotWithResponse(ctx, sel.Project.Id, sel.Env, sel.App.Id, input)
	if err != nil {
		return nil, client.Unreachable(err)
	}
	if resp.StatusCode() != http.StatusOK || resp.JSON200 == nil || resp.JSON200.Data == nil {
		return nil, notFound
	}
	return resp.JSON200.Data, nil
}

func (a *App) backupLsCmd() *cobra.Command {
	var repo string
	cmd := &cobra.Command{
		Use:   "ls",
		Short: "List an app's snapshots, the newest first",
		Args:  usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			sel, err := a.selectTarget(cmd.Context(), c, scopeApp)
			if err != nil {
				return err
			}
			list, err := snapshots(cmd.Context(), c, sel, "", "")
			if err != nil {
				return err
			}
			if repo != "" {
				found, err := resolve.Setting("backup repository", repo, " of "+sel.App.Name, list.repos,
					func(r api.SettingsBaseSettingResp) (string, string) { return r.Id, r.Name })
				if err != nil {
					return err
				}
				if list, err = snapshots(cmd.Context(), c, sel, "", found.Id); err != nil {
					return err
				}
				list.items = slices.DeleteFunc(list.items, func(s api.BackupsnapshotdtoBackupSnapshotResp) bool {
					return s.Repo == nil || s.Repo.Id != found.Id
				})
			}
			if list.total > len(list.items) {
				a.printer.Infof("Showing the newest %d of %d snapshots; --repo narrows them.", len(list.items),
					list.total)
			}
			return a.showSnapshots(list.items)
		},
	}
	cmd.Flags().StringVar(&repo, "repo", "", "only the snapshots of this backup repository")
	return cmd
}

func (a *App) showSnapshots(list []api.BackupsnapshotdtoBackupSnapshotResp) error {
	if a.printer.Structured() {
		return a.printer.Data(list)
	}
	now := time.Now()
	rows := make([][]string, 0, len(list))
	for _, s := range list {
		job := "-"
		if s.Job != nil {
			job = textOf(s.Job.Name)
		}
		repo := "-"
		if s.Repo != nil {
			repo = s.Repo.Name
		}
		rows = append(rows, []string{output.Ago(s.Time, now), s.ShortId, strings.Join(resolve.Deref(s.Paths), ", "),
			sizeOf(int64(s.SizeBytes)), job, repo, orNone(strings.Join(resolve.Deref(s.Tags), ", "))})
	}
	return a.printer.Table([]string{"TIME", "ID", "PATHS", colSize, "JOB", "REPOSITORY", "TAGS"}, rows)
}

func (a *App) backupRunCmd() *cobra.Command {
	var noWait bool
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "run [JOB]",
		Short: "Run a data backup job now, and wait for it",
		Long: "Run one of the app's data backup jobs now, and wait for it as job run does: the app's one data\n" +
			"backup job when it has one, else the one JOB names.",
		Args: usageArgs(cobra.MaximumNArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			name := ""
			if len(args) == 1 {
				name = args[0]
			} else {
				var err error
				if name, err = a.backupJob(ctx); err != nil {
					return err
				}
			}
			return a.jobRun(ctx, name, noWait, timeout)
		},
	}
	cmd.Flags().BoolVar(&noWait, "no-wait", false, "start the run and return")
	cmd.Flags().DurationVar(&timeout, "timeout", defaultJobTimeout, "how long to wait for the run")
	return cmd
}

// backupJob is the name of the app's one data backup job.
func (a *App) backupJob(ctx context.Context) (string, error) {
	c, err := a.client()
	if err != nil {
		return "", err
	}
	sel, err := a.selectTarget(ctx, c, scopeApp)
	if err != nil {
		return "", err
	}
	jobs, err := schedJobs(ctx, c, sel)
	if err != nil {
		return "", err
	}
	var names []string
	for _, j := range jobs {
		if j.JobType == api.SchedJobTypeDataBackup {
			names = append(names, j.Name)
		}
	}
	switch len(names) {
	case 0:
		return "", exitcode.New(exitcode.NotFound, "%s has no data backup job: its Scheduled Jobs make one",
			sel.App.Name)
	case 1:
		return names[0], nil
	}
	return "", exitcode.New(exitcode.Usage, "%s has %d data backup jobs - name one: %s", sel.App.Name, len(names),
		strings.Join(names, ", "))
}

func (a *App) backupFilesCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "files SNAPSHOT [PATH]",
		Short: "List what a snapshot holds, at a path",
		Args:  usageArgs(cobra.RangeArgs(1, 2)), //nolint:mnd // the snapshot, and a path
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			c, err := a.client()
			if err != nil {
				return err
			}
			sel, err := a.selectTarget(ctx, c, scopeApp)
			if err != nil {
				return err
			}
			s, err := snapshot(ctx, c, sel, args[0])
			if err != nil {
				return err
			}
			params := &api.ListAppBackupSnapshotEntriesParams{}
			if len(args) == 2 { //nolint:mnd // a path given
				params.Path = &args[1]
			}
			resp, err := c.ListAppBackupSnapshotEntriesWithResponse(ctx, sel.Project.Id, sel.Env, sel.App.Id, s.Id,
				params)
			if err = client.Check(resp, err); err != nil {
				return err
			}
			entries := resolve.Deref(resp.JSON200.Data)
			if a.printer.Structured() {
				return a.printer.Data(entries)
			}
			rows := make([][]string, 0, len(entries))
			for _, e := range entries {
				name := e.Name
				if deref(e.Dir) {
					name += "/"
				}
				rows = append(rows, []string{name, sizeOf(int64(e.SizeBytes))})
			}
			return a.printer.Table([]string{colName, colSize}, rows)
		},
	}
}

// downloadTimeout is how long a file of a snapshot may take to come.
const downloadTimeout = 2 * time.Hour

func (a *App) backupDownloadCmd() *cobra.Command {
	var to string
	var force bool
	cmd := &cobra.Command{
		Use:   "download SNAPSHOT PATH",
		Short: "Write one file of a snapshot to a local file, or stdout",
		Long: "Write the file at PATH of a snapshot to -O FILE - by default PATH's last part, here - or with\n" +
			"-O - to stdout. A file that is there is replaced with --force alone, once the whole file has come.",
		Args: usageArgs(cobra.ExactArgs(2)), //nolint:mnd // the snapshot and the path
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.backupDownload(cmd.Context(), args[0], args[1], to, force)
		},
	}
	cmd.Flags().StringVarP(&to, "output-file", "O", "", "the file to write; - for stdout")
	cmd.Flags().BoolVar(&force, "force", false, "replace the file when it exists")
	return cmd
}

func (a *App) backupDownload(ctx context.Context, id, file, to string, force bool) error {
	if to == "" {
		to = path.Base(file)
	}
	if _, err := os.Lstat(to); err == nil && to != "-" && !force {
		return exitcode.New(exitcode.Usage, "%s exists: give --force to replace it", to)
	}
	c, err := a.clientWithTimeout(downloadTimeout)
	if err != nil {
		return err
	}
	sel, err := a.selectTarget(ctx, c, scopeApp)
	if err != nil {
		return err
	}
	s, err := snapshot(ctx, c, sel, id)
	if err != nil {
		return err
	}
	resp, err := c.DownloadAppBackupSnapshotFile(ctx, sel.Project.Id, sel.Env, sel.App.Id, s.Id,
		&api.DownloadAppBackupSnapshotFileParams{Path: file})
	if err != nil {
		return client.Unreachable(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 { //nolint:mnd // not a success
		body, _ := io.ReadAll(resp.Body)
		return client.CheckStatus(resp.StatusCode, body)
	}
	if to == "-" {
		if _, err = io.Copy(a.stdout, resp.Body); err != nil {
			return fmt.Errorf("downloading %s: %w", file, err)
		}
		return nil
	}
	n, err := writeWhole(to, resp.Body)
	if err != nil {
		return fmt.Errorf("downloading %s to %s: %w", file, to, err)
	}
	a.printer.Successf("Wrote %s, %s.", to, sizeOf(n))
	return nil
}

// writeWhole writes what r gives to name once all of it has come: into a file
// beside it first, which takes its place; nothing is left when r fails.
func writeWhole(name string, r io.Reader) (int64, error) {
	tmp, err := os.CreateTemp(filepath.Dir(name), "."+filepath.Base(name)+".*")
	if err != nil {
		return 0, err //nolint:wrapcheck // said by the caller
	}
	n, err := io.Copy(tmp, r)
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), name)
	}
	if err != nil {
		_ = os.Remove(tmp.Name())
		return 0, err //nolint:wrapcheck // said by the caller
	}
	return n, nil
}

type backupRestoreFlags struct {
	volume, subpath, path, mode, to string
	stopApp, yes, noWait            bool
	timeout                         time.Duration
}

func (a *App) backupRestoreCmd() *cobra.Command {
	var flags backupRestoreFlags
	cmd := &cobra.Command{
		Use:   "restore SNAPSHOT",
		Short: "Write a snapshot back into an app's volume, and wait for it",
		Long: "Write a snapshot back into an app's volume - where its job backed it up, when the data goes\n" +
			"back into the app it came from - and wait for the task, following its log. replace, the mode by\n" +
			"default, makes the directory what it was, the old one kept aside, the app stopped during it;\n" +
			"overwrite writes over what is there. It changes data: asked at a terminal, --yes elsewhere.",
		Example: "  hivepaas backup restore a1b2c3d4\n" +
			"  hivepaas backup restore a1b2c3d4 --to api-staging --volume data --subpath uploads --yes",
		Args: usageArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.backupRestore(cmd.Context(), args[0], flags)
		},
	}
	f := cmd.Flags()
	f.StringVar(&flags.volume, "volume", "", "the volume to write into, by name or id")
	f.StringVar(&flags.subpath, "subpath", "", "where in the volume")
	f.StringVar(&flags.path, "path", "", "a directory of the snapshot to restore alone")
	f.StringVar(&flags.mode, "mode", string(api.BackupRestoreModeReplace), "replace or overwrite")
	f.StringVar(&flags.to, "to", "", "another app of the environment to write into")
	f.BoolVar(&flags.stopApp, "stop-app", false, "stop the app during an overwrite, start it after; replace always does")
	f.BoolVarP(&flags.yes, "yes", "y", false, "do not ask: for a script")
	f.BoolVar(&flags.noWait, "no-wait", false, "start the restore and return")
	f.DurationVar(&flags.timeout, "timeout", defaultRestoreTimeout, "how long to wait for it")
	return cmd
}

func (a *App) backupRestore(ctx context.Context, id string, flags backupRestoreFlags) error {
	mode := api.BaseBackupRestoreMode(flags.mode)
	if mode != api.BackupRestoreModeReplace && mode != api.BackupRestoreModeOverwrite {
		return exitcode.New(exitcode.Usage, "--mode takes replace or overwrite, not %q", flags.mode)
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	sel, err := a.selectTarget(ctx, c, scopeApp)
	if err != nil {
		return err
	}
	s, err := snapshot(ctx, c, sel, id)
	if err != nil {
		return err
	}
	target := sel
	if flags.to != "" {
		app, err := resolve.New(c).App(ctx, sel.Project.Id, sel.Project.Name, sel.Env, flags.to)
		if err != nil {
			return err
		}
		target = &selection{Project: sel.Project, Env: sel.Env, App: app}
	}
	// replace moves the directory aside: the app is stopped for it, as the
	// server has it.
	stopApp := flags.stopApp || mode == api.BackupRestoreModeReplace
	req := api.BackupsnapshotdtoRestoreBackupSnapshotReq{Mode: mode, SnapshotPath: flags.path, StopApp: stopApp,
		TargetApp: api.BasedtoObjectIDReq{Id: target.App.Id}, Subpath: flags.subpath}
	volumeName, err := restoreVolume(ctx, c, sel, target, s, flags, &req)
	if err != nil {
		return err
	}
	what := fmt.Sprintf("snapshot %s into %s", s.ShortId, target.App.Name)
	if volumeName != "" {
		what += ", volume " + volumeName
		if req.Subpath != "" {
			what += " at " + req.Subpath
		}
	}
	what += " (" + string(mode)
	if stopApp {
		what += ", the app stopped during it"
	}
	what += ")"
	if err = a.confirm(ctx, "restoring "+what, target.App.Name, flags.yes); err != nil {
		return err
	}
	resp, err := c.RestoreAppBackupSnapshotWithResponse(ctx, sel.Project.Id, sel.Env, sel.App.Id, s.Id, req)
	if err = client.Check(resp, err); err != nil {
		return err
	}
	if resp.JSON200 == nil || resp.JSON200.Data == nil || resp.JSON200.Data.Task == nil {
		return exitcode.New(exitcode.Server, "the server answered no task for the restore")
	}
	ref := taskRef{sel: target, id: resp.JSON200.Data.Task.Id}
	a.printer.Infof("Restoring %s, task %s", what, ref.id)
	return a.awaitTask(ctx, c, ref, flags.noWait, flags.timeout)
}

// restoreVolume sets the volume a restore writes into, and answers its name:
// the one --volume names, else where the snapshot's job backed up when the data
// goes back into its app; none otherwise, the server's to refuse. In the job's
// own volume, the subpath is the job's unless --subpath says another.
func restoreVolume(ctx context.Context, c *client.Client, sel, target *selection,
	s *api.BackupsnapshotdtoBackupSnapshotResp, flags backupRestoreFlags,
	req *api.BackupsnapshotdtoRestoreBackupSnapshotReq,
) (string, error) {
	fromJob := s.Job != nil && s.App != nil && s.App.Id == target.App.Id
	if flags.volume == "" && !fromJob {
		return "", nil
	}
	volumes, err := resolve.New(c).Volumes(ctx, sel.Project.Id)
	if flags.volume != "" {
		if err != nil {
			return "", err
		}
		v, err := resolve.Volume(volumes, sel.Project.Name, flags.volume)
		if err != nil {
			return "", err
		}
		req.Volume.Id = v.Id
	} else {
		req.Volume.Id = textOf(s.Job.SourceVolumeId)
	}
	if fromJob && flags.subpath == "" && req.Volume.Id == textOf(s.Job.SourceVolumeId) {
		req.Subpath = textOf(s.Job.SourceVolumeSubpath)
	}
	// Said by its name when it is found.
	for _, v := range volumes {
		if v.Id == req.Volume.Id {
			return v.Name, nil
		}
	}
	return req.Volume.Id, nil
}

// confirm asks to go on with what changes data - typing name, at a terminal -
// unless yes; a script gives --yes.
func (a *App) confirm(ctx context.Context, what, name string, yes bool) error {
	if yes {
		return nil
	}
	if !a.interactive() {
		return exitcode.New(exitcode.Usage, "%s: give --yes to go on without being asked", what)
	}
	a.printer.Warnf("this is %s", what)
	typed, err := a.prompt(ctx, fmt.Sprintf("Type %s to go on: ", name), "--yes")
	if err != nil {
		return err
	}
	if typed != name {
		a.printer.Infof("Nothing was done.")
		return exitcode.Reported(exitcode.Failure)
	}
	return nil
}

func (a *App) backupRmCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "rm SNAPSHOT",
		Short: "Delete a snapshot from its repository",
		Args:  usageArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			c, err := a.client()
			if err != nil {
				return err
			}
			sel, err := a.selectTarget(ctx, c, scopeApp)
			if err != nil {
				return err
			}
			s, err := snapshot(ctx, c, sel, args[0])
			if err != nil {
				return err
			}
			if err = a.confirm(ctx, "deleting snapshot "+s.ShortId, s.ShortId, yes); err != nil {
				return err
			}
			resp, err := c.DeleteAppBackupSnapshotWithResponse(ctx, sel.Project.Id, sel.Env, sel.App.Id, s.Id)
			if err = client.Check(resp, err); err != nil {
				return err
			}
			a.printer.Successf("Deleted snapshot %s.", s.ShortId)
			return nil
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask: for a script")
	return cmd
}
