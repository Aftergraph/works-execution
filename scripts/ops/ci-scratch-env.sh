# shellcheck shell=bash
# Select a writable CI scratch directory without assuming the worker's host layout.
#
# Priority:
#   1. WORKS_SCRATCH_ROOT selected by the worker/operator.
#   2. An already-injected TMPDIR, unless it is the shared host /tmp root.
#   3. WORKS' durable state filesystem when available.
#   4. A repo-local fallback for portable/non-WORKS hosts.
#
# This file is sourced by works.yml nodes so the exported TMPDIR applies to
# Go/cgo/compiler subprocesses in the same shell.
set -euo pipefail

scratch="${WORKS_SCRATCH_ROOT:-}"
if [[ -z "$scratch" ]]; then
  case "${TMPDIR:-}" in
    ""|/tmp) ;;
    *) scratch="$TMPDIR" ;;
  esac
fi

if [[ -z "$scratch" ]] && [[ -d /var/lib/works && -w /var/lib/works ]]; then
  scratch=/var/lib/works/tmp
fi

if [[ -z "$scratch" ]]; then
  scratch="$PWD/.works-tmp"
fi

mkdir -p "$scratch"
export TMPDIR="$scratch"
echo "WORKS_CI_TMPDIR=$TMPDIR"
