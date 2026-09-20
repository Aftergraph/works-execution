#!/usr/bin/env bash
set -euo pipefail
umask 077

EXPECTED_HOST="${EXPECTED_VDS_HOST:-vmi3517816}"
ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TG_REQUIRED_SHA="8a2c8d66a67d036227c77904844f105b39723f58"
WORKS_BASE="http://127.0.0.1:18191"
TG_BASE="http://127.0.0.1:8800"
TMP="$(mktemp -d /tmp/aftergraph-v21-activate.XXXXXX)"
MUTATED=0
TG_CHANGED=0
WORKS_CHANGED=0
TG_ENV_CHANGED=0
WORKS_ENV_CHANGED=0

cleanup() { rm -rf "$TMP"; }
trap cleanup EXIT

fail() { printf 'v21-activate: %s\n' "$*" >&2; exit 2; }
STAGE_FILE="/tmp/aftergraph-v21-activation-stage"
stage() { printf '%s\n' "$1" >"$STAGE_FILE"; echo "v21-activate: stage=$1"; }

stage preflight
[[ "$(hostname -s)" == "$EXPECTED_HOST" ]] || fail "host mismatch"
if [[ "$(id -u)" -eq 0 ]]; then
  SUDO=""
else
  sudo -n true >/dev/null 2>&1 || fail "passwordless sudo unavailable"
  SUDO="sudo -n"
fi

for c in git go curl openssl sha256sum systemctl awk sed install stat; do
  command -v "$c" >/dev/null 2>&1 || fail "missing command: $c"
done

