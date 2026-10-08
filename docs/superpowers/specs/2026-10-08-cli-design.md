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
7. **A write changes what it names, and nothing else.** No command reads a whole
   settings object, edits it and writes it back: a CLI older than the server would
   write back without the fields it does not know, and erase them. `deploy --image`
   and `env set` call server operations that change one thing (§9).
8. **The server says what it is.** `GET /sessions/me` reports the server's version
   and the features the CLI may use (§8); a command that needs a feature the server
   lacks says so instead of failing halfway.

## 1. Scope of the MVP

| Command | What it does |
|---|---|
| `login [URL]`, `logout`, `context ls\|use\|rm`, `whoami` | manage installations and keys |
| `projects ls`, `projects get` | list and show projects, with their environments |
| `apps ls`, `apps get` | list and show apps |
| `link`, `unlink` | tie a directory to an app |
| `deploy [--image REF]` | deploy, and by default wait for the result while following its logs |
| `logs [-f]` | an app's logs |
| `restart` | restart an app |
| `env ls\|set\|unset` | an app's environment variables |
| `templates ls`, `templates deploy` | the template store |
| `api METHOD PATH` | any endpoint, authenticated, JSON in and out |
| `version`, `completion` | the CLI's version and shell completion |

`api` is the escape hatch, as in `gh api`: everything the MVP does not wrap is
still one command away, with the context's URL and key applied.

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
| `-y, --yes` | | do not ask for confirmation |

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
| `deploy --image R` | the same, with `image` (B1) | execute |
| (waiting) | `GET .../deployments/{id}/status`, logs over websocket | read |
| `logs` | `GET .../apps/{appID}/logs` over websocket | read |
| `restart` | `POST .../apps/{appID}/restart` | execute |
| `env ls` | `GET .../apps/{appID}/env-vars` | read |
| `env set`, `env unset` | `PATCH .../apps/{appID}/env-vars` (B2) | write |
| `templates ls` | `GET /app-templates` (`--sort`, `--category`, `--search`) | read |
| `templates deploy T` | `POST .../apps/from-template/preflight`, then `POST .../apps/from-template` | write |

`templates deploy` runs the preflight first, as the dashboard does: what a previous
install left on the volumes is shown, and the creation goes on only with
`--reset-storage` or a confirmation.

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
the secret without echoing it. `--key-id` and `--with-secret` (the secret on stdin)
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

1. `POST .../deploy` starts a deployment; with `--image`, the request carries the
   image (B1) and the server sets it and deploys in one step.
2. Unless `--no-wait`, the CLI follows the deployment's logs over its websocket,
   printing them to stderr, and polls its status every two seconds.
3. It stops when the status is `done` (exit 0), `failed` or `canceled` (exit 8), or
   at `--timeout` (exit 9).

Ctrl-C stops the waiting, not the deployment, and says so with the command that
follows it again (`hivepaas logs --deployment <id> -f`). `--cancel-on-interrupt`
cancels it instead, through `POST .../deployments/{id}/cancel`.

With `-o json`, stdout carries only the finished deployment
(`GET .../deployments/{id}`), so a pipeline can read its id and status.

## 6. Logs

`hivepaas logs [-f] [--since 1h] [--tail 200] [--no-timestamps] [--deployment ID]`

Logs come over a websocket: the app's at `.../apps/{appID}/logs`, a deployment's at
`.../deployments/{id}/logs`, with the query parameters the spec documents (`follow`,
`since`, `duration`, `tail`, `timestamps`). The API key goes in the upgrade
request's headers. Each binary message is a JSON array of `tasklog.LogFrame`
(`type`, `data`, `ts`), the spec's own type. With `-f`, a dropped connection is
opened again with `since` set to the last frame's time, so nothing is lost and
little is repeated.

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
| 3 | not logged in, or the key was refused (401) |
| 4 | not allowed (403) |
| 5 | not found (404), or a name that matches nothing |
| 6 | refused as invalid or conflicting (400, 409, 422) |
| 7 | the server failed or could not be reached (5xx, network) |
| 8 | the deployment failed or was canceled |
| 9 | timed out waiting |
| 130 | interrupted |

