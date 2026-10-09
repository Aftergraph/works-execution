#!/usr/bin/env bash
set -euo pipefail
umask 077

ACTION="${1:-status}"
case "$ACTION" in status|enable) ;; *) echo "usage: $0 [status|enable]" >&2; exit 2 ;; esac

ENV_FILE=/etc/works/works.env
SERVICE=works-worker.service
WORKER_UNIT_GLOB='works-worker*.service'
API_BASE_URL=http://127.0.0.1:18191
SOURCE_ROOT=/var/lib/works
SOURCE_PARENT=/var/lib/works/works-sources
MIN_FREE_KB=1048576
MIN_FREE_INODES=10000

fail(){ printf 'works-source-root: %s\n' "$*" >&2; exit 2; }

# fleet_units lists every ACTIVE works-worker unit on the host. Production has
# historically run more than one worker sharing one binary and one env file;
# activating only $SERVICE would silently leave the others checking out to
# host tmpfs. Inactive units (e.g. standby workers) inherit the env file on
# their next start and are deliberately not restarted.
fleet_units() {
  systemctl list-units --type=service --state=active --plain --no-legend --no-pager "$WORKER_UNIT_GLOB" 2>/dev/null | awk '{print $1}'
}

worker_pid() {
  systemctl show "$1" -p MainPID --value
}

worker_exe() {
  local pid
  pid="$(worker_pid "$1")"
  [[ "$pid" =~ ^[1-9][0-9]*$ ]] || return 1
  readlink -f -- "/proc/$pid/exe"
}

service_contract() {
  local unit="$1"
  systemctl is-active --quiet "$unit" || fail "works_worker_inactive:$unit"

  local env_files pid exe
  env_files="$(systemctl show "$unit" -p EnvironmentFiles --value)"
  [[ "$env_files" == *"$ENV_FILE"* ]] || fail "worker_env_file_not_canonical:$unit"

  pid="$(worker_pid "$unit")"
  [[ "$pid" =~ ^[1-9][0-9]*$ ]] || fail "worker_main_pid_invalid:$unit:$pid"

  exe="$(worker_exe "$unit" || true)"
  [[ -n "$exe" && -x "$exe" && -f "$exe" ]] || fail "worker_executable_unreadable:$unit"
  [[ "$(basename -- "$exe")" == "works-worker" ]] || fail "worker_executable_unexpected:$unit:$exe"
  "$exe" -h 2>&1 | grep -q -- '-source-root' || fail "worker_binary_missing_source_root_contract:$unit"
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
  pid="$(worker_pid "$1")"
  [[ "$pid" =~ ^[1-9][0-9]*$ ]] || return 1
  tr '\0' '\n' <"/proc/$pid/environ" | awk -F= '$1=="WORKS_SOURCE_ROOT"{sub(/^[^=]*=/,""); print; exit}'
}

# fleet_runtime_source_roots prints "<unit> <runtime-value>" per fleet unit.
fleet_runtime_source_roots() {
  local unit
  while IFS= read -r unit; do
    [[ -n "$unit" ]] || continue
    printf '%s %s\n' "$unit" "$(runtime_source_root "$unit" || true)"
  done < <(fleet_units)
}

# fleet_verified checks that every active fleet unit reads SOURCE_ROOT from its
# runtime environment. Any unit still on the old (empty or /tmp-derived) value
# makes the whole activation unverified.
fleet_verified() {
  local unit value
  while IFS=' ' read -r unit value; do
    [[ -n "$unit" ]] || continue
    [[ "$value" == "$SOURCE_ROOT" ]] || return 1
  done < <(fleet_runtime_source_roots)
  return 0
}

# restart_fleet restarts every active unit and requires a fresh PID plus a
# correct runtime readback for each. It fails closed on the first unit that
# does not come back healthy.
restart_fleet() {
  local unit old_pid new_pid i value
  while IFS= read -r unit; do
    [[ -n "$unit" ]] || continue
    old_pid="$(worker_pid "$unit")"
    systemctl restart "$unit"
    local healthy=false
    for _ in $(seq 1 60); do
      if systemctl is-active --quiet "$unit"; then
        new_pid="$(worker_pid "$unit")"
        value="$(runtime_source_root "$unit" || true)"
        if [[ "$new_pid" =~ ^[1-9][0-9]*$ && "$new_pid" != "$old_pid" && "$value" == "$SOURCE_ROOT" ]]; then
          healthy=true
          break
        fi
      fi
      sleep 0.5
    done
    [[ "$healthy" == true ]] || fail "worker_source_root_readback_failed:$unit"
  done < <(fleet_units)
}

