# Agent Capability Declaration — `works-worker`

**Status:** Accepted
**Owner:** Founder (works-execution venture)
**Last reviewed:** 2026-08-31
**Next review:** 2026-11-30 (quarterly)
**Applies to:** `cmd/works-worker` and `internal/worker`

This document is the agent capability declaration for the works-execution worker.
It maps directly onto the AI/agent standards declared in
`docs/standards/registry.json` (domain `ai`) and is treated as authoritative
when the worker is deployed.

Even though `works-worker` is not a machine-learning model, it is an
**autonomous decision-maker**: it polls the control plane, requests leases,
executes arbitrary code, and self-reports results. MITRE ATLAS, NIST AI RMF,
and ISO/IEC 42001 all cover this class of agent.

---

## Identity

| Field | Value |
|---|---|
| Agent ID | `works-worker` |
| Version | semver of `cmd/works-worker/main.go` (built from `internal/worker/`) |
| Maintainer | Founder (works-execution) |
| Repository | `github.com/JonasAbde/works-execution` |
| Source location | `cmd/works-worker/`, `internal/worker/` |
| Container image | none (V1 subprocess executor); planned Docker image in slice 3 |
| Trust class | `standard` (slice 2 default); planned SPIFFE ID `spiffe://works-execution/ns/default/sa/<worker-id>` in slice 3 |

## Purpose

Execute node-level commands from a `Work` graph under a lease, report results
via the control plane, and gracefully surrender the lease on shutdown or
signal. The worker does not initiate new work — it only acts on what the
control plane has authorized via a lease grant.

## Owner

Works-execution founder. The worker binary is open source; the venture holds
the canonical upstream.

## Model

**None.** The worker is a deterministic subprocess executor. It does not call
any ML model, LLM, or external AI service. This is by design (ADR-0003 in
`docs/works-venture-starter-pack/10_ADRS/`) and the design review
(`01_PRODUCT/PRODUCT_SPEC_V1.md`) explicitly defers AI involvement to a later
slice (see `platform-ai-failure-intel` in the registry).

## Capabilities

| Capability | Implementation | Reference |
|---|---|---|
| Poll `/v1/workers/ready` | HTTP GET, every `PollEvery` | `internal/worker/worker.go` |
| Request lease via `POST /v1/leases` | HTTP POST | same |
| Send `POST /v1/leases/{id}/heartbeat` | HTTP POST every `HeartbeatEvery` | same |
| Execute subprocess via `sh -c "<command>"` | `exec.CommandContext` | same |
| Kill subprocess on lease loss | `cmd.Process.Kill()` | same |
| Write artifact to `<ArtifactsDir>/<workID>/<nodeID>.log` | `os.WriteFile` with sha256 | same |
| Report result via `POST /v1/leases/{id}/complete` | HTTP POST | same |
| Voluntarily release lease | `POST /v1/leases/{id}/release` | same |

## Tools

- HTTP client (Go `net/http`).
- Subprocess executor (`os/exec`).
- Local filesystem access (artifacts directory only).
- The `sh` shell.

The worker does **not** have:

- Network access to anything other than the configured API URL.
- Filesystem access to anything other than its `ArtifactsDir`.
- Privilege to mutate Work or Lease state outside the lease protocol.
- Ability to call other workers, schedule itself, or modify its own binary.

## Permissions

Default-deny. The worker is a single-purpose process:

- **Read:** own config flags + the API URL + the ArtifactsDir path.
- **Write:** `<ArtifactsDir>/<workID>/<nodeID>.log` only.
- **Network:** outbound HTTPS/HTTP to the API URL only. All other outbound
  network access from subprocesses is **denied by default** under the
  Hermetic Execution Standard (#111, slice 3).
- **Process:** spawn child processes; receive SIGTERM/SIGINT for graceful
  shutdown; receive SIGKILL from the lease-reaper on lease loss.

## Authority

| Authority | Held? | How granted |
|---|---|---|
| Pick work | **No** | The control plane decides what is "ready"; the worker only polls. |
| Lease work | Yes | `POST /v1/leases/grant` returns 201 → worker owns the lease. |
| Execute code | Yes, within lease | Subprocess execution is the purpose. |
| Modify Work state | No | All state transitions go through the API. |
| Modify Lease state | Yes, on its own lease only | `complete`, `release`, `heartbeat`. |
| Spawn other workers | No | The control plane does that. |
| Read other tenants' work | No | The API scopes by tenant (slice 3 RBAC). |

**Tenant isolation:** the worker only sees works for its tenant. Cross-tenant
data access is impossible because the API rejects the request (slice 3
enforces this in middleware; slice 1+2 enforces via DB schema).

## Risk classification (NIST AI RMF 1.0)

- **Function:** `MANAGE` — the worker autonomously acts on leases.
- **Risk level:** `LIMITED`. Per the EU AI Act's likely tier (pending legal
  review; see `eu-ai-act` row in registry), the worker is **not** a general-purpose
  AI; it is a deterministic subprocess executor. Until counsel confirms,
  this classification is provisional and `BLOCKED: requires-external-audit`
  applies to the formal `eu-ai-act` standard.
