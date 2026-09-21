#!/usr/bin/env bash
set -euo pipefail
umask 077

ACTION="${1:-status}"
WORKFLOW_SHA="${2:-}"
ENV_FILE=/etc/works/works.env
SERVICE=works-api.service
BASE_URL=http://127.0.0.1:18191

AUDIENCE=aftergraph-works
REPOSITORY=Aftergraph/intelligence-systems-research
REPOSITORY_ID=1356862124
REF=refs/heads/bootstrap/lenovo-works-native
WORKFLOW_REF=Aftergraph/intelligence-systems-research/.github/workflows/bootstrap-lenovo-works-native.yml@refs/heads/bootstrap/lenovo-works-native

fail(){ printf 'works-github-oidc-enrollment: %s\n' "$*" >&2; exit 2; }

probe() {
  systemctl is-active --quiet "$SERVICE" || fail "works_api_inactive"
  curl -fsS --max-time 2 "$BASE_URL/healthz" >/dev/null || fail "works_health_failed"
  code="$(curl -sS -o /dev/null -w '%{http_code}' --max-time 5 -X POST     -H 'Content-Type: application/json'     --data '{"worker_id":"wrkr_oidc_status_probe","oidc_token":"deliberately-invalid-jwt"}'     "$BASE_URL/v1/workers/enroll/github-actions" || true)"
  case "$code" in
    401) printf '{"service":"active","health":true,"github_oidc_enrollment":"configured"}\n' ;;
    503) printf '{"service":"active","health":true,"github_oidc_enrollment":"disabled"}\n' ;;
    404) printf '{"service":"active","health":true,"github_oidc_enrollment":"binary_not_deployed"}\n' ;;
    *) fail "unexpected_oidc_probe_status:$code" ;;
  esac
}

case "$ACTION" in
  status) probe; exit 0 ;;
  enable) ;;
  *) fail "usage: $0 [status|enable] [exact_workflow_sha]" ;;
esac

[[ "${EUID:-$(id -u)}" -eq 0 ]] || fail "root_required"
[[ "$WORKFLOW_SHA" =~ ^[0-9a-f]{40}$ ]] || fail "exact_workflow_sha_required"
[[ -f "$ENV_FILE" && ! -L "$ENV_FILE" ]] || fail "canonical_env_file_missing_or_symlinked"
[[ "$(readlink -f -- "$ENV_FILE")" == "$ENV_FILE" ]] || fail "canonical_env_file_redirected"
[[ "$(stat -c '%u:%g' "$ENV_FILE")" == "0:0" ]] || fail "env_file_not_root_owned"
mode="$(stat -c '%a' "$ENV_FILE")"
case "$mode" in 400|600|640) ;; *) fail "env_file_permissions_too_open:$mode" ;; esac

backup="$(mktemp /run/works.env.before-github-oidc.XXXXXX)"
cp -a -- "$ENV_FILE" "$backup"
rollback(){
  rc=$?
  cp -a -- "$backup" "$ENV_FILE" || true
  systemctl restart "$SERVICE" || true
  rm -f -- "$backup"
  exit "$rc"
}
trap rollback ERR INT TERM HUP

tmp="$(mktemp /etc/works/.works.env.github-oidc.XXXXXX)"
awk '!/^WORKS_GITHUB_OIDC_(AUDIENCE|REPOSITORY|REPOSITORY_ID|REF|WORKFLOW_REF|WORKFLOW_SHA)=/' "$ENV_FILE" >"$tmp"
cat >>"$tmp" <<EOF
WORKS_GITHUB_OIDC_AUDIENCE=$AUDIENCE
WORKS_GITHUB_OIDC_REPOSITORY=$REPOSITORY
WORKS_GITHUB_OIDC_REPOSITORY_ID=$REPOSITORY_ID
WORKS_GITHUB_OIDC_REF=$REF
WORKS_GITHUB_OIDC_WORKFLOW_REF=$WORKFLOW_REF
WORKS_GITHUB_OIDC_WORKFLOW_SHA=$WORKFLOW_SHA
EOF
chown --reference="$ENV_FILE" "$tmp"
chmod --reference="$ENV_FILE" "$tmp"
mv -f -- "$tmp" "$ENV_FILE"

systemctl restart "$SERVICE"
healthy=false
for _ in $(seq 1 60); do
  if curl -fsS --max-time 1 "$BASE_URL/healthz" >/dev/null 2>&1; then healthy=true; break; fi
  sleep 0.5
done
[[ "$healthy" == true ]] || fail "works_health_recovery_failed"

out="$(probe)"
case "$out" in *'"configured"'*) ;; *) fail "github_oidc_enrollment_not_enabled:$out" ;; esac

rm -f -- "$backup"
trap - ERR INT TERM HUP
printf '{"service":"active","health":true,"github_oidc_enrollment":"configured","workflow_sha":"%s","secret_value_required":false}\n' "$WORKFLOW_SHA"
