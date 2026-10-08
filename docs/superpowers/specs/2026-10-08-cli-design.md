# The HivePaaS CLI

`hivepaas` is a command-line client for a HivePaaS installation: deploy an app from
CI, follow its logs, change its environment, create an app from a template - what
the dashboard does, from a terminal or a script. It talks to the same REST API the
dashboard uses, through a Go client generated from the server's OpenAPI spec, and
authenticates with an API key.

This document is the design for its first version (the MVP) and what the server
needs for it. Later phases are listed at the end.

---

## Decisions

1. **A repository of its own, `hivepaas/hivepaas-cli`, released on its own.** The
   CLI does not import the server's Go packages, and the server does not embed the
   CLI. What ties them is the API, checked from both sides (§10).
2. **A client generated from the server's OpenAPI spec.** The CLI pins a copy of
   `docs/openapi/swagger.json` from a server release and generates its client with
   oapi-codegen. Only what OpenAPI cannot describe - the websocket streams - is
   written by hand.
3. **API keys, one context per installation.** `hivepaas login` stores a URL and a
   key; the secret goes to the OS keychain. CI gives both in environment variables
   and stores nothing.
4. **Names, not ids.** Projects, environments and apps are named the way a person
   names them, and resolved through the list endpoints with the rules the MCP server
   uses, so the two never disagree about what a name means.
5. **A directory can be linked to an app.** `hivepaas link` writes `.hivepaas.json`;
   in that directory `hivepaas deploy` and `hivepaas logs` need no arguments.
6. **Output for people and for scripts.** Tables by default; `-o json` prints the
   API's own `data`, unchanged, so a script reads the documented API shapes. Exit
   codes are part of the contract (§7).
7. **A write never loses what the CLI does not know.** The CLI changes settings as
   the dashboard does: it reads the object, changes what it was asked to, and
   writes it back under the object's `updateVer`. That is only safe from a client
   that knows every field, so the server refuses a write from a CLI older than its
   API (§8), and the CLI has a test for each request it writes back that fails when
   the request has a field it does not carry over (§10).
8. **The server says what it is.** `GET /sessions/me` reports the server's version,
   its API level and the oldest CLI it takes writes from (§8). Reads from an older
   CLI still work, with a warning, so a server upgrade does not stop a pipeline
   from following its logs.
9. **The CLI updates itself, when asked, from a list signed offline.** It says when
   a newer release is out, and `hivepaas update` installs it. What it installs is
   named, with its checksum, by a list signed with the server's offline release
   keys, as an installation's `release.json` is (§13).

## 1. Scope of the MVP

| Command | What it does |
|---|---|
| `login [URL]`, `logout`, `context ls\|use\|rm`, `whoami` | manage installations and keys |
| `projects ls`, `projects get` | list and show projects, with their environments |
| `apps ls`, `apps get` | list and show apps |
| `link`, `unlink` | tie a directory to an app |
| `deploy [--image REF]` | deploy, and by default wait for the result while following its logs |
| `deploy cancel [ID]` | cancel a deployment: the app's running one when no id is given |
| `logs [-f]` | an app's logs |
| `restart` | restart an app |
| `env ls\|set\|unset` | an app's environment variables |
| `templates ls`, `templates deploy` | the template store |
| `api METHOD PATH` | any endpoint, authenticated, JSON in and out |
| `version`, `completion` | the CLI's version and shell completion |
| `update [--check]` | install a newer release of the CLI, checked against a signed list (§13) |

`api` is the escape hatch, as in `gh api`: everything the MVP does not wrap is
still one command away, with the context's URL and key applied, and `{project}`,
`{env}` and `{app}` in the path filled in from the flags or the link (§4).

## 1.1 What it looks like

Output here is illustrative: the columns and wording are settled while building.

**Logging in**, with a key made in the dashboard:

