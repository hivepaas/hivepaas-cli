package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/hivepaas/hivepaas-cli/internal/api"
	"github.com/hivepaas/hivepaas-cli/internal/client"
)

func (a *App) deploySettingsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "settings",
		Short: "Show what an app deploys: its image or its repository, and its commands",
		Args:  usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			sel, err := a.selectTarget(cmd.Context(), c, scopeApp)
			if err != nil {
				return err
			}
			resp, err := c.GetAppDeploymentSettingsWithResponse(cmd.Context(), sel.Project.Id, sel.Env, sel.App.Id)
			if err = client.Check(resp, err); err != nil {
				return err
			}
			settings := resp.JSON200.Data
			if a.printer.Structured() {
				return a.printer.Data(settings)
			}
			if settings == nil || settings.ActiveMethod == "" {
				a.printer.Infof("%s has never been deployed: hivepaas deploy --image <image>%s, or --repo <url>",
					sel.where(), sel.flags())
				return nil
			}
			for _, row := range settingsRows(settings) {
				fmt.Fprintf(a.stdout, "%-21s%s\n", row[0]+":", row[1])
			}
			return nil
		},
	}
}

// settingsRows are the deployment settings as `deploy settings` shows them.
func settingsRows(s *api.AppsettingsdtoDeploymentSettingsResp) [][2]string {
	rows := [][2]string{{"Method", methodName(s.ActiveMethod)}}
	switch s.ActiveMethod { //nolint:exhaustive // a function's source is the dashboard's
	case api.DeploymentMethodImage:
		if src := s.ImageSource; src != nil {
			rows = append(rows, [2]string{"Image", src.Image},
				[2]string{"Registry credential", refOf(src.RegistryAuth).String()})
		}
	case api.DeploymentMethodRepo:
		if src := s.RepoSource; src != nil {
			rows = append(rows, repoRows(src)...)
		}
	}
	for _, field := range []struct {
		label string
		value *string
	}{
		{"Entrypoint", s.Entrypoint}, {"Command", s.Command}, {"Working directory", s.WorkingDir},
		{"Pre-deploy command", s.PreDeploymentCommand}, {"Post-deploy command", s.PostDeploymentCommand},
	} {
		if textOf(field.value) != "" {
			rows = append(rows, [2]string{field.label, *field.value})
		}
	}
	return rows
}

func repoRows(src *api.AppsettingsdtoDeploymentRepoSourceResp) [][2]string {
	commit := "follows " + refWords(src.RepoRef)
	if src.CommitHash != "" {
		commit = src.CommitHash + ", pinned"
	}
	dockerfile := dockerfileWords(api.AppsettingsdtoDeploymentDockerfileReq{})
	if src.Dockerfile != nil {
		dockerfile = dockerfileWords(api.AppsettingsdtoDeploymentDockerfileReq(*src.Dockerfile))
	}
	var options api.AppsettingsdtoDeploymentRepoOptionsResp
	if src.RepoOptions != nil {
		options = *src.RepoOptions
	}
	return [][2]string{
		{"Repository", src.RepoURL},
		{"Ref", refWords(src.RepoRef)},
		{"Commit", commit},
		{"Git credential", refOf(src.Credentials).String()},
		{"Dockerfile", dockerfile},
		{"Submodules", onOff(options.GitSubmodulesEnabled)},
		{"LFS", onOff(options.GitLfsEnabled)},
		{"Push to", refOf(src.PushToRegistry).String()},
		{"Auto-deploy", onOff(src.AutoDeploy)},
	}
}
