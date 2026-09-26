#!/usr/bin/env bash
set -euo pipefail
umask 077

MARKER='[deploy-works-api]'
SERVICE='works-api.service'
TARGET=''
BASE_URL='http://127.0.0.1:18191'
SMOKE_WORK_ID='wrk_3995b52a8e30d244dc83f6413bba0df2'
RECEIPT_DIR='/var/lib/works/deployments'

fail() {
  printf 'works-api-live-deploy: %s\n' "$*" >&2
  exit 2
}

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

integrity_smoke() {
  local out
  out="$(curl -fsS --max-time 5 "$BASE_URL/v1/works/$SMOKE_WORK_ID/evidence")" || return 1
  grep -Fq '"canonicalization":"aftergraph-json-canonical/1"' <<<"$out" || return 1
  grep -Fq '"algorithm":"sha256"' <<<"$out" || return 1
  grep -Fq '"algorithm":"blake3"' <<<"$out" || return 1
  grep -Fq '"algorithm":"hmac-sha256-v1"' <<<"$out" || return 1
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
  trap - EXIT
  set +e
  if [[ "$rc" -ne 0 && "$mutated" == true && -n "$backup" ]] && sudo test -f "$backup"; then
    sudo cp -a "$backup" "$TARGET"
    sudo systemctl restart "$SERVICE"
    for _ in $(seq 1 40); do
      health_ok && break
      sleep 0.5
    done
    printf 'works-api-live-deploy: rollback=true rc=%s sha=%s\n' "$rc" "$sha" >&2
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
# healthy, prove the live Integrity Fabric surface and return without mutation.
installed_revision="$(revision_of "$TARGET" || true)"
if [[ "$installed_revision" == "$sha" ]] && health_ok && integrity_smoke; then
  printf '{"deployment":"verified","changed":false,"sha":"%s","binary_sha256":"%s","integrity_smoke":true}\n' \
    "$sha" "$candidate_hash"
  exit 0
fi

backup="$TARGET.rollback.$short_sha"
next="$TARGET.next.$short_sha"

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

deployed_at="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
receipt_local="$tmpdir/receipt.json"
cat >"$receipt_local" <<EOF
{
  "schema": "aftergraph.works-api-deployment/1",
  "service": "works-api.service",
  "source_sha": "$sha",
  "binary_sha256": "$candidate_hash",
  "main_pid": $pid,
  "deployed_at": "$deployed_at",
  "healthz": true,
  "integrity_fabric_smoke": true,
  "smoke_work_id": "$SMOKE_WORK_ID"
}
EOF
sudo install -d -m 0750 "$RECEIPT_DIR"
sudo install -m 0640 "$receipt_local" "$RECEIPT_DIR/works-api-$sha.json"

mutated=false

printf '{"deployment":"verified","changed":true,"sha":"%s","binary_sha256":"%s","pid":%s,"integrity_smoke":true,"receipt":"%s"}\n' \
  "$sha" "$candidate_hash" "$pid" "$RECEIPT_DIR/works-api-$sha.json"
