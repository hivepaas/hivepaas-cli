package cmd

import (
	"io"
	"strings"

	"github.com/spf13/cobra"
)

// Tree is the CLI's commands as `hivepaas --help` shows them, the completion
// command included, for what describes them: the docs' command reference. It is
// only read; nothing in it runs.
func Tree() *cobra.Command {
	a := &App{
		stdin: strings.NewReader(""), stdout: io.Discard, stderr: io.Discard,
		getenv: func(string) string { return "" },
	}
	root := a.rootCmd()
	root.InitDefaultCompletionCmd()
	return root
}
