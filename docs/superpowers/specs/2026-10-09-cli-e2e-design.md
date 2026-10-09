# The CLI against a real installation

The unit tests run each command against a fake API: they say what the CLI
sends and how it reads an answer, not that the server takes it. These tests run
the built binary against a throwaway installation, as a person or a CI job
would, and look at what it did - in the API, in the app's containers, at the
app's domain.

## Where they run

On the dashboard's throwaway installation: `e2e/env/up.sh` in the dashboard
repo starts the backend inside dind, the API at `http://localhost:10100` and
the apps' domains at `127.0.0.1:10180` (HTTP) and `:10443` (HTTPS).

- `make e2e` runs them: `go test -tags e2e -count=1 ./e2e/...`. The build tag
  keeps them out of `go test ./...`.
- `HP_E2E_BASE_URL` (default `http://localhost:10100`), `HP_E2E_USERNAME` and
  `HP_E2E_PASSWORD` (default `admin` / `abc123`), `HP_E2E_INGRESS_HTTP` and
  `HP_E2E_INGRESS_HTTPS` (default `127.0.0.1:10180` and `:10443`) - the
  dashboard's names for the same things.
- The local backend, port 10000, is refused: these tests make, deploy and
  remove things, and that one holds a developer's own data.

## How

- `TestMain` builds the CLI once, signs in with the password and makes an API
  key for the run, every action allowed, expiring the next day; it deletes the
  key when the run ends.
- Every command runs as CI runs it: `HIVEPAAS_URL` and `HIVEPAAS_API_KEY` in
  its environment, a config directory of its own, no terminal. `login` is left
  out: it keeps the secret in the OS keychain, and a test run must not write
  there.
- Each test makes its own project, named for the run, and deletes it - with
  its storage - when it ends, failed or not. Tests run in parallel.
- What a command cannot set up - a volume, a mount, a backup repository, a
  scheduled job - is made through the API, as the dashboard's tests do, and
  removed after.
- An app is reached at `<name>.localhost` through the installation's proxy;
  the HTTP client dials the ingress whatever the host.

## What they cover

| Test | Commands |
|---|---|
| An app from an image | `project create`, `app create --image`, `ps`, `logs`, `env ls/set`, `domain add` then a request to it, `cp` in and out, `exec` piped, `app scale`, `app stop/start`, `restart`, `app get -o json`, `deploy ls`, `deploy settings`, `api` |
| Secrets and config files | `secret set` from stdin, `secret ls` hiding the value, a variable naming `${secrets.KEY}` seen in the container, `config-file push/pull/ls/rm` |
| A job | `job ls`, `job run` following its log, a run that fails exits 8, `task ls`, `logs --task` |
| A function | `function init`, `function run`, `function create --domain` then a request to it, `function ls`, `function get`, `function metrics`, `function pull` giving back the files |
| A compose file | `compose up` refusing a missing variable without a terminal, then making the project with `--var` and deploying; `app ls` |
| A data backup | `backup run`, `backup ls`, `backup files`, `backup download -O -`, `backup restore --yes` and the app finding its data again, `backup rm` |
| Asking and failing | `whoami`, `version`, `status`; exit codes - 2 for a usage error and a command that would ask with no terminal, 5 for what is not there |
| A template | `template ls --search`, `template deploy` with a volume parameter, its app running |
| A linked directory | `link`, then `deploy`, `logs` and `deploy settings` without flags, here and below; `unlink` |
| Taking away | `env unset`, `env pull`, `secret set --file`, `secret rm`, `config-file rm`, `open --print`, `domain rm`, `project env add/rm`, `app delete --remove-storage` |
| Following, waiting, canceling | `logs -f` until Ctrl-C (130); a deployment held by its pre-deployment command, `deploy cancel`, `deploy get`; `job run --timeout` exiting 9, `task cancel`, `task get` |
| An app from a repository | `app create --repo --ref --commit`, `deploy --commit none` to follow the branch, `deploy --ref`; each deployment's commit |
| A preview | refused while previews are off; `preview create` building the app's branch, `preview ls`, `preview rm` |

The repository is the one the dashboard's env/up.sh serves inside dind, at
git://127.0.0.1/e2e/shop.git: two commits on main, each a Dockerfile printing
which it is, and develop at the first.