status_probe() {
  systemctl is-active --quiet "$SERVICE" || fail "works_worker_inactive"
  api_health
  local unit exe contract
  exe="$(worker_exe "$SERVICE" || true)"
  contract=false
  if [[ -n "$exe" && -x "$exe" && -f "$exe" ]] && "$exe" -h 2>&1 | grep -q -- '-source-root'; then
    contract=true
  fi
  if [[ "${EUID:-$(id -u)}" -eq 0 && -r "$ENV_FILE" ]]; then
    local configured
    configured="$(configured_value || true)"
    printf '{"worker_service":"active","api_health":true,"binary_source_root_contract":%s,"configured_source_root":"%s","fleet":[' "$contract" "$configured"
    local first=true unit value
    while IFS=' ' read -r unit value; do
      [[ -n "$unit" ]] || continue
      [[ "$first" == true ]] || printf ','
      first=false
      printf '{"unit":"%s","runtime_source_root":"%s"}' "$unit" "$value"
    done < <(fleet_runtime_source_roots)
    printf ']}\n'
  else
    printf '{"worker_service":"active","api_health":true,"binary_source_root_contract":%s,"configured_source_root":"root_read_required","runtime_source_root":"root_read_required","fleet":"root_read_required"}\n' "$contract"
  fi
}

[[ "$ACTION" == status ]] && { status_probe; exit 0; }

[[ "${EUID:-$(id -u)}" -eq 0 ]] || fail "root_required"
[[ -f "$ENV_FILE" && ! -L "$ENV_FILE" ]] || fail "canonical_env_file_missing_or_symlinked"
[[ "$(readlink -f -- "$ENV_FILE")" == "$ENV_FILE" ]] || fail "canonical_env_file_redirected"
[[ "$(stat -c '%u:%g' "$ENV_FILE")" == "0:0" ]] || fail "env_file_not_root_owned"
mode="$(stat -c '%a' "$ENV_FILE")"
case "$mode" in 400|600|640) ;; *) fail "env_file_permissions_too_open:$mode" ;; esac

FLEET="$(fleet_units)"
[[ -n "$FLEET" ]] || fail "no_active_worker_units"
for unit in $FLEET; do
  service_contract "$unit"
done
api_health
root_filesystem_contract

current="$(configured_value || true)"
if [[ "$current" == "$SOURCE_ROOT" ]] && fleet_verified; then
  mkdir -p -- "$SOURCE_PARENT"
  chown root:root "$SOURCE_PARENT"
  chmod 700 "$SOURCE_PARENT"
  [[ "$(stat -c '%u:%g' "$SOURCE_PARENT")" == "0:0" ]] || fail "source_parent_not_root_owned"
  [[ "$(stat -c '%a' "$SOURCE_PARENT")" == "700" ]] || fail "source_parent_mode_invalid"
  printf '{"worker_service":"active","api_health":true,"source_root":"%s","fleet_units":%s,"runtime_verified":true,"changed":false}\n' "$SOURCE_ROOT" "$(printf '%s\n' "$FLEET" | wc -l)"
  exit 0
fi

backup="$(mktemp /run/works.env.before-source-root.XXXXXX)"
cp -a -- "$ENV_FILE" "$backup"

rollback(){
  local rc=$?
  cp -a -- "$backup" "$ENV_FILE" || true
  local unit
  for unit in $FLEET; do
    systemctl restart "$unit" || true
  done
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
[[ "$(stat -c '%u:%g' "$SOURCE_PARENT")" == "0:0" ]] || fail "source_parent_not_root_owned"
[[ "$(stat -c '%a' "$SOURCE_PARENT")" == "700" ]] || fail "source_parent_mode_invalid"

restart_fleet

for unit in $FLEET; do
  service_contract "$unit"
done
api_health
root_filesystem_contract
[[ "$(configured_value || true)" == "$SOURCE_ROOT" ]] || fail "env_source_root_readback_failed"
fleet_verified || fail "fleet_source_root_readback_failed"

rm -f -- "$backup"
trap - ERR INT TERM HUP
printf '{"worker_service":"active","api_health":true,"source_root":"%s","fleet_units":%s,"runtime_verified":true,"changed":true,"credential_value_exposed":false}\n' "$SOURCE_ROOT" "$(printf '%s\n' "$FLEET" | wc -l)"
