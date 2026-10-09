# hivepaas

The command-line client for [HivePaaS](https://github.com/hivepaas/hivepaas): deploy
an app from CI, follow its logs, change its environment, create an app from a
template.

The design is
[docs/superpowers/specs/2026-10-08-cli-design.md](docs/superpowers/specs/2026-10-08-cli-design.md).
There is no release yet: build it from source.

```sh
make build                       # bin/hivepaas
bin/hivepaas login https://paas.example.com
bin/hivepaas link -p shop -e production -a api
bin/hivepaas deploy --image ghcr.io/acme/shop-api:1.4.3
bin/hivepaas deploy --ref release --dockerfile docker/Dockerfile   # an app built from its repository
bin/hivepaas deploy settings
bin/hivepaas ps                                # where each replica runs, and why one failed
bin/hivepaas job run migrate                   # a scheduled job, now, waited for
bin/hivepaas cp ./config.yaml :/app/config.yaml
bin/hivepaas secret set STRIPE_KEY             # the value asked for, not on the command line
bin/hivepaas logs -f --since 10m
bin/hivepaas status                            # what needs attention on the installation
bin/hivepaas backup ls && bin/hivepaas backup restore a1b2c3d4 --stop-app
bin/hivepaas preview create --ref feature/checkout
bin/hivepaas compose up -p shop -e staging     # a compose file's services as apps
bin/hivepaas env set LOG_LEVEL=debug
bin/hivepaas update                # a newer release, checked against a list signed offline
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
bin/hivepaas function init hello --runtime node24       # the runtime's starter code
bin/hivepaas function create hello hello -p shop -e staging --runtime node24
cd hello
../bin/hivepaas function run --query name=Ada           # this code, called once, not deployed
../bin/hivepaas function deploy                         # linked to it: no flags
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

A command that writes an object back - the deployment settings, the environment
variables - carries every field of it through `internal/carry`, whose tests fail on
a field of the request it does not fill.
