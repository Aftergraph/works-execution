#!/usr/bin/env bash
set -euo pipefail
umask 077

TARGET_MAIN_SHA='548c03bd2f77dd55c60c58bbd617f6f1a3fd7a06'
REPO_URL='https://github.com/Aftergraph/works-execution.git'
SERVICE='works-api.service'
BASE_URL='http://127.0.0.1:18191'
SMOKE_WORK_ID='wrk_3995b52a8e30d244dc83f6413bba0df2'
RECEIPT_DIR='/var/lib/works/deployments'

fail() {
  printf 'bootstrap-live-integrity: %s\n' "$*" >&2
  exit 2
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

revision_of() {
  local binary="$1"
  go version -m "$binary" 2>/dev/null |
    sed -n 's/^[[:space:]]*build[[:space:]]*vcs\.revision=//p' |
    head -n 1
}

remote_main="$(git ls-remote "$REPO_URL" refs/heads/main | awk '{print $1}')"
[[ "$remote_main" == "$TARGET_MAIN_SHA" ]] || fail "main_moved:$remote_main"

sudo -n true >/dev/null 2>&1 || fail "noninteractive_root_unavailable"
sudo systemctl is-active --quiet "$SERVICE" || fail "works_api_not_active"

exec_start="$(sudo systemctl show "$SERVICE" -p ExecStart --value)"
TARGET="$(sed -n 's/.*path=\([^ ;}]*\).*/\1/p' <<<"$exec_start" | head -n 1)"
[[ "$TARGET" == /* ]] || fail "exec_start_path_unresolved"
[[ "$(basename -- "$TARGET")" == "works-api" ]] || fail "exec_start_not_works_api:$TARGET"
sudo test -x "$TARGET" || fail "canonical_target_missing:$TARGET"

tmpdir="$(mktemp -d)"
src="$tmpdir/src"
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
    printf 'bootstrap-live-integrity: rollback=true rc=%s target_sha=%s\n' "$rc" "$TARGET_MAIN_SHA" >&2
  fi
  rm -rf "$tmpdir"
  exit "$rc"
}
trap cleanup EXIT

git clone --no-tags --depth 1 --branch main "$REPO_URL" "$src" >/dev/null 2>&1
built_sha="$(git -C "$src" rev-parse HEAD)"
[[ "$built_sha" == "$TARGET_MAIN_SHA" ]] || fail "clone_sha_mismatch:$built_sha"

(
  cd "$src"
  CGO_ENABLED=0 go build -trimpath -o "$candidate" ./cmd/works-api
)

candidate_revision="$(revision_of "$candidate")"
[[ "$candidate_revision" == "$TARGET_MAIN_SHA" ]] || fail "candidate_revision_mismatch:$candidate_revision"
candidate_hash="$(sha256sum "$candidate" | awk '{print $1}')"
[[ "$candidate_hash" =~ ^[0-9a-f]{64}$ ]] || fail "candidate_hash_invalid"

installed_revision="$(revision_of "$TARGET" || true)"
installed_hash="$(sudo sha256sum "$TARGET" | awk '{print $1}')"
if [[ "$installed_revision" == "$TARGET_MAIN_SHA" && "$installed_hash" == "$candidate_hash" ]] && health_ok && integrity_smoke; then
  printf '{"deployment":"verified","changed":false,"source_sha":"%s","binary_sha256":"%s","integrity_smoke":true}\n' \
    "$TARGET_MAIN_SHA" "$candidate_hash"
  exit 0
fi

short_sha="$(printf '%s' "$TARGET_MAIN_SHA" | cut -c1-12)"
backup="$TARGET.rollback.$short_sha"
next="$TARGET.next.$short_sha"

sudo cp -a "$TARGET" "$backup"
sudo install -m 0755 "$candidate" "$next"
sudo mv -f "$next" "$TARGET"
mutated=true

[[ "$(sudo sha256sum "$TARGET" | awk '{print $1}')" == "$candidate_hash" ]] || fail "installed_hash_mismatch"
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
[[ "$(revision_of "$TARGET")" == "$TARGET_MAIN_SHA" ]] || fail "installed_revision_mismatch"

integrity_smoke || fail "integrity_fabric_live_smoke_failed"

deployed_at="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
receipt="$tmpdir/receipt.json"
cat >"$receipt" <<EOF
{
  "schema": "aftergraph.works-api-deployment/1",
  "service": "works-api.service",
  "source_sha": "$TARGET_MAIN_SHA",
  "binary_sha256": "$candidate_hash",
  "main_pid": $pid,
  "deployed_at": "$deployed_at",
  "healthz": true,
  "integrity_fabric_smoke": true,
  "smoke_work_id": "$SMOKE_WORK_ID",
  "bootstrap": true
}
EOF
sudo install -d -m 0750 "$RECEIPT_DIR"
sudo install -m 0640 "$receipt" "$RECEIPT_DIR/works-api-$TARGET_MAIN_SHA.json"

mutated=false
printf '{"deployment":"verified","changed":true,"source_sha":"%s","binary_sha256":"%s","pid":%s,"integrity_smoke":true,"receipt":"%s"}\n' \
  "$TARGET_MAIN_SHA" "$candidate_hash" "$pid" "$RECEIPT_DIR/works-api-$TARGET_MAIN_SHA.json"
