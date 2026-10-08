// Package link is .hivepaas.json: a directory tied to one app of one
// installation, so that commands run in it need no flags. It holds no secret
// and may be committed.
package link

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// FileName is the link file's name.
const FileName = ".hivepaas.json"

// fileMode is the link's: it holds no secret, and is meant to be committed.
const fileMode = 0o644

// Link is .hivepaas.json.
type Link struct {
	URL     string `json:"url"`
	Project Named  `json:"project"`
	Env     string `json:"env"`
	App     Named  `json:"app"`

	// Path is the file it was read from.
	Path string `json:"-"`
}

// Named is an id, which commands use, and a name, for people reading the file.
type Named struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Find reads the link file of dir or the nearest directory above it; nil when
// there is none.
func Find(dir string) (*Link, error) {
	for {
		path := filepath.Join(dir, FileName)
		data, err := os.ReadFile(path)
		if err == nil {
			l := &Link{Path: path}
			if err = json.Unmarshal(data, l); err != nil {
				return nil, fmt.Errorf("reading %s: %w", path, err)
			}
			return l, nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("reading %s: %w", path, err)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return nil, nil
		}
		dir = parent
	}
}

// Write writes l to dir's link file.
func Write(dir string, l *Link) (string, error) {
	data, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return "", fmt.Errorf("writing the link: %w", err)
	}
	path := filepath.Join(dir, FileName)
	if err = os.WriteFile(path, append(data, '\n'), fileMode); err != nil { //nolint:gosec // no secret
		return "", fmt.Errorf("writing %s: %w", path, err)
	}
	return path, nil
}
