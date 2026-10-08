package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/hivepaas/hivepaas-cli/internal/config"
	"github.com/hivepaas/hivepaas-cli/internal/exitcode"
	"github.com/hivepaas/hivepaas-cli/internal/selfupdate"
	"github.com/hivepaas/hivepaas-cli/internal/version"
)

type updateFlags struct {
	check   bool
	version string
	channel string
}

func (a *App) updateCmd() *cobra.Command {
	var flags updateFlags
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Install a newer release of this CLI",
		Long: "Install the newest release of this CLI on its channel - stable, or beta for a beta - or the one\n" +
			"--version names, which may be older. What is installed is checked against the list of releases\n" +
			"signed with HivePaaS's offline release keys: nothing else is.",
		Args: usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.update(cmd.Context(), flags)
		},
	}
	cmd.Flags().BoolVar(&flags.check, "check", false, "only say whether there is a newer release")
	cmd.Flags().StringVar(&flags.version, "version", "", "install this release: v0.2.0")
	cmd.Flags().StringVar(&flags.channel, "channel", "", "stable or beta: this CLI's own when left out")
	return cmd
}

// updateCheck is what update --check answers with -o json.
type updateCheck struct {
	Current string `json:"current"`
	Newest  string `json:"newest"`
	// Newer says update would install Newest.
	Newer bool `json:"newer"`
}

func (a *App) update(ctx context.Context, flags updateFlags) error {
	current, currentErr := selfupdate.ParseVersion(version.Version)
	if currentErr != nil && flags.version == "" {
		return exitcode.New(exitcode.Usage, "this is a development build (%s): give --version to install a release",
			version.Version)
	}
	path, err := a.exe()
	if err != nil {
		return err
	}
	managedBy := selfupdate.ManagedBy(path)
	if managedBy != "" && !flags.check {
		return exitcode.New(exitcode.Failure, "%s was installed by a package manager: update it with %s", path, managedBy)
	}

	u, err := a.updater()
	if err != nil {
		return err
	}
	m, err := u.Fetch(ctx)
	if err != nil {
		if errors.Is(err, selfupdate.ErrSignature) {
			return exitcode.Wrap(exitcode.Failure, err)
		}
		return exitcode.Wrap(exitcode.Server, err)
	}
	target, err := pickRelease(m, current, flags)
	if err != nil {
		return err
	}
	// A development build installs the release it is given; a release moves to
	// a newer one, or to the one --version names.
	install := true
	switch {
	case currentErr != nil:
	case flags.version != "":
		install = target.Compare(current) != 0
	default:
		install = target.Compare(current) > 0
		a.notifier(path).Remember(target)
	}

	if a.printer.Structured() && flags.check {
		return a.printer.Data(updateCheck{Current: version.Version, Newest: target.String(), Newer: install})
	}
	switch {
	case !install && flags.version != "":
		a.printer.Infof("hivepaas %s is the one installed.", version.Version)
		return nil
	case !install:
		a.printer.Infof("hivepaas %s is the newest release.", version.Version)
		return nil
	case flags.check:
		a.printer.Infof("hivepaas %s is out (this is %s): %s installs it.", plain(target), version.Version,
			selfupdate.UpdateCommand(path))
		return nil
	}

	archive, found := m.Releases[target.String()].Archives[selfupdate.Platform(runtime.GOOS, runtime.GOARCH)]
	if !found {
		return exitcode.New(exitcode.NotFound, "hivepaas %s has no build for %s/%s", plain(target), runtime.GOOS,
			runtime.GOARCH)
	}
	a.printer.Infof("Downloading hivepaas %s ...", plain(target))
	binary, err := u.Binary(ctx, target, archive)
	if err != nil {
		if errors.Is(err, selfupdate.ErrChecksum) {
			return exitcode.Wrap(exitcode.Failure, fmt.Errorf("%w; nothing was changed", err))
		}
		return exitcode.Wrap(exitcode.Server, err)
	}
	if err = selfupdate.Replace(path, binary, runtime.GOOS); err != nil {
		if errors.Is(err, fs.ErrPermission) {
			return exitcode.New(exitcode.Failure, "%s cannot be written by you: sudo hivepaas update would", filepath.Dir(path))
		}
		return err
	}
	a.printer.Successf("Updated hivepaas %s -> %s (%s).", version.Version, plain(target), path)
	return nil
}

