# Functions from the terminal: function init, create, deploy, run, pull

A function is code HivePaaS runs on one of its runtimes - a handler that answers
one request. Writing it on one's own machine and sending it from there is what a
command line is for: `hivepaas function` does it on the endpoints the
dashboard's function pages use. It extends [the CLI's design](2026-10-08-cli-design.md);
nothing changes on the server.

| Command | API |
|---|---|
| `function init [DIR] --runtime R` | none: the runtime's starter code, kept in the CLI |
| `function create NAME [DIR]` | `POST .../apps/function`, then the deployment it starts |
| `function deploy [DIR]` | `GET`, `PUT .../apps/{appID}/deployment-settings`; `POST .../deploy` when nothing changed |
| `function run [DIR]` | `POST .../apps/{appID}/function/test-run` |
| `function pull [DIR]` | `GET .../apps/{appID}/deployment-settings` |
| `function get`, `function ls` | `GET .../deployment-settings`; `GET .../apps`, the apps of category `function` |
| `function metrics [--range R]` | `GET .../apps/{appID}/function-metrics` |

`fn` is short for `function`. A function's logs are its app's: `hivepaas logs`.
Its domains are its app's: `hivepaas domain`.

## Runtimes

`node24`, `bun1`, `python313`, `go127`: what the server takes, with their
default entrypoints - `index.js` and `default`, `index.ts` and `default`,
`main.py` and `handler`, the package `.` and `Handle`.

## A function's code, from a directory

`create`, `deploy` and `run` send the code of a directory, `.` unless one is
given, as the API takes it: its files, by path, as text.

- **Left out:** `.git`, `node_modules`, `__pycache__`, `.venv`, `venv`,
  `.hivepaas` (HivePaaS's own directory in a function's source), the link file
  `.hivepaas.json`, `.DS_Store`, and what the directory's own `.gitignore`
  ignores. Its `.gitignore` is read as git reads one at the root of a
  repository: `#` comments, `!` negation, a trailing `/` for directories, a
  slash elsewhere anchoring to the directory, `*`, `?`, `[...]` and `**`.
  `.gitignore` files below it, and the repository's, are not read.
- **Checked before anything is sent,** as the server would refuse it:
  at most 100 files and 1 MB in all - the error names the largest - every file
  text (UTF-8), every path spelled plainly (`A-Z a-z 0-9 . _ / -`, at most 255
  characters). A link is left out and said.
- **`--list`** shows the files that would go and their size, and sends nothing.

## init

`function init [DIR] --runtime R` writes the runtime's starter code into `DIR`,
made if missing: the handler the dashboard starts a function from, which
answers `?name=Ada` with `{"hello":"Ada"}`, and the files it needs
(`package.json`, `go.mod`). `--typescript` writes Node.js's in TypeScript,
`index.ts`, as the dashboard offers. Without `--runtime`, a terminal asks;
elsewhere it is required. A file there already is not overwritten - the command stops and
names it - unless `--force`. The code is a copy of the dashboard's
(`function-templates.constants.ts`), embedded in the CLI: a change of one is
made in the other.

## create

`function create NAME [DIR] --runtime R` makes a function in the selected
environment and deploys it: `DIR`'s code, or with `--repo URL` a repository's
(`--ref`, `--commit`, `--path` - the function's directory in it -,
`--git-credential`, `--auto-deploy`/`--no-auto-deploy`, as `deploy` takes them).
Its settings, all optional, the server's defaults otherwise:

| Flag | Setting |
|---|---|
| `--entrypoint FILE`, `--handler NAME` | where the runtime finds the handler; without `--entrypoint`, the runtime's default file, or else the directory's one `index` file of the runtime's (`index.ts` from `init --typescript`), said |
| `--call-timeout 30s` | how long one call may take, 1s to 15m |
| `--max-concurrency 16` | calls one replica takes at once |
| `--max-body 6MB` | the largest request body |
| `--packages a,b` | Debian packages the runtime image gets; `""` for none |
| `--push-to CRED` | the registry the built image goes to; `none` keeps it on the node |
| `--domain D` | served at `D` over HTTPS from its first deployment |

The command waits for the first deployment, following its logs, as `deploy`
does (`--no-wait`, `--timeout`, Ctrl-C). A function made from a directory's code
has the directory linked to it, so that `function deploy` there needs no flags -
unless `--no-link`, or a link to another app is there already, which is kept and
said.

