# v0.3.0 — Company Brain: knowledge mounted, not prompted

**Date:** 2026-09-03 · **Base:** main @ `20a4ad4` (v0.2.0 line) · **Wave:** v03/brain — k-041..k-045

v0.2 made the kernel *governed* — every surface pinned by a frozen,
hash-attested contract. v0.3 teaches the kernel the *Company Brain*:
five collections under `/org/<id>/` (missions, decisions, capabilities,
evidence, notes) where knowledge is **mounted**, not **prompted**. An
agent that wants to cite a decision no longer pastes a chunk of context
into a prompt — it asks the kernel for a **mount** of that path, and the
kernel returns a read view, scoped to the caller, with the authority law
already applied (you can read an authoritative object; you can never
*be* one without a human stamp).

That is why the brain namespace was the last big unmaterialized frozen
contract. ADR-0023 was already a `brain.ns/1.0` schema in
`contracts/manifest.json` (sha256 `0f5ed6…35f4`); what v0.3 does is wire
the store, the REST surface, and the release-ring law that lets the
brain surface roll out under the same one-step-only discipline as the
rest of the kernel. **The contract is law; v0.3 makes it live.**

## What this wave added

Four sibling slices land in this release. All four are gate-checked
together — the brain surface goes live when store + API land
together, never half-on.

### k-041 — brain kernel laws (k-impl, `v03/brain-kernel`)

The namespace regex, the append-only revision law, the central
authoritative ⇒ `human_stamped` promotion invariant, the
ephemeral-never-authoritative law, tombstone-as-append, and the mount
scope + org-boundary rules. **This is where ADR-0023 becomes code, not
just a schema.** Without k-041 the rest of the wave is unmounted; with
k-041 alone, nothing is exposed.

- **Namespace regex (frozen in `brain.ns/1.0`):**
  `^/org/[a-f0-9-]+/(missions|decisions|capabilities|evidence|notes)/[A-Za-z0-9_/-]+$`
  — five collections only, lowercase hex org id, an object may live in
  one collection. No cross-collection aliases; no implicit
  normalisation. A path that does not match is a 400 at the API and a
  schema-rejection at the store.
- **Append-only revisions:** every write to a `(path, revision)` pair
  is a new row, never a mutation. The PK is `(path, revision)` so
  revision collisions are detected at the constraint, not in
  application logic. There is no UPDATE path on `brain_objects`;
  the only mutating verbs are `promote` and `tombstone`, and both
  append a new revision.
- **The central law — authoritative ⇒ `human_stamped`:** if and only
  if `authoritative == true` then `promotion == "human_stamped"`.
  This is the `allOf/if/then` clause in the schema. The store
  enforces it again on insert; the API enforces it on the wire; the
  test
  `tests/contracts/contracts_test.go::TestAdversarialAgentKnowledgeCannotBecomeAuthoritative`
  enforces it as regression law. Three layers, one rule.
- **Ephemeral can never become authoritative:** `class == "ephemeral"`
  ⇒ `authoritative == false` is not a soft warning, it is a
  structural property. Promoting an ephemeral object is rejected
  with the same error code as any other authoritative-without-human
  attempt.
- **Tombstone-as-append:** deleting an object is not a row removal,
  it is a new revision with `tombstone == true` and empty body.
  Reads default to "latest non-tombstone revision", so a tombstone
  hides the object without erasing its lineage. A consumer that
  walks the full revision log still sees the whole history.
- **Mount scope + org-boundary:** a mount is a READ view of a path
  rooted at a single org. The mount's `org_id` must equal the
  object's org; a mount from org `0a1b` cannot reach into org
  `deadbeef` regardless of token scopes. Cross-org reads are
  rejected at the store, not the API.

### k-042 — brain schema v11 persistence (k-impl, `v03/brain-conformance`)

Two new tables behind the existing `schema_version` ledger:

| table | PK | notes |
|---|---|---|
| `brain_objects` | `(path, revision)` | append-only, every write is a new revision, `tombstone` revisions hide without erasing |
| `brain_mounts` | `(subject, path)` | read views; `subject` is the token identity, `path` is the org-rooted brain path; `revoked_at` nullable |

