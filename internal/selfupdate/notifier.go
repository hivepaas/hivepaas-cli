package selfupdate

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	stateFile = "state.yaml"
	// checkEvery is how often the list is read for a notice.
	checkEvery = 24 * time.Hour
	// checkTimeout is how long the reading may take.
	checkTimeout = 2 * time.Second
	// finishWait is how long a command that ends first waits for the reading.
	finishWait = time.Second
)

// state is what the notice remembers between runs, in state.yaml beside the
// configuration.
type state struct {
	UpdateCheck struct {
		CheckedAt time.Time `yaml:"checkedAt"`
		// Newest is the newest release of the CLI's channel when it was checked.
		Newest string `yaml:"newest,omitempty"`
	} `yaml:"updateCheck"`
}

// Notifier tells, after a command, that a newer release of the CLI is out. It
// reads the list at most once a day, while the command runs, and never makes
// the command fail.
type Notifier struct {
	Dir     string
	Current Version
	Updater *Updater
	// Command is what updates this CLI: hivepaas update, or brew's.
	Command string
	Now     func() time.Time
}

// Start reads the list in the background when the last reading is a day old.
// finish, called once the command is done, waits for that reading a moment and
// answers the notice to print: nothing when this CLI is the newest.
func (n *Notifier) Start(ctx context.Context) (finish func() string) {
	st := n.load()
	if n.Now().Sub(st.UpdateCheck.CheckedAt) < checkEvery {
		return func() string { return n.notice(st.UpdateCheck.Newest) }
	}
	done := make(chan string, 1)
	go func() {
		ctx, cancel := context.WithTimeout(ctx, checkTimeout)
		defer cancel()
		newest := st.UpdateCheck.Newest
		if m, err := n.Updater.Fetch(ctx); err == nil {
			if v, found := m.Newest(ChannelOf(n.Current)); found {
				newest = v.String()
			}
		}
		// A failed reading counts as one: a machine that cannot reach GitHub is
		// not asked again before tomorrow.
		st.UpdateCheck.CheckedAt, st.UpdateCheck.Newest = n.Now(), newest
		n.save(st)
		done <- newest
	}()
	return func() string {
		select {
		case newest := <-done:
			return n.notice(newest)
		case <-time.After(finishWait):
			return n.notice(st.UpdateCheck.Newest)
		}
	}
}

func (n *Notifier) notice(newest string) string {
	v, err := ParseVersion(newest)
	if err != nil || v.Compare(n.Current) <= 0 {
		return ""
	}
	return fmt.Sprintf("A new release of hivepaas is out: %s (this is %s). Update it: %s",
		strings.TrimPrefix(v.String(), "v"), strings.TrimPrefix(n.Current.String(), "v"), n.Command)
}

// Remember records the newest release, after an update or a check that read it.
func (n *Notifier) Remember(newest Version) {
	st := n.load()
	st.UpdateCheck.CheckedAt, st.UpdateCheck.Newest = n.Now(), newest.String()
	n.save(st)
}

func (n *Notifier) load() state {
	var st state
	data, err := os.ReadFile(filepath.Join(n.Dir, stateFile))
	if err == nil {
		_ = yaml.Unmarshal(data, &st)
	}
	return st
}

// save writes the state whole, through a file renamed into place: two commands
// at once each write a state, never a mix of both. A state it cannot write is a
// notice a day later, nothing worse.
func (n *Notifier) save(st state) {
	data, err := yaml.Marshal(st)
	if err != nil {
		return
	}
	if err = os.MkdirAll(n.Dir, 0o700); err != nil && !errors.Is(err, fs.ErrExist) { //nolint:mnd
		return
	}
	tmp, err := os.CreateTemp(n.Dir, ".state-*")
	if err != nil {
		return
	}
	defer os.Remove(tmp.Name())
	if _, err = tmp.Write(data); err == nil && tmp.Close() == nil {
		_ = os.Rename(tmp.Name(), filepath.Join(n.Dir, stateFile))
		return
	}
	_ = tmp.Close()
}
