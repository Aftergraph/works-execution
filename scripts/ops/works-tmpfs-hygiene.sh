#!/usr/bin/env bash
# works-tmpfs-hygiene — diagnose and reclaim a full host tmpfs that is
# killing WORKS source checkouts (incident class: 2026-09-23 /tmp 100%).
#
# The failure signature in WORKS evidence is:
#   source checkout failed: write cred helper:
#   write /tmp/works-sources/.git-cred-helper-...sh: no space left on device
# with duration_ms=0 — the work died before any node command ran.
#
# Contract:
#   - status   read-only diagnosis (always safe)
#   - dry-run  list what WOULD be reclaimed, with sizes (no mutation)
#   - clean    delete the stale entries listed by dry-run (mutation)
#
# Safety invariants:
#   - LIVE paths are never touched: WORKS checkout roots and any path open
#     by a running works process are excluded from cleanup;
#   - only entries older than MIN_AGE_DAYS (default 2) are candidates;
#   - the diagnosis MUST distinguish deleted-but-open (phantom) usage from
#     visible stale files: cleaning visible files cannot reclaim phantom
#     bytes, and phantom usage requires a process restart, not cleanup;
#   - no rm runs outside $TARGET_ROOT (default /tmp).
set -euo pipefail

ACTION="${1:-status}"
case "$ACTION" in status|dry-run|clean) ;; *) echo "usage: $0 [status|dry-run|clean]" >&2; exit 2 ;; esac

TARGET_ROOT="${WORKS_TMPFS_ROOT:-/tmp}"
MIN_AGE_DAYS="${WORKS_TMPFS_MIN_AGE_DAYS:-2}"
EXCLUDES=("${WORKS_TMPFS_EXCLUDES:-/tmp/works-sources}")

fail(){ printf 'works-tmpfs-hygiene: %s\n' "$*" >&2; exit 2; }

[[ "$TARGET_ROOT" == /* && -d "$TARGET_ROOT" ]] || fail "target root must be an existing absolute directory"

# protected_paths prints every path that must survive cleanup: the WORKS
# checkout roots plus anything currently held open by a works process.
# The per-pid pipeline is race-guarded: a process may exit between pgrep
# and the /proc fd read, and that must not fail the whole protection list
# (a silent empty candidate set would be worse than a skipped live check).
protected_paths() {
  printf '%s\n' "${EXCLUDES[@]}"
  {
    for pid in $(pgrep -f 'works-' 2>/dev/null || true); do
      ls -l /proc/"$pid"/fd 2>/dev/null | awk -v root="$TARGET_ROOT" '
        / -> / {
          path=$NF; sub(/^.* -> /, "", path)
          if (index(path, root"/") == 1) print path
        }' || true
    done
    return 0
  } | sort -u
}

# candidates prints one stale top-level entry per line: age-filtered and
# live-filtered. Only $TARGET_ROOT direct children are considered so the
# blast radius is one entry per decision, never a deep walk deleting.
candidates() {
  local prot
  prot="$(mktemp)"
  protected_paths >"$prot"
  find "$TARGET_ROOT" -mindepth 1 -maxdepth 1 -mtime +"$((MIN_AGE_DAYS - 1))" -print0 2>/dev/null |
    while IFS= read -r -d '' entry; do
      if ! grep -Fxq "$entry" "$prot"; then
        printf '%s\n' "$entry"
      fi
    done
  rm -f "$prot"
}

do_status() {
  echo "== filesystem =="
  df -h "$TARGET_ROOT" || true
  df -Pi "$TARGET_ROOT" || true
  echo
  echo "== deleted-but-open (phantom) audit =="
  echo "If visible cleanup does not free bytes, they are held here:"
  local phantom=0
  while IFS= read -r line; do
    [[ -n "$line" ]] || continue
    echo "$line"
    phantom=1
  done < <(find /proc/[0-9]*/fd -lname '* (deleted)' -printf '%p -> %l\n' 2>/dev/null | grep -F "$TARGET_ROOT" || true)
  [[ "$phantom" == 1 ]] || echo "none on $TARGET_ROOT"
  echo
  echo "== top visible consumers =="
  du -xh --max-depth=1 "$TARGET_ROOT" 2>/dev/null | sort -rh | head -20
}

do_dry_run() {
  echo "== dry run: entries older than $MIN_AGE_DAYS days, live paths protected =="
  local total=0 count=0 size
  while IFS= read -r entry; do
    [[ -n "$entry" ]] || continue
    size="$(du -sk "$entry" 2>/dev/null | awk '{print $1}')"
    printf '%8s KB  %s\n' "${size:-0}" "$entry"
    total=$((total + ${size:-0}))
    count=$((count + 1))
  done < <(candidates)
  printf '\n%d entries, %d KB reclaimable\n' "$count" "$total"
}

do_clean() {
  echo "== clean: entries older than $MIN_AGE_DAYS days, live paths protected =="
  local n=0
  while IFS= read -r entry; do
    [[ -n "$entry" ]] || continue
    rm -rf -- "$entry"
    n=$((n + 1))
    printf 'removed %s\n' "$entry"
  done < <(candidates)
  printf '\nremoved %d entries\n' "$n"
  df -h "$TARGET_ROOT"
}

case "$ACTION" in
  status)  do_status ;;
  dry-run) do_dry_run ;;
  clean)   do_clean ;;
esac
