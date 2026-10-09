package funccode

import (
	"path"
	"slices"
	"strings"

	"github.com/hivepaas/hivepaas-cli/internal/exitcode"
)

// The runtimes a function runs on, as the server names them.
const (
	Node24    = "node24"
	Bun1      = "bun1"
	Python313 = "python313"
	Go127     = "go127"
)

// Runtimes are the runtimes, in the order they are offered.
var Runtimes = []string{Node24, Bun1, Python313, Go127}

// defaultEntrypoints are the files each runtime finds its handler in when a
// function names none; Go's is the package, the code's root.
var defaultEntrypoints = map[string]string{Node24: "index.js", Bun1: indexTS, Python313: "main.py", Go127: "."}

// indexTS is the file of a handler in TypeScript.
const indexTS = "index.ts"

// javaScriptFiles are the extensions of a handler's file the JavaScript
// runtimes take: Node.js removes TypeScript's types as it loads a file.
var javaScriptFiles = []string{".js", ".mjs", ".cjs", ".ts", ".mts", ".cts"}

// CheckRuntime refuses a runtime the server does not have.
func CheckRuntime(runtime string) error {
	if !slices.Contains(Runtimes, runtime) {
		return exitcode.New(exitcode.Usage, "--runtime takes %s, not %q", strings.Join(Runtimes, ", "), runtime)
	}
	return nil
}

// Template is the runtime's starter code, and the entrypoint it needs: empty
// for the runtime's default. typeScript asks for Node.js's in TypeScript.
func Template(runtime string, typeScript bool) ([]File, string, error) {
	if err := CheckRuntime(runtime); err != nil {
		return nil, "", err
	}
	if typeScript {
		if runtime != Node24 {
			return nil, "", exitcode.New(exitcode.Usage, "--typescript is for node24: %s's starter code is "+
				"what it is", runtime)
		}
		return slices.Clone(typeScriptTemplate), indexTS, nil
	}
	return slices.Clone(templates[runtime]), "", nil
}

// Entrypoint is the file a function's handler is in when the command line
// names none: empty when the runtime's default is among the files - the server
// fills it - else the code's one index file of the runtime's, if it has one
// (index.ts, from init --typescript).
func Entrypoint(runtime string, files []File) string {
	def := defaultEntrypoints[runtime]
	if runtime == Node24 || runtime == Bun1 {
		var found []string
		for _, f := range files {
			if f.Path == def {
				return ""
			}
			if ext := path.Ext(f.Path); f.Path == "index"+ext && slices.Contains(javaScriptFiles, ext) {
				found = append(found, f.Path)
			}
		}
		if len(found) == 1 {
			return found[0]
		}
	}
	return ""
}

// MissingEntrypoint is the handler's file - entry, else the runtime's default -
// when files do not have it; empty when they do, or for Go, whose entrypoint is
// a package.
func MissingEntrypoint(runtime, entry string, files []File) string {
	if runtime == Go127 {
		return ""
	}
	if entry == "" {
		entry = defaultEntrypoints[runtime]
	}
	for _, f := range files {
		if f.Path == entry {
			return ""
		}
	}
	return entry
}

// DefaultEntrypoint is the runtime's handler file when a function names none.
func DefaultEntrypoint(runtime string) string { return defaultEntrypoints[runtime] }