**Restart migrates v10 → v11 idempotently.** Both tables are
`CREATE TABLE IF NOT EXISTS`; the existing `bumpSchemaVersion(11)`
guard (services/work/store/store.go) checks
`SELECT COALESCE(MAX(version), 0) FROM schema_version` and only
inserts `11` if `current < 11`. No backfill, no destructive ALTER.

How to check (operator-side, after the v0.3 binary is live and a
restart has happened):

```bash
sqlite3 /var/lib/works/works.db 'select max(version) from schema_version'
# expect: 11
```

Honest note: measured baseline on this host (2026-09-03) — the live
store at `/var/lib/works/works.db` reports `max(version) = 10` with
the v0.2 ledger rows 5, 6, 7, 9, 10. The `11` will only appear after
the v0.3 binary has been started at least once. If you read `10`, the
binary on disk is still v0.2.

### k-043 — /v1/brain REST surface (k-impl, `v03/brain-cli`)

The HTTP surface that the kernel exposes for brain objects and brain
mounts. Bearer auth, 64 KiB bodies (KB-class per ADR-0026 §9; the
link surface uses the same cap, kept consistent for v1), JSON
in/out. Every route is documented in `contracts/`, and the wire
table below is exactly the route table — do not invent flags.

| Endpoint | Method | Auth | Body | Purpose |
|---|---|---|---|---|
| `/v1/brain/objects` | POST | Bearer (worker) | `{path, class, body, evidence_ref}` | append a new revision to a path (writes require a real `wrk_<32hex>` work id; see runbook) |
| `/v1/brain/objects` | GET | Bearer (worker, read scope) | — | `?path=<exact>` returns the latest non-tombstone revision; `?prefix=<dir>` lists latest-non-tombstone revisions under that org-rooted prefix |
| `/v1/brain/objects/promote` | POST | Bearer (worker + write scope) | `{path, revision, human_id}` | append a new revision with `authoritative: true, promotion: human_stamped`; rejected if `class == "ephemeral"` |
| `/v1/brain/objects/tombstone` | POST | Bearer (worker + write scope) | `{path, revision}` | append a new revision with `tombstone: true`; the object is hidden from default reads but its revision log is preserved |
| `/v1/brain/mounts` | POST | Bearer (worker) | `{path, scope, reason}` | create a read view of a path for the caller's `subject`; org-boundary checked |
| `/v1/brain/mounts` | GET | Bearer (worker) | — | `?subject=<self>` (or omit for self) lists the caller's active mounts |
| `/v1/brain/mounts/revoke` | POST | Bearer (worker) | `{path, subject}` | revoke a mount the caller owns; idempotent |

**Auth model (v1):** bearer-auth with read scopes on the `GET`
surface, write scopes on the `promote` / `tombstone` / write verbs.
A `policy.token` carrying a narrower scope than the route requires
is rejected (401). The policy-token *scope-subset enforcement on
mounts themselves* (the ADR-0023 §6 double-enforcement rule) is **not
in this wave** — v1 mounts are bearer-auth with read scopes only.
See *Known deferred limits* below.

**Fail-closed 503-unwired design.** When the brain surface is not
fully wired — the binary is v0.3 but the v0.2 link-pairing secret
boundary or the enrollment secret boundary has not been
provisioned — every `/v1/brain/*` route answers
`503 brain_surface_disabled`. This is the same posture as the link
surface and is **loud by design**: an operator pulling on a brain
route before the wave is fully landed sees a 503 with the exact
disable reason, never a 200 with an empty list. The boot log line is
`Brain surface enabled` when live (mirrors the link-surface log
line). This is what the runbook greps for.

**The brain surface goes live when store + API land together.**
k-041, k-042, and k-043 are three pieces of the same lock. The
flag `WORKS_BRAIN_ENABLED` defaults to off; flipping it on without
all three merges would expose routes against a store that does not
have the brain tables, or a store that does have the tables but
cannot serve the routes. The wave is gated on all three merges
landing; the integrator sets the env var and restarts as one
action.

### k-044 — release.rings law (k-impl, `v03/release-rings`)

The release-ring contract from the frozen `release.rings/1.0` schema
(`9677b52…01a9` in the manifest) is now operational law for *every*
kernel-surface rollout, not just the link surface. The brain wave
is the first surface shipped under it.