# Discover live service locations rather than assuming old deployment paths.
TG_REPO="$($SUDO systemctl show tg-gateway.service -p WorkingDirectory --value | tr -d '\r')"
[[ -n "$TG_REPO" && "$TG_REPO" == /* ]] || fail "cannot discover TG WorkingDirectory"
$SUDO test -d "$TG_REPO/.git" || fail "TG WorkingDirectory is not a git checkout"

TG_ENV="$($SUDO systemctl show tg-gateway.service -p EnvironmentFiles --value | awk '{print $1}' | head -n1)"
if [[ -z "$TG_ENV" || "$TG_ENV" != /* ]]; then TG_ENV="$TG_REPO/data/gateway.env"; fi

WORKS_ENV="/etc/works/works.env"
$SUDO test -f "$WORKS_ENV" || fail "WORKS env missing: $WORKS_ENV"

WORKS_EXEC_RAW="$($SUDO systemctl show works-api.service -p ExecStart --value)"
WORKS_BIN="$(printf '%s\n' "$WORKS_EXEC_RAW" | sed -n 's/.*path=\([^ ;}]*\).*/\1/p' | head -n1)"
if [[ -z "$WORKS_BIN" ]]; then
  WORKS_BIN="$($SUDO systemctl cat works-api.service | awk -F= '/^ExecStart=/{print $2; exit}' | awk '{print $1}' | sed 's/^[+!:@-]*//')"
fi
[[ -n "$WORKS_BIN" && "$WORKS_BIN" == /* ]] || fail "cannot discover WORKS ExecStart binary"
$SUDO test -f "$WORKS_BIN" || fail "WORKS binary missing: $WORKS_BIN"

# Live deployment identity. TG code will run from an immutable release
# worktree selected by a systemd WorkingDirectory drop-in; the mutable operator
# checkout is never reset or cleaned.
TG_PREV="$($SUDO git -C "$TG_REPO" rev-parse HEAD)"
TG_RELEASE_ROOT="/opt/aftergraph/trust-gateway/releases"
TG_RELEASE="$TG_RELEASE_ROOT/$TG_REQUIRED_SHA"
TG_DROPIN_DIR="/etc/systemd/system/tg-gateway.service.d"
TG_DROPIN="$TG_DROPIN_DIR/90-aftergraph-v21-release.conf"

# Backup all mutable production state before first write.
$SUDO cp -a "$WORKS_BIN" "$TMP/works-api.prev"
if $SUDO test -e "$TG_DROPIN"; then
  $SUDO cp -a "$TG_DROPIN" "$TMP/tg-dropin.prev"
  TG_DROPIN_EXISTED=1
else
  TG_DROPIN_EXISTED=0
fi
if $SUDO test -e "$TG_ENV"; then
  $SUDO cp -a "$TG_ENV" "$TMP/tg.env.prev"
  TG_ENV_EXISTED=1
else
  TG_ENV_EXISTED=0
fi
$SUDO cp -a "$WORKS_ENV" "$TMP/works.env.prev"

rollback() {
  rc="$?"
  if [[ "$MUTATED" -eq 1 ]]; then
    echo "v21-activate: failure after mutation; rolling back" >&2
    if [[ "$WORKS_CHANGED" -eq 1 ]]; then
      $SUDO cp -a "$TMP/works-api.prev" "$WORKS_BIN" || true
    fi
    if [[ "$TG_CHANGED" -eq 1 ]]; then
      if [[ "$TG_DROPIN_EXISTED" -eq 1 ]]; then
        $SUDO install -d -m 755 "$TG_DROPIN_DIR" || true
        $SUDO cp -a "$TMP/tg-dropin.prev" "$TG_DROPIN" || true
      else
        $SUDO rm -f "$TG_DROPIN" || true
      fi
      $SUDO systemctl daemon-reload >/dev/null 2>&1 || true
    fi
    if [[ "$TG_ENV_CHANGED" -eq 1 ]]; then
      if [[ "$TG_ENV_EXISTED" -eq 1 ]]; then
        $SUDO cp -a "$TMP/tg.env.prev" "$TG_ENV" || true
      else
        $SUDO rm -f "$TG_ENV" || true
      fi
    fi
    if [[ "$WORKS_ENV_CHANGED" -eq 1 ]]; then
      $SUDO cp -a "$TMP/works.env.prev" "$WORKS_ENV" || true
    fi
    $SUDO systemctl restart works-api.service >/dev/null 2>&1 || true
    $SUDO systemctl restart tg-gateway.service >/dev/null 2>&1 || true
  fi
  exit "$rc"
}
trap rollback ERR

# Build and test the merged WORKS code from this checkout.
stage works-build
cd "$ROOT_DIR"
go test ./...
go build -trimpath -o "$TMP/works-api.new" ./cmd/works-api
$SUDO install -m "$(stat -c '%a' "$WORKS_BIN")" "$TMP/works-api.new" "$WORKS_BIN.new"
$SUDO chown --reference="$WORKS_BIN" "$WORKS_BIN.new"
$SUDO mv -f "$WORKS_BIN.new" "$WORKS_BIN"
WORKS_CHANGED=1
MUTATED=1

# Materialize the verified TG merge as an immutable release, without touching
# the mutable operator checkout. The live data path remains the unit's existing
# EnvironmentFile/ReadWritePaths target.
stage tg-release
if ! $SUDO git -C "$TG_REPO" cat-file -e "$TG_REQUIRED_SHA^{commit}" 2>/dev/null; then
  $SUDO git -C "$TG_REPO" fetch --quiet origin "$TG_REQUIRED_SHA"
fi
$SUDO install -d -m 755 "$TG_RELEASE_ROOT"
if ! $SUDO test -d "$TG_RELEASE"; then
  $SUDO git -C "$TG_REPO" worktree add --detach "$TG_RELEASE" "$TG_REQUIRED_SHA" >/dev/null
fi
TG_DEPLOYED="$($SUDO git -C "$TG_RELEASE" rev-parse HEAD)"
[[ "$TG_DEPLOYED" == "$TG_REQUIRED_SHA" ]] || fail "TG release SHA mismatch"

# Preserve the canonical writable data boundary for relative data/ consumers.
if ! $SUDO test -e "$TG_RELEASE/data"; then
  $SUDO ln -s "$(dirname "$TG_ENV")" "$TG_RELEASE/data"
fi

# Host-level smoke of the load-bearing TG seams before restart.
$SUDO node --test \
  "$TG_RELEASE/tests/platform-execution-context.test.js" \
  "$TG_RELEASE/tests/platform-approval-v21.test.js" \
  "$TG_RELEASE/tests/works-context-client.test.js"

# Switch only WorkingDirectory through a systemd drop-in. Existing
# EnvironmentFile and ReadWritePaths stay untouched.
$SUDO install -d -m 755 "$TG_DROPIN_DIR"
printf '[Service]\nWorkingDirectory=%s\n' "$TG_RELEASE" >"$TMP/tg-dropin.new"
$SUDO install -m 644 "$TMP/tg-dropin.new" "$TG_DROPIN"
$SUDO systemctl daemon-reload
TG_CHANGED=1

read_env() {
  local file="$1" key="$2"
  $SUDO awk -v k="$key" 'index($0,k"=")==1 { print substr($0,length(k)+2) }' "$file" 2>/dev/null | tail -n1
}
choose_shared() {
  local key="$1" left="$2" right="$3"
  if [[ -n "$left" && ${#left} -lt 32 ]]; then fail "$key in TG env is shorter than 32 bytes"; fi
  if [[ -n "$right" && ${#right} -lt 32 ]]; then fail "$key in WORKS env is shorter than 32 bytes"; fi
  if [[ -n "$left" && -n "$right" && "$left" != "$right" ]]; then fail "$key differs across services; refusing implicit rotation"; fi
  if [[ -n "$left" ]]; then printf '%s' "$left"; return; fi
  if [[ -n "$right" ]]; then printf '%s' "$right"; return; fi
  openssl rand -hex 32
}
upsert_env() {
  local file="$1" key="$2" value="$3" tmp uid gid
  if ! $SUDO test -e "$file"; then
    $SUDO install -d -m 700 "$(dirname "$file")"
    $SUDO install -m 600 /dev/null "$file"
  fi
  uid="$($SUDO stat -c '%u' "$file")"
  gid="$($SUDO stat -c '%g' "$file")"
  tmp="$(mktemp "$TMP/env.XXXXXX")"
  $SUDO awk -v k="$key" 'index($0,k"=")!=1 { print }' "$file" >"$tmp"
  printf '%s=%s\n' "$key" "$value" >>"$tmp"
  $SUDO chown "$uid:$gid" "$tmp"
  $SUDO chmod 600 "$tmp"
  $SUDO cp -f "$tmp" "$file"
}

stage credentials
tg_token="$(read_env "$TG_ENV" WORKS_API_TOKEN)"
works_token="$(read_env "$WORKS_ENV" WORKS_API_TOKEN)"
tg_bridge="$(read_env "$TG_ENV" WORKS_PLATFORM_BRIDGE_SECRET)"
works_bridge="$(read_env "$WORKS_ENV" WORKS_PLATFORM_BRIDGE_SECRET)"

platform_token="$(choose_shared WORKS_API_TOKEN "$tg_token" "$works_token")"
bridge_secret="$(choose_shared WORKS_PLATFORM_BRIDGE_SECRET "$tg_bridge" "$works_bridge")"

upsert_env "$WORKS_ENV" WORKS_API_TOKEN "$platform_token"
upsert_env "$WORKS_ENV" WORKS_PLATFORM_BRIDGE_SECRET "$bridge_secret"
WORKS_ENV_CHANGED=1
upsert_env "$TG_ENV" WORKS_API_URL "$WORKS_BASE"
upsert_env "$TG_ENV" WORKS_API_TOKEN "$platform_token"
upsert_env "$TG_ENV" WORKS_PLATFORM_BRIDGE_SECRET "$bridge_secret"
TG_ENV_CHANGED=1

stage works-restart
$SUDO systemctl restart works-api.service
for _ in $(seq 1 30); do
  curl -fsS --max-time 2 "$WORKS_BASE/healthz" >/dev/null 2>&1 && break
  sleep 1
done
curl -fsS --max-time 2 "$WORKS_BASE/healthz" >/dev/null

# Authenticated negative smoke: credentials must pass both auth boundaries and
# reach the context lookup. Expected result is 404 for a syntactically valid,
# deliberately unknown context; 401/403/503 is a deployment failure.
SMOKE_BODY='{"execution_context_id":"ctx_00000000000000000000000000000000","execution_pdr_id":"pdr_00000000000000000000000000000000"}'
HTTP_CODE="$(curl -sS -o "$TMP/works-smoke.json" -w '%{http_code}'   -H "Authorization: Bearer $platform_token"   -H "X-Works-Platform-Bridge: $bridge_secret"   -H 'Content-Type: application/json'   --data "$SMOKE_BODY"   "$WORKS_BASE/v1/works/wrk_00000000000000000000000000000000/evidence")"
[[ "$HTTP_CODE" == "404" ]] || fail "WORKS authenticated seam smoke returned HTTP $HTTP_CODE"

stage tg-restart
$SUDO systemctl restart tg-gateway.service
for _ in $(seq 1 30); do
  curl -fsS --max-time 2 "$TG_BASE/healthz" >/dev/null 2>&1 && break
  sleep 1
done
curl -fsS --max-time 2 "$TG_BASE/healthz" >/dev/null

TG_PID="$($SUDO systemctl show tg-gateway.service -p MainPID --value)"
[[ "$TG_PID" =~ ^[1-9][0-9]*$ ]] || fail "TG MainPID unavailable"
$SUDO tr '\0' '\n' < "/proc/$TG_PID/environ" | grep -q '^WORKS_API_URL='
$SUDO tr '\0' '\n' < "/proc/$TG_PID/environ" | grep -q '^WORKS_API_TOKEN='
$SUDO tr '\0' '\n' < "/proc/$TG_PID/environ" | grep -q '^WORKS_PLATFORM_BRIDGE_SECRET='

WORKS_PID="$($SUDO systemctl show works-api.service -p MainPID --value)"
[[ "$WORKS_PID" =~ ^[1-9][0-9]*$ ]] || fail "WORKS MainPID unavailable"
$SUDO tr '\0' '\n' < "/proc/$WORKS_PID/environ" | grep -q '^WORKS_API_TOKEN='
$SUDO tr '\0' '\n' < "/proc/$WORKS_PID/environ" | grep -q '^WORKS_PLATFORM_BRIDGE_SECRET='

stage final-evidence
EVIDENCE_DIR="$(dirname "$TG_ENV")/ops"
$SUDO install -d -m 700 "$EVIDENCE_DIR"
token_fp="$(printf '%s' "$platform_token" | sha256sum | awk '{print substr($1,1,16)}')"
bridge_fp="$(printf '%s' "$bridge_secret" | sha256sum | awk '{print substr($1,1,16)}')"
cat >"$TMP/evidence.json" <<EOF
{
  "schema": "aftergraph.v21.production-activation/1.0",
  "host": "$EXPECTED_HOST",
  "activated_at": "$(date -u +%Y-%m-%dT%H:%M:%SZ)",
  "works_source_head": "$(git -C "$ROOT_DIR" rev-parse HEAD)",
  "trust_gateway_head": "$TG_DEPLOYED",
  "works_api_url": "$WORKS_BASE",
  "platform_token_sha256_prefix": "$token_fp",
  "bridge_secret_sha256_prefix": "$bridge_fp",
  "works_health": "pass",
  "works_dual_auth_smoke": "pass",
  "trust_gateway_health": "pass",
  "runtime_env_presence": "pass",
  "secrets_exposed": false
}
EOF
$SUDO install -m 600 "$TMP/evidence.json" "$EVIDENCE_DIR/v21-production-activation.json"

stage complete
MUTATED=0
trap - ERR

echo "v21-activate: PASS host=$EXPECTED_HOST"
echo "v21-activate: WORKS and TG healthy; dual credentials active"
echo "v21-activate: secret material was not printed"
