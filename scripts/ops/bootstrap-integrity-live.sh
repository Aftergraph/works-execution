#!/usr/bin/env bash
set -euo pipefail
umask 077
export PATH='/usr/local/go/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin'
export GOPATH='/var/lib/works/gopath'
export GOMODCACHE='/var/lib/works/gomodcache'
export GOCACHE='/var/lib/works/gocache'

TARGET_SHA='89537cc376228c6eff42855d2be9e567e22eabe4'
SHORT="$(printf '%s' "$TARGET_SHA" | cut -c1-12)"
UNIT="aftergraph-works-bootstrap-$SHORT"
STAGE="/run/$UNIT"
PAYLOAD="$STAGE/promote.sh"

fail() {
  printf 'works-bootstrap: %s\n' "$*" >&2
  exit 2
}

[ "$(id -u)" -eq 0 ] || fail "root_worker_required"

# Critical invariant: the leased WORKS process performs no long-lived work.
# It only launches a detached root oneshot. This avoids coupling deployment
# survival to the 25 s worker lease or to the API/worker restart being deployed.
rm -rf "$STAGE"
install -d -m 0700 "$STAGE"

cat >"$PAYLOAD" <<'PROMOTE'
#!/usr/bin/env bash
set -euo pipefail
umask 077
export PATH='/usr/local/go/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin'
export GOPATH='/var/lib/works/gopath'
export GOMODCACHE='/var/lib/works/gomodcache'
export GOCACHE='/var/lib/works/gocache'

TARGET_SHA='89537cc376228c6eff42855d2be9e567e22eabe4'
SHORT="$(printf '%s' "$TARGET_SHA" | cut -c1-12)"
REMOTE='https://github.com/Aftergraph/works-execution.git'
STAGE="/run/aftergraph-works-bootstrap-$SHORT"
RECEIPT_DIR='/var/lib/works/deployments'
BACKUP_DIR="$RECEIPT_DIR/backups/$TARGET_SHA"
ENV_FILE='/etc/works/works.env'
SMOKE_WORK_ID='wrk_3995b52a8e30d244dc83f6413bba0df2'
API='http://127.0.0.1:18191'
mutated=false
tmp=''
build=''

revision_of() {
  go version -m "$1" 2>/dev/null |
    sed -n 's/^[[:space:]]*build[[:space:]]*vcs\.revision=//p' |
    head -n 1
}

modified_of() {
  go version -m "$1" 2>/dev/null |
    sed -n 's/^[[:space:]]*build[[:space:]]*vcs\.modified=//p' |
    head -n 1
}

service_path() {
  local raw path
  raw="$(systemctl show "$1" -p ExecStart --value)"
  path="$(sed -n 's/.*path=\([^ ;}]*\).*/\1/p' <<<"$raw" | head -n1)"
  [ -n "$path" ] && [ "$(printf '%s' "$path" | cut -c1)" = "/" ]
  printf '%s\n' "$path"
}

atomic_install() {
  local src="$1" dst="$2" next
  next="$dst.next.$SHORT"
  install -m 0755 "$src" "$next"
  mv -f "$next" "$dst"
}

write_receipt() {
  local state="$1" reason="$2" out tmpout
  install -d -m 0750 "$RECEIPT_DIR"
  out="$RECEIPT_DIR/works-bootstrap-$TARGET_SHA.json"
  tmpout="$out.tmp"
  printf '{"schema":"aftergraph.works-bootstrap/1","target_sha":"%s","state":"%s","reason":"%s","observed_at":"%s"}\n' \
    "$TARGET_SHA" "$state" "$reason" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" >"$tmpout"
  mv -f "$tmpout" "$out"
  chmod 0640 "$out"
}

rollback() {
  local rc=$?
  trap - ERR
  set +e
  if [ "$mutated" = true ]; then
    [ -f "$BACKUP_DIR/works-api" ] && atomic_install "$BACKUP_DIR/works-api" "$API_TARGET"
    [ -f "$BACKUP_DIR/works-worker" ] && atomic_install "$BACKUP_DIR/works-worker" "$WORKER_TARGET"
    [ -f "$BACKUP_DIR/works" ] && atomic_install "$BACKUP_DIR/works" "$CLI_TARGET"
    systemctl restart works-api.service works-worker.service works-worker-2.service works-worker-3.service
  fi
  write_receipt FAILED_ROLLED_BACK "exit_$rc"
  [ -z "$tmp" ] || rm -rf "$tmp"
  [ -z "$build" ] || rm -rf "$build"
  rm -rf "$STAGE"
  exit "$rc"
}
trap rollback ERR

sleep 3

# Evidence producer key: provision once, never print. The works-api reads it
# via EnvironmentFile and enables GET /v1/works/{id}/evidence. Without it the
# Integrity Fabric smoke is 503 fail-closed by design.
if [ -f "$ENV_FILE" ] && ! grep -q '^WORKS_EVIDENCE_HMAC_KEY=' "$ENV_FILE"; then
  key="$(head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n')"
  printf 'WORKS_EVIDENCE_HMAC_KEY=%s\n' "$key" >>"$ENV_FILE"
  chmod 0640 "$ENV_FILE"
  echo 'works-bootstrap: evidence hmac key provisioned'
