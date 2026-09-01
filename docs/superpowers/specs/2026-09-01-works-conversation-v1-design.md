# WORKS Conversation V1 — Hybrid Experience Design

**Status:** Approved direction, written design awaiting final spec review  
**Date:** 2026-09-01  
**Repositories:** `JonasAbde/works-execution` + `JonasAbde/autonomous-venture-company`  
**Track:** Fast for this docs-only design; implementation is Normal  
**Primary product goal:** Deliver a fully working V1 where a human can converse with Friday/workers, turn conversation into governed WORK, watch execution live, intervene, approve, inspect artifacts/evidence, and recover the same state after refresh or reconnect.

## 1. Decision

Use the approved **Hybrid C** architecture:

- `works-execution` remains the canonical execution control plane.
- AVC Platform Web becomes the first rich WORKS conversation/client shell.
- Conversation state and presentation never become execution authority.
- Existing AVC approval/identity authority remains authoritative for privileged human decisions.
- WORKS accepts only bounded requests and evidence/approval references that its control plane can validate.

The product must feel like one workspace, but the implementation must preserve source-of-truth boundaries.

```text
Human
  ↓
AVC Platform Web / WORKS Conversation
  ↓
Friday / Worker interaction runtime
  ↓
Conversation intent / @worker preference
  ↓
AVC server-side WORKS bridge
  ↓
works-execution API
  ↓
Work → Mission → Nodes → Leases → Worker
  ↓
Logs / artifacts / evidence / state / budget
  ↓
Typed event projection
  ↓
Conversation living timeline
```

## 2. Why this architecture

`works-execution` already owns the primitives that should survive workers and UI sessions: durable Work, mission contracts, state transitions, leases, checkpoint handoff persistence, evidence, budget semantics, capability policy, audit events, logs, runners, and a live SSE-backed execution view. The repo contract explicitly requires that workers remain disposable and the control plane owns state.

AVC already has the richer human product surface and canonical identity/governance concepts: Platform Web, Missions, Approvals, Evidence, Workforce, protected session boundaries, and Ask AVC architecture. Rebuilding all of that inside the Go execution repo would create a second product shell and eventually duplicate authority.

V1 therefore composes the two systems rather than merging them.

## 3. Source-of-truth matrix

| Concern | Canonical owner | Client responsibility |
|---|---|---|
| Conversation + messages | AVC conversation/agent owner | Render and compose |
| Principal/session | AVC identity boundary | Display current principal and scope |
| Worker identity/availability | AVC Workforce projection | Mention/search workers; never invent runtime authority |
| Work state | `works-execution` | Render projections only |
| Mission/node/lease state | `works-execution` | Render timeline and status |
| Execution logs | `works-execution` | Tail/read only |
| Artifacts | `works-execution` / provider-owned artifact refs | Render/download through governed route |
| Evidence / quittance | `works-execution` evidence layer | Render provenance and verification |
| Human approvals | AVC canonical approval owner | Submit explicit human decision |
| Capability/policy | WORKS + AVC authority adapter according to boundary | Never grant from UI state |
| Budget | `works-execution` mission/budget ledger | Render ceiling/consumption; request human action when paused |
| UI busy/queued state | Browser-local projection only | Never treated as durable work state |

## 4. V1 user experience

The main surface is a **living conversation workspace**, not a chat page with a dashboard beside it.

Desktop layout:

```text
┌──────────────────┬────────────────────────────────────┬───────────────────┐
│ WORKSPACE        │ CONVERSATION / LIVING TIMELINE     │ CONTEXT           │
│                  │                                    │                   │
│ Friday           │ Jonas                              │ Worker            │
│ Workers          │ Ship the release candidate.        │ Work              │
│ Conversations    │                                    │ Mission           │
│ Work             │ Friday                             │ Budget            │
│                  │ I assigned the bounded build work. │ Evidence          │
│                  │                                    │ Activity          │
│                  │ ⚙ Work · RUNNING · Forge           │                   │
│                  │ ▸ node 2/4 · tests                 │                   │
│                  │ ▸ live activity                    │                   │
│                  │                                    │                   │
│                  │ 📦 Artifact                         │                   │
│                  │ 🧾 Evidence                         │                   │
│                  │ 🔐 Approval required                │                   │
│                  │                                    │                   │
│                  │ [ message / @worker / commands ]   │                   │
└──────────────────┴────────────────────────────────────┴───────────────────┘
```

