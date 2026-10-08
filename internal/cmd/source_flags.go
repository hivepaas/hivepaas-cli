package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/hivepaas/hivepaas-cli/internal/api"
	"github.com/hivepaas/hivepaas-cli/internal/client"
	"github.com/hivepaas/hivepaas-cli/internal/exitcode"
	"github.com/hivepaas/hivepaas-cli/internal/resolve"
)

// The flags of each source, and of the container's commands.
var (
	imageFlagNames = []string{"image", "registry-auth"}
	repoFlagNames  = []string{"repo", "ref", "commit", "git-credential", "dockerfile", "dockerfile-inline",
		"dockerfile-auto", "scan-path", "submodules", "no-submodules", "lfs", "no-lfs", "push-to",
		"auto-deploy", "no-auto-deploy"}
	commandFlagNames = []string{"entrypoint", "command", "workdir", "pre-deploy", "post-deploy"}
)

// sourceFlags are the flags that change an app's deployment settings: its
// source - an image, or a repository - and its container's commands.
type sourceFlags struct {
	cmd *cobra.Command

	use, image, registryAuth                            string
	repo, ref, commit, gitCredential, pushTo            string
	dockerfile, dockerfileInline, scanPath              string
	dockerfileAuto, submodules, noSubmodules, lfs       bool
	noLFS, autoDeploy, noAutoDeploy                     bool
	entrypoint, command, workdir, preDeploy, postDeploy string
}

func addSourceFlags(cmd *cobra.Command) *sourceFlags {
	s := &sourceFlags{cmd: cmd}
	f := cmd.Flags()
	f.StringVar(&s.use, "use", "", "switch what the app deploys: image, or repo")
	f.StringVar(&s.image, "image", "", "deploy this image: ghcr.io/acme/api:1.4.3")
	f.StringVar(&s.registryAuth, "registry-auth", "", "pull the image with this registry credential; none for none")
	f.StringVar(&s.repo, "repo", "", "build this git repository: https://github.com/acme/api.git")
	f.StringVar(&s.ref, "ref", "", "the branch to build, or tags/NAME for a tag")
	f.StringVar(&s.commit, "commit", "", "build this commit, its full SHA, and stay at it; none to follow the ref")
	f.StringVar(&s.gitCredential, "git-credential", "", "clone with this git credential; none for none")
	f.StringVar(&s.dockerfile, "dockerfile", "", "the Dockerfile's path in the repository")
	f.StringVar(&s.dockerfileInline, "dockerfile-inline", "",
		"use this local file as the Dockerfile, kept in the settings; - reads stdin")
	f.BoolVar(&s.dockerfileAuto, "dockerfile-auto", false, "generate the Dockerfile from the source")
	f.StringVar(&s.scanPath, "scan-path", "", "the directory a generated Dockerfile is made from")
	f.BoolVar(&s.submodules, "submodules", false, "clone the git submodules")
	f.BoolVar(&s.noSubmodules, "no-submodules", false, "do not clone the git submodules")
	f.BoolVar(&s.lfs, "lfs", false, "fetch the Git LFS files")
	f.BoolVar(&s.noLFS, "no-lfs", false, "do not fetch the Git LFS files")
	f.StringVar(&s.pushTo, "push-to", "",
		"push the built image to the registry of this credential; none keeps it on the node")
	f.BoolVar(&s.autoDeploy, "auto-deploy", false, "deploy each push to the ref")
	f.BoolVar(&s.noAutoDeploy, "no-auto-deploy", false, "do not deploy on push")
	f.StringVar(&s.entrypoint, "entrypoint", "", `the container's entrypoint; "" for the image's`)
	f.StringVar(&s.command, "command", "", `the container's command; "" for the image's`)
	f.StringVar(&s.workdir, "workdir", "", `the container's working directory; "" for the image's`)
	f.StringVar(&s.preDeploy, "pre-deploy", "", `a command run before each deployment; "" for none`)
	f.StringVar(&s.postDeploy, "post-deploy", "", `a command run after each deployment; "" for none`)
	for _, pair := range [][2]string{{"submodules", "no-submodules"}, {"lfs", "no-lfs"},
		{"auto-deploy", "no-auto-deploy"}, {"dockerfile-auto", "dockerfile-inline"}} {
		cmd.MarkFlagsMutuallyExclusive(pair[0], pair[1])
	}
	return s
}

// given are the flags of names the command line gives, as it gives them.
func (s *sourceFlags) given(names []string) []string {
	var out []string
	for _, name := range names {
		if s.cmd.Flags().Changed(name) {
			out = append(out, "--"+name)
		}
	}
	return out
}

// asked says whether the flags ask for a change of the deployment settings.
func (s *sourceFlags) asked() bool {
	return s.cmd.Flags().Changed("use") || len(s.given(imageFlagNames)) > 0 ||
		len(s.given(repoFlagNames)) > 0 || len(s.given(commandFlagNames)) > 0
}