## 8. Knowing the server

`GET /sessions/me` gains a `server` object (B0):

```json
"server": { "version": "v1.0.0-beta4", "versionCode": "v000001", "features": ["deploy-image", "env-vars-patch"] }
```

- `features` names what the server can do that a CLI may need, one string each,
  added with the feature. A command checks for the one it needs and, when it is
  missing, says which server version brings it. This is sturdier than comparing
  versions: beta, stable and later backports all just list what they have.
- A server that sends no `server` predates B0: the CLI goes on, and the commands
  that need B1 or B2 say the server is too old.
- `hivepaas version` prints the CLI's version, the server release its spec was
  pinned from, and the current context's server version.

## 9. What the server needs

Three small additions to hivepaas, made before the CLI's first release:

| | Change | Why |
|---|---|---|
| B0 | `GET /sessions/me` answers `server: {version, versionCode, features}` | §8. `GET /system/hivepaas/release-info` is for an admin with Write on the System module, which a developer's key is not. |
| B1 | `POST .../deploy` takes an optional `image` | Deploying a new tag from CI is one call, atomic, and changes nothing else: today it is a `PUT` of the whole deployment settings, whose response and request shapes differ, and an older client would erase fields it does not know. Refused for an app that does not deploy an image. |
| B2 | `PATCH .../env-vars` with `set` and `unset` lists | `env set A=1` changes A. The server applies the change under the settings' `updateVer`, so two changes at once do not lose each other, and an older client cannot drop fields it does not know. |

B2's body, for each of the three lists the `PUT` has (`runtime`, `shared`,
`buildtime`):

```json
{ "set": [{ "kind": "runtime", "key": "A", "value": "1", "isLiteral": false }],
  "unset": [{ "kind": "runtime", "key": "B" }] }
```

It answers the env vars as `GET` does, and applies them as the `PUT` does today.

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
  cmd/          one file per command (cobra)
Makefile        build, test, lint, gen, update-spec, spec-check
.goreleaser.yaml
.github/workflows/  ci.yml, spec-main.yml, release.yml
```

Dependencies are kept few: cobra, gorilla/websocket (as the server uses),
go-keyring, `golang.org/x/term`, the oapi-codegen runtime, `gopkg.in/yaml.v3`.
Tables use `text/tabwriter`.

## 12. Releases and installation

- goreleaser builds `hivepaas` for Linux, macOS and Windows, amd64 and arm64, on a
  `v*` tag; the GitHub release carries the archives, `checksums.txt`, and a build
  provenance attestation (`actions/attest-build-provenance`), which
  `gh attestation verify` checks.
- A Homebrew tap, `hivepaas/homebrew-tap`, and an install script on hivepaas.com
  follow the first release.
- The CLI's version is its own semver. `hivepaas version` says which server
  release it was built against (§8).

## 13. Testing

- Unit tests for resolution, the link file, configuration, output and exit codes.
- Command tests against an `httptest` server answering with the generated types,
  and golden files for table output.
- A smoke test against a running installation, before each release: login with a
  key, `projects ls`, `deploy --image` with `--wait`, `logs -f`, `env set`.

## 14. Later

- `hivepaas exec` / `ssh`: a shell in the app's container, over the terminal
  websocket (`.../terminal`, with `w`, `h` and `shell`).
- `hivepaas export` and `hivepaas apply -f`: the configuration spec, validated and
  shown before it is applied.
- `hivepaas compose up -f docker-compose.yml`: a project from a compose file.
- `hivepaas login` through the browser: a device flow, which needs the server to
  issue a key the person approves in the dashboard (B3).
- A GitHub Action wrapping `hivepaas deploy`.
- `hivepaas up`: deploy the working directory's source, which needs the server to
  build from an uploaded archive.
