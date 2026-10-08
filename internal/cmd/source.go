package cmd

import (
	"fmt"
	"strings"

	"github.com/hivepaas/hivepaas-cli/internal/api"
	"github.com/hivepaas/hivepaas-cli/internal/exitcode"
)

// none clears a credential, or a pinned commit.
const none = "none"

// defaultDockerfile is where a build looks for its Dockerfile unless told.
const defaultDockerfile = "Dockerfile"

// headRef is the ref the server keeps for none: the default branch.
const headRef = "HEAD"

// sourceInput is what the flags ask of the deployment settings, the credentials
// found and the files read. A nil field leaves its setting as it is.
type sourceInput struct {
	use                   api.BaseDeploymentMethod
	imageFlags, repoFlags []string

	image        *string
	registryAuth *settingRef

	repo, ref, commit                 *string
	gitCredential, pushTo             *settingRef
	dockerfilePath, dockerfileContent *string
	dockerfileAuto                    bool
	scanPath                          *string
	submodules, lfs, autoDeploy       *bool

	entrypoint, command, workingDir, preDeploy, postDeploy *string
}

// settingRef is a credential a flag names; the zero one is none.
type settingRef struct{ ID, Name string }

func (r settingRef) String() string {
	if r.ID == "" {
		return none
	}
	return r.Name
}

// refOf is a credential as the settings answer it.
func refOf(setting *api.SettingsBaseSettingResp) settingRef {
	if setting == nil || setting.Id == "" {
		return settingRef{}
	}
	return settingRef{ID: setting.Id, Name: setting.Name}
}

// targetMethod is the method the settings deploy with once changed: the one
// --use names, else the app's, else - for an app never deployed - the one its
// flags are of.
func targetMethod(app string, current api.BaseDeploymentMethod, in *sourceInput) (api.BaseDeploymentMethod, error) {
	if current == api.DeploymentMethodFunction {
		return "", exitcode.New(exitcode.Invalid, "%s is a function: its source is set in the dashboard, and "+
			"hivepaas deploy alone deploys it", app)
	}
	otherThan := func(method api.BaseDeploymentMethod) []string {
		if method == api.DeploymentMethodImage {
			return in.repoFlags
		}
		return in.imageFlags
	}
	switch {
	case in.use != "":
		if other := otherThan(in.use); len(other) > 0 {
			return "", exitcode.New(exitcode.Usage, "--use %s takes no %s", in.use, strings.Join(other, ", "))
		}
		return in.use, nil
	case current != "":
		other := otherThan(current)
		if len(other) == 0 {
			return current, nil
		}
		switchTo := api.DeploymentMethodImage
		if current == api.DeploymentMethodImage {
			switchTo = api.DeploymentMethodRepo
		}
		verb := "is"
		if len(other) > 1 {
			verb = "are"
		}
		return "", exitcode.New(exitcode.Invalid, "%s %s: %s %s for an app that %s - --use %s switches it",
			app, deploysWith(current), strings.Join(other, ", "), verb, deploysWith(switchTo), switchTo)
	case len(in.imageFlags) > 0 && len(in.repoFlags) > 0:
		return "", exitcode.New(exitcode.Usage, "%s and %s are of two sources: an app deploys an image, or "+
			"builds a repository", strings.Join(in.imageFlags, ", "), strings.Join(in.repoFlags, ", "))
	case len(in.repoFlags) > 0:
		return api.DeploymentMethodRepo, nil
	case len(in.imageFlags) > 0:
		return api.DeploymentMethodImage, nil
	}
	return "", exitcode.New(exitcode.Invalid, "%s has never been deployed: give --image, or --repo", app)
}

func deploysWith(method api.BaseDeploymentMethod) string {
	if method == api.DeploymentMethodImage {
		return "deploys an image"
	}
	return "builds from a repository"
}

// applySource changes req - the settings as they are, carried over - as in asks,
// and answers each change as it is said: "Ref: main -> release". It answers none
// when the settings are as asked already.
func applySource(app string, current *api.AppsettingsdtoDeploymentSettingsResp,
	req *api.AppsettingsdtoUpdateAppDeploymentSettingsReq, in *sourceInput,
) ([]string, error) {
	method, err := targetMethod(app, current.ActiveMethod, in)
	if err != nil {
		return nil, err
	}
	ch := &changes{}
	if method != current.ActiveMethod {
		ch.note("Method", methodName(current.ActiveMethod), methodName(method))
		req.ActiveMethod = method
	}
	if current.ActiveMethod == "" && req.Notification == nil {
		// As the dashboard starts an app: its notices go where the project's do.
		req.Notification = &api.BasedtoBaseEventNotificationReq{SuccessUseDefault: true, FailureUseDefault: true}
	}
	if method == api.DeploymentMethodImage {
		err = applyImage(app, current, req, in, ch)
	} else {
		err = applyRepo(app, current, req, in, ch)
	}
	if err != nil {
		return nil, err
	}
	ch.text("Entrypoint", &req.Entrypoint, in.entrypoint)
	ch.text("Command", &req.Command, in.command)
	ch.text("Working directory", &req.WorkingDir, in.workingDir)
	ch.text("Pre-deploy command", &req.PreDeploymentCommand, in.preDeploy)
	ch.text("Post-deploy command", &req.PostDeploymentCommand, in.postDeploy)
	return ch.lines, nil
}

