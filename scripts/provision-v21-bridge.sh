#!/usr/bin/env bash
set -euo pipefail
umask 077

EXPECTED_HOST="${EXPECTED_VDS_HOST:-vmi3517816}"
TG_UNIT="tg-gateway.service"
WORKS_UNIT="works-api.service"
SHARED_DIR="${AFTERGRAPH_ENV_DIR:-/etc/aftergraph}"
SHARED_ENV="${SHARED_DIR}/v21-bridge.env"
TG_DROPIN="/etc/systemd/system/${TG_UNIT}.d/90-aftergraph-v21-bridge.conf"
WORKS_DROPIN="/etc/systemd/system/${WORKS_UNIT}.d/90-aftergraph-v21-bridge.conf"
EVIDENCE_DIR="${EVIDENCE_DIR_OVERRIDE:-/var/lib/aftergraph/ops}"
TG_BASE="http://127.0.0.1:8800"
WORKS_BASE="http://127.0.0.1:18191"
STAMP="$(date -u +%Y%m%dT%H%M%SZ)"
MUTATED=0

host="$(hostname -s)"
[[ "${host}" == "${EXPECTED_HOST}" ]] || {
  echo "provision: host mismatch (${host}); expected ${EXPECTED_HOST}" >&2
  exit 10
}
[[ "$(id -u)" -eq 0 ]] || { echo "provision: must run as root" >&2; exit 11; }

for unit in "${TG_UNIT}" "${WORKS_UNIT}"; do
  systemctl cat "${unit}" >/dev/null 2>&1 || {
    echo "provision: required unit missing: ${unit}" >&2
    exit 13
  }
done
command -v openssl >/dev/null || { echo "provision: openssl missing" >&2; exit 14; }
command -v curl >/dev/null || { echo "provision: curl missing" >&2; exit 14; }

if [[ "${PROVISION_DRY_RUN:-0}" == "1" ]]; then
  test -w /etc/systemd/system
  mkdir -p "${SHARED_DIR}" "${EVIDENCE_DIR}"
  test -w "${SHARED_DIR}"
  echo "provision: DRY-RUN PASS host=${host}"
  echo "provision: dedicated shared env + systemd drop-in boundary is writable"
  exit 0
fi

read_env() {
  local file="$1" key="$2"
  [[ -f "${file}" ]] || return 0
  awk -v k="${key}" 'index($0,k"=")==1 { print substr($0,length(k)+2) }' "${file}" | tail -n1
}

