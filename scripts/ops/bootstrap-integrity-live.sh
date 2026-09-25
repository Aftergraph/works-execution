#!/usr/bin/env bash
set -euo pipefail
umask 077

TARGET_SHA='548c03bd2f77dd55c60c58bbd617f6f1a3fd7a06'
REMOTE='https://github.com/Aftergraph/works-execution.git'
SHORT="$(printf '%s' "$TARGET_SHA" | cut -c1-12)"
UNIT="aftergraph-works-bootstrap-$SHORT"
STAGE="/run/$UNIT"
PAYLOAD="$STAGE/promote.sh"

fail() {
  printf 'works-bootstrap: %s\n' "$*" >&2
  exit 2
}

revision_of() {
  go version -m "$1" 2>/dev/null |
    sed -n 's/^[[:space:]]*build[[:space:]]*vcs\.revision=//p' |
    head -n 1
}

[ "$(id -u)" -eq 0 ] || fail "root_worker_required"
remote_main="$(git ls-remote "$REMOTE" refs/heads/main | awk 'NR==1{print $1}')"
[ "$remote_main" = "$TARGET_SHA" ] || fail "main_moved:$remote_main"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
git -C "$tmp" init -q
git -C "$tmp" remote add origin "$REMOTE"
git -C "$tmp" fetch -q --no-tags --depth=1 origin main
fetched="$(git -C "$tmp" rev-parse FETCH_HEAD)"
[ "$fetched" = "$TARGET_SHA" ] || fail "fetch_mismatch:$fetched"
git -C "$tmp" checkout -q --detach FETCH_HEAD

(
  cd "$tmp"
  go vet ./...
  go test ./... -count=1
  CGO_ENABLED=0 go build -trimpath -o "$tmp/works-api" ./cmd/works-api
  CGO_ENABLED=0 go build -trimpath -o "$tmp/works-worker" ./cmd/works-worker
  CGO_ENABLED=0 go build -trimpath -o "$tmp/works" ./cmd/works
)

for b in works-api works-worker works; do
  rev="$(revision_of "$tmp/$b")"
  [ "$rev" = "$TARGET_SHA" ] || fail "$b revision mismatch:$rev"
done

rm -rf "$STAGE"
install -d -m 0700 "$STAGE"
install -m 0755 "$tmp/works-api" "$STAGE/works-api"
install -m 0755 "$tmp/works-worker" "$STAGE/works-worker"
install -m 0755 "$tmp/works" "$STAGE/works"

cat >"$PAYLOAD" <<'PROMOTE'
#!/usr/bin/env bash
set -euo pipefail
umask 077

TARGET_SHA='548c03bd2f77dd55c60c58bbd617f6f1a3fd7a06'
SHORT="$(printf '%s' "$TARGET_SHA" | cut -c1-12)"
STAGE="/run/aftergraph-works-bootstrap-$SHORT"
RECEIPT_DIR='/var/lib/works/deployments'
BACKUP_DIR="$RECEIPT_DIR/backups/$TARGET_SHA"
SMOKE_WORK_ID='wrk_3995b52a8e30d244dc83f6413bba0df2'
API='http://127.0.0.1:18191'
REMOTE='https://github.com/Aftergraph/works-execution.git'
mutated=false

revision_of() {
  go version -m "$1" 2>/dev/null |
    sed -n 's/^[[:space:]]*build[[:space:]]*vcs\.revision=//p' |
    head -n 1
}

service_path() {
  raw="$(systemctl show "$1" -p ExecStart --value)"
  path="$(sed -n 's/.*path=\([^ ;}]*\).*/\1/p' <<<"$raw" | head -n1)"
  [ -n "$path" ] && [ "$(printf '%s' "$path" | cut -c1)" = "/" ]
  printf '%s\n' "$path"
}

atomic_install() {
  src="$1"
  dst="$2"
  next="$dst.next.$SHORT"
  install -m 0755 "$src" "$next"
  mv -f "$next" "$dst"
}

write_receipt() {
  state="$1"
  reason="$2"
  install -d -m 0750 "$RECEIPT_DIR"
  out="$RECEIPT_DIR/works-bootstrap-$TARGET_SHA.json"
  tmpout="$out.tmp"
  printf '{"schema":"aftergraph.works-bootstrap/1","target_sha":"%s","state":"%s","reason":"%s","observed_at":"%s"}\n' \
    "$TARGET_SHA" "$state" "$reason" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" >"$tmpout"
  mv -f "$tmpout" "$out"
  chmod 0640 "$out"
}

rollback() {
  rc=$?
  trap - ERR
  set +e
  if [ "$mutated" = true ]; then
    [ -f "$BACKUP_DIR/works-api" ] && atomic_install "$BACKUP_DIR/works-api" "$API_TARGET"
    [ -f "$BACKUP_DIR/works-worker" ] && atomic_install "$BACKUP_DIR/works-worker" "$WORKER_TARGET"
    [ -f "$BACKUP_DIR/works" ] && atomic_install "$BACKUP_DIR/works" "$CLI_TARGET"
    systemctl restart works-api.service works-worker.service works-worker-2.service works-worker-3.service
  fi
  write_receipt FAILED_ROLLED_BACK "exit_$rc"
  rm -rf "$STAGE"
  exit "$rc"
}
trap rollback ERR