Mobile collapses to one primary timeline with sheets for Worker/Work/Evidence/Activity. The chat composer stays reachable; side context never becomes a tiny desktop panel squeezed into a phone.

### 4.1 Timeline item types

V1 supports these first-class timeline records:

1. `message` — human/Friday/worker prose.
2. `work` — Work/Mission status card.
3. `activity` — current node/tool/log activity.
4. `artifact` — file/build/report/output reference.
5. `evidence` — proof, verifier result, quittance, hashes/provenance.
6. `approval` — explicit human decision request and outcome.
7. `handoff` — worker-to-worker transfer and receiving worker status.
8. `system` — reconnect, degraded mode, cancellation, failure or recovery.

A timeline item may update in place as its canonical backing object changes. Historical execution truth remains in WORKS/AVC; the UI does not rewrite history.

## 5. Composer behavior

The composer supports:

- normal text;
- `@worker` mention/search;
- `/command` or skill shortcuts where an existing governed skill exists;
- attachments through AVC-owned intake;
- send while idle;
- queue while one turn is in progress;
- Stop when an active cancellable execution exists.

### Queue semantics

Queued user text is browser-local in V1 and visibly marked `Queued`. It is not silently durable. If the tab is closed before it is sent, the UI must warn that queued text is unsent rather than pretending it is preserved.

Once a message is accepted by the conversation owner, it becomes durable before any agent response is streamed.

### Stop semantics

`Stop` means:

- stop the current conversation turn if only generation is active;
- request cancellation of associated WORK when execution has begun and cancellation is permitted;
- never fabricate immediate cancellation if WORKS is still reconciling a lease/provider.

The timeline shows `Cancellation requested` until canonical work state confirms the transition.

## 6. From chat to WORK

A user message does not directly mutate WORKS from browser code.

Flow:

```text
message accepted
  ↓
Friday/worker interprets bounded intent
  ↓
structured WorkProposal
  ↓
server validates principal + scope + requested worker preference
  ↓
AVC WORKS bridge maps to canonical WORKS request
  ↓
POST /v1/works
  ↓
work_id returned
  ↓
conversation event binds message_id ↔ work_id
  ↓
UI begins canonical work projection
```

`@Forge` or another worker mention is a **preference/request**, not execution authority. The WORKS scheduler/control plane may accept, remap or reject it based on eligibility/capability/pool state.

V1 must preserve the existing rule that WORKS works without AI. The conversation layer is an optional producer of valid Work requests; CLI/API creation remains fully functional.

## 7. WORKS event contract for the rich client

The current server-rendered `/v1/ui/events` proves SSE viability but is an implementation-facing Web UI feed. V1 adds a stable authenticated machine contract rather than coupling Platform Web to HTML-fragment behavior.

Proposed endpoint:

```text
GET /v1/works/{work_id}/events
Accept: text/event-stream
Authorization: Bearer <service/user-bound token>
Last-Event-ID: <cursor>
```

Event envelope:

```json
{
  "id": "evt_01...",
  "work_id": "work:...",
  "type": "work.state.changed",
  "observed_at": "2026-09-01T21:00:00Z",
  "sequence": 42,
  "data": {}
}
```

V1 event types:

- `work.created`
- `work.state.changed`
- `mission.node.state.changed`
- `worker.assigned`
- `worker.lease.changed`
- `activity.log.appended`
- `artifact.created`
- `evidence.recorded`
- `verification.completed`
- `work.waiting_human`
- `work.cancel.requested`
- `work.completed`
- `work.failed`

SSE is resumable using `Last-Event-ID`/sequence. If the stream cannot resume from retained events, the server emits a reset signal and the client refetches the canonical work snapshot before continuing.

## 8. Conversation projection

AVC does not copy the full WORKS database into conversation storage.

Conversation persistence stores references such as:

```ts
type WorkBinding = {
  conversationId: string;
  messageId: string;
  workId: string;
  createdAt: string;
};
```

The visible timeline is a projection built from:

```text
Conversation messages/events
        +
WORKS work snapshot/events
        +
AVC approval/evidence projections
```

This permits refresh/reconnect without trusting stale client state.

## 9. Approvals and WAITING_HUMAN

WORKS already supports `WAITING_HUMAN`, checkpoint persistence and pause semantics. V1 uses that state instead of inventing a chat-specific `needsApproval` state.

Flow:

```text
WORKS reaches human boundary
  ↓
checkpoint persisted atomically
  ↓
Work → WAITING_HUMAN
  ↓
AVC receives/renders approval request
  ↓
Human reviews blast radius, reason, budget, evidence, rollback
  ↓
AVC canonical approval decision
  ↓
approval receipt/ref passed through server bridge
  ↓
WORKS validates bound resume request
  ↓
resume from checkpoint
```

The agent cannot approve its own consequential action. A browser button cannot widen capability. Approval must be bound to principal, work, requested action/scope and current revision/checkpoint.

## 10. Handoffs

A handoff is work movement, not merely forwarded prose.

V1 handoff record includes:

```text
from worker
requested target worker
work_id / node_id
objective
constraints
expected result
checkpoint/evidence refs
attempt/depth metadata
```

The receiving worker gets only the authority/capability already allowed by canonical scheduling and policy. A worker mention cannot transfer the sender's full authority by implication.

The conversation timeline renders who requested the handoff, who accepted it, and whether it failed or was remapped.

## 11. Live execution and computer/activity experience

V1 guarantees a **Live Work** view for every running Work:

- active worker;
- mission/node state;
- elapsed time;
- budget state;
- log/activity tail;
- latest artifacts;
- evidence count;
- stop/cancel availability.

Provider adapters may also expose a `live_view` capability (browser/desktop/terminal). If present, Platform Web renders the live surface in the context panel and can expose human takeover only through an explicit provider adapter contract.

If `live_view` is absent, the product remains fully functional using Activity/Logs. V1 therefore does not make a browser container a mandatory execution primitive.

Human takeover is supported in V1 only for providers that can truthfully expose:

```text
help_requested
control_available
take_control
release_control
```

While human control is active, automated provider actions must be refused/paused rather than racing the human.

## 12. Generative / Living UI

The conversation may render typed components, but V1 does **not** execute arbitrary model-generated React/HTML.

The model selects from a registry of trusted renderers:

```text
WorkCard
ActivityCard
ArtifactCard
EvidenceCard
ApprovalCard
HandoffCard
ErrorCard
```

Each renderer consumes validated typed data from canonical sources. This produces generative composition without turning model output into an executable frontend supply-chain problem.

Later versions may add sandboxed published components behind a separate admission contract.

## 13. AVC Platform implementation boundary

The first rich client is added to `apps/platform-web`.

Recommended feature units:

```text
src/features/conversation/
  api/
  components/
  hooks/
  model/
  pages/
```

Key responsibilities:

- `ConversationWorkspacePage` — route composition only.
- `ConversationTimeline` — ordered mixed timeline.
- `Composer` — text, mentions, commands, queue, stop.
- `WorkCard` — canonical work summary.
- `ActivityPanel` — log/live activity.
- `ApprovalCard` — reuse canonical approval service, not local approval state.
- `EvidenceCard` — reuse Evidence projection.
- `WorksBridgeClient` — browser calls AVC same-origin server functions only.
- `WorksEventStream` — resumable typed event consumption.

Existing Platform Web Missions, Approvals and Evidence surfaces remain valid deep-detail destinations; the conversation workspace composes them rather than replacing them.

## 14. Server-side WORKS bridge

The browser never receives a long-lived WORKS service bearer token.

AVC server-side bridge responsibilities:

1. resolve current AVC session/principal;
2. authorize access to the requested tenant/workspace;
3. map AVC scope to permitted WORKS tenant/project scope;
4. call WORKS API using server-held credentials or short-lived delegated credential;
5. propagate correlation IDs;
6. redact upstream secrets/errors;
7. stream only events the principal may observe;
8. reject cross-tenant work IDs even if guessed.

No direct `fetch("https://works...", {Authorization: serviceToken})` from React.

## 15. Resilience and degraded mode

The experience must remain truthful when either side is unavailable.

### Conversation runtime unavailable

- existing WORKS execution continues;
- current Work cards can still refresh if bridge access remains healthy;
- composer becomes unavailable with explicit reason.

### WORKS unavailable

- historical conversation remains readable;
- execution cards show `Execution status unavailable` rather than stale `Running` as truth;
- no new Work is claimed created until server acceptance returns.

### SSE interrupted

- reconnect with last sequence/event ID;
- refetch snapshot after retention/reset conflict;
- do not duplicate timeline items.

### Worker crash

- conversation shows canonical recovery/release/retry state from WORKS;
- worker failure does not delete Work or conversation binding.

### Approval conflict

- current canonical approval/work revision wins;
- stale approval UI must refetch rather than retry blindly.

## 16. Security rules