existing_token="$(read_env "${SHARED_ENV}" WORKS_API_TOKEN)"
existing_bridge="$(read_env "${SHARED_ENV}" WORKS_PLATFORM_BRIDGE_SECRET)"
if [[ -n "${existing_token}" && ${#existing_token} -lt 32 ]]; then
  echo "provision: existing WORKS_API_TOKEN is shorter than 32 bytes; refusing silent rotation" >&2
  exit 20
fi
if [[ -n "${existing_bridge}" && ${#existing_bridge} -lt 32 ]]; then
  echo "provision: existing WORKS_PLATFORM_BRIDGE_SECRET is shorter than 32 bytes; refusing silent rotation" >&2
  exit 20
fi

platform_token="${existing_token:-$(openssl rand -hex 32)}"
bridge_secret="${existing_bridge:-$(openssl rand -hex 32)}"

mkdir -p "${SHARED_DIR}" "$(dirname "${TG_DROPIN}")" "$(dirname "${WORKS_DROPIN}")" "${EVIDENCE_DIR}"

ENV_BACKUP=""
TG_BACKUP=""
WORKS_BACKUP=""
[[ -f "${SHARED_ENV}" ]] && { ENV_BACKUP="${SHARED_ENV}.bak.${STAMP}"; cp -a "${SHARED_ENV}" "${ENV_BACKUP}"; }
[[ -f "${TG_DROPIN}" ]] && { TG_BACKUP="${TG_DROPIN}.bak.${STAMP}"; cp -a "${TG_DROPIN}" "${TG_BACKUP}"; }
[[ -f "${WORKS_DROPIN}" ]] && { WORKS_BACKUP="${WORKS_DROPIN}.bak.${STAMP}"; cp -a "${WORKS_DROPIN}" "${WORKS_BACKUP}"; }

rollback() {
  local rc="$?"
  if [[ "${MUTATED}" -eq 1 ]]; then
    if [[ -n "${ENV_BACKUP}" ]]; then cp -a "${ENV_BACKUP}" "${SHARED_ENV}"; else rm -f "${SHARED_ENV}"; fi
    if [[ -n "${TG_BACKUP}" ]]; then cp -a "${TG_BACKUP}" "${TG_DROPIN}"; else rm -f "${TG_DROPIN}"; fi
    if [[ -n "${WORKS_BACKUP}" ]]; then cp -a "${WORKS_BACKUP}" "${WORKS_DROPIN}"; else rm -f "${WORKS_DROPIN}"; fi
    systemctl daemon-reload || true
  fi
  exit "${rc}"
}
trap rollback ERR

tmp="$(mktemp "${SHARED_DIR}/v21-bridge.env.tmp.XXXXXX")"
cat >"${tmp}" <<EOF
WORKS_API_URL=${WORKS_BASE}
WORKS_API_TOKEN=${platform_token}
WORKS_PLATFORM_BRIDGE_SECRET=${bridge_secret}
EOF
chmod 600 "${tmp}"
chown root:root "${tmp}"
mv -f "${tmp}" "${SHARED_ENV}"

dropin_tmp="$(mktemp)"
cat >"${dropin_tmp}" <<EOF
[Service]
EnvironmentFile=${SHARED_ENV}
EOF
chmod 644 "${dropin_tmp}"
cp -f "${dropin_tmp}" "${TG_DROPIN}"
cp -f "${dropin_tmp}" "${WORKS_DROPIN}"
rm -f "${dropin_tmp}"
MUTATED=1

systemctl daemon-reload

# Verify systemd sees the dedicated file without printing environment values.
systemctl cat "${TG_UNIT}" | grep -Fq "EnvironmentFile=${SHARED_ENV}"
systemctl cat "${WORKS_UNIT}" | grep -Fq "EnvironmentFile=${SHARED_ENV}"

if [[ "${PROVISION_RESTART:-0}" == "1" ]]; then
  systemctl restart "${WORKS_UNIT}"
  for _ in $(seq 1 20); do
    curl -fsS --max-time 2 "${WORKS_BASE}/healthz" >/dev/null 2>&1 && break
    sleep 1
  done
  curl -fsS --max-time 2 "${WORKS_BASE}/healthz" >/dev/null

  systemctl restart "${TG_UNIT}"
  for _ in $(seq 1 20); do
    curl -fsS --max-time 2 "${TG_BASE}/healthz" >/dev/null 2>&1 && break
    sleep 1
  done
  curl -fsS --max-time 2 "${TG_BASE}/healthz" >/dev/null
fi

token_fp="$(printf '%s' "${platform_token}" | sha256sum | awk '{print substr($1,1,16)}')"
bridge_fp="$(printf '%s' "${bridge_secret}" | sha256sum | awk '{print substr($1,1,16)}')"
cat >"${EVIDENCE_DIR}/v21-bridge-provision.json" <<EOF
{
  "schema": "aftergraph.v21.bridge-provision/1.0",
  "host": "${host}",
  "provisioned_at": "$(date -u +%Y-%m-%dT%H:%M:%SZ)",
  "shared_environment_file": "${SHARED_ENV}",
  "works_api_url": "${WORKS_BASE}",
  "platform_token_sha256_prefix": "${token_fp}",
  "bridge_secret_sha256_prefix": "${bridge_fp}",
  "systemd_dropins": "pass",
  "services_restarted": "${PROVISION_RESTART:-0}",
  "secrets_exposed": false
}
EOF
chmod 600 "${EVIDENCE_DIR}/v21-bridge-provision.json"

MUTATED=0
trap - ERR
echo "provision: PASS host=${host} shared-env=${SHARED_ENV}"
echo "provision: credentials staged without printing secret material"
echo "provision: evidence=${EVIDENCE_DIR}/v21-bridge-provision.json"
