# WORKS host tmpfs hygiene

## Problem

A full host tmpfs (`/tmp`) kills WORKS source checkouts before any node
command runs. Incident 2026-09-23: `/tmp` on the production VDS reached
12 GB/12 GB (100%); works died at checkout with

```text
source checkout failed: write cred helper:
write /tmp/works-sources/.git-cred-helper-…sh: no space left on device
```

`duration_ms=0` — no pipeline node executed, so the GitHub
`works-execution` status went red on every push to every onboarding repo
while the commits themselves were healthy.

## Diagnosis first — visible vs phantom bytes

Before deleting anything, distinguish the two failure modes:

- **Visible stale files** (the 2026-09-23 case): cleanup reclaims the space.
- **Deleted-but-open (phantom) bytes**: held by live processes; cleanup
  cannot reclaim them and a process restart is required instead.

The helper's `status` action performs the audit:

```bash
bash scripts/ops/works-tmpfs-hygiene.sh status
```

It prints filesystem usage/inodes, a deleted-but-open audit filtered to the
target root, and the top visible consumers.

## Contract

```bash
bash scripts/ops/works-tmpfs-hygiene.sh status   # read-only diagnosis
bash scripts/ops/works-tmpfs-hygiene.sh dry-run   # what would be reclaimed
bash scripts/ops/works-tmpfs-hygiene.sh clean    # delete stale entries
```

Safety invariants:

- only top-level entries older than `WORKS_TMPFS_MIN_AGE_DAYS` (default 2)
  are candidates;
- the WORKS checkout root (`/tmp/works-sources` by default, override with
  `WORKS_TMPFS_EXCLUDES`) and any path currently open by a `works-` process
  are never touched;
- `clean` only consumes the exact candidate list `dry-run` shows;
- no `rm` runs outside `WORKS_TMPFS_ROOT` (default `/tmp`).

## Relationship to the source root

This runbook is relief for the *legacy* `/tmp` checkout path. The
structural fix is `WORKS_SOURCE_ROOT` (see `source-root-activation.md`),
which moves checkouts to a persistent filesystem. While any deployed
worker predates the `-source-root` contract, `/tmp` hygiene is the only
mitigation for checkout-stage `no space left on device` failures.

## Incident evidence chain (what to record)

- `df -h /tmp` and `df -Pi /tmp` before and after cleanup;
- the deleted-but-open audit output (to prove which failure mode it was);
- dry-run candidate count and reclaimed bytes;
- the first WORKS work that succeeds after cleanup, with its
  `works-execution` commit-status flip to SUCCESS on an exact head.
