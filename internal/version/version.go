// Package version is the CLI's own version, set when a release is built.
package version

// Version is the CLI's release, set with -ldflags by `make build` and the release
// build; "dev" for a build from source.
var Version = "dev"
