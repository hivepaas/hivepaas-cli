// Package funccode is a function's code as the API takes it - its files, by
// path, as text - read from a directory, and the starter code of each runtime.
package funccode

import (
	"cmp"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/hivepaas/hivepaas-cli/internal/exitcode"
)

// What a function's inline code may be: the server's limits.
const (
	MaxFiles   = 100
	MaxSize    = 1 * mb
	MaxPathLen = 255
)

const (
	kb = 1 << 10
	mb = 1 << 20
	// largestNamed is how many of the largest files a refusal for size names.
	largestNamed = 3
)

// pathPattern is a path spelled as the server takes one.
var pathPattern = regexp.MustCompile(`^[A-Za-z0-9._/-]+$`)

// File is one file of a function's code: its path, with / between its parts.
type File struct {
	Path    string
	Content string
}

// Code is a directory's code: its files, sorted by path, their size, and the
// links left out.
type Code struct {
	Files []File
	Size  int64
	Links []string
	// LeftOut are files that are not code, which a function never sends.
	LeftOut []string
}

// Never sent: libraries, git's, HivePaaS's own - .hivepaas is its directory in
// a function's source, .hivepaas.json the CLI's link.
var (
	skippedDirs  = []string{".git", "node_modules", "__pycache__", ".venv", "venv", ".hivepaas"}
	skippedFiles = []string{".hivepaas.json", ".DS_Store"}
)

// Collect reads dir's code, leaving out what a function never sends and what
// dir's .gitignore ignores, and checks it as the server would.
func Collect(dir string) (*Code, error) {
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return nil, exitcode.New(exitcode.Usage, "%s is not a directory", dir)
	}
	var ig *Ignore
	if text, err := os.ReadFile(filepath.Join(dir, ".gitignore")); err == nil {
		ig = ParseIgnore(string(text))
	}
	// Read through a root: nothing is read outside dir, a link's target included.
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", dir, err)
	}
	defer root.Close()
	files := root.FS()
	code := &Code{}
	err = fs.WalkDir(files, ".", func(rel string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		switch {
		case entry.IsDir():
			if slices.Contains(skippedDirs, entry.Name()) || ig.Match(rel, true) {
				return filepath.SkipDir
			}
			return nil
		case slices.Contains(skippedFiles, entry.Name()) || ig.Match(rel, false):
			return nil
		case entry.Name() == ".env" || strings.HasPrefix(entry.Name(), ".env."):
			code.LeftOut = append(code.LeftOut, rel)
			return nil
		case entry.Type()&fs.ModeSymlink != 0:
			code.Links = append(code.Links, rel)
			return nil
		case !entry.Type().IsRegular():
			return nil
		}
		content, err := fs.ReadFile(files, rel)
		if err != nil {
			return err //nolint:wrapcheck // said below, with the file's path
		}
		code.Files = append(code.Files, File{Path: rel, Content: string(content)})
		code.Size += int64(len(content))
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", dir, err)
	}
	return code, check(dir, code)
}

// check refuses code the server would: too many files, too large, a file that
// is not text, a path spelled otherwise.
func check(dir string, code *Code) error {
	if len(code.Files) == 0 {
		return exitcode.New(exitcode.Invalid, "%s has no code to send", dir)
	}
	if len(code.Files) > MaxFiles {
		return exitcode.New(exitcode.Invalid, "%s holds %d files, more than the %d a function's code may: "+
			"leave out what it does not need with a .gitignore", dir, len(code.Files), MaxFiles)
	}
	if code.Size > MaxSize {
		largest := slices.Clone(code.Files)
		slices.SortFunc(largest, func(a, b File) int { return cmp.Compare(len(b.Content), len(a.Content)) })
		names := make([]string, 0, largestNamed)
		for _, f := range largest[:min(largestNamed, len(largest))] {
			names = append(names, fmt.Sprintf("%s (%s)", f.Path, sizeWords(int64(len(f.Content)))))
		}
		return exitcode.New(exitcode.Invalid, "%s's code is %s, more than the 1 MB a function's code may: "+
			"the largest are %s", dir, sizeWords(code.Size), strings.Join(names, ", "))
	}
	var errs []error
	for _, f := range code.Files {
		if len(f.Path) > MaxPathLen || !pathPattern.MatchString(f.Path) {
			errs = append(errs, fmt.Errorf("%q: a path is spelled with A-Z a-z 0-9 . _ / - only, "+
				"in at most %d characters", f.Path, MaxPathLen))
		}
		if !utf8.ValidString(f.Content) || strings.ContainsRune(f.Content, 0) {
			errs = append(errs, fmt.Errorf("%s is not text: a function's code is sent as text", f.Path))
		}
	}
	if len(errs) > 0 {
		return exitcode.Wrap(exitcode.Invalid, errors.Join(errs...))
	}
	return nil
}

// sizeWords is a size as people say it: 900 KB, 1.2 MB.
func sizeWords(n int64) string {
	switch {
	case n >= mb:
		return fmt.Sprintf("%.1f MB", float64(n)/mb)
	case n >= kb:
		return fmt.Sprintf("%d KB", n/kb)
	}
	return fmt.Sprintf("%d B", n)
}
