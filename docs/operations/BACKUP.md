# Backup & Restore — works-execution V1

**Status:** Active runbook · **Source:** ADR-0005 (SQLite for V1 state) · **Audit ref:** S10 (2026-09-01)

ADR-0005 chose SQLite for V1 state and promised this runbook in slice 2. Scope: the control-plane database, what is deliberately not backed up, the restore procedure, and the monthly restore drill.

## What holds state

| Data | Location | Backed up? |
|---|---|---|
| Control-plane state (works, leases, runners, provenance, handoffs, mission contracts) | `$WORKS_DB` (SQLite, WAL) | **Yes — online backup below** |
| Worker sandbox + toolchain caches | `/var/lib/works` | No — rebuildable |
| Artifacts | `$WORKS_ARTIFACTS` | No for V1 — evidence bundles carry the hashes |
| Secrets (`WORKS_ENROLL_SECRET`, `WORKS_WEBHOOK_SECRET`, `WORKS_GITHUB_TOKEN`) | systemd unit / env on the VDS | **Never** — rotate on compromise; do not restore stale copies |
| Repo, works.yml, contracts, kanban board | git | No — git is the source of truth |

## Online backup (safe under WAL, no downtime)

Never `cp` the live `*.db` while the API runs: WAL mode means `*.db` and `*.db-wal` must be captured atomically. Use SQLite's online backup instead:

```bash
mkdir -p /var/backups/works
sqlite3 "$WORKS_DB" ".backup '/var/backups/works/works-$(date -u +%Y%m%dT%H%M%SZ).db'"
sqlite3 /var/backups/works/works-*.db "PRAGMA integrity_check;"
rsync -a --delete /var/backups/works/ backup-user@offsite-host:/srv/backups/works/
```

ADR-0005's plain `cp *.db` is valid only after a clean shutdown or an explicit `PRAGMA wal_checkpoint(TRUNCATE);`.

## Schedule and retention (V1 defaults)

- Frequency: daily (cron on the API host). RPO target: 24h.
- Retention: 14 daily on-host, 14 daily offsite.
- Upgrade path: litestream continuous replication (SQLite-native, sub-minute RPO) before any multi-tenant use.

Cron (root crontab on the VDS — substitute the absolute DB path; systemd env is not available in cron):

```
17 3 * * * sqlite3 /path/to/works.db ".backup '/var/backups/works/works-$(date -u +\%Y\%m\%dT\%H\%M\%SZ).db'" && find /var/backups/works -name 'works-*.db' -mtime +14 -delete
```

## Restore procedure

1. Stop the control plane: `systemctl stop works-api`. Workers lose heartbeats; the lease reaper expires their leases — that is the designed recovery path, not an incident.
2. Restore the database:
   ```bash
   mv "$WORKS_DB" "$WORKS_DB.broken-$(date -u +%s)"
   sqlite3 "$WORKS_DB" ".restore '/var/backups/works/works-<timestamp>.db'"
   sqlite3 "$WORKS_DB" "PRAGMA integrity_check;"
   ```
3. Restart and verify: `systemctl start works-api`, then
   - `GET /healthz` → 200
   - `GET /readyz` → 200
   - `works pilot --once` → work reaches SUCCEEDED end-to-end
4. Expect in-flight works from after the snapshot to reappear queued or attempt-failed; the state machine re-executes them. Evidence bundles re-attach on completion.

## Monthly restore drill

- Restore the newest backup to a scratch path, run `PRAGMA integrity_check`, submit one smoke work against the scratch DB, and record the result:
  - [ ] YYYY-MM: pass/fail + notes
- A backup that has never been restored is a hypothesis, not a backup.

## Known gaps (deliberate for V1)

- Single VDS: RTO is "reprovision + restore". Document DNS + systemd unit provisioning before multi-tenant.
- No point-in-time recovery until litestream (or equivalent) is adopted.
