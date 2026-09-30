#!/usr/bin/env bash
set -euo pipefail
umask 077

MARKER='[deploy-works-api]'
SERVICE='works-api.service'
TARGET=''
BASE_URL='http://127.0.0.1:18191'
SMOKE_WORK_ID='wrk_3995b52a8e30d244dc83f6413bba0df2'
RECEIPT_DIR='/var/lib/works/deployments'
DEPLOY_LOCK='/run/lock/aftergraph-works-api-deploy.lock'
LOCK_WAIT_SECONDS=180

fail() {
  printf 'works-api-live-deploy: %s\n' "$*" >&2
  exit 2
}

# The native WORKS pipeline and an operator can observe the same marked main
# commit. Serialize them before either can create a rollback copy or replace
# the API binary. Wait briefly so duplicate exact-head jobs reconcile through
# the idempotent path after the first deployment completes. When invoked by a
# least-privilege operator, acquire the root-owned lock while re-executing the
# guarded script under sudo.
if [[ "${WORKS_API_DEPLOY_LOCKED:-}" != "1" ]]; then
  command -v flock >/dev/null 2>&1 || fail "flock_unavailable"
  if [[ "$(id -u)" -ne 0 ]]; then
    if sudo -n env WORKS_API_DEPLOY_LOCKED=1 flock -w "$LOCK_WAIT_SECONDS" "$DEPLOY_LOCK" "$0" "$@"; then
      exit 0
    fi
    fail "deployment_lock_timeout_or_operator_authority_unavailable"
  fi
  exec 9>"$DEPLOY_LOCK"
  flock -w "$LOCK_WAIT_SECONDS" 9 || fail "deployment_lock_timeout"
fi

json_skip() {
  printf '{"deployment":"skipped","reason":"%s","sha":"%s"}\n' "$1" "$2"
}

revision_of() {
  local binary="$1"
  go version -m "$binary" 2>/dev/null |
    sed -n 's/^[[:space:]]*build[[:space:]]*vcs\.revision=//p' |
    head -n 1
}

modified_of() {
  local binary="$1"
  go version -m "$binary" 2>/dev/null |
    sed -n 's/^[[:space:]]*build[[:space:]]*vcs\.modified=//p' |
    head -n 1
}

health_ok() {
  curl -fsS --max-time 2 "$BASE_URL/healthz" >/dev/null 2>&1
}

load_enrollment_secret() {
  if [[ -n "${WORKS_ENROLL_SECRET:-}" ]]; then
    printf '%s' "$WORKS_ENROLL_SECRET"
    return 0
  fi

  local env_file="${WORKS_DEPLOY_ENV_FILE:-/etc/works/works.env}"
  sudo -n awk -F= '
    $1 == "WORKS_ENROLL_SECRET" {
      value = substr($0, index($0, "=") + 1)
      first = substr(value, 1, 1)
      last = substr(value, length(value), 1)
      if ((first == "\"" && last == "\"") || (first == "\047" && last == "\047")) {
        value = substr(value, 2, length(value) - 2)
      }
      print value
      found = 1
      exit
    }
    END { if (!found) exit 1 }
  ' "$env_file"
}

integrity_smoke() {
  local enroll_secret request_file enrollment_file bearer_config out
  [[ -n "${tmpdir:-}" && -d "$tmpdir" ]] || return 1
  enroll_secret="$(load_enrollment_secret)" || return 1
  [[ -n "$enroll_secret" ]] || return 1

  request_file="$tmpdir/deploy-smoke-enroll-request.json"
  enrollment_file="$tmpdir/deploy-smoke-enrollment.json"
  bearer_config="$tmpdir/deploy-smoke-bearer.curl"
  printf '%s' "$enroll_secret" |
    python3 -c 'import json,sys; print(json.dumps({"worker_id":"wrkr_deploy_smoke","challenge":sys.stdin.read(),"ttl_seconds":60}))' \
      > "$request_file" || return 1
  curl -fsS --max-time 5 -H 'Content-Type: application/json' \
    --data-binary "@$request_file" "$BASE_URL/v1/workers/enroll" > "$enrollment_file" || return 1
  if ! python3 - "$enrollment_file" "$bearer_config" <<'PY'
import json
import re
import sys

with open(sys.argv[1], encoding="utf-8") as source:
    token = json.load(source)["token"]
if not re.fullmatch(r"[A-Za-z0-9_.-]{16,4096}", token):
    raise SystemExit("enrollment returned an invalid bearer token")
with open(sys.argv[2], "w", encoding="utf-8") as config:
    config.write(f'header = "Authorization: Bearer {token}"\n')
PY
  then
    return 1
  fi
  chmod 0600 "$bearer_config" || return 1

  out="$(curl -fsS --max-time 5 --config "$bearer_config" \
    "$BASE_URL/v1/works/$SMOKE_WORK_ID/evidence")" || return 1
  grep -Fq '"canonicalization":"aftergraph-json-canonical/1"' <<<"$out" || return 1
  grep -Fq '"algorithm":"sha256"' <<<"$out" || return 1
  grep -Fq '"algorithm":"blake3"' <<<"$out" || return 1
  grep -Fq '"algorithm":"hmac-sha256-v1"' <<<"$out" || return 1
}

