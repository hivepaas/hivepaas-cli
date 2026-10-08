// Command hivepaas is the command-line client of a HivePaaS installation.
package main

import (
	"os"

	"github.com/hivepaas/hivepaas-cli/internal/cmd"
)

func main() {
	os.Exit(cmd.Execute())
}
