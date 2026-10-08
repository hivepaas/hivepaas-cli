package cmd

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/hivepaas/hivepaas-cli/internal/exitcode"
	"github.com/hivepaas/hivepaas-cli/internal/link"
)

func (a *App) linkCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "link",
		Short: "Tie this directory to an app, so commands run in it need no flags",
		Long: "Tie this directory to an app: link writes " + link.FileName + ", which commands run here or " +
			"below read for the project, environment and app. It holds no secret and may be committed.",
		Args: usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			// What the flags or a prompt name, not an older link above.
			sel, err := a.selectTargetIgnoringLink(cmd, c)
			if err != nil {
				return err
			}
			wd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("finding the working directory: %w", err)
			}
			path, err := link.Write(wd, &link.Link{
				URL:     c.Target.URL,
				Project: link.Named{ID: sel.Project.Id, Name: sel.Project.Name},
				Env:     sel.Env,
				App:     link.Named{ID: sel.App.Id, Name: sel.App.Name},
			})
			if err != nil {
				return err
			}
			a.printer.Successf("Linked %s to %s / %s / %s on %s (%s).", wd, sel.Project.Name, sel.Env, sel.App.Name,
				hostOf(c.Target.URL), filepath.Base(path))
			return nil
		},
	}
}

func (a *App) unlinkCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "unlink",
		Short: "Remove this directory's link",
		Args:  usageArgs(cobra.NoArgs),
		RunE: func(_ *cobra.Command, _ []string) error {
			err := os.Remove(link.FileName)
			if errors.Is(err, fs.ErrNotExist) {
				return exitcode.New(exitcode.NotFound, "this directory has no %s", link.FileName)
			}
			if err != nil {
				return fmt.Errorf("removing %s: %w", link.FileName, err)
			}
			a.printer.Successf("Removed %s.", link.FileName)
			return nil
		},
	}
}