- **Impact profile:** the worker can execute arbitrary user-supplied shell
  commands. The blast radius is bounded by:
  1. Lease scope (only the granted node).
  2. Hermetic execution default (slice 3) — no network, no secrets by default.
  3. Timeout (`TimeoutS` field on Node, propagated as `exec.CommandContext`).
- **Failure modes:**
  - Subprocess crash → attempt marked failed.
  - Lease lost → subprocess killed, attempt marked cancelled.
  - API unreachable → backoff, retry; eventually worker exits.

## Allowed actions

- Run `Node.Run` as `sh -c <command>`.
- Create the artifact file under `<ArtifactsDir>/<workID>/<nodeID>.log`.
- POST result, evidence, and artifact metadata back via `/v1/leases/{id}/complete`.
- Respond to SIGTERM by cancelling the in-flight subprocess and releasing
  the lease.

## Prohibited actions

- Modify the Work object directly (must go through `/v1/leases/{id}/complete`).
- Read or write to filesystem paths outside `ArtifactsDir` and its parent.
- Make outbound HTTP requests other than to the configured API URL.
- Bypass the lease protocol (e.g. spawning children that grant themselves leases).
- Persist long-lived credentials anywhere on disk.
- Network access from the subprocess (slice 3 hermetic default; denied at
  the subprocess layer).
- Self-modification (no update mechanism in V1).

## Escalation rules

- Subprocess fails → attempt marked failed → if no other node in the work
  can succeed, the work transitions to `FAILED` automatically. **No automatic
  retry.** Retry is a separate policy in slice 3.
- Lease lost → subprocess killed → attempt marked cancelled → reaper returns
  the node to ready → next worker can re-lease.
- API returns 5xx → worker backs off and retries.
- Worker process crashes → reaper detects stale lease within ≤30s → same
  recovery as above.

## Human approval requirements

**None for V1.** The worker acts only on leases that the control plane granted
based on queued Work objects. A human approves Work creation through the CLI
or, in the future, a GitHub webhook. The worker itself does not require
real-time human approval for any action.

In slice 3, capability manifests can require human approval for specific
side effects (e.g. network egress). That approval will live in the policy
layer, not in the worker.

## Memory scope

**Stateless.** The worker holds no memory between Work executions. The only
state it maintains in-process is the current lease and the current subprocess.
On restart, it polls fresh.

Persistent memory about Works lives in the control plane, not the worker.
This is the inverse of typical agent design and is the pack's explicit
position: the control plane owns state (ADR-0002).

## Evidence requirements

Every execution produces the following evidence, persisted by the worker and
retrievable via `GET /v1/works/{id}/evidence` (slice 3):

- Attempt record (id, node_id, worker_id, started_at, finished_at, exit_code,
  status).
- Artifact file (sha256 content-addressed, size, MIME).
- Evidence record (build/test type, pass/fail result, signer=worker_id,
  environment string).
- Lease record (id, granted_at, expires_at, last_beat_at, terminal status).

Slice 3 will wrap these in a signed evidence bundle per the Evidence-First
CI Standard (#113) and Workflow Provenance Standard (#122).

## Evaluation criteria

- **Functional:** can the worker execute a single-node work end-to-end and
  reach `SUCCEEDED`? Validated by `go test -tags=e2e ./e2e/` (slice 1+2).
- **Reliability:** does the lease-reaper detect a kill -9 of the worker
  within ≤30s? Validated by `go test -tags=e2e_chaos ./e2e/` (slice 2).
- **Safety:** does the worker respect capability manifests, kill the
  subprocess on lease loss, and avoid filesystem escape? Validated by
  slice 3 hermetic + capability tests.
- **Throughput:** does the worker process polls without leaking goroutines
  or descriptors? Validated by a soak test (slice 4).

## Termination conditions

The worker terminates on any of:

- SIGTERM / SIGINT (graceful; releases leases, kills subprocesses).
- Unrecoverable API error (configurable threshold; default 10 consecutive
  failures → exit).
- Process crash (caught by the reaper; lease is revoked).

There is no automatic restart. Restart policy is the operator's call.

---

## Standards mapping

This declaration satisfies or contributes to:

- `iso-iec-42001-2023` (AIMS): policy-level registration of an AI capability.
- `iso-iec-23894-2023` (Risk): risk classification above.
- `nist-ai-rmf-1.0` (Govern/Map/Measure/Manage): all four functions.
- `mitre-atlas` (Threat): worker is non-LLM; threat model is subprocess abuse,
  not prompt injection. See `docs/standards/mappings/ai.md` for the per-tactic
  analysis.
- `platform-policy-enforced-action` (#125): every action runs after policy
  check (the lease grant IS the policy check).
- `platform-action-attestation` (#123): per-attempt attestation emitted on
  `/complete`.
- `platform-execution-evidence` (#124): evidence fields above.

## Change log

- 2026-08-31 — Initial declaration. Slice 2 worker.