// commandLine is the flags as given, to be given again: --image nginx:1.30.
func (s *sourceFlags) commandLine() string {
	var parts []string
	for _, name := range append(append(append([]string{"use"}, imageFlagNames...), repoFlagNames...),
		commandFlagNames...) {
		flag := s.cmd.Flags().Lookup(name)
		if !flag.Changed {
			continue
		}
		if flag.Value.Type() == "bool" {
			parts = append(parts, "--"+name)
		} else {
			parts = append(parts, "--"+name+" "+shellWord(flag.Value.String()))
		}
	}
	return strings.Join(parts, " ")
}

// input is what the flags ask, with the credentials they name found among the
// environment's and the Dockerfile they name read.
func (s *sourceFlags) input(ctx context.Context, c *client.Client, sel *selection, stdin io.Reader) (
	*sourceInput, error,
) {
	in := &sourceInput{imageFlags: s.given(imageFlagNames), repoFlags: s.given(repoFlagNames)}
	switch s.use {
	case "":
	case string(api.DeploymentMethodImage), string(api.DeploymentMethodRepo):
		in.use = api.BaseDeploymentMethod(s.use)
	default:
		return nil, exitcode.New(exitcode.Usage, "--use takes image or repo, not %q", s.use)
	}
	changed := s.cmd.Flags().Changed
	text := func(name, value string) *string {
		if !changed(name) {
			return nil
		}
		return &value
	}
	pair := func(on, off string) *bool {
		switch {
		case changed(on):
			return ptr(true)
		case changed(off):
			return ptr(false)
		}
		return nil
	}
	in.image, in.repo, in.ref = text("image", s.image), text("repo", s.repo), text("ref", s.ref)
	if in.commit = text("commit", s.commit); in.commit != nil && strings.EqualFold(*in.commit, none) {
		in.commit = ptr("")
	}
	in.dockerfilePath, in.scanPath, in.dockerfileAuto = text("dockerfile", s.dockerfile),
		text("scan-path", s.scanPath), s.dockerfileAuto
	in.submodules, in.lfs = pair("submodules", "no-submodules"), pair("lfs", "no-lfs")
	in.autoDeploy = pair("auto-deploy", "no-auto-deploy")
	in.entrypoint, in.command, in.workingDir = text("entrypoint", s.entrypoint), text("command", s.command),
		text("workdir", s.workdir)
	in.preDeploy, in.postDeploy = text("pre-deploy", s.preDeploy), text("post-deploy", s.postDeploy)
	if changed("dockerfile-inline") {
		content, err := readInline(s.dockerfileInline, stdin)
		if err != nil {
			return nil, err
		}
		in.dockerfileContent = &content
	}

	r := resolve.New(c)
	where := fmt.Sprintf(" in %s / %s", sel.Project.Name, sel.Env)
	registry := func(input string) (*settingRef, error) {
		if strings.EqualFold(input, none) {
			return &settingRef{}, nil
		}
		auths, err := r.RegistryAuths(ctx, sel.Project.Id, sel.Env)
		if err != nil {
			return nil, err
		}
		auth, err := resolve.Credential("registry credential", input, where, auths,
			func(a api.RegistryauthdtoRegistryAuthResp) (string, string) { return a.Id, a.Name })
		if err != nil {
			return nil, err
		}
		return &settingRef{ID: auth.Id, Name: auth.Name}, nil
	}
	var err error
	if changed("registry-auth") {
		if in.registryAuth, err = registry(s.registryAuth); err != nil {
			return nil, err
		}
	}
	if changed("push-to") {
		if in.pushTo, err = registry(s.pushTo); err != nil {
			return nil, err
		}
	}
	if changed("git-credential") {
		if in.gitCredential, err = gitCredential(ctx, r, sel, where, s.gitCredential); err != nil {
			return nil, err
		}
	}
	return in, nil
}

func gitCredential(ctx context.Context, r *resolve.Resolver, sel *selection, where, input string) (
	*settingRef, error,
) {
	if strings.EqualFold(input, none) {
		return &settingRef{}, nil
	}
	creds, err := r.GitCredentials(ctx, sel.Project.Id, sel.Env)
	if err != nil {
		return nil, err
	}
	cred, err := resolve.Credential("git credential", input, where, creds,
		func(g api.GitcredentialdtoGitCredentialResp) (string, string) { return g.Id, g.Name })
	if err != nil {
		return nil, err
	}
	return &settingRef{ID: cred.Id, Name: cred.Name}, nil
}

// readInline is the content of a Dockerfile given by its local path, or - for
// stdin.
func readInline(path string, stdin io.Reader) (string, error) {
	var data []byte
	var err error
	if path == "-" {
		data, err = io.ReadAll(stdin)
	} else {
		data, err = os.ReadFile(path)
	}
	if err != nil {
		return "", exitcode.New(exitcode.Usage, "--dockerfile-inline: %v", err)
	}
	return string(data), nil
}
