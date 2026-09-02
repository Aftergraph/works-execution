# WORKS Conversation V1 — cross-repo acceptance runbook

**Branch:** `design/works-conversation-v1` (works-execution) + `design/works-conversation-v1` (autonomous-venture-company)

**Gate:** Task 8 — cross-repo E2E acceptance + production configuration.

## 1. Protocol surface (works-execution, this branch adds)

| Route | Auth | Purpose |
|---|---|---|
| `POST /v1/works` | Bearer | Create work (queue, objective, graph, mission contract) |
| `GET /v1/works/{id}` | Bearer | Canonical state + evidence + attempts |
| `GET /v1/works/{id}/events?after=N&limit=M` | Bearer | **REST journal listing** (conversation mirror cursor; SSE when no query params) — added by this slice for the AVC mirror loop |
| `GET /v1/works/{id}/handoff` | Bearer | Read-only handoff checkpoint (`checkpoint_hash`, `state`) |
| `POST /v1/works/{id}/suspend` | Bearer + bridge | Bridge-bound suspend → WAITING_HUMAN + durable handoff |
| `POST /v1/works/{id}/resume` | Bearer + bridge | Resume from exact checkpoint (fail-closed: no handoff → 409) |
| `POST /v1/works/{id}/cancel` | Bearer | Cancel (QUEUED → CANCELLED) |
| `POST /v1/leases/{id}/complete` | Bearer | Complete + evidence + artifact |
| `GET /v1/works/{id}/nodes/{n}/logs` | Bearer | Node log = artifact (same-host: `WORKS_ARTIFACTS/<work>/<node>.log`) |
| `GET /v1/workers/ready` | Bearer | Ready nodes |
| `POST /v1/leases` | Bearer | Grant lease |

**Journal completeness (this slice):** the live execution timeline requires every canonical
state transition to be mirrored. `GrantLease` (QUEUED→RUNNING, post-commit best-effort),
`CompleteLease` (RUNNING→VERIFYING→SUCCEEDED, eventful) and `ResumeFromCheckpoint`
(SUSPENDED/WAITING_HUMAN→RUNNING, eventful) all emit `work.state.changed` journal rows.

## 2. Verify

```bash
export PATH=/usr/local/go/bin:/usr/bin:/bin
cd /tmp/wt-conv-v1        # works worktree on design/works-conversation-v1
go build ./... && go vet ./...
go test ./... -count=1

cd /tmp/wt-avc-conv-v1    # AVC worktree on design/works-conversation-v1
pnpm test                 # full AVC gates incl. the cross-repo E2E
```

The E2E (`tests/works-conversation-v1.e2e.test.ts`) builds the REAL works-api binary,
enrolls a real worker token, drives the REAL conversation worker through the REAL
platform-web bridge (mocked session resolver only), executes the WORKS worker protocol
in-test, and asserts the full V1 loop:

1. conversation create via bridge → 201
2. human message durable (message-before-progress) → snapshot
3. `/work` proposal → REAL `POST /v1/works` (QUEUED, real objective)
4. driver executes `run` node → lease complete + evidence + artifact log
5. verify node ready → **bridge suspend** → WAITING_HUMAN + handoff persisted
6. SSE via bridge → `approval.required` (checkpointHash bound) — mirrored live
7. kernel receipt (opaque token, derived operationId) → resume → RUNNING; replay refused
8. verify node executes → SUCCEEDED; evidence ≥2 pass; node log contains objective
9. reconnect from cursor 0 → replay includes `work.state.changed` SUCCEEDED mirror
10. second conversation → stop → CANCELLED + `cancel.requested` SSE event

Fail-closed test: conversation worker WITHOUT a kernel client refuses every resume (403).

## 3. Production configuration gaps closed by this slice

| Gap | Fix |
|---|---|
| platform-web session resolver never granted `missions:prepare` (every conversation POST 403s) | `PLATFORM_CAPABILITIES` += `missions:prepare` |
| conversation worker's `createWork` body didn't map to the canonical API shape (no runnable graph; node scripts referenced `$WORKS_ARTIFACT_DIR` which is NOT injected by works-api/works-worker → `/result.txt` writes) | self-contained bounded template: objective echoed to stdout (log artifact), verify re-checks length deterministically |
| no REST journal listing for the mirror cursor | `?after=&limit=` JSON listing on `GET /v1/works/{id}/events` (SSE unchanged) |
| lease/resume transitions not journaled (empty live timeline) | eventful journals as above |
| E2E binary resolution | sibling worktree lookup; binary surface guard (suspend route probe) fails loudly on a stale checkout |

## 4. Evidence

- Works gate: `go build ./... && go vet ./... && go test ./... -count=1` all green.
- AVC gate: `pnpm test` (root vitest incl. E2E + platform-web) green.
- E2E: `Tests 2 passed (2)`.