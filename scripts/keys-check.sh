#!/bin/bash
# Checks that the CLI trusts the keys hivepaas signs with: the *.pub.pem in
# internal/selfupdate/keys are those of RELEASEKEYS, a hivepaas checkout's
# hivepaas_app/pkg/releasesig/releasekeys, the same files and nothing else.
#
# A key hivepaas adds must reach the CLI before it signs the CLI's list with it,
# and a key it removes must leave: see the keys' README there.
set -euo pipefail

ours=internal/selfupdate/keys
theirs="${RELEASEKEYS:?usage: make keys-check RELEASEKEYS=<hivepaas>/hivepaas_app/pkg/releasesig/releasekeys}"
[[ -d "$theirs" ]] || { echo "keys-check: $theirs is not a directory" >&2; exit 2; }

list() { (cd "$1" && ls -1 -- *.pub.pem 2>/dev/null | sort); }
if [[ "$(list "$ours")" != "$(list "$theirs")" ]]; then
  echo "keys-check: the CLI's keys are not hivepaas's" >&2
  diff <(list "$ours") <(list "$theirs") | sed 's/^/  /' >&2 || true
  echo "  copy $theirs/*.pub.pem into $ours, and remove what is not there" >&2
  exit 1
fi
for key in $(list "$ours"); do
  cmp -s "$ours/$key" "$theirs/$key" || { echo "keys-check: $key differs from hivepaas's" >&2; exit 1; }
done
echo "keys-check: the CLI trusts hivepaas's release keys: $(list "$ours" | tr '\n' ' ')"
