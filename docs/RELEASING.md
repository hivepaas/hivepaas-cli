# Releasing the CLI

How a version of `hivepaas` reaches the people who use it: what to set up once,
then the steps of every release, a beta (`v0.3.0-beta1`) or a stable one
(`v0.3.0`).

## What a release is, and who can change it

| Piece | Where | Made by | Trusted because |
|---|---|---|---|
| The archives and `checksums.txt` | the GitHub Release of the tag | the Release workflow, on a tag | their build provenance (`gh attestation verify`), and the signed list below |
| `release.json` / `release.signed.json` | the `release` branch | you: `make release-manifest`, then signed offline in hivepaas | `hivepaas update` installs only an archive whose SHA-256 the list carries, signed by both of hivepaas's release keys under the CLI's context |

The workflow never signs and never publishes. A compromised CI can build archives,
but cannot make `hivepaas update` install them: that takes the offline keys.

## Versions

- Tags: `vMAJOR.MINOR.PATCH`, or `vMAJOR.MINOR.PATCH-betaN` for a beta - **no dot
  before the number**, as for the server: the CLI compares versions in that form.
- A stable CLI follows the `stable` channel; a beta follows the newer of `beta` and
  `stable`.
- The CLI's version is its own: it does not follow the server's.

## Once, before the first release

1. **GitHub environment `release`** (Settings › Environments): required reviewers,
   and the deployment tag rule `v*` with no branch. The job that drafts the release
   waits for a reviewer.
2. **Rulesets** (Settings › Rules): tags `v*` created only by maintainers, never
   updated or deleted; branch `release` changed only through a pull request, no
   force push, no deletion.
3. **The signing tool**: `RELEASESIGN_SHA` in hivepaas's Makefile must name a
   commit whose `tools/releasesign` takes `-context` (hivepaas `6f8aa215` or later).
4. **The `release` branch** does not exist until the first release creates it
   (step 7).

## Every release

The example is `v0.2.0`.

1. **The spec of a released server.** The CLI calls a server people run:
   `make update-spec REF=<hivepaas tag>`, commit, and main is green. The Release
   workflow refuses a tag whose `internal/api/SPEC_REF` is not a hivepaas tag.

2. **Tag**, on the commit to release:
   ```bash
   git tag -a v0.2.0 -m "hivepaas CLI v0.2.0"
   git push origin v0.2.0
   ```
   The **Release** workflow checks the tag - the spec is a server release's, the
   client is what it generates, the CLI trusts hivepaas's release keys, the tests
   pass - then, after a reviewer approves, builds the six archives and drafts the
   GitHub Release with them, `checksums.txt`, and their provenance.

3. **The list.** With `gh` signed in (a draft is visible only to those who can
   push):
   ```bash
   make release-manifest TAG=v0.2.0
   ```
   It adds the release to `release.json`, with the SHA-256 of its six archives from
   the draft's `checksums.txt`, and makes it the `stable` channel's (`beta` for a
   beta); it refuses to move a channel back. Review the diff.

4. **Sign, on the offline machine**, with both repositories checked out side by
   side:
   ```bash
   cd hivepaas
   make release-sign KEYS="/offline/2026_ed.key /offline/2026_ml.key" CONTEXT=cli \
     IN=../hivepaas-cli/release.json OUT=../hivepaas-cli/release.signed.json
   ```
   It writes `release.signed.json` and checks it again with openssl, under the
   CLI's context. Commit `release.json` and `release.signed.json` to `main` through
   a pull request.

5. **Check the draft.** Download an archive for your machine with
   `gh release download v0.2.0 -p '*darwin_arm64*'`, check it with
   `gh attestation verify <archive> --repo hivepaas/hivepaas-cli`, and run it: `version`,
   `login`, `app ls`, a `deploy`.

6. **Publish** the draft.

7. **Offer it.** A pull request from `main` into `release`; the first time, create
   `release` from that commit. From then on CLIs of the channel are told, and
   `hivepaas update` installs it.

## Undoing a release

- **Before step 7**: delete the draft; nothing was offered to anyone.
- **After step 7**: point `release` back at the previous signed list (a pull
  request reverting step 7). CLIs stop being offered the release; those that
  updated stay on it until a fixed release supersedes it, or until their users run
  `hivepaas update --version <previous>`.
- Never move or delete a pushed tag: the list names releases by it.

## Keys

The CLI trusts the public keys of hivepaas's
`hivepaas_app/pkg/releasesig/releasekeys`, copied into `internal/selfupdate/keys`.
`make keys-check RELEASEKEYS=<hivepaas>/hivepaas_app/pkg/releasesig/releasekeys`
compares the two; the weekly *Spec of hivepaas main* job and the Release workflow
run it. When hivepaas adds a key, copy it here and release before the list is
signed with it; sign the list with the old key too for as long as CLIs built
before that matter - a CLI updates only when its user asks.
