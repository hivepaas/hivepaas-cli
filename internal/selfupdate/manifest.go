package selfupdate

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// The channels a release is offered on.
const (
	ChannelStable = "stable"
	ChannelBeta   = "beta"
)

var (
	platformPattern = regexp.MustCompile(`^[a-z0-9]+_[a-z0-9]+$`)
	sha256Pattern   = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// Manifest is the list of the CLI's releases, release.json: each channel's
// current release, and every release's archives.
type Manifest struct {
	Channels map[string]string  `json:"channels"`
	Releases map[string]Release `json:"releases"`
}

// Release is a release's archives, by platform: darwin_arm64.
type Release struct {
	ReleaseDate string             `json:"releaseDate"`
	Archives    map[string]Archive `json:"archives"`
}

// Archive is an archive of a release, and the SHA-256 it must have.
type Archive struct {
	File   string `json:"file"`
	SHA256 string `json:"sha256"`
}

// Platform is the key of an OS and architecture's archive: darwin_arm64.
func Platform(goos, goarch string) string { return goos + "_" + goarch }

// ArchiveFile is the file name goreleaser gives a release's archive for a
// platform: hivepaas_0.2.0_darwin_arm64.tar.gz.
func ArchiveFile(v Version, platform string) string {
	ext := "tar.gz"
	if strings.HasPrefix(platform, "windows_") {
		ext = "zip"
	}
	return fmt.Sprintf("hivepaas_%s_%s.%s", strings.TrimPrefix(v.String(), "v"), platform, ext)
}

// ParseManifest reads a list and checks it whole: each release's version is one,
// each of its archives is the file goreleaser names for its platform with a
// SHA-256, and each channel names a listed release - a release for stable, which
// never offers a beta. A list that fails any of it is refused, not partly used.
func ParseManifest(data []byte) (*Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("the list of releases: %w", err)
	}
	return &m, m.Validate()
}

// Validate checks the list as ParseManifest does.
func (m *Manifest) Validate() error {
	for name, release := range m.Releases {
		v, err := ParseVersion(name)
		if err != nil {
			return fmt.Errorf("the list of releases: %w", err)
		}
		if v.String() != name {
			return fmt.Errorf("the list of releases: %q is written %q", name, v.String()) //nolint:err113
		}
		if len(release.Archives) == 0 {
			return fmt.Errorf("the list of releases: %s has no archives", name) //nolint:err113
		}
		for platform, archive := range release.Archives {
			if !platformPattern.MatchString(platform) {
				return fmt.Errorf("the list of releases: %s: %q is not a platform", name, platform) //nolint:err113
			}
			if archive.File != ArchiveFile(v, platform) {
				return fmt.Errorf("the list of releases: %s %s: the file is %q, not %q", //nolint:err113
					name, platform, archive.File, ArchiveFile(v, platform))
			}
			if !sha256Pattern.MatchString(archive.SHA256) {
				return fmt.Errorf("the list of releases: %s %s: %q is not a SHA-256", name, platform, //nolint:err113
					archive.SHA256)
			}
		}
	}
	for channel, name := range m.Channels {
		if channel != ChannelStable && channel != ChannelBeta {
			return fmt.Errorf("the list of releases: %q is not a channel", channel) //nolint:err113
		}
		if _, found := m.Releases[name]; !found {
			return fmt.Errorf("the list of releases: channel %s is %s, which it does not list", channel, name) //nolint:err113
		}
		if v, _ := ParseVersion(name); channel == ChannelStable && v.IsPrerelease() {
			return fmt.Errorf("the list of releases: channel stable is %s, a beta", name) //nolint:err113
		}
	}
	return nil
}

// Newest is the release a CLI on channel is offered: the stable channel's, or
// for beta the newer of beta's and stable's - a stable release supersedes the
// betas before it.
func (m *Manifest) Newest(channel string) (Version, bool) {
	var newest Version
	found := false
	candidates := []string{m.Channels[ChannelStable]}
	if channel == ChannelBeta {
		candidates = append(candidates, m.Channels[ChannelBeta])
	}
	for _, name := range candidates {
		v, err := ParseVersion(name)
		if err != nil {
			continue
		}
		if !found || v.Compare(newest) > 0 {
			newest, found = v, true
		}
	}
	return newest, found
}

// Versions are the releases the list has, newest first.
func (m *Manifest) Versions() []Version {
	versions := make([]Version, 0, len(m.Releases))
	for name := range m.Releases {
		if v, err := ParseVersion(name); err == nil {
			versions = append(versions, v)
		}
	}
	sort.Slice(versions, func(i, j int) bool { return versions[i].Compare(versions[j]) > 0 })
	return versions
}

// ChannelOf is the channel a CLI of version v follows: beta for a beta.
func ChannelOf(v Version) string {
	if v.IsPrerelease() {
		return ChannelBeta
	}
	return ChannelStable
}