func applyImage(app string, current *api.AppsettingsdtoDeploymentSettingsResp,
	req *api.AppsettingsdtoUpdateAppDeploymentSettingsReq, in *sourceInput, ch *changes,
) error {
	if req.ImageSource == nil {
		req.ImageSource = &api.AppsettingsdtoDeploymentImageSourceReq{}
	}
	src := req.ImageSource
	if in.image != nil && *in.image != src.Image {
		if src.Image == "" {
			ch.lines = append(ch.lines, "Image: "+*in.image)
		} else {
			ch.lines = append(ch.lines, "Image: "+imageChange(src.Image, *in.image))
		}
		src.Image = *in.image
	}
	if src.Image == "" {
		return exitcode.New(exitcode.Usage, "%s has no image to deploy: give --image", app)
	}
	var was settingRef
	if current.ImageSource != nil {
		was = refOf(current.ImageSource.RegistryAuth)
	}
	ch.ref("Registry credential", &src.RegistryAuth, was, in.registryAuth)
	return nil
}

func applyRepo(app string, current *api.AppsettingsdtoDeploymentSettingsResp,
	req *api.AppsettingsdtoUpdateAppDeploymentSettingsReq, in *sourceInput, ch *changes,
) error {
	created := req.RepoSource == nil
	if created {
		if in.repo == nil {
			return exitcode.New(exitcode.Usage, "%s has no repository to build: give --repo", app)
		}
		// As the dashboard's form starts one.
		req.RepoSource = &api.AppsettingsdtoDeploymentRepoSourceReq{
			RepoType: api.RepoTypeGit,
			Dockerfile: api.AppsettingsdtoDeploymentDockerfileReq{Source: api.DockerfileSourceManual,
				Path: defaultDockerfile},
		}
	}
	src := req.RepoSource
	var was api.AppsettingsdtoDeploymentRepoSourceResp
	if current.RepoSource != nil {
		was = *current.RepoSource
	}
	// A pinned commit is of the repository and the ref it was pinned on.
	moved := false
	if in.repo != nil && *in.repo != src.RepoURL {
		ch.note("Repository", src.RepoURL, *in.repo)
		src.RepoURL, moved = *in.repo, true
	}
	if in.ref != nil && normalRef(*in.ref) != normalRef(src.RepoRef) {
		was := refWords(src.RepoRef)
		if created {
			was = ""
		}
		ch.note("Ref", was, refWords(*in.ref))
		src.RepoRef, moved = *in.ref, true
	}
	if moved && in.commit == nil && src.CommitHash != "" {
		ch.lines = append(ch.lines, "Commit: "+src.CommitHash+" -> none, following "+refWords(src.RepoRef))
		src.CommitHash = ""
	}
	if in.commit != nil && !strings.EqualFold(*in.commit, src.CommitHash) {
		ch.lines = append(ch.lines, "Commit: "+orNone(src.CommitHash)+" -> "+orNone(*in.commit))
		src.CommitHash = *in.commit
	}
	ch.ref("Git credential", &src.Credentials, refOf(was.Credentials), in.gitCredential)
	if err := applyDockerfile(&src.Dockerfile, in, ch); err != nil {
		return err
	}
	ch.toggle("Submodules", &src.RepoOptions.GitSubmodulesEnabled, in.submodules)
	ch.toggle("LFS", &src.RepoOptions.GitLfsEnabled, in.lfs)
	ch.ref("Push to", &src.PushToRegistry, refOf(was.PushToRegistry), in.pushTo)
	if in.autoDeploy != nil && *in.autoDeploy != autoDeploys(src.AutoDeploy) {
		ch.lines = append(ch.lines, "Auto-deploy: "+onOff(autoDeploys(src.AutoDeploy))+" -> "+onOff(*in.autoDeploy))
		src.AutoDeploy = ptr(*in.autoDeploy)
	}
	return nil
}