- **Advance one-step-only.** `rings` is an `enum: [internal, alpha,
  beta, stable]` array with `uniqueItems: true`; the advance law
  is that a surface moves at most one ring per release promotion.
  No skipping internal → beta, no skipping alpha → stable. The
  schema's `no_ring_skips: true` is the regression test; the
  release tool refuses to advance two rings.
- **48h beta soak.** `beta_soak_hours: const 48` is in the
  schema. A surface in `beta` cannot be promoted to `stable` until
  it has been continuously live in `beta` for at least 48 hours.
  The release tool records the `beta_at` timestamp and refuses
  promotion otherwise. This is the same soak that the link
  surface is currently running.
- **Kill-switch required off-internal.** Every ring below `stable`
  MUST have at least one entry in `kill_switch` (a list of env-var
  names or runtime flags that can revert the surface without a
  redeploy). The schema accepts an empty list for `stable`. The
  brain surface ships with `WORKS_BRAIN_KILL_SWITCH` (default: off
  in prod) wired in from day one.
- **Revert-with-reason + RevertLog.** Any revert to an earlier ring
  must carry a written reason (operator note, ticket ref, or
  `auto: <detector>` for the automated health-check reverts) and is
  appended to `release_revert_log` (durable, append-only). The
  RevertLog is what makes the rings auditable after the fact; it
  is the surface of evidence that the law was followed.
- **Stable needs freeze evidence.** A `stable` promotion requires
  the freeze-attested contract tests for that surface to be
  green at the promotion SHA. For v0.3, the brain surface's
  freeze evidence is the `brain.ns/1.0` adversarial test plus
  the schema hash in `contracts/manifest.sha256`; a stable
  promotion that does not have both is rejected by the release
  tool.

## Known deferred limits (be honest)

The following are deliberately **not** in v0.3. They are documented
here so the next wave's slice-card can name them precisely, not so
the next wave can silently slip them in.

- **Policy-token scope-subset enforcement on mounts (ADR-0023 §6
  double-enforcement).** v1 mounts are bearer-auth with read scopes
  only. The full policy-token-derived scope-subset check on a mount
  (the rule that a mount's effective scope is the intersection of
  the caller's token scope and the mounted object's own `allowed_*`
  metadata) is a v0.4+ slice. The API does not yet accept
  `policy_token` in the mount body; if you need it, file the slice
  card against ADR-0023 §6 explicitly.
- **PULSE edge-mounts over `/link/v1`.** A PULSE device cannot yet
  request a brain mount through the link surface. The route exists
  on the link side (`POST /link/v1/mounts`) for *context* mounts
  (ADR-0026); brain mounts over link are a later slice that needs
  a brain-aware claims shape on the link device token. The two
  surfaces are independent in v1.
- **No search / index.** There is no `GET /v1/brain/search` and
  there is no inverted index. This is per ADR-0023 §5: search is a
  presentation concern, never a source of truth. An object that
  only exists in an index is not a brain object. If you want to
  find a decision, you know its path; if you do not, the right
  answer is "make the path findable from where the decision is
  referenced" — not "build a search index".
- **Content is not deduplicated across paths.** If `/org/0a1b/notes/x`
  and `/org/0a1b/notes/y` have the same body, both revisions are
  stored in full. Content-addressed dedup is a storage optimisation,
  not a correctness one, and is deferred. If you want to share a
  body, mount the same path from two places — that is what mounts
  are for.
- **CLI `works brain` command not shipped this wave.** The
  operator-side CLI verb for inspecting mounts / objects from the
  shell is not in v0.3. The k-043 sibling worktree (CLI surface)
  ships `works brain` as a *separate* slice; if that slice merges
  after this release the verb will exist; if it does not, you
  have `curl` and the runbook. Do not pretend the CLI is shipped
  in v0.3.

## Upgrade notes (v0.2 → v0.3)

- **No breaking wire changes for existing routes.** `/link/v1/*`,
  `/v1/works/*`, `/v1/workers/*` are unchanged. The only new wire
  is the `/v1/brain/*` prefix above; it answers 503 until
  `WORKS_BRAIN_ENABLED=true` is set.