// pickRelease is the release update installs: the one --version names, which
// may be older, or the newest of the channel.
func pickRelease(m *selfupdate.Manifest, current selfupdate.Version, flags updateFlags) (selfupdate.Version, error) {
	if flags.version != "" {
		v, err := selfupdate.ParseVersion(flags.version)
		if err != nil {
			return v, exitcode.Wrap(exitcode.Usage, err)
		}
		if _, found := m.Releases[v.String()]; !found {
			names := make([]string, 0, len(m.Releases))
			for _, r := range m.Versions() {
				names = append(names, r.String())
			}
			return v, exitcode.New(exitcode.NotFound, "%s is not a release of the CLI: %s", v, strings.Join(names, ", "))
		}
		return v, nil
	}
	channel := flags.channel
	switch channel {
	case "":
		channel = selfupdate.ChannelOf(current)
	case selfupdate.ChannelStable, selfupdate.ChannelBeta:
	default:
		return selfupdate.Version{}, exitcode.New(exitcode.Usage, "--channel takes stable or beta, not %q", channel)
	}
	v, found := m.Newest(channel)
	if !found {
		return v, exitcode.New(exitcode.NotFound, "the %s channel has no release yet", channel)
	}
	return v, nil
}

func isTerminal(w io.Writer) bool {
	file, ok := w.(*os.File)
	return ok && term.IsTerminal(int(file.Fd())) //nolint:gosec // a file descriptor fits an int
}

// plain is a version as the CLI says its own: 0.3.0, without the v.
func plain(v selfupdate.Version) string { return strings.TrimPrefix(v.String(), "v") }

// updater reads the signed list; tests point it elsewhere.
func (a *App) updater() (*selfupdate.Updater, error) {
	if a.newUpdater != nil {
		return a.newUpdater()
	}
	return selfupdate.NewUpdater()
}

// exe is the running binary, the one update replaces.
func (a *App) exe() (string, error) {
	if a.executable != nil {
		return a.executable()
	}
	return selfupdate.Executable()
}

func (a *App) notifier(path string) *selfupdate.Notifier {
	current, _ := selfupdate.ParseVersion(version.Version)
	dir, _ := config.Dir(a.getenv)
	u, _ := a.updater()
	return &selfupdate.Notifier{
		Dir: dir, Current: current, Updater: u, Command: selfupdate.UpdateCommand(path), Now: time.Now,
	}
}

// noNotice are the commands after which no notice is printed: update says it
// itself, and completion's output is read by a shell.
var noNotice = map[string]bool{"update": true, "completion": true, "__complete": true, "__completeNoDesc": true}

// startNotice starts the notice of a newer release, when one may be printed: a
// release build, run by a person at a terminal, not in CI. finish answers the
// line to print once the command is done.
func (a *App) startNotice(ctx context.Context, root *cobra.Command, args []string) (finish func() string) {
	none := func() string { return "" }
	if _, err := selfupdate.ParseVersion(version.Version); err != nil ||
		a.getenv("CI") != "" || a.getenv("HIVEPAAS_NO_UPDATE_NOTIFIER") != "" || !isTerminal(a.stderr) {
		return none
	}
	if cmd, _, err := root.Find(args); err == nil && noNotice[cmd.Name()] {
		return none
	}
	path, err := a.exe()
	if err != nil {
		return none
	}
	if runtime.GOOS == "windows" {
		selfupdate.RemoveOld(path)
	}
	n := a.notifier(path)
	if n.Updater == nil {
		return none
	}
	return n.Start(ctx)
}