```console
$ hivepaas login https://paas.example.com
API key ID: 01J9Z6M2QK...
API key secret: ****************
Logged in to https://paas.example.com as dev@example.com (HivePaaS v1.0.0-beta4).
Saved as context "paas.example.com", now the current one.

$ hivepaas context ls
CURRENT  NAME              URL                       USER
*        paas.example.com  https://paas.example.com  dev@example.com
         local             http://localhost:10000    admin@example.com
```

**Looking around:**

```console
$ hivepaas projects ls
NAME  KEY   ENVS                              STATUS
Shop  shop  development, staging, production  active
Blog  blog  production                        active

$ hivepaas apps ls -p shop -e production
NAME       KEY     KIND      STATUS   UPDATED
API        api     -         active   2h ago
Web        web     -         active   1d ago
Orders DB  db      postgres  active   5d ago
cache      cache   valkey    active   5d ago
```

**Linking a directory**, after which the app needs no flags:

```console
$ cd ~/src/shop-api
$ hivepaas link -p shop -e production -a api
Linked ~/src/shop-api to shop / production / api on paas.example.com (.hivepaas.json).
```

**Deploying** a new image, waiting, with the deployment's logs:

```console
$ hivepaas deploy --image ghcr.io/acme/shop-api:1.4.3
Image: ghcr.io/acme/shop-api:1.4.2 -> 1.4.3
Deploying api (shop / production), deployment 01JA2C7W...
  14:02:11  Pulling ghcr.io/acme/shop-api:1.4.3
  14:02:19  Updating the service
  14:02:31  1/1 tasks running and healthy
Deployed in 24s.

$ hivepaas deploy --image ghcr.io/acme/shop-api:1.4.4
Deploying api (shop / production), deployment 01JA2E3K...
  14:20:05  Pulling ghcr.io/acme/shop-api:1.4.4
^C
Stopped waiting. Deployment 01JA2E3K... is still running on the server.
  Follow it:  hivepaas logs --deployment 01JA2E3K... -f
  Cancel it:  hivepaas deploy cancel 01JA2E3K...

$ hivepaas deploy cancel
Canceled deployment 01JA2E3K... of api (shop / production).

$ hivepaas deploy
Deploying api (shop / production), deployment 01JA2D0F...
  ...
Deployment failed after 41s: the health check did not pass.
Its logs: hivepaas logs --deployment 01JA2D0F...
$ echo $?
8
```

**From CI**, with nothing stored - a GitHub Actions step:

```yaml
- name: Deploy
  env:
    HIVEPAAS_URL: https://paas.example.com
    HIVEPAAS_API_KEY: ${{ secrets.HIVEPAAS_API_KEY }}   # <keyId>:<secret>
  run: hivepaas deploy -p shop -e production -a api --image ghcr.io/acme/shop-api:${{ github.sha }}
```

**Logs:**

```console
$ hivepaas logs -f --since 10m
2026-10-08T14:02:33Z  Listening on :8080
2026-10-08T14:03:01Z  GET /healthz 200 1ms
^C
```

**Environment variables:**

```console
$ hivepaas env ls
KIND     KEY           VALUE
runtime  DATABASE_URL  ${db.HIVEPAAS_URL}
runtime  LOG_LEVEL     info
build    NODE_ENV      production

$ hivepaas env set LOG_LEVEL=debug FEATURE_X=on
Changed LOG_LEVEL, added FEATURE_X (runtime) on api.

$ hivepaas env set --build NODE_ENV=staging
$ hivepaas env unset FEATURE_X
Removed FEATURE_X (runtime) from api.
```

**Restarting** another app of the same environment:

```console
$ hivepaas restart -a web
Restarted web (shop / production).
```

**Templates:**

```console
$ hivepaas templates ls --sort popular --search sql
NAME      TITLE        STARS  VERSIONS
postgres  PostgreSQL   17.2k  18, 17, 16
mariadb   MariaDB      6.4k   11.8, 11.4
mysql     MySQL        12.1k  8.4

$ hivepaas templates deploy postgres --name orders-db -p shop -e staging --param dataVolume=default
Nothing is left on the volumes by a previous install.
Created orders-db (PostgreSQL 18) in shop / staging.
Deployed in 18s.
```