verify_live_candidate() {
  local installed_revision installed_hash pid runtime_hash
  installed_revision="$(revision_of "$TARGET" || true)"
  installed_hash="$(sudo sha256sum "$TARGET" | awk '{print $1}')" || return 1
  pid="$(sudo systemctl show "$SERVICE" -p MainPID --value)" || return 1
  [[ "$pid" =~ ^[1-9][0-9]*$ ]] || return 1
  runtime_hash="$(sudo sha256sum "/proc/$pid/exe" | awk '{print $1}')" || return 1
  [[ "$installed_revision" == "$sha" && "$installed_hash" == "$candidate_hash" &&
    "$runtime_hash" == "$candidate_hash" ]] || return 1
  health_ok || return 1
  printf '%s' "$pid"
}

write_receipt() {
  local changed="$1" pid="$2" verified_at deployed_at receipt_local receipt_path receipt_tmp nonce
  [[ "$changed" == "true" || "$changed" == "false" ]] || return 1
  verified_at="$(date -u +%Y-%m-%dT%H:%M:%SZ)" || return 1
  nonce="$(date -u +%Y%m%dT%H%M%SZ).$$" || return 1
  if [[ "$changed" == "true" ]]; then
    deployed_at="\"$verified_at\""
  else
    deployed_at="null"
  fi

  receipt_local="$tmpdir/receipt-$changed.json"
  receipt_path="$RECEIPT_DIR/works-api-$sha.json"
  receipt_tmp="$RECEIPT_DIR/.works-api-$sha.$nonce.tmp"
  cat >"$receipt_local" <<EOF
{
  "schema": "aftergraph.works-api-deployment/2",
  "service": "works-api.service",
  "source_sha": "$sha",
  "binary_sha256": "$candidate_hash",
  "main_pid": $pid,
  "deployed_at": $deployed_at,
  "verified_at": "$verified_at",
  "changed": $changed,
  "healthz": true,
  "integrity_fabric_smoke": true,
  "smoke_work_id": "$SMOKE_WORK_ID"
}
EOF
  sudo install -d -m 0750 "$RECEIPT_DIR" || return 1
  sudo install -m 0640 "$receipt_local" "$receipt_tmp" || return 1
  if ! sudo mv -f "$receipt_tmp" "$receipt_path" || ! sudo cmp -s "$receipt_local" "$receipt_path"; then
    sudo rm -f "$receipt_tmp" >/dev/null 2>&1 || true
    return 1
  fi
  printf '%s' "$receipt_path"
}

sha="$(git rev-parse HEAD)"
[[ "$sha" =~ ^[0-9a-f]{40}$ ]] || fail "head_not_exact_sha:$sha"
short_sha="$(printf '%s' "$sha" | cut -c1-12)"

# Pull requests execute the same WORKS graph. Deployment is allowed only when
# the exact candidate is the current origin/main commit.
git fetch --no-tags --depth=1 origin main >/dev/null 2>&1 || fail "fetch_main_failed"
main_sha="$(git rev-parse FETCH_HEAD)"
if [[ "$sha" != "$main_sha" ]]; then
  json_skip "not_current_main" "$sha"
  exit 0
fi

message="$(git log -1 --pretty=%B)"
if [[ "$message" != *"$MARKER"* ]]; then
  json_skip "one_shot_marker_absent" "$sha"
  exit 0
fi

