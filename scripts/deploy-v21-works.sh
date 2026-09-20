#!/usr/bin/env bash
set -euo pipefail
umask 077

UNIT="works-api.service"
EXPECTED_HOST="${EXPECTED_VDS_HOST:-vmi3517816}"
EXPECTED_SOURCE_SHA="${EXPECTED_SOURCE_SHA:-ab8c1d2a6cc322b3d730b1514b1141b8ee65310c}"
BASE="http://127.0.0.1:18191"
EVIDENCE_DIR="${EVIDENCE_DIR_OVERRIDE:-/var/lib/aftergraph/ops}"

[[ "$(hostname -s)" == "${EXPECTED_HOST}" ]] || { echo "deploy: wrong host" >&2; exit 10; }
[[ "$(id -u)" -eq 0 ]] || { echo "deploy: root required" >&2; exit 11; }
systemctl cat "${UNIT}" >/dev/null 2>&1 || { echo "deploy: unit missing" >&2; exit 12; }

# This ops branch may contain only deployment files beyond the merge SHA.
# Any Go/module/build drift means we are no longer deploying the merged source.
git diff --quiet "${EXPECTED_SOURCE_SHA}" -- '*.go' 'go.mod' 'go.sum' 'Makefile' || {
  echo "deploy: source inputs differ from expected merged SHA" >&2
  exit 13
}

make build >/dev/null
[[ -x bin/works-api ]] || { echo "deploy: bin/works-api missing after build" >&2; exit 14; }

exec_raw="$(systemctl show "${UNIT}" -p ExecStart --value)"
target="$(printf '%s' "${exec_raw}" | grep -oE 'path=[^ ;}]+' | head -n1 | cut -d= -f2- || true)"
[[ -n "${target}" && "$(basename "${target}")" == "works-api" && -f "${target}" ]] || {
  echo "deploy: cannot identify installed works-api binary from ExecStart" >&2
  exit 15
}

mkdir -p "${EVIDENCE_DIR}"
stamp="$(date -u +%Y%m%dT%H%M%SZ)"
backup="${target}.bak.${stamp}"
cp -a "${target}" "${backup}"

rollback() {
  local rc="$?"
  echo "deploy: failure; restoring prior works-api binary" >&2
  cp -a "${backup}" "${target}" || true
  systemctl restart "${UNIT}" >/dev/null 2>&1 || true
  exit "${rc}"
}
trap rollback ERR

install -m 0755 bin/works-api "${target}"
systemctl restart "${UNIT}"

healthy=0
for _ in $(seq 1 30); do
  if curl -fsS --max-time 2 "${BASE}/healthz" >/dev/null 2>&1; then
    healthy=1
    break
  fi
  sleep 1
done
[[ "${healthy}" -eq 1 ]] || { echo "deploy: health gate failed" >&2; exit 20; }

installed_sha="$(sha256sum "${target}" | awk '{print $1}')"
built_sha="$(sha256sum bin/works-api | awk '{print $1}')"
[[ "${installed_sha}" == "${built_sha}" ]] || { echo "deploy: installed binary hash mismatch" >&2; exit 21; }

cat >"${EVIDENCE_DIR}/v21-works-deploy.json" <<EOF
{
  "schema": "aftergraph.v21.works-deploy/1.0",
  "host": "$(hostname -s)",
  "source_merge_sha": "${EXPECTED_SOURCE_SHA}",
  "deployed_at": "$(date -u +%Y-%m-%dT%H:%M:%SZ)",
  "binary_sha256": "${installed_sha}",
  "unit": "${UNIT}",
  "health": "pass",
  "rollback_backup_present": true,
  "secrets_exposed": false
}
EOF
chmod 600 "${EVIDENCE_DIR}/v21-works-deploy.json"
trap - ERR

echo "deploy: PASS source=${EXPECTED_SOURCE_SHA} unit=${UNIT} health=pass"
echo "deploy: evidence=${EVIDENCE_DIR}/v21-works-deploy.json"