**For scripts**: the API's own data, and exit codes:

```console
$ hivepaas apps ls -p shop -e production -o json | jq -r '.[].name'
api
web
db
cache

$ hivepaas api GET /projects/{project}/{env}/apps/{app}/routing-settings | jq .data.domains
```

**When something is wrong:**

```console
$ hivepaas logs -a apii
Error: no app "apii" in shop / production. Its apps: api, web, db, cache.
$ echo $?
5

$ hivepaas env set LOG_LEVEL=warn
Error: paas.example.com takes changes only from a newer CLI: it is at API level 15,
this CLI at 14. Update it: hivepaas update
$ echo $?
10
```

## 2. Commands

Every command takes these global flags:

| Flag | Environment | Meaning |
|---|---|---|
| `--context NAME` | `HIVEPAAS_CONTEXT` | the installation to use, instead of the current one |
| `-p, --project` | `HIVEPAAS_PROJECT` | project, by name, key or id |
| `-e, --env` | `HIVEPAAS_ENV` | environment, by name |
| `-a, --app` | `HIVEPAAS_APP` | app, by name, key or id |
| `-o, --output` | | `table` (default), `json` or `yaml` |
| `--no-color` | `NO_COLOR` | no ANSI colors; also off when stdout is not a terminal |
| `--debug` | `HIVEPAAS_DEBUG` | log each request and response to stderr, credentials redacted |

