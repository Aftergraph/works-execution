# WORKS source checkout root

## Problem

WORKS source checkout historically derived its workspace from `os.TempDir()`, producing
`/tmp/works-sources` on Linux. This made source acquisition depend on host tmpfs capacity
and mount policy before a work node command could start.

A production worker can now pin source checkout to an explicit absolute root with either:

```bash
works-worker -source-root /var/lib/works
```

or:

```bash
WORKS_SOURCE_ROOT=/var/lib/works
```

The worker creates a private `works-sources/` child below that root and still removes each
per-work checkout after execution.

## Production requirements

The configured root must:

- be an absolute path;
- be writable by the worker service account;
- reside on a filesystem with enough capacity for concurrent shallow clones;
- permit execution when workloads need repository-provided native binaries;
- not be a shared world-writable directory;
- be monitored independently from `/tmp`.

A relative source root is rejected fail-closed before clone.

## VDS migration

For the current VDS worker, use a persistent WORKS-owned location rather than host tmpfs:

```text
WORKS_SOURCE_ROOT=/var/lib/works
effective checkout parent=/var/lib/works/works-sources
```

Before restart:

1. verify the target filesystem has sufficient bytes and inodes;
2. verify ownership/permissions match the worker service account;
3. build the exact candidate `works-worker` binary;
4. record the candidate SHA;
5. update the service environment or `-source-root` argument;
6. restart the worker through the governed deployment path;
7. verify the process command/environment and binary SHA;
8. submit a small exact-SHA work and require source checkout + node execution evidence.

Rollback is removing `WORKS_SOURCE_ROOT` / `-source-root` and restoring the prior binary.
Do not roll back to `/tmp` while the host tmpfs is capacity-exhausted.

## Incident classification

A source checkout failure caused by an unavailable/exhausted source root is runner
infrastructure failure. It is not a repository code failure because the repository command
has not started.
