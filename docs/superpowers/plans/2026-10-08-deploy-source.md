# Deploying from an image or a repository - implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `hivepaas deploy` sets every field of an app's deployment settings for an image or a git repository, in one write; `deploy settings` shows them; `app create` takes the same flags; Tab completes the credentials.

**Architecture:** The flags become a `sourceInput` (credentials resolved by name, files read); a pure `applySource` changes the request carried over from the `GET` and says each change; `changeSource` runs it inside the existing `writeBack` (read, change, write under `updateVer`, one retry). No change: `POST .../deploy`. Nothing changes on the server.

**Tech Stack:** Go, cobra, the oapi-codegen client in `internal/api`, testify; command tests against `fakeAPI` (`internal/cmd/cmd_test.go`).

**Spec:** [docs/superpowers/specs/2026-10-08-deploy-source-design.md](../specs/2026-10-08-deploy-source-design.md)

**Base:** `bd6b8b1` (app create and app delete). The patches in [2026-10-08-deploy-source/](2026-10-08-deploy-source/) were replayed on it task by task: each task's new tests failed first for the reason given, then the task's checks passed - `go build ./...`, `go vet ./...`, `golangci-lint run ./...` (0 issues), `go test ./...` - and `go test -race ./...` at the end; the six apply in order with `git apply --index`. Main moves fast: if a patch does not apply, re-apply its hunks by their anchors (they are named in each task) rather than by line. Each commit ends with the `Co-Authored-By` trailer the session gives.

## Global Constraints

- Writes carry every field over through `internal/carry` and `writeBack` (`internal/cmd/scale.go`): never build a settings request from scratch.
- Messages and errors follow the CLI's voice: stderr for people, `-o json` prints the API's `data` only.
- Exit codes (design §7): 2 usage, 5 not found, 6 invalid for the app's state.
- Lines of 120 characters at most, US spelling (`make lint`).
- No server change, no change of the pinned spec (`internal/api`).

## Review Focus

