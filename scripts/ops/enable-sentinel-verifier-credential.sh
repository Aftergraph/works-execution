#!/usr/bin/env bash
set -euo pipefail
umask 077

ENV_FILE="${WORKS_ENV_FILE:-/etc/works/works.env}"
SERVICE="${WORKS_SERVICE:-works-api.service}"
WORKS_URL="${WORKS_URL:-http://127.0.0.1:18191}"
TOKEN_KEY="WORKS_VERIFIER_TOKEN"

fail() {
  printf 'works-verifier-credential: FAIL: %s\n' "$*" >&2
  exit 1
}

[[ "${EUID:-$(id -u)}" -eq 0 ]] || fail "run as root"
command -v openssl >/dev/null 2>&1 || fail "openssl required"
command -v curl >/dev/null 2>&1 || fail "curl required"
command -v systemctl >/dev/null 2>&1 || fail "systemctl required"
command -v stat >/dev/null 2>&1 || fail "stat required"
command -v mktemp >/dev/null 2>&1 || fail "mktemp required"

[[ -f "$ENV_FILE" && ! -L "$ENV_FILE" ]] || fail "canonical env file missing or symlinked: $ENV_FILE"
[[ "$(stat -c '%u:%g' "$ENV_FILE")" == "0:0" ]] || fail "env file must be root-owned"
MODE="$(stat -c '%a' "$ENV_FILE")"
case "$MODE" in 400|600|640) ;; *) fail "env file mode too open: $MODE" ;; esac

systemctl is-active --quiet "$SERVICE" || fail "$SERVICE not active"
curl -fsS --max-time 2 "$WORKS_URL/healthz" >/dev/null || fail "WORKS health check failed"

probe_verifier() {
  curl -sS -o /dev/null -w '%{http_code}' --max-time 2 -X POST     -H 'Content-Type: application/json'     -H 'X-WORKS-Verifier-Token: definitely-wrong-verifier-token-probe-000000000000'     --data '{"result":"passed","verifier_id":"sentinel:credential-probe","evidence_ref":"dvr_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","verified_at":"2026-09-20T00:00:00Z"}'     "$WORKS_URL/v1/works/wrk_credential_probe_not_real/verification" || true
}

probe_enrollment() {
  curl -sS -o /dev/null -w '%{http_code}' --max-time 2 -X POST     -H 'Content-Type: application/json'     --data '{"worker_id":"wrkr_credential_probe","challenge":"definitely-wrong-enrollment-probe"}'     "$WORKS_URL/v1/workers/enroll" || true
}

BEFORE_VERIFIER="$(probe_verifier)"
BEFORE_ENROLLMENT="$(probe_enrollment)"
[[ "$BEFORE_VERIFIER" == "503" || "$BEFORE_VERIFIER" == "401" ]] || fail "unexpected verifier preflight status: $BEFORE_VERIFIER"
[[ "$BEFORE_ENROLLMENT" == "401" ]] || fail "worker enrollment is not configured as expected: $BEFORE_ENROLLMENT"

BACKUP="$(mktemp /run/works.env.before-verifier.XXXXXX)"
cp -a -- "$ENV_FILE" "$BACKUP"

rollback() {
  local rc=$?
  cp -a -- "$BACKUP" "$ENV_FILE" || true
  systemctl restart "$SERVICE" || true
  rm -f -- "$BACKUP"
  exit "$rc"
}
trap rollback ERR INT TERM HUP

CURRENT="$(
  env -i ENV_FILE="$ENV_FILE" bash -c '
    set -a
    # shellcheck disable=SC1090
    source "$ENV_FILE"
    set +a
    printf "%s" "${WORKS_VERIFIER_TOKEN:-}"
  '
)"

if [[ ${#CURRENT} -lt 32 ]]; then
  TOKEN="$(openssl rand -hex 32)"
  TMP="$(mktemp "${ENV_FILE}.verifier.XXXXXX")"
  awk -v key="$TOKEN_KEY" '$0 !~ "^" key "=" { print }' "$ENV_FILE" >"$TMP"
  printf '%s=%s\n' "$TOKEN_KEY" "$TOKEN" >>"$TMP"
  chown --reference="$ENV_FILE" "$TMP"
  chmod --reference="$ENV_FILE" "$TMP"
  mv -f -- "$TMP" "$ENV_FILE"
  TOKEN=""
fi
CURRENT=""

systemctl restart "$SERVICE"

HEALTHY=false
for _ in $(seq 1 60); do
  if curl -fsS --max-time 1 "$WORKS_URL/healthz" >/dev/null 2>&1; then
    HEALTHY=true
    break
  fi
  sleep 0.5
done
[[ "$HEALTHY" == true ]] || fail "WORKS did not recover after credential activation"

AFTER_VERIFIER="$(probe_verifier)"
AFTER_ENROLLMENT="$(probe_enrollment)"
[[ "$AFTER_VERIFIER" == "401" ]] || fail "verification ingest did not activate fail-closed auth: $AFTER_VERIFIER"
[[ "$AFTER_ENROLLMENT" == "401" ]] || fail "worker enrollment regressed after restart: $AFTER_ENROLLMENT"

rm -f -- "$BACKUP"
trap - ERR INT TERM HUP

printf '{"works_health":true,"verification_ingest_configured":true,"wrong_verifier_token_status":401,"worker_enrollment_preserved":true,"credential_value_exposed":false}\n'
