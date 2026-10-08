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
bin/hivepaas logs -f --since 10m
bin/hivepaas env set LOG_LEVEL=debug
```

From CI, with nothing stored:

```yaml
- name: Deploy
  env:
    HIVEPAAS_URL: https://paas.example.com
    HIVEPAAS_API_KEY: ${{ secrets.HIVEPAAS_API_KEY }}   # <keyId>:<secret>
  run: hivepaas deploy -p shop -e production -a api --image ghcr.io/acme/shop-api:${{ github.sha }}
```

## Developing

| Target | What it does |
| --- | --- |
| `make build`, `make test`, `make lint` | as they say |
| `make gen` | the client in `internal/api`, from the pinned spec `internal/api/openapi.json` |
| `make update-spec REF=<hivepaas tag>` | pin the spec of a server release; `SPEC=<file>` for a local one |
| `make spec-check SPEC=<file>` | build and test against another spec, as hivepaas's CI does for every change of its API |

A command that writes an object back - the deployment settings, the environment
variables - carries every field of it through `internal/carry`, whose tests fail on
a field of the request it does not fill.