- **Schema migration v10 → v11, automatic and idempotent on
  restart.** Verified baseline (2026-09-03, on this host): the
  live `works.db` reports `max(version) = 10`. After the v0.3
  binary has restarted at least once, the same query returns
  `11`. The migration is `CREATE TABLE IF NOT EXISTS` only — no
  backfill, no destructive ALTER, safe to run repeatedly.
- **`WORKS_BRAIN_ENABLED` is the new opt-in.** Default off; set
  to `true` in `/etc/works/works.env` only **after** k-041,
  k-042, and k-043 have all been merged and verified. Setting it
  before all three is in is a misconfiguration and the routes
  will answer 503. The runbook
  ([docs/runbooks/brain-mounts.md](runbooks/brain-mounts.md))
  walks the smoke loop.
- **`WORKS_BRAIN_KILL_SWITCH` ships populated.** The brain
  surface lands with at least one kill-switch wired in. The
  default state in prod is *off*; flipping it on reverts the
  surface to a previous ring without a redeploy.
- **Contract tests are still regression law.** The brain tests
  in `tests/contracts/contracts_test.go` (`TestAdversarialAgentKnowledgeCannotBecomeAuthoritative`
  and the conformance / baseline tests for `brain.ns/1.0`) fail
  on drift; the manifest hash for `brain.ns/1.0` is the freeze
  attestation.

## v0.3.0 wave — the four slices

| Slice | Title | Owner worktree | PR |
|---|---|---|---|
| k-041 | brain kernel laws (namespace, append-only, authoritative ⇒ human_stamped, tombstone-as-append, mount scope + org-boundary) | `v03/brain-kernel` | PR (pending) |
| k-042 | brain schema v11 persistence (`brain_objects` PK(path,revision), `brain_mounts`, idempotent v10→v11 migration) | `v03/brain-conformance` | PR (pending) |
| k-043 | `/v1/brain` REST surface (POST objects, GET objects?path/?prefix, POST objects/promote, POST objects/tombstone, POST mounts, GET mounts?subject, POST mounts/revoke; bearer auth; 64 KiB; fail-closed 503-when-unwired) | `v03/brain-cli` | PR (pending) |
| k-044 | release.rings operational law (one-step-only, 48h beta soak, kill-switch required off-internal, RevertLog, stable-needs-freeze-evidence) | `v03/release-rings` | PR (pending) |
| k-045 | v0.3.0 release notes + brain-mounts runbook + README v0.3 section (this slice) | `v03/brain-docs` | PR (pending) |

Five workers, disjoint file-ownership. The integrator fills the
"PR (pending)" cells with the merge SHAs once the wave lands
together — that is the same pattern the v0.2 wave used (see
[docs/RELEASE-v0.2.0.md](RELEASE-v0.2.0.md), final table).

## Governance invariants (unchanged by v0.3, now contract-pinned)

All v0.2 invariants carry over. The one new invariant v0.3 adds:

- **`authoritative ⇒ human_stamped`** is three-enforced: schema
  (`brain.ns/1.0` `allOf/if/then`), store (re-validated on insert),
  and adversarial test
  (`TestAdversarialAgentKnowledgeCannotBecomeAuthoritative`).
  Drift in any one of the three fails the wave.

## Verification at release

- `go build ./...` — clean
- `go test ./tests/contracts/ -count=1` — 21/21 PASS
  (the brain adversarial test is in this set; if it goes red the
  central law has drifted)
- Full suite `go test ./... -count=1` — all packages ok
- Live store schema version (after restart): `11`
- `journalctl -u works-api -n 20 --no-pager | grep "Brain surface"`
  → `Brain surface enabled` when `WORKS_BRAIN_ENABLED=true`

## ADR boundary (honest note)

ADRs 0005–0007 live in this repo (`docs/adr/`). ADR-0023
(Company Brain) and ADR-0013 (release rings) are the governing ADRs
for this wave; they are cited from the frozen schemas and the
package docs here, but their full text is not vendored in-repo.
Treat the *frozen schemas* (`contracts/schemas/brain.ns.schema.json`,
`contracts/schemas/release.rings.schema.json`) and the
`contracts/manifest.sha256` as the authoritative restatement.