## deploy

`function deploy [DIR]` sends the function's code and settings, which deploys
it, and waits as `deploy` does.

- **Inline code:** `DIR`'s files replace the function's, whole - a file removed
  here is removed there. What changed is said: files added, changed, removed.
- **A repository's code:** no `DIR`. `--ref`, `--commit`, `--path`,
  `--git-credential` and the auto-deploy flags change what is built.
- **Switching:** `--use inline` sends `DIR`'s code to a function built from a
  repository; `--use repo --repo URL` makes one with inline code build from a
  repository. The function's runtime and settings stay.
- **Settings:** the flags of `create` but `--domain` change one setting each; the
  rest stays. The write is a read-modify-write under `updateVer`, as `deploy`'s:
  a change made meanwhile is read, and changed, again.
- **Nothing changed** - the same files and settings: the function is deployed
  again (`POST .../deploy`), as `hivepaas deploy` does.
- An app that is not a function is refused, with `hivepaas deploy` named; and
  `hivepaas deploy` on a function with source flags names `function deploy`.
  `hivepaas deploy` alone still deploys a function again.

## run

`function run [DIR]` calls the function once with `DIR`'s code, not yet saved
or deployed, in a throwaway container on a build node with the function's
variables and secrets - the dashboard's Test run.

- **The request:** `--method GET`, `--path /`, `--query K=V` and `--header 'K: V'`
  (each repeatable), `--data TEXT`, `@FILE`, or `@-` for stdin; at most 1 MB.
- **What it answered:** the body on stdout, as it is, for a pipe; on stderr the
  status, how long it took, then what the function logged - a message logged
  with the context's logger as its text, what it printed as it is, the
  runtime's line of the call left out. The libraries the
  run installed first are said, their log with `--debug`. A body or a log cut at
  1 MB is said.
- **Exit:** 0 when the function answered, whatever its status - as `curl`;
  with `--fail`, 8 for a status of 400 or more. 8 when it did not answer: an
  error, a timeout, libraries that failed to install, a handler that is not
  there; the server's outcome and error are said.
- **Lock files:** a run that resolved the libraries brings back the lock files
  it made or changed (`package-lock.json`, `bun.lock`, `requirements.lock`; for
  Go `go.mod` and `go.sum`). They are said; `--save-lock` writes them into `DIR`,
  so that each deployment installs the same versions.
- **`-o json`:** the whole answer, the body as text when it is UTF-8 and as
  base64 (`bodyBase64`) when not.
- It waits up to 20 minutes - a call may take 15, and libraries are installed
  first; Ctrl-C stops it.

The API types the answer's body as an array of numbers; it is JSON's base64. The
CLI reads the answer itself rather than through the generated client.

A directory given to `deploy`, `run` or `pull` is where the target's link is
looked for first; with none there, the working directory's link is, as for
every command.

## pull

`function pull [DIR]` writes the function's code, as kept on the server - code
written in the dashboard - into `DIR`, made if missing. A file there with other
content stops it, named, unless `--force`. A function built from a repository
has no code to pull: the command names the repository, ref and directory.

## get, ls, metrics

- `function get [FUNCTION]`: runtime, entrypoint, code (inline: files and size;
  a repository: URL, ref, commit, directory), packages, call timeout, max
  concurrency, max body, registry. `-o json` gives the function's source as the
  API does.
- `function ls`: the environment's functions - the apps of category `function` -
  as `app ls` lists apps.
- `function metrics [--range 1h|6h|24h|7d]`, 24h by default: calls, failed, 4xx,
  5xx, p50/p95/p99, by outcome, and the paths called most. When the server has
  none it says why (its `reason`).

## Exit codes

As for every command: 2 usage, 5 not found, 6 refused by the server's checks,
7 server, 8 a deployment - or a run - that failed, 9 timeout, 130 interrupted.
A directory whose code the server would refuse - too large, binary, a path
spelled otherwise - is 6, before anything is sent.

## Left out

- Scheduled calls of a function (a job of kind function-invoke): made in the
  dashboard; `hivepaas job` runs them.
- Calling a deployed function: it is an address - `curl` it, or `hivepaas open`.
- A function's starter code from the server: the CLI keeps its own copy.
