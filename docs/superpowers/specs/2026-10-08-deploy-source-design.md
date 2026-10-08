# Deploying from an image or a repository

`hivepaas deploy` sets one field of the deployment settings, the image. This
design gives it the rest of them: everything the API's deployment settings take
for an app that deploys an image and for one built from a git repository, so a
script can set up and deploy an app the way the dashboard's form does. It
extends §5 of [the CLI's design](2026-10-08-cli-design.md); nothing changes on
the server.

## What the API takes

`PUT .../apps/{appID}/deployment-settings` (`appsettingsdto.DeploymentSettingsReq`):

| Field | What it is |
|---|---|
| `activeMethod` | `image`, `repo` or `function`: which source deploys |
| `imageSource.image`, `.registryAuth.id` | the image, and the registry credential it is pulled with |
| `repoSource.repoType`, `.repoURL`, `.repoRef` | `git`, the repository, the branch or tag |
| `repoSource.commitHash` | a commit the app is pinned at; empty follows the ref |
| `repoSource.credentials.id` | the git credential it is cloned with: a GitHub App, a token, an SSH key |
| `repoSource.dockerfile` | `source` `manual` or `auto`; `path`; `content` (≤ 16 KB) written at `path`; `scanPath` for `auto` |
| `repoSource.repoOptions` | `gitSubmodulesEnabled`, `gitLfsEnabled` |
| `repoSource.pushToRegistry.id` | the registry the built image is pushed to; none keeps it on the node that built it |
| `repoSource.autoDeploy` | whether a push to the ref deploys, through the repository's webhook; on when left out |
| `entrypoint`, `command`, `workingDir` | the container's, empty for the image's own |
| `preDeploymentCommand`, `postDeploymentCommand` | run around each deployment |
| `notification`, `functionSource` | left to the dashboard: carried over as they are |

`POST .../apps/{appID}/deploy` takes `noCache` and `changeId` (`pr-<n>` makes
HivePaaS comment the result on that pull request).

Three facts of the API shape the command:

1. **A write of the settings deploys.** The server starts one deployment for each
   write and answers its id. So every flag that changes the settings is a flag of
   `deploy`, all of them go in one write, and a run with nothing to change calls
   `POST .../deploy` instead.
2. **The server checks a build's source before it saves it**: the repository can be
   cloned with the credential, the registry pushed to. The CLI does not check it
   again; a refusal exits 6 with the server's words.
3. **Both sources are kept.** The settings hold `imageSource` and `repoSource`
   side by side and `activeMethod` picks one; switching keeps the other's.

## The command

```
hivepaas deploy [source flags] [command flags] [--no-cache] [--change-id ID] [--no-wait] [--timeout 30m]
hivepaas deploy settings
```

### Image

| Flag | Field |
|---|---|
| `--image REF` | `imageSource.image` (`repo:tag` or `repo@sha256:...`) |
| `--registry-auth NAME\|none` | `imageSource.registryAuth.id` |

### Repository

| Flag | Field |
|---|---|
| `--repo URL` | `repoSource.repoURL`, with `repoType` `git` |
| `--ref REF` | `repoSource.repoRef`; a pinned commit goes with a change of ref, unless `--commit` is given too |
| `--commit SHA\|none` | `repoSource.commitHash`: pin the app at a commit, or follow the ref again |
| `--git-credential NAME\|none` | `repoSource.credentials.id` |
| `--dockerfile PATH` | `dockerfile.path`, `source` `manual`: the repository's own file |
| `--dockerfile-inline FILE` | `dockerfile.content` from a local file (`-` for stdin), `source` `manual` |
| `--dockerfile-auto` | `dockerfile.source` `auto`: generated from the source |
| `--scan-path DIR` | `dockerfile.scanPath`, for a generated Dockerfile |
| `--submodules`, `--no-submodules` | `repoOptions.gitSubmodulesEnabled` |
| `--lfs`, `--no-lfs` | `repoOptions.gitLfsEnabled` |
| `--push-to NAME\|none` | `repoSource.pushToRegistry.id` |
| `--auto-deploy`, `--no-auto-deploy` | `repoSource.autoDeploy` |

### Either

| Flag | Field |
|---|---|
| `--use image\|repo` | `activeMethod` |
| `--entrypoint`, `--command`, `--workdir` | `entrypoint`, `command`, `workingDir`; `""` for the image's |
| `--pre-deploy CMD`, `--post-deploy CMD` | `preDeploymentCommand`, `postDeploymentCommand`; `""` for none |
| `--no-cache` | `POST deploy {noCache}` |
| `--change-id ID` | `POST deploy {changeId}` |

## Rules

**One read, one write.** The CLI reads the settings, carries every field over
(`internal/carry`), changes what the flags ask, and writes them back under
`updateVer`; a change made meanwhile is read again, once, as `--image` does
today. Each change is said on stderr, `Ref: main -> release`, before the
deployment's logs. When the flags ask for nothing that differs, nothing is
written: `The deployment settings are as asked already.`, then `POST .../deploy`.
"Differs" is as the server keeps a value: a ref as `refs/heads/main` for `main`,
a commit in either case, a path without its leading `/` - so a script run twice
deploys twice through the same call, never through a write that changes nothing.
`deploy` with no settings flag reads nothing and only posts, as today.

**Which source.** The settings deploy with:

- the method `--use` names; a flag of the other source with it is a usage
  error (exit 2): `--use image takes no --ref`;
- else the app's method; a flag of the other source is refused (exit 6):
  `api builds from a repository: --image is for an app that deploys an image -
  --use image switches it`. A typo in CI never turns an app built from source
  into one running an image;
- else, for an app never deployed, the source its flags are of; flags of both
  are a usage error. A first deployment also gets the notification defaults the
  dashboard gives (`successUseDefault`, `failureUseDefault`), as `--image` does.

Switching to a source the app has never had needs its main field: `--use repo`
without `--repo` when there is no repository is a usage error, `--use image`
without `--image` when there is no image too. A new repository starts as the
dashboard's form does: `git`, the Dockerfile at `Dockerfile` in it, no
credential, auto-deploy left to the server (on).

A function's source is the dashboard's: on a function, any settings flag exits
6, and `deploy` alone still deploys it.

**Credentials by name.** `--registry-auth` and `--push-to` name a registry
credential, `--git-credential` a git credential, as the dashboard lists them for
the app's environment: `GET /projects/{projectID}/{projectEnv}/registry-auth` and
`.../git-credentials`, which include the project's and the installation's shared
down to it. A name matches by id exactly, else by name ignoring case; none, or
more than one, are errors listing the candidates, as for projects and apps.
`none`, in any case, clears the field.

**The Dockerfile.** `--dockerfile PATH` alone is the repository's file at PATH
(an inline content goes). `--dockerfile-inline FILE` writes the file's content at
the path (`--dockerfile PATH` if given, else the path the settings have);
`--dockerfile-auto` and `--dockerfile-inline` exclude each other. `--scan-path`
is for a generated Dockerfile: with a manual one it exits 6. The server's limit
of 16 KB holds for the content; the CLI does not repeat it.

**A pinned commit.** `--commit SHA` keeps the app at that commit: a later
`deploy`, or Deploy in the dashboard, builds it again, as an image keeps its tag.
A push to the ref, when auto-deploy is on, deploys the push and unpins the app;
`--commit none` and a change of `--ref` or `--repo` unpin it too - the commit is
of the repository and the ref it was pinned on. When the app deploys on
push and `--commit` is given, the CLI warns that the same commit may be deployed
twice - once by the push, once by the command - and names `--no-auto-deploy`.

**`--no-cache` and `--change-id`** belong to `POST .../deploy`. With a change of
the settings, whose write starts its own deployment, they would be dropped
silently: the CLI refuses the pair (exit 2) and says to deploy the change first.
`--no-cache` on an app that deploys an image is a usage error, as today.

**Output.** Unchanged: the deployment's logs on stderr, then its outcome; with
`-o json` stdout carries only the finished deployment.

## `deploy settings`

Shows what the app deploys, from `GET .../deployment-settings`: the method; the
image and its registry credential; or the repository, the ref, the commit (pinned,
or following the ref), the git credential, the Dockerfile, submodules, LFS, the
registry pushed to, auto-deploy; then the commands that are set. Credentials by
name. `-o json` prints the API's `data`.

## Tab completion

`--registry-auth`, `--push-to` and `--git-credential` complete the names of the
environment's credentials, with their kind beside them, and `none`; `--use`
completes `image` and `repo`.

## `app create`

`app create` takes the same source and command flags: `--repo` and the rest
create an app built from source, deployed once its variables and port are set,
as `--image` does today. The rules above apply to it as to an app never
deployed, and are checked before the app is created: flags no app could deploy
with create nothing.

## Not in this

- A function's source, the deployment notifications, and the image naming the
  settings answer (`image.repoName`, `tagPrefix`): the dashboard's.
- A commit or ref for one deployment only: the API has no such field on
  `POST .../deploy`, and pinning through the settings is what the dashboard does.
- `hivepaas up`, building the working directory: still later (§15 of the design).

## Releasing

Every field used here is in the spec the CLI pins today. The release itself pins
a server tag (`make update-spec REF=v1.0.0-beta4`), as §10 of the design says.
