#!/usr/bin/env bash
set -euo pipefail

RUNNER_NAME="${1:-vps-ci-01}"
WAIT_SECONDS="${RUNNER_RECOVERY_WAIT_SECONDS:-20}"

mapfile -t services < <(systemctl list-unit-files --type=service --no-legend 'actions.runner.*' 2>/dev/null |
  awk '{print $1}' | grep -F ".$RUNNER_NAME.service" || true)

if [[ ${#services[@]} -ne 1 ]]; then
  printf '{"status":"INFRA_UNAVAILABLE","reason":"runner_service_resolution","runner":"%s","matches":%d}\n' "$RUNNER_NAME" "${#services[@]}"
  exit 2
fi

service="${services[0]}"
before="$(systemctl is-active "$service" 2>/dev/null || true)"
changed=false

if [[ "$before" != "active" ]]; then
  sudo -n systemctl restart "$service"
  changed=true
fi

deadline=$((SECONDS + WAIT_SECONDS))
while (( SECONDS < deadline )); do
  if [[ "$(systemctl is-active "$service" 2>/dev/null || true)" == "active" ]]; then
    if journalctl -u "$service" --since '-2 minutes' --no-pager 2>/dev/null |
      grep -Eq 'Listening for Jobs|Connected to GitHub'; then
      printf '{"status":"RECOVERED","runner":"%s","service":"%s","changed":%s,"listener":true}\n' "$RUNNER_NAME" "$service" "$changed"
      exit 0
    fi
  fi
  sleep 1
done

printf '{"status":"INFRA_UNAVAILABLE","reason":"listener_not_proven","runner":"%s","service":"%s","changed":%s}\n' "$RUNNER_NAME" "$service" "$changed"
exit 3
