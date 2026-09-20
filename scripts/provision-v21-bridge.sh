#!/usr/bin/env bash
set -euo pipefail
umask 077

EXPECTED_HOST="${EXPECTED_VDS_HOST:-vmi3517816}"
TG_UNIT="tg-gateway.service"
WORKS_UNIT="works-api.service"
TG_BASE="http://127.0.0.1:8800"
WORKS_BASE="http://127.0.0.1:18191"
STAMP="$(date -u +%Y%m%dT%H%M%SZ)"
APPLIED=0

short_host="$(hostname -s)"
if [[ "${short_host}" != "${EXPECTED_HOST}" ]]; then
  echo "provision: host mismatch (${short_host}); expected ${EXPECTED_HOST}" >&2
  exit 10
fi
if [[ "$(id -u)" -ne 0 ]]; then
  echo "provision: must run as root" >&2
  exit 11
fi
for unit in "${TG_UNIT}" "${WORKS_UNIT}"; do
  systemctl cat "${unit}" >/dev/null 2>&1 || {
    echo "provision: required unit missing: ${unit}" >&2
    exit 13
  }
done

unit_workdir() {
  systemctl show "$1" -p WorkingDirectory --value | head -n1
}

unit_envfile() {
  local raw
  raw="$(systemctl show "$1" -p EnvironmentFiles --value | head -n1)"
  raw="${raw#-}"
  printf '%s' "${raw%% *}"
}

TG_REPO="${TG_REPO_OVERRIDE:-$(unit_workdir "${TG_UNIT}")}"
TG_ENV="${TG_ENV_OVERRIDE:-$(unit_envfile "${TG_UNIT}")}"
WORKS_ENV="${WORKS_ENV_OVERRIDE:-$(unit_envfile "${WORKS_UNIT}")}"
EVIDENCE_DIR="${TG_REPO}/data/ops"

for p in "${TG_REPO}" "${TG_ENV}" "${WORKS_ENV}"; do
  [[ -e "${p}" ]] || { echo "provision: required runtime path missing: ${p}" >&2; exit 12; }
done

remote="$(git -C "${TG_REPO}" remote get-url origin 2>/dev/null || true)"
case "${remote}" in
  https://github.com/Aftergraph/trust-gateway.git|git@github.com:Aftergraph/trust-gateway.git) ;;
  *)
    echo "provision: TG WorkingDirectory is not canonical Aftergraph/trust-gateway (origin=${remote:-missing})" >&2
    exit 15
    ;;
esac

command -v openssl >/dev/null || { echo "provision: openssl missing" >&2; exit 14; }
command -v curl >/dev/null || { echo "provision: curl missing" >&2; exit 14; }

if [[ "${PROVISION_DRY_RUN:-0}" == "1" ]]; then
  echo "provision: DRY-RUN PASS host=${short_host}"
  echo "provision: systemd-derived paths and canonical TG remote verified"
  exit 0
fi

read_env() {
  local file="$1" key="$2"
  awk -v k="${key}" 'index($0,k"=")==1 { print substr($0,length(k)+2) }' "${file}" | tail -n1
}

