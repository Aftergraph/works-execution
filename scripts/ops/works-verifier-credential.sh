#!/usr/bin/env bash
set -euo pipefail
umask 077

ACTION="${1:-status}"
case "$ACTION" in status|enable) ;; *) echo "usage: $0 [status|enable]" >&2; exit 2 ;; esac

ENV_FILE=/etc/works/works.env
SERVICE=works-api.service
BASE_URL=http://127.0.0.1:18191

fail(){ printf 'works-verifier-credential: %s\n' "$*" >&2; exit 2; }

status_probe() {
  systemctl is-active --quiet "$SERVICE" || fail "works_api_inactive"
  curl -fsS --max-time 2 "$BASE_URL/healthz" >/dev/null || fail "works_health_failed"
  local code
  code="$(curl -sS -o /dev/null -w '%{http_code}' --max-time 2 -X POST     -H 'Content-Type: application/json'     -H 'X-WORKS-Verifier-Token: deliberately-wrong-status-probe-token-0000000000'     --data '{"result":"passed","verifier_id":"sentinel:credential-status","evidence_ref":"dvr_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","verified_at":"2026-09-20T08:00:00Z"}'     "$BASE_URL/v1/works/wrk_credential_status_not_real/verification" || true)"
  case "$code" in
    401) printf '{"service":"active","health":true,"verification_ingest":"configured"}\n' ;;
    503) printf '{"service":"active","health":true,"verification_ingest":"unconfigured"}\n' ;;
    *) fail "unexpected_verification_probe_status:$code" ;;
  esac
}

[[ "$ACTION" == status ]] && { status_probe; exit 0; }

[[ "${EUID:-$(id -u)}" -eq 0 ]] || fail "root_required"
[[ -f "$ENV_FILE" && ! -L "$ENV_FILE" ]] || fail "canonical_env_file_missing_or_symlinked"
[[ "$(readlink -f -- "$ENV_FILE")" == "$ENV_FILE" ]] || fail "canonical_env_file_redirected"
[[ "$(stat -c '%u:%g' "$ENV_FILE")" == "0:0" ]] || fail "env_file_not_root_owned"
mode="$(stat -c '%a' "$ENV_FILE")"
case "$mode" in 400|600|640) ;; *) fail "env_file_permissions_too_open:$mode" ;; esac

before="$(status_probe)"
case "$before" in
  *'"configured"'*) exit 0 ;;
  *'"unconfigured"'*) ;;
  *) fail "unexpected_status_probe_result" ;;
esac

enroll_code="$(curl -sS -o /dev/null -w '%{http_code}' --max-time 2 -X POST   -H 'Content-Type: application/json'   --data '{"worker_id":"wrkr_credential_precheck","challenge":"deliberately-wrong-enrollment-challenge"}'   "$BASE_URL/v1/workers/enroll" || true)"
[[ "$enroll_code" == 401 ]] || fail "worker_enrollment_not_configured:$enroll_code"

backup="$(mktemp /run/works.env.before-verifier.XXXXXX)"
cp -a -- "$ENV_FILE" "$backup"
rollback(){
  local rc=$?
  cp -a -- "$backup" "$ENV_FILE" || true
  systemctl restart "$SERVICE" || true
  rm -f -- "$backup"
  exit "$rc"
}
trap rollback ERR INT TERM HUP

set -a
# shellcheck disable=SC1090
source "$ENV_FILE"
set +a
current="${WORKS_VERIFIER_TOKEN:-}"

if [[ ${#current} -lt 32 ]]; then
  token="$(openssl rand -hex 32)"
  tmp="$(mktemp /etc/works/.works.env.verifier.XXXXXX)"
  awk '!/^WORKS_VERIFIER_TOKEN=/' "$ENV_FILE" >"$tmp"
  printf 'WORKS_VERIFIER_TOKEN=%s\n' "$token" >>"$tmp"
  chown --reference="$ENV_FILE" "$tmp"
  chmod --reference="$ENV_FILE" "$tmp"
  mv -f -- "$tmp" "$ENV_FILE"
  token=""
fi
current=""

systemctl restart "$SERVICE"
healthy=false
for _ in $(seq 1 60); do
  if curl -fsS --max-time 1 "$BASE_URL/healthz" >/dev/null 2>&1; then healthy=true; break; fi
  sleep 0.5
done
[[ "$healthy" == true ]] || fail "works_health_recovery_failed"

verify_code="$(curl -sS -o /dev/null -w '%{http_code}' --max-time 2 -X POST   -H 'Content-Type: application/json'   -H 'X-WORKS-Verifier-Token: deliberately-wrong-enable-probe-token-000000000'   --data '{"result":"passed","verifier_id":"sentinel:credential-enable","evidence_ref":"dvr_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","verified_at":"2026-09-20T08:00:00Z"}'   "$BASE_URL/v1/works/wrk_credential_enable_not_real/verification" || true)"
[[ "$verify_code" == 401 ]] || fail "verification_ingest_not_enabled:$verify_code"

enroll_code="$(curl -sS -o /dev/null -w '%{http_code}' --max-time 2 -X POST   -H 'Content-Type: application/json'   --data '{"worker_id":"wrkr_credential_postcheck","challenge":"deliberately-wrong-enrollment-challenge"}'   "$BASE_URL/v1/workers/enroll" || true)"
[[ "$enroll_code" == 401 ]] || fail "worker_enrollment_regressed:$enroll_code"

rm -f -- "$backup"
trap - ERR INT TERM HUP
printf '{"service":"active","health":true,"verification_ingest":"configured","worker_enrollment":"preserved","credential_value_exposed":false}\n'
