package selfupdate

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	// ListURL is where the signed list of releases is read: the release branch,
	// which moves to a release once it is checked.
	ListURL = "https://raw.githubusercontent.com/hivepaas/hivepaas-cli/release/release.signed.json"
	// DownloadURL is where a release's archives are: .../<version>/<file>.
	DownloadURL = "https://github.com/hivepaas/hivepaas-cli/releases/download"

	maxList    = 1 << 20
	maxArchive = 100 << 20
	maxBinary  = 100 << 20

	downloadTimeout = 5 * time.Minute
)

var (
	// ErrChecksum is an archive that is not the one the signed list names.
	ErrChecksum = errors.New("the archive is not the one the signed list names: its SHA-256 differs")
	errNoBinary = errors.New("the archive has no hivepaas binary")
	errTooLarge = errors.New("too large")
)

// Updater reads the signed list of releases and fetches a release's binary.
type Updater struct {
	HTTP *http.Client
	Keys Keys
	// ListURL and DownloadURL are the package's, but in tests.
	ListURL, DownloadURL string
}

// NewUpdater is an updater with the keys this CLI was built with.
func NewUpdater() (*Updater, error) {
	keys, err := EmbeddedKeys()
	if err != nil {
		return nil, err
	}
	return &Updater{
		HTTP: &http.Client{Timeout: downloadTimeout}, Keys: keys, ListURL: ListURL, DownloadURL: DownloadURL,
	}, nil
}

// Fetch is the list of releases, verified.
func (u *Updater) Fetch(ctx context.Context) (*Manifest, error) {
	content, err := u.get(ctx, u.ListURL, maxList)
	if err != nil {
		return nil, fmt.Errorf("reading the list of releases: %w", err)
	}
	data, err := Open(u.Keys, content)
	if err != nil {
		return nil, err
	}
	return ParseManifest(data)
}

// Binary is the hivepaas binary of a release's archive, downloaded and checked
// against the SHA-256 the signed list gives.
func (u *Updater) Binary(ctx context.Context, v Version, archive Archive) ([]byte, error) {
	content, err := u.get(ctx, u.DownloadURL+"/"+v.String()+"/"+archive.File, maxArchive)
	if err != nil {
		return nil, fmt.Errorf("downloading %s: %w", archive.File, err)
	}
	sum := sha256.Sum256(content)
	if hex.EncodeToString(sum[:]) != archive.SHA256 {
		return nil, fmt.Errorf("%s: %w", archive.File, ErrChecksum)
	}
	if strings.HasSuffix(archive.File, ".zip") {
		return fromZip(content, "hivepaas.exe")
	}
	return fromTarGz(content, "hivepaas")
}

func (u *Updater) get(ctx context.Context, url string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err //nolint:wrapcheck // the callers say what they read
	}
	resp, err := u.HTTP.Do(req)
	if err != nil {
		return nil, err //nolint:wrapcheck
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s answered %s", url, resp.Status) //nolint:err113
	}
	content, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err //nolint:wrapcheck
	}
	if int64(len(content)) > limit {
		return nil, fmt.Errorf("%s: %w", url, errTooLarge)
	}
	return content, nil
}

// fromTarGz is the file name at the top of a tar.gz archive, which goreleaser
// puts the binary at.
func fromTarGz(content []byte, name string) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(content))
	if err != nil {
		return nil, fmt.Errorf("reading the archive: %w", err)
	}
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil, errNoBinary
		}
		if err != nil {
			return nil, fmt.Errorf("reading the archive: %w", err)
		}
		if hdr.Name == name && hdr.Typeflag == tar.TypeReg {
			return readLimited(tr)
		}
	}
}

func fromZip(content []byte, name string) ([]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(content), int64(len(content)))
	if err != nil {
		return nil, fmt.Errorf("reading the archive: %w", err)
	}
	for _, f := range zr.File {
		if f.Name != name || !f.Mode().IsRegular() {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, fmt.Errorf("reading the archive: %w", err)
		}
		defer rc.Close()
		return readLimited(rc)
	}
	return nil, errNoBinary
}

func readLimited(r io.Reader) ([]byte, error) {
	binary, err := io.ReadAll(io.LimitReader(r, maxBinary+1))
	if err != nil {
		return nil, fmt.Errorf("reading the archive: %w", err)
	}
	if len(binary) > maxBinary {
		return nil, fmt.Errorf("the binary: %w", errTooLarge)
	}
	return binary, nil
}

// Executable is the running binary's path, its symlinks resolved: the file to
// replace.
func Executable() (string, error) {
	path, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("finding the running binary: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("finding the running binary: %w", err)
	}
	return resolved, nil
}

// ManagedBy is the command that updates a binary something else installed -
// Homebrew, scoop - which a replacement would leave thinking it has the old one.
// Empty for a binary the CLI may replace.
func ManagedBy(path string) string {
	p := strings.ToLower(strings.ReplaceAll(path, `\`, "/"))
	switch {
	case strings.Contains(p, "/cellar/"), strings.Contains(p, "/homebrew/"), strings.Contains(p, "/linuxbrew/"):
		return "brew upgrade hivepaas"
	case strings.Contains(p, "/scoop/apps/"):
		return "scoop update hivepaas"
	}
	return ""
}

// UpdateCommand is the command that updates the binary at path.
func UpdateCommand(path string) string {
	if by := ManagedBy(path); by != "" {
		return by
	}
	return "hivepaas update"
}

// Replace puts binary in place of the file at path, with its mode. It is written
// beside it and renamed over it, so the change is whole or not at all. A running
// executable cannot be replaced on Windows: there the old one is renamed out of
// the way first, to path.old, which RemoveOld removes on a later run.
func Replace(path string, binary []byte, goos string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("the running binary: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".hivepaas-update-*")
	if err != nil {
		return fmt.Errorf("writing next to %s: %w", path, err)
	}
	defer os.Remove(tmp.Name()) // nothing to remove once it is renamed
	if _, err = tmp.Write(binary); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("writing the new binary: %w", err)
	}
	if err = tmp.Close(); err != nil {
		return fmt.Errorf("writing the new binary: %w", err)
	}
	if err = os.Chmod(tmp.Name(), info.Mode().Perm()); err != nil {
		return fmt.Errorf("writing the new binary: %w", err)
	}
	if goos != "windows" {
		if err = os.Rename(tmp.Name(), path); err != nil {
			return fmt.Errorf("replacing %s: %w", path, err)
		}
		return nil
	}
	old := path + ".old"
	_ = os.Remove(old)
	if err = os.Rename(path, old); err != nil {
		return fmt.Errorf("moving %s aside: %w", path, err)
	}
	if err = os.Rename(tmp.Name(), path); err != nil {
		_ = os.Rename(old, path)
		return fmt.Errorf("replacing %s: %w", path, err)
	}
	return nil
}

// RemoveOld removes what a replacement on Windows left beside the binary.
func RemoveOld(path string) {
	_ = os.Remove(path + ".old")
}