# The deployment worker may mutate production only through passwordless,
# non-interactive operator authority. Never prompt for credentials.
sudo -n true >/dev/null 2>&1 || fail "noninteractive_deploy_authority_unavailable"
sudo systemctl is-active --quiet "$SERVICE" || fail "works_api_not_active"
exec_start="$(sudo systemctl show "$SERVICE" -p ExecStart --value)"
TARGET="$(sed -n 's/.*path=\([^ ;}]*\).*/\1/p' <<<"$exec_start" | head -n 1)"
[[ "$TARGET" == /* ]] || fail "exec_start_path_unresolved"
[[ "$(basename -- "$TARGET")" == "works-api" ]] || fail "exec_start_not_works_api:$TARGET"
sudo test -x "$TARGET" || fail "canonical_target_missing:$TARGET"

tmpdir="$(mktemp -d)"
candidate="$tmpdir/works-api"
backup=""
mutated=false

cleanup() {
  local rc=$?
  local restore_next backup_hash pid running_hash rollback_verified
  trap - EXIT
  set +e
  if [[ "$rc" -ne 0 && "$mutated" == true && -n "$backup" ]] && sudo test -f "$backup"; then
    restore_next="$TARGET.rollback-next.$short_sha.$$"
    rollback_verified=false
    if sudo cp -a "$backup" "$restore_next" && sudo mv -f "$restore_next" "$TARGET" &&
      sudo systemctl restart "$SERVICE"; then
      for _ in $(seq 1 40); do
        health_ok && break
        sleep 0.5
      done
      backup_hash="$(sudo sha256sum "$backup" | awk '{print $1}')"
      pid="$(sudo systemctl show "$SERVICE" -p MainPID --value)"
      if [[ "$pid" =~ ^[1-9][0-9]*$ ]] && health_ok; then
        running_hash="$(sudo sha256sum "/proc/$pid/exe" | awk '{print $1}')"
        [[ "$running_hash" == "$backup_hash" ]] && rollback_verified=true
      fi
    fi
    if [[ "$rollback_verified" == true ]]; then
      printf 'works-api-live-deploy: rollback=verified rc=%s sha=%s binary_sha256=%s\n' "$rc" "$sha" "$backup_hash" >&2
    else
      printf 'works-api-live-deploy: rollback=FAILED rc=%s sha=%s\n' "$rc" "$sha" >&2
    fi
  fi
  rm -rf "$tmpdir"
  exit "$rc"
}
trap cleanup EXIT

CGO_ENABLED=0 go build -trimpath -o "$candidate" ./cmd/works-api
candidate_revision="$(revision_of "$candidate")"
[[ "$candidate_revision" == "$sha" ]] || fail "candidate_revision_mismatch:$candidate_revision"
[[ "$(modified_of "$candidate")" == "false" ]] || fail "candidate_tree_modified"

candidate_hash="$(sha256sum "$candidate" | awk '{print $1}')"
[[ "$candidate_hash" =~ ^[0-9a-f]{64}$ ]] || fail "candidate_hash_invalid"

# Idempotent retry: if this exact source revision is already installed and
# exact build is running and healthy, prove the live Integrity Fabric surface
# and return without mutation. A matching VCS revision alone is insufficient:
# compiler/build flags can produce a different binary from the reviewed one.
installed_revision="$(revision_of "$TARGET" || true)"
installed_hash="$(sudo sha256sum "$TARGET" | awk '{print $1}')"
pid="$(sudo systemctl show "$SERVICE" -p MainPID --value)"
running_hash=""
if [[ "$pid" =~ ^[1-9][0-9]*$ ]]; then
  running_hash="$(sudo sha256sum "/proc/$pid/exe" | awk '{print $1}')"
fi
if [[ "$installed_revision" == "$sha" && "$installed_hash" == "$candidate_hash" &&
  "$running_hash" == "$candidate_hash" ]]; then
  health_ok || fail "idempotent_health_failed"
  integrity_smoke || fail "idempotent_integrity_fabric_live_smoke_failed"
  pid="$(verify_live_candidate)" || fail "idempotent_live_candidate_mismatch_after_smoke"
  receipt_path="$(write_receipt false "$pid")" || fail "idempotent_deployment_receipt_write_failed"
  printf '{"deployment":"verified","changed":false,"sha":"%s","binary_sha256":"%s","pid":%s,"integrity_smoke":true,"receipt":"%s"}\n' \
    "$sha" "$candidate_hash" "$pid" "$receipt_path"
  exit 0
fi

deployment_id="$(date -u +%Y%m%dT%H%M%SZ).$$"
backup="$TARGET.rollback.$short_sha.$deployment_id"
next="$TARGET.next.$short_sha.$deployment_id"

sudo cp -a "$TARGET" "$backup"
sudo install -m 0755 "$candidate" "$next"
sudo mv -f "$next" "$TARGET"
mutated=true

installed_hash="$(sudo sha256sum "$TARGET" | awk '{print $1}')"
[[ "$installed_hash" == "$candidate_hash" ]] || fail "installed_hash_mismatch"

sudo systemctl restart "$SERVICE"
healthy=false
for _ in $(seq 1 60); do
  if health_ok; then
    healthy=true
    break
  fi
  sleep 0.5
done
[[ "$healthy" == true ]] || fail "health_recovery_failed"

pid="$(sudo systemctl show "$SERVICE" -p MainPID --value)"
[[ "$pid" =~ ^[1-9][0-9]*$ ]] || fail "invalid_main_pid:$pid"
runtime_hash="$(sudo sha256sum "/proc/$pid/exe" | awk '{print $1}')"
[[ "$runtime_hash" == "$candidate_hash" ]] || fail "running_binary_hash_mismatch"

installed_revision="$(revision_of "$TARGET")"
[[ "$installed_revision" == "$sha" ]] || fail "installed_revision_mismatch"

integrity_smoke || fail "integrity_fabric_live_smoke_failed"

pid="$(verify_live_candidate)" || fail "live_candidate_mismatch_after_smoke"
receipt_path="$(write_receipt true "$pid")" || fail "deployment_receipt_write_failed"

mutated=false

printf '{"deployment":"verified","changed":true,"sha":"%s","binary_sha256":"%s","pid":%s,"integrity_smoke":true,"receipt":"%s"}\n' \
  "$sha" "$candidate_hash" "$pid" "$receipt_path"