1. A re-run with the same flags writes nothing and posts (`normalRef` mirrors the server's `githelper.NormalizeRepoRef`; a ref given as `main` is the stored `refs/heads/main`).
2. A run writes nothing it was not asked to: the rest of the settings carried as they are, a new repository's defaults only when there was none.
3. A typo of the other source on an app (`--image` on an app built from source) is refused, not a switch.
4. `--no-cache`/`--change-id` never go silently missing with a change of the settings.
5. A credential named wrongly stops the command before anything is written - and before `app create` creates the app.

Each has its test in the task that owns the code.

---

### Task 1: The settings a source flag changes

**Files:**
- Create: `internal/cmd/source.go`
- Test: `internal/cmd/source_test.go`

**Interfaces:**
- Consumes: `methodName`, `imageChange` (`internal/cmd/deploy.go`), `ptr` (`internal/cmd/apps.go`), `carry.DeploymentSettings`.
- Produces: `type sourceInput struct{...}` (fields: `use api.BaseDeploymentMethod`, `imageFlags, repoFlags []string`, `image *string`, `registryAuth *settingRef`, `repo, ref, commit *string`, `gitCredential, pushTo *settingRef`, `dockerfilePath, dockerfileContent *string`, `dockerfileAuto bool`, `scanPath *string`, `submodules, lfs, autoDeploy *bool`, `entrypoint, command, workingDir, preDeploy, postDeploy *string`); `type settingRef struct{ ID, Name string }` with `String()`; `refOf(*api.SettingsBaseSettingResp) settingRef`; `applySource(app string, current *api.AppsettingsdtoDeploymentSettingsResp, req *api.AppsettingsdtoUpdateAppDeploymentSettingsReq, in *sourceInput) ([]string, error)`; `targetMethod`; `dockerfileWords(api.AppsettingsdtoDeploymentDockerfileReq) string`; `autoDeploys(*bool) bool`; `normalRef`, `shortRef`; `orNone`, `onOff`, `textOf`; consts `none`, `defaultDockerfile`.

- [ ] **Step 1: Write the failing tests** - `internal/cmd/source_test.go` from [01-source.patch](2026-10-08-deploy-source/01-source.patch): a pinned commit leaves with its ref, `--commit none` unpins; the same ref in any spelling changes nothing; the Dockerfile generated, inline at a path, `--scan-path` refused on a manual one; the other source needs `--use`, `--use image` needs an image, the repository is kept; an app never deployed (new repository's defaults, notification defaults, both sources refused, commands alone refused); a function refused; commands cleared and set, switches, credentials to none and to another; a moved repository unpins as a new ref does; a run of the same flags writes nothing (a commit in the other case, paths with a leading `/`) while the path a generated Dockerfile is written at is a change; a new repository's lines (`Ref: main`, not `HEAD -> main`).
- [ ] **Step 2: Run them** - `go test ./internal/cmd/ -run 'TestARef|TestTheSame|TestTheDockerfile|TestTheOther|TestAnAppNever|TestAFunction|TestTheCommands|TestAMoved|TestARunOf|TestANewRepo'`. Expected: build failure, `undefined: sourceInput`, `undefined: applySource`.
- [ ] **Step 3: Implement** - `internal/cmd/source.go` from the patch: `targetMethod` (the method `--use` names, else the app's, else the flags' for an app never deployed; a function refused), `applySource`, `applyImage`, `applyRepo` (a new repository as the dashboard's form starts one: `git`, manual `Dockerfile`; a change of repository or ref unpins the commit unless `--commit` is given; commits compared in any case), `applyDockerfile` (paths without their leading `/`, as the server keeps them), `refWords` (`the default branch` for `HEAD`), and the `changes` lines (`Ref: main -> release`).
- [ ] **Step 4: Run them** - same command. Expected: PASS. Then `gofmt -l internal`, `go vet ./...`, `golangci-lint run ./...` (0 issues), `go test ./...`.
- [ ] **Step 5: Commit** - `git apply`/edit, then `git commit -m "deploy: what each flag of a source changes in the deployment settings"`.

### Task 2: The flags, the credentials by name, and deploy

**Files:**
- Create: `internal/resolve/credentials.go`, `internal/cmd/source_flags.go`
- Modify: `internal/resolve/resolve.go` - `pick`'s error for more than one match says `give its id` when the items have no key of their own (credentials).
- Modify: `internal/cmd/deploy.go` - from `type deployFlags struct {` up to `func methodName(`: `deployFlags` gains `source`, `changeID`; `deployCmd` registers the source flags, `--change-id`, help and examples; `deploy` posts with `ChangeId`; new `postOnly`, `errUnchanged`, `changeSource`; `setImage` goes.
- Modify: `internal/cmd/appcreate.go` - its `a.setImage(...)` call becomes `a.changeSource(ctx, c, sel, &sourceInput{image: &flags.image, imageFlags: []string{"--image"}}, nil)` (Task 4 gives it the flags).
- Test: `internal/cmd/deploy_source_test.go`

**Interfaces:**
- Consumes: Task 1's `sourceInput`, `applySource`, `autoDeploys`, `shortRef`; `writeBack` (`scale.go`), `carry.DeploymentSettings`.
- Produces: `(*resolve.Resolver).RegistryAuths(ctx, projectID, env) ([]api.RegistryauthdtoRegistryAuthResp, error)`, `(*resolve.Resolver).GitCredentials(...) ([]api.GitcredentialdtoGitCredentialResp, error)`, `resolve.Credential[T](what, input, where string, items []T, idName func(T) (string, string)) (T, error)`; `addSourceFlags(*cobra.Command) *sourceFlags`, `(*sourceFlags).asked() bool`, `(*sourceFlags).input(ctx, c, sel, stdin) (*sourceInput, error)`; `(*App).changeSource(ctx, c, sel, in, check func(*api.AppsettingsdtoUpdateAppDeploymentSettingsReq, bool) error) (deploymentID string, err error)`.

- [ ] **Step 1: Write the failing tests** - `internal/cmd/deploy_source_test.go` from [02-flags-and-deploy.patch](2026-10-08-deploy-source/02-flags-and-deploy.patch): one write of `--ref`, `--commit`, `--no-auto-deploy` with the rest carried, waited for, no POST; the double-deploy warning; credentials by name from the environment's lists, `None` in any case clears, a name not found writes nothing (exit 5), two credentials one name matches ask for an id (exit 2); nothing to change posts with `noCache` and `changeId`; `--no-cache` with a change refused (exit 2), nothing written or posted; `--image` on an app built from source refused (exit 6), `--use image` switches it and keeps the repository; `--dockerfile-inline` from a file, a missing file exit 2.
- [ ] **Step 2: Run them** - `go test ./internal/cmd/ -run 'TestDeploy'`. Expected: FAIL - `unknown flag: --ref` and the like, exit 2 where 0 is expected.
- [ ] **Step 3: Implement** - the files and edits above, from the patch.
- [ ] **Step 4: Run them** - same command, then the whole package and the checks. Expected: PASS; `TestDeployAnImage`, `TestAppCreateWithAnImage`, `TestAppCreateThatFailsHalfWay` still pass.
- [ ] **Step 5: Commit** - `git commit -m "deploy: every field of an image's or a repository's settings, in one write"`.

### Task 3: deploy settings

**Files:**
- Create: `internal/cmd/deploy_settings.go`
- Modify: `internal/cmd/deploy.go` - `cmd.AddCommand(a.deployCancelCmd(), a.deployLsCmd(), a.deployGetCmd(), a.deploySettingsCmd())`
- Test: `internal/cmd/deploy_settings_test.go`

**Interfaces:**
- Consumes: `refOf`, `shortRef`, `dockerfileWords`, `onOff`, `textOf`, `methodName`.
- Produces: `(*App).deploySettingsCmd() *cobra.Command`, `settingsRows(*api.AppsettingsdtoDeploymentSettingsResp) [][2]string`.

- [ ] **Step 1: Write the failing test** - `TestDeploySettings` from [03-deploy-settings.patch](2026-10-08-deploy-source/03-deploy-settings.patch): the repository's rows, aligned, credentials by name, `Commit: follows main`; `-o json` is the API's data.
- [ ] **Step 2: Run it** - `go test ./internal/cmd/ -run TestDeploySettings`. Expected: FAIL, `unknown command "settings" for "hivepaas deploy"`.
- [ ] **Step 3: Implement** - from the patch.
- [ ] **Step 4: Run it**, then the checks. Expected: PASS.
- [ ] **Step 5: Commit** - `git commit -m "deploy settings: what an app deploys"`.

### Task 4: app create takes the same flags

**Files:**
- Modify: `internal/cmd/appcreate.go` - `appCreateFlags.image` becomes `source *sourceFlags` (`addSourceFlags(cmd)`); help and an example with `--repo`; the input is resolved, and `applySource` run on empty settings to check it as for an app never deployed, right after `selectTarget`, before `CreateAppWithResponse`; the last step is `a.changeSource(ctx, c, sel, in, nil)`, its failure saying `deploy it with hivepaas deploy <the flags as given>`.
- Modify: `internal/cmd/source_flags.go` - add `(*sourceFlags).commandLine() string` before `// input is what the flags ask`.
- Test: `internal/cmd/appcreate_source_test.go`

- [ ] **Step 1: Write the failing tests** - from [04-app-create.patch](2026-10-08-deploy-source/04-app-create.patch): `TestAppCreateFromARepository` - a git credential not found creates nothing (exit 5); found, the app is created and its repository written, which deploys it. `TestAppCreateChecksItsFlagsFirst` - `--image` with `--ref` (exit 2), `--command` alone (exit 6), `--scan-path` with a manual Dockerfile (exit 6) create no app.
- [ ] **Step 2: Run them** - `go test ./internal/cmd/ -run 'TestAppCreateFromARepository|TestAppCreateChecksItsFlagsFirst'`. Expected: FAIL, exit 2 (`unknown flag: --repo`) where 5 is expected.
- [ ] **Step 3: Implement** - from the patch.
- [ ] **Step 4: Run it**, then the checks; `TestAppCreateThatFailsHalfWay` still says `deploy it with hivepaas deploy --image NOT/VALID -p shop -e production -a web`. Expected: PASS.
- [ ] **Step 5: Commit** - `git commit -m "app create: an app built from its repository, with deploy's flags"`.

### Task 5: Tab completes the credentials

**Files:**
- Modify: `internal/cmd/completion.go` - in `registerCompletions`' `walk`, a command with a `push-to` flag registers `registry-auth`, `push-to`, `git-credential` and `use`; new `noCredential`, `registryAuthChoices`, `gitCredentialChoices`.
- Test: `internal/cmd/completion_source_test.go`

- [ ] **Step 1: Write the failing test** - `TestTabCompletesCredentials` from [05-completion.patch](2026-10-08-deploy-source/05-completion.patch): `none` first, then each credential with its kind; `--use` offers `image`, `repo`.
- [ ] **Step 2: Run it** - `go test ./internal/cmd/ -run TestTabCompletesCredentials`. Expected: FAIL, nothing offered (`:0`).
- [ ] **Step 3: Implement** - from the patch.
- [ ] **Step 4: Run it**, then the checks. Expected: PASS.
- [ ] **Step 5: Commit** - `git commit -m "Tab completes the credentials deploy names"`.

### Task 6: The design and the README

**Files:**
- Modify: `docs/superpowers/specs/2026-10-08-cli-design.md` - §1's `deploy` and `app create` rows, a `deploy settings` row; the API table's `deploy` with source flags and `deploy settings`; §5's synopsis and step 1.
- Modify: `README.md` - two examples, and `--commit ${{ github.sha }}` for CI.

- [ ] **Step 1: Edit** - from [06-docs.patch](2026-10-08-deploy-source/06-docs.patch).
- [ ] **Step 2: Check** - `go test ./...`, `go test -race ./...`, `golangci-lint run ./...`, `make build`, then `bin/hivepaas deploy --help` reads as the spec says.
- [ ] **Step 3: Commit** - `git commit -m "Design and README: deploying from an image or a repository"`.

## Review of the replayed patches

A fresh reviewer read the whole diff against the spec. Fixed in the patches, each with a test that failed first: `app create` checked its flags only after creating the app; the path a generated Dockerfile is written at was not seen as a change; a re-run with a leading `/` or a commit in the other case wrote and deployed again; a new repository's lines read `Ref: HEAD -> main`; `none` was case-sensitive; credentials were asked for a key they do not have; a moved repository kept its pinned commit silently.

Left as they are (minor):
- The exclusive pairs (`--lfs`/`--no-lfs`, ...) exit 1 with cobra's own words, not 2: cobra's flag-group check skips the CLI's flag-error handler. `scale`, `env` and `template deploy` have it already; a fix belongs to the root command, for all of them.
- Credentials are not filtered by status: a disabled one is found by name, and the server refuses it.
- A completed name with a space (`Docker Hub`) is quoted by the shell's completion, not by the CLI.

## Before release

The CLI's release pins a server tag (`make update-spec REF=v1.0.0-beta4`); every field used here is in the spec pinned today, so this plan needs no spec change. A smoke test against a running installation, as §14 of the design asks: `deploy --use repo --repo ... --git-credential ... --push-to ...` on a test app, `deploy --commit <sha>`, `deploy settings`, `deploy --ref main` (no write, a POST).