- Fail closed on missing identity/scope mapping.
- No client-side authority calculation.
- No agent-created approval receipt.
- No browser-held WORKS service secret.
- No cross-tenant event streaming.
- Evidence content follows existing classification/redaction rules.
- Attachments use AVC-owned intake; opaque references are passed to agents/WORKS.
- `@worker` changes preference/context, not capabilities.
- Cancellation, resume and approval actions are idempotent and revision-bound.

## 17. Testing strategy

### works-execution

- contract tests for event envelope and sequence ordering;
- SSE resume/reset tests;
- auth/tenant isolation tests;
- cancellation state tests;
- WAITING_HUMAN resume with valid/invalid approval reference;
- worker crash/recovery event continuity;
- existing `go vet ./...`, `go build ./...`, `go test ./...`, e2e remain green.

### AVC Platform Web

- Vitest component tests for every timeline card;
- composer queue/stop behavior;
- mention selection does not grant authority;
- reconnect deduplicates events;
- stale approval conflict handling;
- degraded-mode rendering;
- accessibility: keyboard navigation, focus semantics, live-region restraint, reduced motion;
- browser acceptance flow against a real local WORKS API.

### Cross-repo acceptance

One automated/dev-stack flow must prove:

```text
sign in
→ open conversation
→ send work request
→ durable message accepted
→ Work created in works-execution
→ worker executes
→ live timeline updates
→ artifact/evidence appears
→ Work enters WAITING_HUMAN
→ human approves in AVC
→ WORKS resumes from checkpoint
→ verification succeeds
→ completion/quittance visible
→ refresh browser
→ same conversation and canonical Work state restored
```

A second acceptance test stops/cancels a running Work and verifies the UI never claims cancellation before canonical state confirms it.

## 18. V1 Definition of Done

V1 is complete only when all are true:

1. Real authenticated conversation UI runs in AVC Platform Web.
2. Messages are durable before streamed reply/progress.
3. `@worker` selection is functional and remains non-authoritative.
4. A conversation can create a real WORKS Work through the server bridge.
5. Running Work updates appear live without manual refresh.
6. User can type/queue another message while work is active.
7. Stop/cancel uses canonical WORKS transition and truthful pending state.
8. Logs/activity are visible for running Work.
9. At least one real artifact is rendered from a completed execution.
10. Evidence/verifier/quittance data is visible from canonical records.
11. WAITING_HUMAN renders an AVC approval request.
12. Valid human approval resumes the same Work/checkpoint.
13. At least one worker-to-worker handoff is durable and visible.
14. Refresh/reconnect restores conversation + Work binding without duplicated events.
15. Degraded states are explicit when conversation or WORKS is unavailable.
16. Cross-tenant access tests fail closed.
17. Works execution continues to function via CLI/API without any AI/client dependency.
18. Both repositories pass their canonical test/build/type/lint/CI gates on exact heads.

## 19. Explicit non-goals for V1

- arbitrary model-generated executable React;
- mandatory per-worker browser VM/container;
- replacing AVC identity or approval authority;
- moving conversation storage into WORKS;
- making Platform Web a second Work database;
- autonomous payments or unrestricted irreversible external writes;
- generalized marketplace/plugin system;
- perfect multi-device queued-draft synchronization;
- replacing the existing read-only WORKS `/v1/ui` execution view.

## 20. OpenBot lessons adopted without cloning its architecture

V1 deliberately adopts these experience patterns:

- persistent coworker presence;
- honest turn-level busy state rather than model-run-only state;
- queue while worker is busy;
- stop/cancel visibility;
- live activity alongside conversation;
- human intervention/takeover when provider supports it;
- typed generative components;
- durable handoff outcomes and visible failures.

WORKS differs in one central principle: **the Work remains primary and the worker is disposable**. Conversation is the control/interaction layer around durable execution, not the execution truth itself.

## 21. Implementation decomposition

Implementation should be split into independently reviewable vertical slices:

1. **WORKS event projection** — stable authenticated per-Work SSE + snapshot/reconnect semantics.
2. **AVC bridge** — protected same-origin server adapter to WORKS.
3. **Conversation shell** — timeline + composer + durable conversation binding.
4. **Live Work** — state/activity/artifact/evidence cards.
5. **Control** — queue, stop/cancel, WAITING_HUMAN + approval resume.
6. **Handoff** — worker addressing and durable transfer display.
7. **Acceptance/degraded mode** — real dual-repo E2E, reconnection, tenant/security tests.

Each slice must leave a testable working product state; no giant frontend branch that becomes green only after every subsystem lands.
