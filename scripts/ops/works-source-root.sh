#!/usr/bin/env bash
set -euo pipefail
umask 077

ACTION="\${1:-status}"
case "$ACTION" in status|enable) ;; *) echo "usage: $0 [status|enable]" >&2; exit 2 ;; esac

ENV_FILE=/etc/works/works.env
SERVICE=works-worker.service
API_BASE_URL=http://127.0.0.1:18191
WORKER_BIN=/opt/works/bin/works-worker
SOURCE_ROOT=/var/lib/works
SOURCE_PARENT=/var/lib/works/works-sources
MIN_FREE_KB=1048576
MIN_FREE_INODES=10000

fail(){ printf 'works-source-root: %s\n' "$*" >&2; exit 2; }

service_contract() {
  systemctl is-active --quiet "$SERVICE" || fail "works_worker_inactive"
  local env_files exec_start
  env_files="$(systemctl show "$SERVICE" -p EnvironmentFiles --value)"
  exec_start="$(systemctl show "$SERVICE" -p ExecStart --value)"
  [[ "$env_files" == *"$ENV_FILE"* ]] || fail "worker_env_file_not_canonical"
  [[ "$exec_start" == *"$WORKER_BIN"* ]] || fail "worker_exec_not_canonical"
  [[ -x "$WORKER_BIN" && -f "$WORKER_BIN" && ! -L "$WORKER_BIN" ]] || fail "canonical_worker_binary_missing_or_redirected"
  "$WORKER_BIN" -h 2>&1 | grep -q -- '-source-root' || fail "worker_binary_missing_source_root_contract"
}

api_health() {
  curl -fsS --max-time 2 "$API_BASE_URL/healthz" >/dev/null || fail "works_api_health_failed"
}

root_filesystem_contract() {
  [[ -d "$SOURCE_ROOT" && ! -L "$SOURCE_ROOT" ]] || fail "source_root_missing_or_symlinked"
  [[ "$(readlink -f -- "$SOURCE_ROOT")" == "$SOURCE_ROOT" ]] || fail "source_root_redirected"

  local opts free_kb free_inodes
  opts="$(findmnt -no OPTIONS --target "$SOURCE_ROOT" 2>/dev/null || true)"
  [[ -n "$opts" ]] || fail "source_root_mount_unknown"
  case ",$opts," in *,noexec,*) fail "source_root_mount_noexec" ;; esac

  free_kb="$(df -Pk "$SOURCE_ROOT" | awk 'NR==2 {print $4}')"
  free_inodes="$(df -Pi "$SOURCE_ROOT" | awk 'NR==2 {print $4}')"
  [[ "$free_kb" =~ ^[0-9]+$ && "$free_kb" -ge "$MIN_FREE_KB" ]] || fail "source_root_low_space_kb:$free_kb"
  [[ "$free_inodes" =~ ^[0-9]+$ && "$free_inodes" -ge "$MIN_FREE_INODES" ]] || fail "source_root_low_inodes:$free_inodes"
}

configured_value() {
  awk -F= '$1=="WORKS_SOURCE_ROOT"{sub(/^[^=]*=/,""); print; exit}' "$ENV_FILE"
}

runtime_source_root() {
  local pid
  pid="$(systemctl show "$SERVICE" -p MainPID --value)"
  [[ "$pid" =~ ^[1-9][0-9]*$ ]] || return 1
  tr '\0' '\n' <"/proc/$pid/environ" | awk -F= '$1=="WORKS_SOURCE_ROOT"{sub(/^[^=]*=/,""); print; exit}'
}

status_probe() {
  service_contract
  api_health
  if [[ "\${EUID:-$(id -u)}" -eq 0 && -r "$ENV_FILE" ]]; then
    local configured runtime
    configured="$(configured_value || true)"
    runtime="$(runtime_source_root || true)"
    printf '{"worker_service":"active","api_health":true,"binary_source_root_contract":true,"configured_source_root":"%s","runtime_source_root":"%s"}\n' "$configured" "$runtime"
  else
    printf '{"worker_service":"active","api_health":true,"binary_source_root_contract":true,"configured_source_root":"root_read_required","runtime_source_root":"root_read_required"}\n'
  fi
}

[[ "$ACTION" == status ]] && { status_probe; exit 0; }

[[ "\${EUID:-$(id -u)}" -eq 0 ]] || fail "root_required"
[[ -f "$ENV_FILE" && ! -L "$ENV_FILE" ]] || fail "canonical_env_file_missing_or_symlinked"
[[ "$(readlink -f -- "$ENV_FILE")" == "$ENV_FILE" ]] || fail "canonical_env_file_redirected"
[[ "$(stat -c '%u:%g' "$ENV_FILE")" == "0:0" ]] || fail "env_file_not_root_owned"
mode="$(stat -c '%a' "$ENV_FILE")"
case "$mode" in 400|600|640) ;; *) fail "env_file_permissions_too_open:$mode" ;; esac

service_contract
api_health
root_filesystem_contract

current="$(configured_value || true)"
runtime="$(runtime_source_root || true)"
if [[ "$current" == "$SOURCE_ROOT" && "$runtime" == "$SOURCE_ROOT" ]]; then
  mkdir -p -- "$SOURCE_PARENT"
  chmod 700 "$SOURCE_PARENT"
  printf '{"worker_service":"active","api_health":true,"source_root":"%s","runtime_verified":true,"changed":false}\n' "$SOURCE_ROOT"
  exit 0
fi

backup="$(mktemp /run/works.env.before-source-root.XXXXXX)"
cp -a -- "$ENV_FILE" "$backup"
rollback(){
  local rc=$?
  cp -a -- "$backup" "$ENV_FILE" || true
  systemctl restart "$SERVICE" || true
  rm -f -- "$backup"
  exit "$rc"
}
trap rollback ERR INT TERM HUP

tmp="$(mktemp /etc/works/.works.env.source-root.XXXXXX)"
awk '!/^WORKS_SOURCE_ROOT=/' "$ENV_FILE" >"$tmp"
printf 'WORKS_SOURCE_ROOT=%s\n' "$SOURCE_ROOT" >>"$tmp"
chown --reference="$ENV_FILE" "$tmp"
chmod --reference="$ENV_FILE" "$tmp"
mv -f -- "$tmp" "$ENV_FILE"

mkdir -p -- "$SOURCE_PARENT"
chown root:root "$SOURCE_PARENT"
chmod 700 "$SOURCE_PARENT"

systemctl restart "$SERVICE"

healthy=false
for _ in $(seq 1 60); do
  if systemctl is-active --quiet "$SERVICE"; then
    runtime="$(runtime_source_root || true)"
    if [[ "$runtime" == "$SOURCE_ROOT" ]]; then
      healthy=true
      break
    fi
  fi
  sleep 0.5
done
[[ "$healthy" == true ]] || fail "worker_source_root_readback_failed"

api_health
root_filesystem_contract
[[ "$(configured_value || true)" == "$SOURCE_ROOT" ]] || fail "env_source_root_readback_failed"
[[ "$(runtime_source_root || true)" == "$SOURCE_ROOT" ]] || fail "runtime_source_root_readback_failed"

rm -f -- "$backup"
trap - ERR INT TERM HUP
printf '{"worker_service":"active","api_health":true,"source_root":"%s","runtime_verified":true,"changed":true,"credential_value_exposed":false}\n' "$SOURCE_ROOT"