sleep 8
remote_main="$(git ls-remote "$REMOTE" refs/heads/main | awk 'NR==1{print $1}')"
[ "$remote_main" = "$TARGET_SHA" ]

API_TARGET="$(service_path works-api.service)"
WORKER_TARGET="$(service_path works-worker.service)"
[ "$(basename "$API_TARGET")" = works-api ]
[ "$(basename "$WORKER_TARGET")" = works-worker ]
CLI_TARGET="$(dirname "$WORKER_TARGET")/works"

[ -x "$API_TARGET" ]
[ -x "$WORKER_TARGET" ]
[ -x "$CLI_TARGET" ]

for b in works-api works-worker works; do
  [ "$(revision_of "$STAGE/$b")" = "$TARGET_SHA" ]
done

install -d -m 0700 "$BACKUP_DIR"
cp -a "$API_TARGET" "$BACKUP_DIR/works-api"
cp -a "$WORKER_TARGET" "$BACKUP_DIR/works-worker"
cp -a "$CLI_TARGET" "$BACKUP_DIR/works"

atomic_install "$STAGE/works-api" "$API_TARGET"
atomic_install "$STAGE/works-worker" "$WORKER_TARGET"
atomic_install "$STAGE/works" "$CLI_TARGET"
mutated=true

api_hash="$(sha256sum "$STAGE/works-api" | awk '{print $1}')"
worker_hash="$(sha256sum "$STAGE/works-worker" | awk '{print $1}')"
cli_hash="$(sha256sum "$STAGE/works" | awk '{print $1}')"
[ "$(sha256sum "$API_TARGET" | awk '{print $1}')" = "$api_hash" ]
[ "$(sha256sum "$WORKER_TARGET" | awk '{print $1}')" = "$worker_hash" ]
[ "$(sha256sum "$CLI_TARGET" | awk '{print $1}')" = "$cli_hash" ]

systemctl restart works-api.service
ok=false
for _ in $(seq 1 80); do
  if curl -fsS --max-time 2 "$API/healthz" >/dev/null 2>&1; then
    ok=true
    break
  fi
  sleep 0.5
done
[ "$ok" = true ]

systemctl restart works-worker.service works-worker-2.service works-worker-3.service
sleep 2

[ "$(revision_of "$API_TARGET")" = "$TARGET_SHA" ]
[ "$(revision_of "$WORKER_TARGET")" = "$TARGET_SHA" ]
[ "$(revision_of "$CLI_TARGET")" = "$TARGET_SHA" ]

api_pid="$(systemctl show works-api.service -p MainPID --value)"
worker_pid="$(systemctl show works-worker.service -p MainPID --value)"
[ -n "$api_pid" ]
[ -n "$worker_pid" ]
[ "$(sha256sum "/proc/$api_pid/exe" | awk '{print $1}')" = "$api_hash" ]
[ "$(sha256sum "/proc/$worker_pid/exe" | awk '{print $1}')" = "$worker_hash" ]

"$CLI_TARGET" runners | grep -q wrkr_prod

smoke="$(curl -fsS --max-time 8 "$API/v1/works/$SMOKE_WORK_ID/evidence")"
grep -Fq '"canonicalization":"aftergraph-json-canonical/1"' <<<"$smoke"
grep -Fq '"algorithm":"sha256"' <<<"$smoke"
grep -Fq '"algorithm":"blake3"' <<<"$smoke"
grep -Fq '"algorithm":"hmac-sha256-v1"' <<<"$smoke"

install -d -m 0750 "$RECEIPT_DIR"
out="$RECEIPT_DIR/works-bootstrap-$TARGET_SHA.json"
tmpout="$out.tmp"
printf '{"schema":"aftergraph.works-bootstrap/1","target_sha":"%s","state":"VERIFIED","api_binary_sha256":"%s","worker_binary_sha256":"%s","cli_binary_sha256":"%s","api_pid":%s,"worker_pid":%s,"healthz":true,"integrity_fabric_smoke":true,"smoke_work_id":"%s","observed_at":"%s"}\n' \
  "$TARGET_SHA" "$api_hash" "$worker_hash" "$cli_hash" "$api_pid" "$worker_pid" "$SMOKE_WORK_ID" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" >"$tmpout"
mv -f "$tmpout" "$out"
chmod 0640 "$out"

mutated=false
rm -rf "$STAGE"
PROMOTE
chmod 0700 "$PAYLOAD"

systemd-run --unit="$UNIT" --collect --property=Type=oneshot "$PAYLOAD"
systemctl is-active --quiet "$UNIT" || systemctl is-activating --quiet "$UNIT" || fail "detached_unit_not_started"
printf 'works-bootstrap: scheduled unit=%s target_sha=%s\n' "$UNIT" "$TARGET_SHA"