// applyDockerfile changes the Dockerfile as in asks: the repository's own, one
// given inline, or one generated from the source.
func applyDockerfile(d *api.AppsettingsdtoDeploymentDockerfileReq, in *sourceInput, ch *changes) error {
	was := *d
	switch {
	case in.dockerfileAuto:
		d.Source, d.Content = api.DockerfileSourceAuto, nil
	case in.dockerfileContent != nil:
		d.Source, d.Content = api.DockerfileSourceManual, in.dockerfileContent
	case in.dockerfilePath != nil:
		d.Source, d.Content = api.DockerfileSourceManual, nil
	}
	// The server keeps both paths without a leading /.
	if in.dockerfilePath != nil {
		d.Path = strings.TrimPrefix(*in.dockerfilePath, "/")
	}
	if in.scanPath != nil {
		if d.Source != api.DockerfileSourceAuto {
			return exitcode.New(exitcode.Invalid, "--scan-path is for a generated Dockerfile: give --dockerfile-auto")
		}
		d.ScanPath = ptr(strings.TrimPrefix(*in.scanPath, "/"))
	}
	before, after := dockerfileWords(was), dockerfileWords(*d)
	switch {
	case before != after:
		ch.note("Dockerfile", before, after)
	case textOf(was.Content) != textOf(d.Content):
		ch.lines = append(ch.lines, "Dockerfile: "+after+", its content changed")
	}
	return nil
}

// dockerfileWords says which Dockerfile a build uses.
func dockerfileWords(d api.AppsettingsdtoDeploymentDockerfileReq) string {
	path := d.Path
	if path == "" {
		path = defaultDockerfile
	}
	switch {
	case d.Source == api.DockerfileSourceAuto:
		words := "generated"
		if scan := textOf(d.ScanPath); scan != "" {
			words += " from " + scan
		}
		if path != defaultDockerfile {
			words += ", at " + path
		}
		return words
	case textOf(d.Content) != "":
		return fmt.Sprintf("given inline (%d bytes), at %s", len(*d.Content), path)
	}
	return path + " in the repository"
}

// autoDeploys is whether a push deploys, as the server reads the field: on when
// left out.
func autoDeploys(field *bool) bool { return field == nil || *field }

// normalRef is a ref as the server keeps it (githelper.NormalizeRepoRef): a bare
// name is a branch.
func normalRef(ref string) string {
	if ref == "" || ref == headRef {
		return headRef
	}
	ref = strings.TrimPrefix(ref, "refs/")
	for _, kind := range []string{"heads/", "tags/"} {
		if strings.HasPrefix(ref, kind) {
			return "refs/" + ref
		}
	}
	for _, kind := range []string{"pull/", "merge-requests/"} {
		if after, found := strings.CutPrefix(ref, kind); found {
			return "refs/" + kind + strings.TrimSuffix(after, "/head") + "/head"
		}
	}
	return "refs/heads/" + ref
}

// refWords says a ref as messages do: main, tags/v1.2.0, or the default branch.
func refWords(ref string) string {
	if normalRef(ref) == headRef {
		return "the default branch"
	}
	return shortRef(ref)
}

// shortRef is a ref as a person gives it back: main, tags/v1.2.0.
func shortRef(ref string) string {
	ref = normalRef(ref)
	if branch, found := strings.CutPrefix(ref, "refs/heads/"); found {
		return branch
	}
	return strings.TrimPrefix(ref, "refs/")
}

// changes are the changes made, as they are said.
type changes struct{ lines []string }

// note says a setting changing from was to now; nothing when they are the same.
func (c *changes) note(label, was, now string) {
	switch was {
	case now:
	case "":
		c.lines = append(c.lines, label+": "+now)
	default:
		c.lines = append(c.lines, label+": "+was+" -> "+now)
	}
}

// text changes a text setting to to, when it is given.
func (c *changes) text(label string, field, to *string) {
	if to == nil || *to == *field {
		return
	}
	c.lines = append(c.lines, label+": "+orNone(*field)+" -> "+orNone(*to))
	*field = *to
}

// toggle changes a switch to to, when it is given.
func (c *changes) toggle(label string, field, to *bool) {
	if to == nil || *to == *field {
		return
	}
	c.lines = append(c.lines, label+": "+onOff(*field)+" -> "+onOff(*to))
	*field = *to
}

// ref changes a credential to to, when it is given; was names the one there.
func (c *changes) ref(label string, field *api.BasedtoObjectIDReq, was settingRef, to *settingRef) {
	if to == nil || to.ID == field.Id {
		return
	}
	c.lines = append(c.lines, label+": "+was.String()+" -> "+to.String())
	field.Id = to.ID
}

func orNone(s string) string {
	if s == "" {
		return none
	}
	return s
}

func onOff(on bool) string {
	if on {
		return "on"
	}
	return "off"
}

func textOf(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
