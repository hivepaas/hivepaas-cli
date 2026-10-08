// Command releasemanifest adds a release to release.json, the list of the CLI's
// releases that `hivepaas update` installs from once it is signed: the
// release's archives and their SHA-256, from the checksums.txt of its draft, and
// the release as its channel's current one.
//
//	go run ./tools/releasemanifest -tag v0.2.0 [-checksums checksums.txt] [-date 2026-10-20] [-file release.json]
//
// Without -checksums it reads the draft's with `gh release download`, which a
// draft needs: signed in, with access to the repository.
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/hivepaas/hivepaas-cli/internal/selfupdate"
)

const (
	repo = "hivepaas/hivepaas-cli"
	// fileMode is release.json's: a file of the repository.
	fileMode = 0o644
)

// platforms are the builds .goreleaser.yaml makes: a release lists every one.
var platforms = []string{
	"darwin_amd64", "darwin_arm64", "linux_amd64", "linux_arm64", "windows_amd64", "windows_arm64",
}

func main() {
	tag := flag.String("tag", "", "the release: v0.2.0")
	checksums := flag.String("checksums", "", "the release's checksums.txt; its draft's when left out")
	date := flag.String("date", time.Now().UTC().Format(time.DateOnly), "the release date")
	file := flag.String("file", "release.json", "the list to add it to")
	flag.Parse()
	if err := run(*tag, *checksums, *date, *file); err != nil {
		fmt.Fprintln(os.Stderr, "releasemanifest:", err)
		os.Exit(1)
	}
}

func run(tag, checksumsFile, date, file string) error {
	v, err := selfupdate.ParseVersion(tag)
	if err != nil || v.String() != tag {
		return fmt.Errorf("-tag %q is not a release tag such as v0.2.0 or v0.3.0-beta1", tag) //nolint:err113
	}
	if _, err = time.Parse(time.DateOnly, date); err != nil {
		return fmt.Errorf("-date %q is not a date such as 2026-10-20", date) //nolint:err113
	}
	sums, err := readChecksums(tag, checksumsFile)
	if err != nil {
		return err
	}
	m, err := load(file)
	if err != nil {
		return err
	}
	if err = add(m, v, date, sums); err != nil {
		return err
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err //nolint:wrapcheck
	}
	if err = os.WriteFile(file, append(data, '\n'), fileMode); err != nil { //nolint:gosec // a file for the repository
		return err //nolint:wrapcheck
	}
	fmt.Fprintf(os.Stdout, "%s: %s, %d archives, now the %s channel's release\n", file, tag, len(platforms),
		selfupdate.ChannelOf(v))
	fmt.Fprintln(os.Stdout, "Next: review it, then sign it offline - make release-sign CONTEXT=cli in hivepaas "+
		"(docs/RELEASING.md).")
	return nil
}

func readChecksums(tag, path string) ([]byte, error) {
	if path != "" {
		return os.ReadFile(path) //nolint:wrapcheck
	}
	out, err := exec.Command("gh", "release", "download", tag, "--repo", repo, //nolint:gosec // a fixed command
		"--pattern", "checksums.txt", "--output", "-").Output()
	if err != nil {
		return nil, fmt.Errorf("reading the checksums.txt of %s with gh: %w", tag, err)
	}
	return out, nil
}

func load(file string) (*selfupdate.Manifest, error) {
	m := &selfupdate.Manifest{Channels: map[string]string{}, Releases: map[string]selfupdate.Release{}}
	data, err := os.ReadFile(file)
	if errors.Is(err, fs.ErrNotExist) {
		return m, nil
	}
	if err != nil {
		return nil, err //nolint:wrapcheck
	}
	return selfupdate.ParseManifest(data)
}

// add puts the release in the list, with an archive for every platform, and
// makes it its channel's - never moving a channel back to an older release.
func add(m *selfupdate.Manifest, v selfupdate.Version, date string, sums []byte) error {
	byFile := map[string]string{}
	scanner := bufio.NewScanner(bytes.NewReader(sums))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 2 { //nolint:mnd // sha256 and file
			byFile[fields[1]] = fields[0]
		}
	}
	release := selfupdate.Release{ReleaseDate: date, Archives: map[string]selfupdate.Archive{}}
	for _, platform := range platforms {
		name := selfupdate.ArchiveFile(v, platform)
		sum, found := byFile[name]
		if !found {
			return fmt.Errorf("checksums.txt has no %s: is it the checksums.txt of %s?", name, v) //nolint:err113
		}
		release.Archives[platform] = selfupdate.Archive{File: name, SHA256: sum}
	}
	if m.Channels == nil {
		m.Channels = map[string]string{}
	}
	if m.Releases == nil {
		m.Releases = map[string]selfupdate.Release{}
	}
	channel := selfupdate.ChannelOf(v)
	if current, err := selfupdate.ParseVersion(m.Channels[channel]); err == nil && current.Compare(v) > 0 {
		return fmt.Errorf("the %s channel is at %s, newer than %s", channel, current, v) //nolint:err113
	}
	m.Releases[v.String()] = release
	m.Channels[channel] = v.String()
	return m.Validate()
}