fi

# Exact-main freshness gate immediately before any build/mutation.
remote_main="$(git ls-remote "$REMOTE" refs/heads/main | awk 'NR==1{print $1}')"
[ "$remote_main" = "$TARGET_SHA" ]

install -d -m 0755 "$GOCACHE" "$GOMODCACHE" "$GOPATH"
tmp="$(mktemp -d)"
build="$(mktemp -d)"
git -C "$tmp" init -q
git -C "$tmp" remote add origin "$REMOTE"
git -C "$tmp" fetch -q --no-tags --depth=1 origin main
[ "$(git -C "$tmp" rev-parse FETCH_HEAD)" = "$TARGET_SHA" ]
git -C "$tmp" checkout -q --detach FETCH_HEAD

# Binaries are built into a staging dir OUTSIDE the checkout: writing them
# into $tmp would dirty the tree and stamp later builds vcs.modified=true.
(
  cd "$tmp"
  go vet ./...
  go test ./... -count=1
  CGO_ENABLED=0 go build -trimpath -o "$build/works-api" ./cmd/works-api
  CGO_ENABLED=0 go build -trimpath -o "$build/works-worker" ./cmd/works-worker
  CGO_ENABLED=0 go build -trimpath -o "$build/works" ./cmd/works
)

for b in works-api works-worker works; do
  [ -x "$build/$b" ]
  [ "$(revision_of "$build/$b")" = "$TARGET_SHA" ]
  [ "$(modified_of "$build/$b")" = false ]
done

# Revalidate main after the potentially long build, before mutation.
[ "$(git ls-remote "$REMOTE" refs/heads/main | awk 'NR==1{print $1}')" = "$TARGET_SHA" ]

API_TARGET="$(service_path works-api.service)"
WORKER_TARGET="$(service_path works-worker.service)"
[ "$(basename "$API_TARGET")" = works-api ]
[ "$(basename "$WORKER_TARGET")" = works-worker ]
CLI_TARGET="$(dirname "$WORKER_TARGET")/works"
[ -x "$API_TARGET" ] && [ -x "$WORKER_TARGET" ] && [ -x "$CLI_TARGET" ]

install -d -m 0700 "$BACKUP_DIR"
cp -a "$API_TARGET" "$BACKUP_DIR/works-api"
cp -a "$WORKER_TARGET" "$BACKUP_DIR/works-worker"
cp -a "$CLI_TARGET" "$BACKUP_DIR/works"

api_hash="$(sha256sum "$build/works-api" | awk '{print $1}')"
worker_hash="$(sha256sum "$build/works-worker" | awk '{print $1}')"
cli_hash="$(sha256sum "$build/works" | awk '{print $1}')"

atomic_install "$build/works-api" "$API_TARGET"
atomic_install "$build/works-worker" "$WORKER_TARGET"
atomic_install "$build/works" "$CLI_TARGET"
mutated=true

[ "$(sha256sum "$API_TARGET" | awk '{print $1}')" = "$api_hash" ]
[ "$(sha256sum "$WORKER_TARGET" | awk '{print $1}')" = "$worker_hash" ]
[ "$(sha256sum "$CLI_TARGET" | awk '{print $1}')" = "$cli_hash" ]

systemctl restart works-api.service
healthy=false
for _ in $(seq 1 80); do
  if curl -fsS --max-time 2 "$API/healthz" >/dev/null 2>&1; then
    healthy=true
    break
  fi
  sleep 0.5
done
[ "$healthy" = true ]

systemctl restart works-worker.service works-worker-2.service works-worker-3.service
sleep 3

[ "$(revision_of "$API_TARGET")" = "$TARGET_SHA" ]
[ "$(revision_of "$WORKER_TARGET")" = "$TARGET_SHA" ]
[ "$(revision_of "$CLI_TARGET")" = "$TARGET_SHA" ]

api_pid="$(systemctl show works-api.service -p MainPID --value)"
worker_pid="$(systemctl show works-worker.service -p MainPID --value)"
[ "$api_pid" -gt 0 ] && [ "$worker_pid" -gt 0 ]
[ "$(sha256sum "/proc/$api_pid/exe" | awk '{print $1}')" = "$api_hash" ]
[ "$(sha256sum "/proc/$worker_pid/exe" | awk '{print $1}')" = "$worker_hash" ]

WORKS_API="$API" "$CLI_TARGET" runners | grep -q wrkr_prod

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
rm -rf "$tmp" "$build" "$STAGE"
PROMOTE
chmod 0700 "$PAYLOAD"

# Replace any stale transient unit name from a prior failed bootstrap.
systemctl reset-failed "$UNIT.service" >/dev/null 2>&1 || true
systemd-run --no-block --unit="$UNIT" --collect --property=Type=oneshot "$PAYLOAD"
printf 'works-bootstrap: detached unit scheduled unit=%s target_sha=%s\n' "$UNIT" "$TARGET_SHA"
