# hivepaas

The command-line client for [HivePaaS](https://github.com/hivepaas/hivepaas): deploy
an app from CI, follow its logs, change its environment, create an app from a
template.

The design is
[docs/superpowers/specs/2026-10-08-cli-design.md](docs/superpowers/specs/2026-10-08-cli-design.md).
It is made for HivePaaS v1.0.0-beta4 (API level 2). Against a server whose API is
newer it still reads, says so, and the server refuses its changes until it is
updated.

## Install

Each release's archives are on its
[GitHub Release](https://github.com/hivepaas/hivepaas-cli/releases): Linux, macOS
and Windows (a `.zip`), on amd64 and arm64. Check where an archive was built
before you run it:

```sh
v=1.0.0-beta1 os=darwin arch=arm64
gh release download "v$v" --repo hivepaas/hivepaas-cli -p "hivepaas_${v}_${os}_${arch}.tar.gz"
gh attestation verify "hivepaas_${v}_${os}_${arch}.tar.gz" --repo hivepaas/hivepaas-cli
tar xzf "hivepaas_${v}_${os}_${arch}.tar.gz" hivepaas && sudo mv hivepaas /usr/local/bin/
```

From then on `hivepaas update` installs a newer release, and only one that a
list signed offline with HivePaaS's release keys names. To build it from source
instead: `make build`, which writes `bin/hivepaas`.

## Use

```sh
hivepaas login https://paas.example.com
hivepaas link -p shop -e production -a api
hivepaas deploy --image ghcr.io/acme/shop-api:1.4.3
hivepaas deploy --ref release --dockerfile docker/Dockerfile   # an app built from its repository
hivepaas deploy settings
hivepaas ps                                # where each replica runs, and why one failed
hivepaas job run migrate                   # a scheduled job, now, waited for
hivepaas cp ./config.yaml :/app/config.yaml
hivepaas secret set STRIPE_KEY             # the value asked for, not on the command line
hivepaas secret set SENTRY_DSN --scope project   # the project's, for its apps
hivepaas logs -f --since 10m
hivepaas status                            # what needs attention on the installation
hivepaas backup ls && hivepaas backup restore a1b2c3d4 --stop-app
hivepaas preview create --ref feature/checkout
hivepaas compose up -p shop -e staging     # a compose file's services as apps
hivepaas env set LOG_LEVEL=debug
hivepaas update                # a newer release, checked against a list signed offline
```

From CI, with nothing stored:

```yaml
- name: Deploy
  env:
    HIVEPAAS_URL: https://paas.example.com
    HIVEPAAS_API_KEY: ${{ secrets.HIVEPAAS_API_KEY }}   # <keyId>:<secret>
  run: hivepaas deploy -p shop -e production -a api --image ghcr.io/acme/shop-api:${{ github.sha }}
```

An app built from its repository deploys the commit a job was run for with
`--commit ${{ github.sha }}`; with `--no-auto-deploy` the push itself does not
deploy it too.

A function is a directory's code
([design](docs/superpowers/specs/2026-10-09-function-design.md)):

```sh
hivepaas function init hello --runtime node24       # the runtime's starter code
hivepaas function create hello hello -p shop -e staging --runtime node24
cd hello
hivepaas function run --query name=Ada              # this code, called once, not deployed
hivepaas function deploy                            # linked to it: no flags
```

## Developing

| Target | What it does |
| --- | --- |
| `make build`, `make test`, `make lint` | as they say |
| `make gen` | the client in `internal/api`, from the pinned spec `internal/api/openapi.json` |
| `make update-spec REF=<hivepaas tag>` | pin the spec of a server release; `SPEC=<file>` for a local one |
| `make spec-check SPEC=<file>` | build and test against another spec, as hivepaas's CI does for every change of its API |
| `make release-manifest TAG=<tag>` | add a release to `release.json`, the list `hivepaas update` installs from: [docs/RELEASING.md](docs/RELEASING.md) |
| `make keys-check RELEASEKEYS=<dir>` | the CLI trusts the release keys hivepaas signs with |
| `make e2e` | the built CLI against a throwaway installation: the dashboard's `e2e/env/up.sh` first, then `HP_E2E_BASE_URL=http://localhost:10100`; [the design](docs/superpowers/specs/2026-10-09-cli-e2e-design.md) |

A command that writes an object back - the deployment settings, the environment
variables - carries every field of it through `internal/carry`, whose tests fail on
a field of the request it does not fill.