What each MVP command calls, and what the API key needs (an API key carries read,
execute, write and delete actions, never beyond its owner's permissions):

| Command | API | Key needs |
|---|---|---|
| `login`, `whoami` | `GET /sessions/me` | read |
| `projects ls` | `GET /projects` | read |
| `projects get P` | `GET /projects/{projectID}` | read |
| `apps ls` | `GET /projects/{projectID}/{projectEnv}/apps` | read |
| `apps get A` | `GET .../apps/{appID}` | read |
| `deploy` | `POST .../apps/{appID}/deploy` | execute |
| `deploy --image R` | `GET` then `PUT .../deployment-settings` with the image changed: the change deploys | write |
| (waiting) | `GET .../deployments/{id}/status`, logs over websocket | read |
| `deploy cancel` | `GET .../deployments` for the running one, `POST .../deployments/{id}/cancel` | execute |
| `logs` | `GET .../apps/{appID}/logs` over websocket | read |
| `restart` | `POST .../apps/{appID}/restart` | execute |
| `env ls` | `GET .../apps/{appID}/env-vars` | read |
| `env set`, `env unset` | `GET` then `PUT .../apps/{appID}/env-vars` | write |
| `templates ls` | `GET /app-templates` (`--sort name\|popular\|trending\|new`, `--category`, `--search`) | read |
| `templates deploy T` | `POST .../apps/from-template/preflight`, then `POST .../apps/from-template` | write |

`templates deploy` runs the preflight first, as the dashboard does: what a previous
install left on the volumes is shown, and the creation goes on only with
`--keep-storage`, `--reset-storage` or the answer to a question in a terminal; a
script that gives neither exits 6 with nothing created. What the creation would
refuse - a domain served already, a port held - is printed, and exits 6 too. A
parameter naming a volume or an app takes its name, and the CLI sends the volume's
id and the app's key; a volume parameter left out takes the project's only volume,
as the dashboard's form does. Then the CLI waits for each deployment the creation
started - the dependencies', the app's, its components' - and says how each ended.

`env set KEY=VALUE...` and `env unset KEY...` change the runtime variables, or
with `--build` or `--shared` the build-time or shared ones - the three lists the
API keeps. `--literal` sets a value that is not expanded (`isLiteral`). The lists
show the variables HivePaaS sets (`isSystem`) beside the app's own: `env ls` leaves
them out, `env ls --all` shows them and the inherited ones, and a write sends only
the app's own.

## 3. Configuration and credentials

**Where.** `$HIVEPAAS_CONFIG_DIR`, or else `$XDG_CONFIG_HOME/hivepaas`, or else
`~/.config/hivepaas` - on macOS too, as `gh` does, so that a dotfiles setup finds
it; `%AppData%\hivepaas` on Windows.

**`config.yaml`** holds the contexts, never a secret:

```yaml
current: prod
contexts:
  prod:
    url: https://paas.example.com
    keyId: 01J9Z6...
  local:
    url: http://localhost:10000
    keyId: 01J9Y2...
```

**Secrets** go to the OS keychain (`github.com/zalando/go-keyring`), service
`hivepaas`, account `<url> <keyId>`. Where there is no keychain - a Linux server
without a secret service - `login` says so and writes `credentials.yaml` beside the
config, mode 0600, only with `--insecure-storage`.

**In CI** nothing is stored: `HIVEPAAS_URL` and `HIVEPAAS_API_KEY` (`<keyId>:<secret>`)
are read on every run, and take precedence over any context. This is the form the
API's `Authorization: Bearer <keyId>:<secret>` already takes.

**`hivepaas login [URL]`** asks for what it is not given: the URL, the key id, and
the secret without echoing it. A URL without a scheme is `https`, but for this
machine - `localhost:10000`, `127.0.0.1`, a `.localhost` name - where a local
installation serves plain `http`. The CLI never falls back from `https` to `http`
on its own: a server that answers plain HTTP is named, with the `http://` URL to
give if that is the one meant, since the key's secret would then cross the
network unencrypted. `--key-id` and `--with-secret` (the secret on stdin)
make it non-interactive. It calls `GET /sessions/me` before saving anything, so a
context that is saved works, and names the user and the server's version. The key
is created in the dashboard - Settings, API keys - with the actions the commands
need (§2); the CLI never asks for a password.

## 4. What a command acts on

A command that acts on an app finds it in this order, and stops at the first that
answers:

1. the flags `-p`, `-e`, `-a`;
2. the environment variables `HIVEPAAS_PROJECT`, `HIVEPAAS_ENV`, `HIVEPAAS_APP`;
3. the link file, `.hivepaas.json`, searched for from the working directory up;
4. a prompt to pick one, when stdin and stdout are terminals;
5. an error saying which flag is missing.

**`hivepaas link`** writes `.hivepaas.json` in the working directory:

```json
{
  "url": "https://paas.example.com",
  "project": { "id": "01J...", "name": "shop" },
  "env": "production",
  "app": { "id": "01J...", "name": "api" }
}
```

It holds no secret and may be committed. Ids are what commands use; names are for
people reading the file. A command run in a linked directory against a context
whose URL is another installation stops: deploying to the wrong installation is the
mistake this file is most likely to cause.

**Names are matched as the MCP server matches them**
(`hivepaas_app/interface/mcp/resolve.go`): by id or key exactly, else by name
ignoring case, through the list endpoints as the key's user - so what the user may
not see is "not found", exactly like a name that does not exist. No match, and
more than one, are both errors listing the candidates. An app made underneath
another (a template's component) is matched by its own name, and listed with its
owner's.

## 5. Deploying

```
hivepaas deploy [--image REF] [--no-cache] [--no-wait] [--timeout 30m]
```

1. With `--image`, the CLI sets the image in the app's deployment settings: it
   reads them, changes the image, and writes them back under their `updateVer` - a
   change made meanwhile in the dashboard makes the write fail rather than be
   overwritten, and the CLI reads again and retries once. A change of the
   deployment settings deploys the app: the server starts that deployment itself,
   and answers its id, so the CLI does not call `POST .../deploy` too. Without
   `--image`, or when the image is the app's already, `POST .../deploy` starts it.
2. Unless `--no-wait`, the CLI follows the deployment's logs over its websocket,
   printing them to stderr, and polls its status every two seconds.
3. It stops when the status is `done` (exit 0), `failed` or `canceled` (exit 8), or
   at `--timeout` (exit 9).

**Ctrl-C stops the CLI, not the deployment.** The deployment goes on on the
server, and the CLI says so before it exits (130), with the two commands that
pick it up again: `hivepaas logs --deployment <id> -f` to follow it, and
`hivepaas deploy cancel <id>` to cancel it. Cancelling is a decision of its own,
never a side effect of an interrupted terminal or a CI job that timed out.

With `-o json`, stdout carries only the finished deployment
(`GET .../deployments/{id}`), so a pipeline can read its id and status.

## 6. Logs

`hivepaas logs [-f] [--since 1h] [--tail 200] [--no-timestamps] [--deployment ID]`

Logs come over a websocket: the app's at `.../apps/{appID}/logs`, a deployment's at
`.../deployments/{id}/logs`, with the query parameters the spec documents (`follow`,
`since`, `duration`, `tail`, `timestamps`). The API key goes in the upgrade
request's headers. Each binary message is a JSON array of `tasklog.LogFrame`
(`type`, `data`, `ts`), the spec's own type. With `-f`, a dropped connection is
opened again with `since` set to the last frame's second, and the lines seen
already are left out. The times are always asked for, so that there is a last one;
`--no-timestamps` only leaves them out of what is written. A deployment's stream
ends with the deployment, and before it starts: it is opened again while the
deployment's status says it has not finished.

With `-o json` each line is a JSON object of its own, a frame, so that a pipe reads
them as they come; there is no `-o yaml` for a stream.

## 7. Output and errors

- Tables and progress are for people; stdout carries the result, stderr everything
  else, so `hivepaas apps ls -o json | jq` never reads a progress line.
- `-o json` and `-o yaml` print the response's `data` as the API sends it: the
  types are the spec's, and the CLI adds nothing a script would come to depend on.
- An API error is the server's `hperrors.ErrorInfo`: the CLI prints its `detail`,
  its `code`, and each field error. With `-o json` it prints the `ErrorInfo` itself
  to stderr.

Exit codes:

| Code | Meaning |
|---|---|
| 0 | success |
| 1 | an error no other code describes |
| 2 | a usage error: an unknown flag, a missing argument |
| 3 | not logged in, or the key was refused (401 with `ERR_NO_SESSION`, `ERR_SESSION_*`, `ERR_API_KEY_INVALID`) |
| 4 | not allowed (403, and the API's 401 `ERR_UNAUTHORIZED` for an action the key or its user may not take) |
| 5 | not found (404), or a name that matches nothing |
| 6 | refused as invalid or conflicting (400, 409, 422) |
| 7 | the server failed or could not be reached (5xx, network) |
| 8 | the deployment failed or was canceled |
| 9 | timed out waiting |
| 10 | the server takes no writes from a CLI this old (426) |
| 130 | interrupted |

## 8. Knowing the server, and what it takes from the CLI

**The API level** is a number the server's spec carries, raised whenever the
request of a write operation that already existed changes: a field added to its
body, a parameter added. `make gen-swag` keeps a fingerprint of each write
operation's request in `docs/openapi/api-level.json` and raises the level when
one of them changes; CI fails when the committed level is behind the spec. New
operations, responses and descriptions do not move it, so a release that only
adds or fixes things asks nothing of the CLI. (hivepaas `ea89ac59`; the level
started at 1.)

**The CLI says what it is.** Every request carries
`HivePaaS-CLI: <cli version>; api-level=<level of the spec it was built from>`,
from the first release on - a CLI that never sent it could never be told it is
too old.

**The server refuses writes from an older CLI.** A request that is not a `GET`,
carrying the header with a level below the server's, is answered `426 Upgrade
Required`, with an `ErrorInfo` naming both levels and how to update. A `GET` is
served. Every request with the header is answered the server's level in
`HivePaaS-API-Level`, and a CLI below it warns once that it reads and may not
write. Requests without the header -
the dashboard, `curl`, a script - are not concerned. That rule is what makes the
read-change-write of §5 and `env set` safe: the CLI writing a settings object
back knows every field the server has.

**A newer CLI works with an older server**: the server ignores fields it does not
know. A command that needs what the server does not have yet says so, from
`GET /sessions/me`, which now answers:

```json
"server": { "version": "v1.0.0-beta4", "apiLevel": 14, "minCliApiLevel": 14 }
```

`hivepaas version` prints the CLI's version and API level, the server release
its spec was pinned from, and the current context's server version and level.

## 9. What the server needs

| | Change |
|---|---|
| B0 (done, `ea89ac59`) | `GET /sessions/me` answers `server: {version, apiLevel, minCliApiLevel}`. `GET /system/hivepaas/release-info` is for an admin with Write on the System module, which a developer's key is not. |
| B1 (done, `ea89ac59`) | The API level: computed by `make gen-swag` from the write requests in the spec, kept in the spec's `info` (`x-api-level`) and `base.APILevel`, checked in CI. `middleware/clilevel` answers 426 (`ERR_CLI_OUTDATED`) to a write from a CLI whose level is below it, and tells every CLI request the server's level in `HivePaaS-API-Level`. |

Nothing else: the CLI writes through the endpoints the dashboard uses.

## 10. The generated client

- **Generator.** oapi-codegen v2, models and client, into `internal/api`. Pinned
  in the Makefile; upgraded on purpose.
- **The spec** is `internal/api/openapi.json`, with the server ref it came from in
  `internal/api/SPEC_REF`. `make update-spec REF=v1.0.0-beta4` fetches it from that
  tag of hivepaas; `make update-spec SPEC=../hivepaas/docs/openapi/swagger.json`
  takes a local file. A release pins a released server, never `main`.
- **`make spec-check SPEC=<file>`** generates the client from that file and runs
  `go build ./...` and `go test ./...`. It is the contract with the server's CI:
  hivepaas's `cli-compat` job runs it against every change to the server, so a
  change that breaks the CLI fails in its own pull request. It leaves the
  regenerated client in the working tree; `git checkout internal/api` undoes it.
- **Weekly**, a job runs `spec-check` against hivepaas `main`, to hear early about
  changes the next server release will bring.
- **Every request the CLI writes back is carried over whole.** The CLI turns what
  a `GET` answers into what the `PUT` takes - the shapes differ: the deployment
  settings answer a registry auth as an object and take its id. For each of
  those requests a test walks the generated request type and fails on a field
  the conversion does not fill. A server change adding a field to such a request
  therefore fails hivepaas's `cli-compat` job until the CLI carries the field,
  and the CLI release goes out before, or with, the server release that raises
  the API level.
- **What is known of the spec** (2026-10-08, against hivepaas `ddc19486`): 763
  operations, each with its own id; every route in it and nothing else; query
  parameters checked against what handlers read; optional, nullable and
  any-value fields marked from the Go types. The generated client is 5.8 MB of Go,
  builds as is, and a binary using it is about 9 MB. The server's CI builds a
  client from every change of the spec (`tools/swag/check-client.sh`).

## 11. Repository layout

```
cmd/hivepaas/main.go
internal/
  api/          the generated client, openapi.json, SPEC_REF
  client/       the client as the commands use it: base URL, key headers, errors as
                typed values, retries on 502/503/504 for reads, --debug logging
  config/       contexts, the keychain, the environment variables
  link/         .hivepaas.json
  resolve/      names to ids
  stream/       the websocket streams: logs now, the terminal later
  output/       table, JSON, YAML; colors
  selfupdate/   the signed release list, its keys, the notice, the replacement
  cmd/          one file per command (cobra)
Makefile        build, test, lint, gen, update-spec, spec-check, release-manifest
release.json, release.signed.json   the releases the CLI updates to (§13)
docs/RELEASING.md
.goreleaser.yaml
.github/workflows/  ci.yml, spec-main.yml, release.yml
```

Dependencies are kept few: cobra, gorilla/websocket (as the server uses),
go-keyring, `golang.org/x/term`, the oapi-codegen runtime, `gopkg.in/yaml.v3`.
Tables use `text/tabwriter`.

## 12. Releases and installation

- goreleaser (`.goreleaser.yaml`, `.github/workflows/release.yml`) builds `hivepaas`
  for Linux, macOS and Windows, amd64 and arm64, on a `v*` tag; the GitHub release
  carries the archives, `checksums.txt`, and a build provenance attestation
  (`actions/attest-build-provenance`), which `gh attestation verify` checks. The
  release is a draft, published by hand, as the server's are.
- A tag is released only with the spec of a server release: `internal/api/SPEC_REF`
  must name a tag of hivepaas, and the client must be what that spec generates. The
  first release therefore follows the first server release with the API level.
- The build is reproducible: the binaries are built with `-trimpath` and the
  commit's time, and the archives' files carry the commit's time and root as owner,
  so a tag built twice gives the same `checksums.txt`.
- A Homebrew tap, `hivepaas/homebrew-tap`, and an install script on hivepaas.com
  follow the first release.
- The CLI's version is its own semver. `hivepaas version` says which server
  release it was built against (§8).

## 13. Updating itself

The server refuses writes from a CLI older than its API (§8), so updating the CLI is
something everyone who uses it does, and it takes one command. The CLI says when a
newer release is out, and `hivepaas update` installs it - only when asked: the CLI
never replaces itself on its own. A pipeline whose binary changes between two runs
is not the same pipeline, and a program that rewrites itself unasked is the last
thing anyone audits.

### What is trusted

A binary that replaces itself runs what it downloads, with its user's rights, on
every machine it is on. A GitHub Release alone is not trusted for that - an account
or a workflow taken over could put anything in one. The CLI trusts a list of its
releases signed offline with the server's release keys, the way an installation
trusts `release.json`.

- **`release.json`**, in this repository, names each channel's current release, and
  for every release the SHA-256 of each archive:

  ```json
  {
    "channels": { "stable": "v0.2.0", "beta": "v0.3.0-beta1" },
    "releases": {
      "v0.2.0": {
        "releaseDate": "2026-10-20",
        "archives": {
          "darwin_arm64": { "file": "hivepaas_0.2.0_darwin_arm64.tar.gz", "sha256": "..." }
        }
      }
    }
  }
  ```

  `make release-manifest TAG=v0.2.0` writes a release's entry from its draft's
  `checksums.txt`. Releases stay in the list, so `--version` can go back to one.
- **`release.signed.json`** is that list signed, on the offline machine, with the
  server's tool and keys - `make release-sign` in hivepaas - under a context of its
  own, `hivepaas-cli-release-v1`. As for the server, both an ed25519 and an
  ML-DSA-65 signature by a key the CLI was built with are required. The context
  keeps the two uses apart - a signed CLI list is not a valid server release, nor
  the reverse - so one set of offline keys serves both. The CLI embeds the same
  `*.pub.pem` as `hivepaas_app/pkg/releasesig/releasekeys`, and a rotation adds the
  new key to both. `make keys-check` compares the two, in the weekly job and before
  a release.
- **The `release` branch** of hivepaas-cli holds the `release.signed.json` the CLI
  reads, from
  `https://raw.githubusercontent.com/hivepaas/hivepaas-cli/release/release.signed.json`.
  A release is offered once the branch moves to it, after its draft is checked - as
  for the server.
- An archive is downloaded from the release the signed list names,
  `https://github.com/hivepaas/hivepaas-cli/releases/download/<version>/<file>`, and
  used only when its SHA-256 is the signed one.

What it does not stop: someone in control of the repository can serve an older
signed list, which hides newer releases. That cannot make the CLI run anything
unsigned, and the CLI never moves to a version older than its own unless
`--version` names it.

### Telling the user

- At most once a day the CLI reads the signed list in the background, while the
  command runs, and keeps what it found in `state.yaml` beside the config. When the
  channel's release is newer than the CLI, a line on stderr after the command says
  so:

  ```
  A new release of hivepaas is out: 0.3.0 (this is 0.2.1). Update it: hivepaas update
  ```
- It never makes a command fail, and hardly slower: the check has two seconds, and a
  command that ends before it waits for it at most one second, once a day. A failure
  - no network, a machine that cannot reach GitHub - is silent.
- Not in CI (`CI` set), not when stderr is not a terminal, not with
  `HIVEPAAS_NO_UPDATE_NOTIFIER=1`, not for a `dev` build.
- A stable CLI follows the `stable` channel; a beta follows the newer of `beta` and
  `stable`.
- When a server refuses a write (exit 10), or answers an API level above the CLI's,
  the message names the command that updates this CLI.

### `hivepaas update`

```
hivepaas update [--check] [--version vX.Y.Z] [--channel stable|beta]
```

1. It reads the signed list and verifies it, and picks the channel's release, or
   the one `--version` names, which must be in the list. Without `--version` it never
   installs a version older than its own. `--check` stops here and says what it
   would do.
2. It refuses where something else installed it, and names what updates it there:
   Homebrew (`brew upgrade hivepaas`), scoop. It tells from where its executable is,
   symlinks resolved.
3. It downloads the archive for its OS and architecture, checks its SHA-256 against
   the signed one, and takes the binary out of it - that one file, at most 100 MB.
4. It writes the binary next to the running one, with its mode, and renames it over
   it: the replacement is atomic, and a failure leaves the old binary. On Windows,
   where a running executable cannot be replaced, the old one is renamed to
   `hivepaas.exe.old` first, and the next run removes it.
5. Without the right to write there - `/usr/local/bin` owned by root - it says so,
   and that `sudo hivepaas update` would.

No library: a few hundred lines on the standard library, its `crypto/mldsa`
included, as the server verifies its releases.

### Releasing, with the list

`docs/RELEASING.md` in this repository: the tag drafts the release (§12);
`make release-manifest TAG=...` adds it to `release.json`; `make release-sign` in
hivepaas, offline, with `IN`, `OUT` and `CONTEXT=cli`, signs it; both files go to
`main` through a pull request; the draft is checked and published; a pull request
moves the `release` branch, and from then on CLIs are told.

### What the server repository changes

- `tools/releasesign` signs and verifies under the context it is given: `-context
  cli` for `hivepaas-cli-release-v1`, the server's when left out, nothing else.
  `scripts/release-sign.sh` passes it on (`CONTEXT=cli`) and checks the envelope
  with openssl under it. `RELEASESIGN_SHA` then moves to the reviewed commit, as for
  any change of the tool.
- `docs/RELEASING.md` and the keys' README name the CLI as a holder of the keys:
  a key added to `releasekeys` is added to the CLI before it signs anything.

The first release of the CLI carries the verification and the keys; `update` has
something to update to from the second.

## 14. Testing

- Unit tests for resolution, the link file, configuration, output and exit codes.
- Command tests against an `httptest` server answering with the generated types,
  and golden files for table output.
- A smoke test against a running installation, before each release: login with a
  key, `projects ls`, `deploy --image` with `--wait`, `logs -f`, `env set`.

## 15. Later

- `hivepaas exec` / `ssh`: a shell in the app's container, over the terminal
  websocket (`.../terminal`, with `w`, `h` and `shell`).
- `hivepaas export` and `hivepaas apply -f`: the configuration spec, validated and
  shown before it is applied.
- `hivepaas compose up -f docker-compose.yml`: a project from a compose file.
- `hivepaas login` through the browser: a device flow, which needs the server to
  issue a key the person approves in the dashboard (B2).
- A GitHub Action wrapping `hivepaas deploy`.
- `hivepaas up`: deploy the working directory's source, which needs the server to
  build from an uploaded archive.
