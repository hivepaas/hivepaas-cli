package cmd

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hivepaas/hivepaas-cli/internal/api"
	"github.com/hivepaas/hivepaas-cli/internal/carry"
	"github.com/hivepaas/hivepaas-cli/internal/exitcode"
)

const sha = "0123456789abcdef0123456789abcdef01234567"

// repoSettings are an app built from its repository's main branch, as the API
// answers them.
const repoSettings = `{"data":{"activeMethod":"repo",
	"repoSource":{"repoType":"git","repoId":"github.com/acme/api","repoURL":"https://github.com/acme/api.git",
		"repoRef":"refs/heads/main","commitHash":"","repoOptions":{"gitSubmodulesEnabled":false,"gitLfsEnabled":false},
		"credentials":{"id":"G1","name":"acme-app","kind":"github-app"},
		"dockerfile":{"source":"manual","path":"Dockerfile"},
		"pushToRegistry":{"id":"R1","name":"ghcr"},"autoDeploy":true},
	"command":"serve","updateVer":7}}`

// settingsOf is a GET's answer, and the request carried over from it.
func settingsOf(t *testing.T, body string) (
	*api.AppsettingsdtoDeploymentSettingsResp, *api.AppsettingsdtoUpdateAppDeploymentSettingsReq,
) {
	t.Helper()
	var got struct {
		Data *api.AppsettingsdtoDeploymentSettingsResp `json:"data"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &got))
	req, err := carry.DeploymentSettings(got.Data)
	require.NoError(t, err)
	return got.Data, req
}

func apply(t *testing.T, body string, in *sourceInput) (
	*api.AppsettingsdtoUpdateAppDeploymentSettingsReq, []string, error,
) {
	t.Helper()
	current, req := settingsOf(t, body)
	lines, err := applySource("api", current, req, in)
	return req, lines, err
}

func TestARefOfItsOwnUnpinsTheCommit(t *testing.T) {
	pinned := `{"data":{"activeMethod":"repo","repoSource":{"repoURL":"https://github.com/acme/api.git",
		"repoRef":"refs/heads/main","commitHash":"` + sha + `","dockerfile":{"source":"manual","path":"Dockerfile"}}}}`

	req, lines, err := apply(t, pinned, &sourceInput{ref: ptr("release"), repoFlags: []string{"--ref"}})

	require.NoError(t, err)
	assert.Equal(t, "release", req.RepoSource.RepoRef)
	assert.Empty(t, req.RepoSource.CommitHash, "the commit was on the ref it leaves")
	assert.Equal(t, []string{"Ref: main -> release", "Commit: " + sha + " -> none, following release"}, lines)

	req, lines, err = apply(t, pinned, &sourceInput{commit: ptr(""), repoFlags: []string{"--commit"}})
	require.NoError(t, err)
	assert.Empty(t, req.RepoSource.CommitHash)
	assert.Equal(t, []string{"Commit: " + sha + " -> none"}, lines)
}

// The server keeps refs whole, refs/heads/main; the one given is a person's.
func TestTheSameRefChangesNothing(t *testing.T) {
	for _, ref := range []string{"main", "heads/main", "refs/heads/main"} {
		_, lines, err := apply(t, repoSettings, &sourceInput{ref: ptr(ref), repoFlags: []string{"--ref"}})
		require.NoError(t, err)
		assert.Empty(t, lines, ref)
	}
	assert.Equal(t, "tags/v1.2.0", shortRef("refs/tags/v1.2.0"))
	assert.Equal(t, "refs/pull/12/head", normalRef("pull/12"))
	assert.Equal(t, "HEAD", shortRef(""))
}

func TestTheDockerfile(t *testing.T) {
	req, lines, err := apply(t, repoSettings, &sourceInput{dockerfileAuto: true, scanPath: ptr("apps/web"),
		repoFlags: []string{"--dockerfile-auto", "--scan-path"}})
	require.NoError(t, err)
	assert.Equal(t, api.AppsettingsdtoDeploymentDockerfileReq{Source: api.DockerfileSourceAuto, Path: "Dockerfile",
		ScanPath: ptr("apps/web")}, req.RepoSource.Dockerfile)
	assert.Equal(t, []string{"Dockerfile: Dockerfile in the repository -> generated from apps/web"}, lines)

	req, lines, err = apply(t, repoSettings, &sourceInput{dockerfileContent: ptr("FROM scratch\n"),
		dockerfilePath: ptr("deploy/Dockerfile"), repoFlags: []string{"--dockerfile-inline", "--dockerfile"}})
	require.NoError(t, err)
	assert.Equal(t, api.AppsettingsdtoDeploymentDockerfileReq{Source: api.DockerfileSourceManual,
		Path: "deploy/Dockerfile", Content: ptr("FROM scratch\n")}, req.RepoSource.Dockerfile)
	assert.Equal(t, []string{"Dockerfile: Dockerfile in the repository -> given inline (13 bytes), at " +
		"deploy/Dockerfile"}, lines)

	_, _, err = apply(t, repoSettings, &sourceInput{scanPath: ptr("apps/web"), repoFlags: []string{"--scan-path"}})
	assert.Equal(t, exitcode.Invalid, exitcode.Of(err), "a Dockerfile of the repository's is not generated")
}

func TestTheOtherSourceNeedsUse(t *testing.T) {
	_, _, err := apply(t, repoSettings, &sourceInput{image: ptr("nginx:1.30"), imageFlags: []string{"--image"}})
	assert.Equal(t, exitcode.Invalid, exitcode.Of(err))
	assert.EqualError(t, err, "api builds from a repository: --image is for an app that deploys an image - "+
		"--use image switches it")

	_, _, err = apply(t, repoSettings, &sourceInput{use: api.DeploymentMethodImage, ref: ptr("main"),
		repoFlags: []string{"--ref"}})
	assert.Equal(t, exitcode.Usage, exitcode.Of(err))
	assert.EqualError(t, err, "--use image takes no --ref")

	_, _, err = apply(t, repoSettings, &sourceInput{use: api.DeploymentMethodImage})
	assert.Equal(t, exitcode.Usage, exitcode.Of(err), "an app with no image yet is given one")
	assert.EqualError(t, err, "api has no image to deploy: give --image")

	req, lines, err := apply(t, repoSettings, &sourceInput{use: api.DeploymentMethodImage,
		image: ptr("nginx:1.30"), imageFlags: []string{"--image"}})
	require.NoError(t, err)
	assert.Equal(t, api.DeploymentMethodImage, req.ActiveMethod)
	assert.Equal(t, "https://github.com/acme/api.git", req.RepoSource.RepoURL, "the repository is kept")
	assert.Equal(t, []string{"Method: repository -> image", "Image: nginx:1.30"}, lines)
}

func TestAnAppNeverDeployed(t *testing.T) {
	fresh := `{"data":{"activeMethod":"","updateVer":0}}`

	req, lines, err := apply(t, fresh, &sourceInput{repo: ptr("https://github.com/acme/web.git"),
		repoFlags: []string{"--repo"}})
	require.NoError(t, err)
	assert.Equal(t, api.DeploymentMethodRepo, req.ActiveMethod)
	assert.Equal(t, &api.AppsettingsdtoDeploymentRepoSourceReq{RepoType: api.RepoTypeGit,
		RepoURL:    "https://github.com/acme/web.git",
		Dockerfile: api.AppsettingsdtoDeploymentDockerfileReq{Source: api.DockerfileSourceManual, Path: "Dockerfile"},
	}, req.RepoSource, "as the dashboard's form starts one; auto-deploy left to the server")
	assert.True(t, req.Notification.SuccessUseDefault)
	assert.Equal(t, []string{"Method: repository", "Repository: https://github.com/acme/web.git"}, lines)

	_, _, err = apply(t, fresh, &sourceInput{image: ptr("nginx"), repo: ptr("https://github.com/acme/web.git"),
		imageFlags: []string{"--image"}, repoFlags: []string{"--repo"}})
	assert.Equal(t, exitcode.Usage, exitcode.Of(err))

	_, _, err = apply(t, fresh, &sourceInput{command: ptr("serve")})
	assert.Equal(t, exitcode.Invalid, exitcode.Of(err))
	assert.EqualError(t, err, "api has never been deployed: give --image, or --repo")
}

func TestAFunctionsSourceIsTheDashboards(t *testing.T) {
	_, _, err := apply(t, `{"data":{"activeMethod":"function"}}`, &sourceInput{command: ptr("serve")})
	assert.Equal(t, exitcode.Invalid, exitcode.Of(err))
}

func TestTheCommandsAndTheSwitches(t *testing.T) {
	req, lines, err := apply(t, repoSettings, &sourceInput{command: ptr(""), preDeploy: ptr("./migrate"),
		lfs: ptr(true), autoDeploy: ptr(false), gitCredential: &settingRef{}, pushTo: &settingRef{ID: "R2",
			Name: "Docker Hub"}, repoFlags: []string{"--lfs", "--no-auto-deploy", "--git-credential", "--push-to"}})

	require.NoError(t, err)
	assert.Empty(t, req.Command)
	assert.Equal(t, "./migrate", req.PreDeploymentCommand)
	assert.True(t, req.RepoSource.RepoOptions.GitLfsEnabled)
	assert.Equal(t, ptr(false), req.RepoSource.AutoDeploy)
	assert.Empty(t, req.RepoSource.Credentials.Id)
	assert.Equal(t, "R2", req.RepoSource.PushToRegistry.Id)
	assert.Equal(t, []string{
		"Git credential: acme-app -> none", "LFS: off -> on", "Push to: ghcr -> Docker Hub", "Auto-deploy: on -> off",
		"Command: serve -> none", "Pre-deploy command: none -> ./migrate",
	}, lines)
}

// A moved repository leaves its pinned commit as a change of ref does: the
// commit is of the old one.
func TestAMovedRepositoryUnpinsToo(t *testing.T) {
	pinned := `{"data":{"activeMethod":"repo","repoSource":{"repoURL":"https://github.com/acme/api.git",
		"repoRef":"refs/heads/main","commitHash":"` + sha + `","dockerfile":{"source":"manual","path":"Dockerfile"}}}}`

	req, lines, err := apply(t, pinned, &sourceInput{repo: ptr("https://github.com/acme/api2.git"),
		repoFlags: []string{"--repo"}})

	require.NoError(t, err)
	assert.Empty(t, req.RepoSource.CommitHash)
	assert.Equal(t, []string{"Repository: https://github.com/acme/api.git -> https://github.com/acme/api2.git",
		"Commit: " + sha + " -> none, following main"}, lines)
}

// What the server keeps as it was given back is the same setting: a run of the
// same flags writes nothing.
func TestARunOfTheSameFlagsWritesNothing(t *testing.T) {
	upper := `{"data":{"activeMethod":"repo","repoSource":{"repoURL":"https://github.com/acme/api.git",
		"repoRef":"HEAD","commitHash":"0123456789ABCDEF0123456789ABCDEF01234567",
		"dockerfile":{"source":"auto","path":"gen/Dockerfile","scanPath":"apps/web"}}}}`

	_, lines, err := apply(t, upper, &sourceInput{commit: ptr(sha), dockerfileAuto: true,
		dockerfilePath: ptr("/gen/Dockerfile"), scanPath: ptr("/apps/web"),
		repoFlags: []string{"--commit", "--dockerfile-auto", "--dockerfile", "--scan-path"}})
	require.NoError(t, err)
	assert.Empty(t, lines, "a commit in either case, and paths the server trims of their /")

	req, lines, err := apply(t, upper, &sourceInput{dockerfileAuto: true, dockerfilePath: ptr("Dockerfile"),
		repoFlags: []string{"--dockerfile-auto", "--dockerfile"}})
	require.NoError(t, err)
	assert.Equal(t, "Dockerfile", req.RepoSource.Dockerfile.Path)
	assert.Equal(t, []string{"Dockerfile: generated from apps/web, at gen/Dockerfile -> generated from apps/web"},
		lines, "where a generated Dockerfile is written is a change too")

	_, lines, err = apply(t, upper, &sourceInput{ref: ptr("main"), repoFlags: []string{"--ref"}})
	require.NoError(t, err)
	assert.Equal(t, "Ref: the default branch -> main", lines[0])
}

// A new repository's lines say what it is, not what it replaces.
func TestANewRepositorysLines(t *testing.T) {
	_, lines, err := apply(t, `{"data":{"activeMethod":""}}`, &sourceInput{
		repo: ptr("https://github.com/acme/web.git"), ref: ptr("main"), repoFlags: []string{"--repo", "--ref"}})

	require.NoError(t, err)
	assert.Equal(t, []string{"Method: repository", "Repository: https://github.com/acme/web.git", "Ref: main"}, lines)
}