choose_shared() {
  local key="$1" left="$2" right="$3"
  if [[ -n "${left}" && ${#left} -lt 32 ]]; then
    echo "provision: ${key} in TG env is shorter than 32 bytes; refusing silent rotation" >&2
    exit 20
  fi
  if [[ -n "${right}" && ${#right} -lt 32 ]]; then
    echo "provision: ${key} in WORKS env is shorter than 32 bytes; refusing silent rotation" >&2
    exit 20
  fi
  if [[ -n "${left}" && -n "${right}" && "${left}" != "${right}" ]]; then
    echo "provision: ${key} differs across TG and WORKS; refusing destructive rotation" >&2
    exit 21
  fi
  if [[ -n "${left}" ]]; then printf '%s' "${left}"; return; fi
  if [[ -n "${right}" ]]; then printf '%s' "${right}"; return; fi
  openssl rand -hex 32
}

upsert_env() {
  local file="$1" key="$2" value="$3" tmp uid gid
  tmp="$(mktemp "${file}.tmp.XXXXXX")"
  uid="$(stat -c '%u' "${file}")"
  gid="$(stat -c '%g' "${file}")"
  awk -v k="${key}" 'index($0,k"=")!=1 { print }' "${file}" >"${tmp}"
  printf '%s=%s\n' "${key}" "${value}" >>"${tmp}"
  chown "${uid}:${gid}" "${tmp}"
  chmod 600 "${tmp}"
  mv -f "${tmp}" "${file}"
}

TG_BACKUP="${TG_ENV}.bak.${STAMP}"
WORKS_BACKUP="${WORKS_ENV}.bak.${STAMP}"
cp -a "${TG_ENV}" "${TG_BACKUP}"
cp -a "${WORKS_ENV}" "${WORKS_BACKUP}"

rollback() {
  local rc="$?"
  if [[ "${APPLIED}" -eq 1 ]]; then
    echo "provision: failure after mutation; restoring env backups" >&2
    cp -a "${TG_BACKUP}" "${TG_ENV}" || true
    cp -a "${WORKS_BACKUP}" "${WORKS_ENV}" || true
    systemctl restart "${WORKS_UNIT}" >/dev/null 2>&1 || true
    systemctl restart "${TG_UNIT}" >/dev/null 2>&1 || true
  fi
  exit "${rc}"
}
trap rollback ERR

tg_token="$(read_env "${TG_ENV}" WORKS_API_TOKEN)"
works_token="$(read_env "${WORKS_ENV}" WORKS_API_TOKEN)"
tg_bridge="$(read_env "${TG_ENV}" WORKS_PLATFORM_BRIDGE_SECRET)"
works_bridge="$(read_env "${WORKS_ENV}" WORKS_PLATFORM_BRIDGE_SECRET)"

platform_token="$(choose_shared WORKS_API_TOKEN "${tg_token}" "${works_token}")"
bridge_secret="$(choose_shared WORKS_PLATFORM_BRIDGE_SECRET "${tg_bridge}" "${works_bridge}")"

upsert_env "${WORKS_ENV}" WORKS_API_TOKEN "${platform_token}"
upsert_env "${WORKS_ENV}" WORKS_PLATFORM_BRIDGE_SECRET "${bridge_secret}"
upsert_env "${TG_ENV}" WORKS_API_URL "${WORKS_BASE}"
upsert_env "${TG_ENV}" WORKS_API_TOKEN "${platform_token}"
upsert_env "${TG_ENV}" WORKS_PLATFORM_BRIDGE_SECRET "${bridge_secret}"
APPLIED=1

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

mkdir -p "${EVIDENCE_DIR}"
token_fp="$(printf '%s' "${platform_token}" | sha256sum | awk '{print substr($1,1,16)}')"
bridge_fp="$(printf '%s' "${bridge_secret}" | sha256sum | awk '{print substr($1,1,16)}')"
cat >"${EVIDENCE_DIR}/v21-bridge-provision.json" <<EOF
{
  "schema": "aftergraph.v21.bridge-provision/1.0",
  "host": "${short_host}",
  "provisioned_at": "$(date -u +%Y-%m-%dT%H:%M:%SZ)",
  "works_api_url": "${WORKS_BASE}",
  "platform_token_sha256_prefix": "${token_fp}",
  "bridge_secret_sha256_prefix": "${bridge_fp}",
  "works_health": "pass",
  "trust_gateway_health": "pass",
  "secrets_exposed": false
}
EOF
chmod 600 "${EVIDENCE_DIR}/v21-bridge-provision.json"
APPLIED=0
trap - ERR

echo "provision: PASS host=${short_host} works=:18191 tg=:8800"
echo "provision: credentials synchronized without printing secret material"
echo "provision: evidence=${EVIDENCE_DIR}/v21-bridge-provision.json"
