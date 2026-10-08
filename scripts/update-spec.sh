#!/bin/bash
# Pins the spec the client is generated from: REF=<hivepaas tag or commit> takes
# it from that ref on GitHub, SPEC=<file> from a file (a hivepaas checkout's
# docs/openapi/swagger.json while developing both).
set -eo pipefail

dest=internal/api/openapi.json
if [[ -n "${SPEC}" ]]; then
  cp "${SPEC}" "${dest}"
  echo "local $(date -u +%Y-%m-%d)" > internal/api/SPEC_REF
elif [[ -n "${REF}" ]]; then
  curl -fsSL "https://raw.githubusercontent.com/hivepaas/hivepaas/${REF}/docs/openapi/swagger.json" -o "${dest}"
  echo "${REF}" > internal/api/SPEC_REF
else
  echo "usage: make update-spec REF=<hivepaas tag or commit> | SPEC=<file>" >&2
  exit 2
fi
