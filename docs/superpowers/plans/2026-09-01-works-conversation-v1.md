# WORKS Conversation V1 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship a fully working authenticated WORKS Conversation V1 where a human chats with Friday/workers, creates real durable WORK, watches execution live, sees artifacts/evidence, handles `WAITING_HUMAN`, cancels safely, and restores the same state after refresh/reconnect.

**Architecture:** `JonasAbde/works-execution` remains the canonical Work/Mission/Execution authority. `JonasAbde/autonomous-venture-company` owns identity, conversation persistence, worker presentation, human approval receipts, and the rich Platform Web experience. Platform Web talks only to same-origin AVC server functions; those functions resolve the trusted AVC session and relay to an AVC conversation worker, which calls WORKS and Agent Gateway using server-held credentials. No browser code receives WORKS service credentials, and no conversation/UI state becomes execution authority.

**Tech Stack:** Go 1.23+, SQLite, `net/http`, SSE, CloudEvents-style event envelopes, TypeScript 6, Zod contracts, Cloudflare Worker + D1, Cloudflare Pages Functions, React 19, Vite 6, Vitest 4.

**Spec:** `docs/superpowers/specs/2026-09-01-works-conversation-v1-design.md`

## Global Constraints

- `works-execution` Work/Mission/lease/evidence state is canonical execution truth; workers remain disposable.
- Existing WORKS CLI/API behavior must keep working without AI.
- AVC session/identity and Kernel approval receipt authority remain canonical for privileged human decisions.
- `@worker` is a preference only; it never grants capability or bypasses scheduler eligibility.
- User messages are persisted before agent/work progress is emitted.
- Browser-local queued text is visibly non-durable until accepted by the server.
- SSE is resumable with monotonic sequence IDs; reset requires canonical snapshot refetch.
- No arbitrary model-generated HTML/React. Only trusted typed renderers are allowed in V1.
- No long-lived WORKS bearer/service secret in browser storage, JavaScript bundles, query strings, or logs.
- Missing identity, scope mapping, approval binding, or bridge configuration fails closed.
- WORKS gate: `go vet ./...`, `go build ./...`, `go test ./...` including e2e.
- AVC Platform gate: `pnpm --filter platform-web typecheck`, `pnpm --filter platform-web test`, `pnpm --filter platform-web build`, then exact-head `avc/ci-local` per repository rules.

---

## File Structure

### `JonasAbde/works-execution`

- `services/work/store/events.go` — durable per-Work event journal and sequence cursor.
- `services/work/store/events_test.go` — ordering, filtering, idempotency, restart tests.
- `services/work/store/store.go` — schema v9, Store interface additions, mutation event emission.
- `services/api/work_events.go` — authenticated machine SSE endpoint.
- `services/api/work_events_test.go` — snapshot, resume cursor, reset, isolation tests.
- `services/api/work_resume.go` — platform-bridge-bound resume endpoint for `WAITING_HUMAN`/`SUSPENDED`.
- `services/api/work_resume_test.go` — checkpoint/receipt binding and idempotency tests.
- `services/api/api.go` — route registration and `/events`/`resume` dispatch.
- `cmd/works-api/main.go` — `WORKS_PLATFORM_BRIDGE_SECRET` wiring.

### `JonasAbde/autonomous-venture-company`

- `packages/contracts/src/works-conversation.ts` — typed conversation projection, Work binding, WORKS event, work proposal, approval resume contracts.
- `packages/contracts/src/works-conversation.test.ts` — contract/adversarial tests.
- `packages/contracts/src/index.ts` — export the new contracts.
- `apps/conversation-worker/package.json` — worker package.
- `apps/conversation-worker/wrangler.toml` — D1 + Agent Gateway configuration.
- `apps/conversation-worker/migrations/0001_conversation_v1.sql` — conversation/message/event/work-binding persistence.
- `apps/conversation-worker/src/store.ts` — D1 persistence port.
- `apps/conversation-worker/src/works-client.ts` — server-held WORKS client.
- `apps/conversation-worker/src/agent-client.ts` — Agent Gateway A2A advisory client.
- `apps/conversation-worker/src/worker.ts` — HTTP routes, orchestration, SSE and approval resume relay.
- `apps/conversation-worker/src/*.test.ts` — persistence, auth boundary, work binding, reconnect tests.
- `apps/platform-web/functions/api/conversations/bridge.js` — trusted session → conversation-worker bridge.
- `apps/platform-web/functions/api/conversations/index.js` — list/create/send entrypoint.
- `apps/platform-web/functions/api/conversations/[conversationId].js` — conversation snapshot.
- `apps/platform-web/functions/api/conversations/[conversationId]/events.js` — proxied SSE.
- `apps/platform-web/functions/api/conversations/[conversationId]/stop.js` — stop/cancel request.
- `apps/platform-web/functions/api/conversations/[conversationId]/resume.js` — human approval resume request.
- `apps/platform-web/src/integrations/works-conversation-api.ts` — validated same-origin client.
- `apps/platform-web/src/integrations/works-conversation-api.test.ts` — malformed/upstream/error tests.
- `apps/platform-web/src/features/conversation/model/types.ts` — UI-only derived state.
- `apps/platform-web/src/features/conversation/model/workforce-reference.ts` — shared stable worker references.
- `apps/platform-web/src/features/conversation/hooks/useConversationStream.ts` — reconnect/dedupe/reset.
- `apps/platform-web/src/features/conversation/components/ConversationTimeline.tsx` — mixed timeline renderer.
- `apps/platform-web/src/features/conversation/components/Composer.tsx` — text, `@worker`, queue, stop.
- `apps/platform-web/src/features/conversation/components/cards/*.tsx` — Work/Activity/Artifact/Evidence/Approval/Handoff/System cards.
- `apps/platform-web/src/pages/ConversationWorkspacePage.tsx` — route composition only.
- `apps/platform-web/src/pages/ConversationWorkspacePage.test.tsx` — vertical UI behavior.
- `apps/platform-web/src/app/product-shell.ts` — add active Conversation/Friday surface.
- `apps/platform-web/src/app/App.tsx` — route surface to page.
- `apps/platform-web/src/styles/conversation.css` — adaptive desktop/mobile layout, reduced-motion rules.
- `tests/works-conversation-v1.e2e.test.ts` — cross-repo contract acceptance fixture.

---

### Task 1: Add a durable WORKS event journal

**Files:**
- Create: `services/work/store/events.go`
- Create: `services/work/store/events_test.go`
- Modify: `services/work/store/store.go`

**Interfaces:**
- Produces:
  ```go
  type WorkEvent struct {
      Sequence   int64           `json:"sequence"`
      ID         string          `json:"id"`
      WorkID     string          `json:"work_id"`
      Type       string          `json:"type"`
      ObservedAt time.Time       `json:"observed_at"`
      Data       json.RawMessage `json:"data"`
  }

  AppendWorkEvent(ctx context.Context, event WorkEvent) (WorkEvent, error)
  ListWorkEventsAfter(ctx context.Context, workID string, after int64, limit int) ([]WorkEvent, error)
  OldestWorkEventSequence(ctx context.Context, workID string) (int64, error)
  LatestWorkEventSequence(ctx context.Context, workID string) (int64, error)
  ```
- Later tasks consume the event journal for SSE. It is a projection/event history, not mutation authority.

- [ ] **Step 1: Write failing store tests**

```go
func TestWorkEventsAreMonotonicAndWorkScoped(t *testing.T) {
    s := openTestStore(t)
    ctx := context.Background()
    first, err := s.AppendWorkEvent(ctx, store.WorkEvent{ID: "evt_a", WorkID: "work:a", Type: "work.created", Data: json.RawMessage(`{"state":"CREATED"}`)})
    if err != nil { t.Fatal(err) }
    second, err := s.AppendWorkEvent(ctx, store.WorkEvent{ID: "evt_b", WorkID: "work:a", Type: "work.state.changed", Data: json.RawMessage(`{"state":"QUEUED"}`)})
    if err != nil { t.Fatal(err) }
    if second.Sequence <= first.Sequence { t.Fatalf("sequence did not increase") }
    rows, err := s.ListWorkEventsAfter(ctx, "work:a", first.Sequence, 100)
    if err != nil { t.Fatal(err) }
    if len(rows) != 1 || rows[0].ID != "evt_b" { t.Fatalf("unexpected rows: %#v", rows) }
}

func TestWorkEventIdempotencyByID(t *testing.T) {
    s := openTestStore(t)
    ctx := context.Background()
    input := store.WorkEvent{ID: "evt_same", WorkID: "work:a", Type: "evidence.recorded", Data: json.RawMessage(`{"id":"e1"}`)}
    a, err := s.AppendWorkEvent(ctx, input)
    if err != nil { t.Fatal(err) }
    b, err := s.AppendWorkEvent(ctx, input)
    if err != nil { t.Fatal(err) }
    if a.Sequence != b.Sequence { t.Fatalf("duplicate event allocated new sequence") }
}
```

- [ ] **Step 2: Run the targeted test and verify RED**

Run: `go test ./services/work/store -run 'TestWorkEvent' -count=1`
Expected: compile failure because `WorkEvent`/journal methods do not exist.

- [ ] **Step 3: Add schema v9 and journal implementation**

Add to the SQLite schema:

```sql
CREATE TABLE IF NOT EXISTS work_events (
    sequence     INTEGER PRIMARY KEY AUTOINCREMENT,
    id           TEXT NOT NULL UNIQUE,
    work_id      TEXT NOT NULL REFERENCES works(id) ON DELETE CASCADE,
    type         TEXT NOT NULL,
    observed_at  TEXT NOT NULL,
    data_json    TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_work_events_work_sequence
ON work_events(work_id, sequence);
```

Bump `SchemaVersion` from `8` to `9`. `AppendWorkEvent` must `INSERT OR IGNORE`, then read the existing row by ID so retries return the original sequence. Clamp list limits to `1..1000`.

- [ ] **Step 4: Emit journal records from canonical mutations**

At minimum emit:

```text
CreateWork        -> work.created
UpdateState       -> work.state.changed
AppendAttempt     -> activity.attempt.changed
AppendEvidence    -> evidence.recorded
AppendArtifact    -> artifact.created
GrantLease        -> worker.lease.granted
RenewLease        -> worker.lease.renewed
Release/Revoke    -> worker.lease.released / worker.lease.revoked
CompleteLease     -> worker.lease.completed
SuspendWork       -> work.waiting_human or work.suspended
ResumeCheckpoint  -> work.resumed
```

Emit only **after** the canonical transaction succeeds. Event emission failure must be surfaced for journal-owned mutation wrappers used by V1; do not claim resumable events while silently dropping them. Existing CloudEvents audit remains separate.

- [ ] **Step 5: Verify GREEN and restart persistence**

Run: `go test ./services/work/store -count=1`
Expected: PASS including reopen-the-database event cursor test.

- [ ] **Step 6: Commit**

```bash
git add services/work/store/store.go services/work/store/events.go services/work/store/events_test.go
git commit -m "feat(events): add durable per-work event journal"
```

---

### Task 2: Expose resumable WORKS SSE and bound resume

**Files:**
- Create: `services/api/work_events.go`
- Create: `services/api/work_events_test.go`
- Create: `services/api/work_resume.go`
- Create: `services/api/work_resume_test.go`
- Modify: `services/api/api.go`
- Modify: `cmd/works-api/main.go`

**Interfaces:**
- Produces:
  - `GET /v1/works/{id}/events`
  - `POST /v1/works/{id}/resume`
- Resume body:
  ```json
  {
    "approval_receipt_id": "...",
    "principal_id": "...",
    "tenant_id": "...",
    "checkpoint_hash": "...",
    "idempotency_key": "..."
  }
  ```
- Platform bridge header: `X-Works-Platform-Bridge: <secret>`; secret minimum 32 bytes.

- [ ] **Step 1: Write RED SSE tests**

Test that:

```text
GET /v1/works/work:a/events
Accept: text/event-stream
Last-Event-ID: 12
```

returns only sequence `>12`, writes `id: <sequence>`, `event: <type>`, JSON `data`, and sends `X-Accel-Buffering: no`. A cursor older than the retained oldest event returns:

```text
event: reset
data: {"reason":"cursor_expired","work_id":"work:a"}
```

A request for another/nonexistent work returns 404 rather than leaking event existence.

- [ ] **Step 2: Implement SSE using journal polling, not HTML UI diff state**

The handler must:

```go
for {
    rows, err := s.Store.ListWorkEventsAfter(ctx, workID, cursor, 200)
    // emit rows in sequence order
    // when empty, emit a comment heartbeat and wait 750ms
}
```

Do not reuse `/v1/ui/events`; that feed is HTML-view implementation detail and has no durable cursor.

- [ ] **Step 3: Add bridge-secret configuration**

`cmd/works-api/main.go` reads `WORKS_PLATFORM_BRIDGE_SECRET`. Empty means `/resume` is unavailable with 503. Never log the secret.

- [ ] **Step 4: Write RED resume tests**

Cases:

```text
missing bridge secret/header        -> 503/401
non-WAITING_HUMAN state             -> 409
missing approval receipt id         -> 400
wrong checkpoint hash               -> 409
same idempotency key + same payload -> same successful result
same key + changed payload           -> 409
valid bound request                  -> RUNNING
```

- [ ] **Step 5: Add checkpoint record access**

Extend store with a read-only record:

```go
type HandoffRecord struct {
    ID          string
    WorkID      string
    ToState     workgraph.State
    PayloadHash string
    Handoff     workgraph.Handoff
    CreatedAt   time.Time
}
```

`LatestHandoffRecord(ctx, workID)` verifies the persisted payload hash exactly as `LatestHandoff` does. Resume compares request `checkpoint_hash` to the current record before calling `ResumeFromCheckpoint`.

- [ ] **Step 6: Implement idempotent resume receipt binding**

Add a small `work_resume_receipts` table keyed by `idempotency_key`, storing `work_id`, payload hash, approval receipt id, principal, tenant and resulting state. A replay of identical input returns the stored result; changed input under the same key returns conflict.

- [ ] **Step 7: Run WORKS gates**

```bash
go vet ./...
go build ./...
go test ./...
```

Expected: all green.

- [ ] **Step 8: Commit**

```bash
git add services/api services/work/store cmd/works-api/main.go
git commit -m "feat(api): add resumable work events and governed resume"
```

---

### Task 3: Define AVC WORKS Conversation wire contracts

**Files:**
- Create: `packages/contracts/src/works-conversation.ts`
- Create: `packages/contracts/src/works-conversation.test.ts`
- Modify: `packages/contracts/src/index.ts`

**Interfaces:**
- Produces canonical wire schemas used by Conversation Worker and Platform Web.

- [ ] **Step 1: Write contract RED tests**

The schemas must reject unknown keys and cross-scope ambiguity. Define:

```ts
export const worksActorSchema = z.discriminatedUnion("kind", [
  z.object({ kind: z.literal("human"), principalId: uuidSchema }).strict(),
  z.object({ kind: z.literal("friday") }).strict(),
  z.object({ kind: z.literal("worker"), workerId: nonEmptyStringSchema }).strict(),
  z.object({ kind: z.literal("system") }).strict(),
]);

export const worksConversationMessageSchema = z.object({
  id: uuidSchema,
  conversationId: uuidSchema,
  tenantId: uuidSchema,
  actor: worksActorSchema,
  text: z.string().trim().min(1).max(16_000),
  createdAt: isoDateTimeSchema,
  workId: nonEmptyStringSchema.optional(),
}).strict();

export const worksWorkBindingSchema = z.object({
  conversationId: uuidSchema,
  messageId: uuidSchema,
  tenantId: uuidSchema,
  workId: nonEmptyStringSchema,
  createdAt: isoDateTimeSchema,
}).strict();

export const worksEventEnvelopeSchema = z.object({
  id: nonEmptyStringSchema,
  workId: nonEmptyStringSchema,
  type: nonEmptyStringSchema,
  observedAt: isoDateTimeSchema,
  sequence: z.number().int().nonnegative(),
  data: z.record(z.string(), z.unknown()),
}).strict();
```

Also define `workProposalSchema`, `conversationSnapshotSchema`, `conversationEventSchema`, `worksResumeRequestSchema`, `worksResumeReceiptSchema`.

- [ ] **Step 2: Run contract test and verify RED**

Run: `pnpm --filter @avc/contracts test -- works-conversation`
Expected: missing module/schema failure.

- [ ] **Step 3: Implement schemas and export them**

`workProposalSchema` must include only bounded inputs needed to build an existing WORKS Work:

```ts
{
  objective: string,
  requestedWorkerId?: string,
  repository?: string,
  revision?: string,
  maxCostUsd?: number,
  verification: "deterministic" | "human_review",
  idempotencyKey: string
}
```

No capability list or production-access boolean may come from the model proposal.

- [ ] **Step 4: Verify contracts**

Run contract tests plus package typecheck/build according to existing workspace scripts.

- [ ] **Step 5: Commit in AVC repo**

```bash
git add packages/contracts/src/works-conversation.ts packages/contracts/src/works-conversation.test.ts packages/contracts/src/index.ts
git commit -m "feat(contracts): add WORKS conversation V1 wire schemas"
```

---

### Task 4: Build the durable AVC Conversation Worker

**Files:**
- Create the `apps/conversation-worker/**` files listed in File Structure.

**Interfaces:**
- Internal routes, callable only by Platform Web bridge/service binding:
  ```text
  POST /internal/platform/conversations
  GET  /internal/platform/conversations
  GET  /internal/platform/conversations/:id
  POST /internal/platform/conversations/:id/messages
  GET  /internal/platform/conversations/:id/events
  POST /internal/platform/conversations/:id/stop
  POST /internal/platform/conversations/:id/resume
  ```
- Every request carries trusted server-generated context:
  ```ts
  {
    actorId: string,
    scope: { organizationId: string; workspaceId: string; tenantId: string; productInstanceId: string },
    capabilities: readonly string[],
    requestId: string
  }
  ```

- [ ] **Step 1: Write D1 migration and RED persistence tests**

Create tables:

```sql
CREATE TABLE conversations (
  id TEXT PRIMARY KEY,
  tenant_id TEXT NOT NULL,
  owner_principal_id TEXT NOT NULL,
  title TEXT NOT NULL,
  status TEXT NOT NULL CHECK(status IN ('active','archived')),
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE conversation_messages (
  id TEXT PRIMARY KEY,
  conversation_id TEXT NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
  tenant_id TEXT NOT NULL,
  actor_kind TEXT NOT NULL,
  actor_id TEXT,
  text TEXT NOT NULL,
  work_id TEXT,
  created_at TEXT NOT NULL
);

CREATE TABLE conversation_events (
  sequence INTEGER PRIMARY KEY AUTOINCREMENT,
  id TEXT NOT NULL UNIQUE,
  conversation_id TEXT NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
  tenant_id TEXT NOT NULL,
  type TEXT NOT NULL,
  observed_at TEXT NOT NULL,
  data_json TEXT NOT NULL
);

CREATE TABLE conversation_work_bindings (
  conversation_id TEXT NOT NULL,
  message_id TEXT NOT NULL,
  tenant_id TEXT NOT NULL,
  work_id TEXT NOT NULL,
  created_at TEXT NOT NULL,
  PRIMARY KEY (conversation_id, work_id)
);
```

All reads bind `conversation_id + tenant_id`; guessed IDs from another tenant return the same not-found response as absent IDs.

- [ ] **Step 2: Implement store methods and verify message-before-progress law**

`appendHumanMessage()` must commit the message row before `runTurn()` calls Agent Gateway or WORKS. Test by injecting a failing agent client and then reading the conversation: the human message must still exist.

- [ ] **Step 3: Implement Agent Gateway client**

Call the existing A2A endpoint with `A2A-Version` and `decision.support` for ordinary Friday replies. Use only server-held identity/service credentials. Parse the returned task result into text; on gateway failure append a `system.degraded` conversation event rather than deleting the human message.

- [ ] **Step 4: Implement bounded work-intent path**

For V1, work creation is explicit and deterministic from the composer using `/work` or the structured `workProposal` payload. Natural-language Friday discussion remains advisory; it may suggest a work proposal, but the server creates WORK only from a payload that passes `workProposalSchema`.

Map proposal to existing `workgraph.Work` fields server-side. `requestedWorkerId` becomes a scheduling hint/constraint label only when it maps to an allowed pool/worker preference; it never sets authority.

- [ ] **Step 5: Implement WORKS client**

Required calls:

```text
POST /v1/works
GET  /v1/works/:id
GET  /v1/works/:id/events
POST /v1/works/:id/cancel
POST /v1/works/:id/resume
GET  /v1/works/:id/evidence
GET  /v1/works/:id/nodes/:node/logs
```

All requests use server-held `WORKS_API_TOKEN`; resume additionally uses `WORKS_PLATFORM_BRIDGE_SECRET`.

- [ ] **Step 6: Mirror WORKS events into conversation event references**

Do not copy the whole WORKS database. Persist only the last forwarded sequence plus typed event/reference data needed to rebuild the mixed timeline. On reconnect, fetch the canonical WORKS snapshot if the upstream emits `reset`.

- [ ] **Step 7: Add conversation SSE**

SSE frame format:

```text
id: 42
event: conversation.event
data: {"id":"...","conversationId":"...","sequence":42,"type":"work.state.changed","data":{...}}
```

`Last-Event-ID` resumes after that sequence. First connection returns a snapshot event before live events.

- [ ] **Step 8: Run worker tests and commit**

Use package-local Vitest/Miniflare-style tests already used by other Cloudflare apps in AVC. Verify tenant isolation, D1 restart persistence, agent failure degradation, real work binding and SSE dedupe.

```bash
git add apps/conversation-worker
git commit -m "feat(conversation): add durable WORKS conversation worker"
```

---

### Task 5: Add trusted Platform Web conversation bridge

**Files:**
- Create: `apps/platform-web/functions/api/conversations/bridge.js`
- Create: remaining `apps/platform-web/functions/api/conversations/**` route files.
- Create tests beside the Pages Functions test harness used for mission/serviceops routes.

**Interfaces:**
- Browser only talks to `/api/conversations...` with same-origin credentials.
- Bridge calls `context.env.AVC_CONVERSATION.fetch(...)`.

- [ ] **Step 1: Write RED authorization tests**

Assert:

```text
no trusted session                  -> session boundary response
missing missions:read               -> 403 for read
missing missions:prepare            -> 403 for /work send/stop
cross-tenant conversation id        -> upstream 404 preserved
missing conversation service binding -> 503
```

- [ ] **Step 2: Implement bridge by following `functions/api/missions/bridge.js` pattern**

```js
const bridgeContext = (session, requestId) => ({
  actorId: session.principalId,
  scope: {
    organizationId: session.organizationId,
    workspaceId: session.workspaceId,
    tenantId: session.tenantId,
    productInstanceId: session.productInstanceId,
  },
  capabilities: session.capabilities,
  requestId,
});
```

Never forward the incoming `Cf-Access-Jwt-Assertion` to WORKS. The Conversation Worker receives only resolved scoped identity from this bridge.

- [ ] **Step 3: Proxy SSE without buffering**

The events function preserves `Content-Type: text/event-stream`, `Cache-Control: no-cache`, and `X-Accel-Buffering: no`; it forwards `Last-Event-ID` but strips unrelated client authorization headers.

- [ ] **Step 4: Verify Pages Functions tests and commit**

```bash
git add apps/platform-web/functions/api/conversations
git commit -m "feat(platform): bridge trusted sessions to WORKS conversations"
```

---

### Task 6: Build the Platform Web living conversation UI

**Files:**
- Create all `apps/platform-web/src/features/conversation/**` files.
- Create: `apps/platform-web/src/pages/ConversationWorkspacePage.tsx`
- Create: `apps/platform-web/src/pages/ConversationWorkspacePage.test.tsx`
- Modify: `apps/platform-web/src/app/product-shell.ts`
- Modify: `apps/platform-web/src/app/App.tsx`
- Modify/refactor: `apps/platform-web/src/pages/WorkforcePage.tsx` to import shared workforce reference.
- Create: `apps/platform-web/src/styles/conversation.css`

**Interfaces:**
- `loadConversation(id)`
- `createConversation()`
- `sendConversationMessage(id, draft)`
- `stopConversation(id)`
- `resumeConversation(id, approval)`
- `subscribeConversation(id, { lastEventId, onEvent, onReset })`

- [ ] **Step 1: Write API client RED tests**

Follow `mission-api.ts` conventions: typed error code, retryability, bounded request ID, runtime payload validation. Reject malformed success payloads as `UNAVAILABLE`, never cast blindly.

- [ ] **Step 2: Implement the route and product-shell entry**

Add active surface:

```ts
{
  id: "conversation",
  label: "Friday",
  href: "/conversation",
  purpose: "Converse with Friday/workers and operate governed WORK in one living timeline.",
  group: "operator",
  availability: "active",
  authorityOwner: "Conversation Worker projections + WORKS execution authority",
  requiredCapability: "missions:read",
}
```

Update the `ProductShellSurface["id"]` union and App switch.

- [ ] **Step 3: Extract shared worker reference**

Move the stable IDs/names/boundary copy from `WorkforcePage.tsx` into `features/conversation/model/workforce-reference.ts`. Both Workforce and Composer import it. The file explicitly labels entries as presentation/reference identities, not availability truth.

- [ ] **Step 4: Build mixed timeline card tests first**

Test renderers for:

```text
message
work
activity
artifact
evidence
approval
handoff
system
```

Cards must accept validated typed props only. No `dangerouslySetInnerHTML`, eval, remote component code or model-selected module path.

- [ ] **Step 5: Build Composer queue/stop behavior**

State model:

```ts
type ComposerState =
  | { mode: "idle" }
  | { mode: "sending" }
  | { mode: "working"; cancellable: boolean }
  | { mode: "degraded"; reason: string };
```

While `working`, a new draft is added to a visibly labeled in-memory queue. `beforeunload` is registered only when unsent queued drafts exist. Stop changes to `Cancellation requested` until canonical WORKS event confirms cancellation.

- [ ] **Step 6: Implement `@worker` picker and `/work` proposal composer**

Selecting `@Forge`/`@Atlas` etc. stores only `requestedWorkerId` in the outgoing draft. The UI must include helper text: scheduling/authority is resolved server-side. `/work` opens bounded fields for objective, verification mode, optional budget and worker preference; it does not expose raw capability or production-access toggles.

- [ ] **Step 7: Implement resumable stream hook**

`useConversationStream` keeps the highest applied sequence in a ref, ignores `<= lastSequence`, reconnects with `Last-Event-ID`, and on reset calls `loadConversation()` before accepting new live events.

- [ ] **Step 8: Adaptive layout**

Desktop: roster/context + timeline + context/activity panel. Mobile: timeline first, composer fixed/reachable, context/activity shown as accessible sheets/details. Respect `prefers-reduced-motion`; all status changes have textual equivalents.

- [ ] **Step 9: Run Platform gates**

```bash
pnpm --filter platform-web typecheck
pnpm --filter platform-web test
pnpm --filter platform-web build
```

Expected: green.

- [ ] **Step 10: Commit**

```bash
git add apps/platform-web/src apps/platform-web/package.json
git commit -m "feat(platform): add WORKS living conversation workspace"
```

---

### Task 7: Wire human approval receipts to `WAITING_HUMAN`

**Files:**
- Modify: `apps/conversation-worker/src/worker.ts`
- Modify: `apps/conversation-worker/src/works-client.ts`
- Add approval integration tests.
- Reuse existing AVC Kernel approval-receipt authority; do not mint a new receipt format.

**Interfaces:**
- Consumes existing `avc.approval-receipt.v1` receipt token/record from Kernel authority.
- Produces a bridge-bound WORKS resume request containing only receipt ID plus current checkpoint binding after the AVC side verifies/consumes the receipt.

- [ ] **Step 1: Write RED stale/invalid approval tests**

Cases:

```text
agent-generated fake receipt        -> refused
expired receipt                     -> refused
receipt for different tenant        -> refused
receipt for different work/checkpoint -> refused
already consumed by same operation  -> idempotent success
already consumed by other operation -> conflict
valid receipt                       -> WORKS resume called once
```

- [ ] **Step 2: Map WORKS waiting-human event into AVC approval scope**

The scope must bind at least:

```text
action = works.resume
principalId
tenantId
workId represented in mission/task binding
current checkpoint hash/revision
operationId
idempotencyKey
```

Use the existing Kernel receipt service/verification path. Do not parse a token and trust its payload without signature + durable consumption validation.

- [ ] **Step 3: Resume WORKS only after successful receipt consumption**

Then send the bound request to `/v1/works/{id}/resume`. Append conversation events for `approval.accepted`, `work.resume.requested`, and canonical `work.resumed` when WORKS confirms it.

- [ ] **Step 4: Add ApprovalCard behavior**

The card shows reason, requested action, current budget, evidence refs and checkpoint state. A stale response forces a snapshot refetch; it never blindly retries.

- [ ] **Step 5: Commit**

```bash
git add apps/conversation-worker apps/platform-web/src/features/conversation
git commit -m "feat(approval): resume WORKS from bound AVC approval receipts"
```

---

### Task 8: Cross-repo end-to-end acceptance and production readiness

**Files:**
- Create/modify: `tests/works-conversation-v1.e2e.test.ts` in AVC.
- Add local/dev stack fixture scripts in the narrowest existing test infrastructure location.
- Update relevant runbooks with exact environment bindings; no architecture rewrite.

**Interfaces:**
- Tests the product boundary, not mocks alone.

- [ ] **Step 1: Build the happy-path acceptance fixture**

The test must prove:

```text
trusted AVC session
→ create conversation
→ durable human message
→ submit bounded /work proposal with @worker preference
→ real POST /v1/works creates Work
→ worker leases and runs node
→ conversation stream receives running state
→ artifact + evidence appear
→ work enters WAITING_HUMAN with persisted checkpoint
→ AVC approval receipt is issued/consumed
→ WORKS resumes from exact checkpoint
→ verification succeeds
→ completion/quittance/evidence visible
→ close/reopen client projection
→ same conversation + same Work state restored
```

Use a deterministic local worker command such as:

```sh
printf 'v1 artifact\n' > "$WORKS_ARTIFACT_DIR/result.txt"
```

plus a deterministic verification node. AI/Agent Gateway may be stubbed only for the Friday prose portion of this acceptance; Work creation/execution, persistence, event streaming, approval and evidence must be real.

- [ ] **Step 2: Add cancellation acceptance**

Start a long-running node, request Stop, assert UI receives `cancel requested`, revoke/cancel canonical Work, then assert terminal `CANCELLED`. The test fails if the client marks it cancelled before WORKS does.

- [ ] **Step 3: Add reconnect acceptance**

Disconnect after event N, create additional canonical events, reconnect with `Last-Event-ID=N`, verify no duplicate timeline IDs. Also force a cursor-expired reset and verify snapshot replacement.

- [ ] **Step 4: Security acceptance**

Prove:

```text
cross-tenant conversation/work guesses -> not found
browser bundle contains no WORKS secret
missing service binding -> explicit degraded state
@worker preference cannot widen node permissions
invalid approval cannot resume Work
```

- [ ] **Step 5: Full verification**

WORKS:

```bash
go vet ./...
go build ./...
go test ./...
```

AVC:

```bash
pnpm --filter @avc/contracts test
pnpm --filter platform-web typecheck
pnpm --filter platform-web test
pnpm --filter platform-web build
pnpm test -- works-conversation-v1
```

Then run the repository-required exact-head `avc/ci-local` gate and preserve evidence for the exact integrated head.

- [ ] **Step 6: Production configuration verification**

Required server-side configuration names:

```text
WORKS_API_URL
WORKS_API_TOKEN
WORKS_PLATFORM_BRIDGE_SECRET
AVC_CONVERSATION service binding
AVC_AGENT_GATEWAY service binding
Conversation Worker D1 binding
Kernel approval receipt verification/consumption binding
```

Readiness fails closed when any required binding is missing. `/health/readiness` for the Conversation Worker reports only boolean/configuration readiness and version/provenance, never secret values.

- [ ] **Step 7: Commit acceptance evidence/docs**

```bash
git add tests docs/runbooks apps/conversation-worker apps/platform-web
git commit -m "test(conversation): prove WORKS Conversation V1 end to end"
```

---

## Implementation Order / Merge Discipline

1. Merge WORKS Task 1 before Task 2.
2. Merge AVC Task 3 before Task 4.
3. Task 4 may start against the published WORKS Task 2 contract only after Task 2 contract tests are green.
4. Task 5 depends on Task 4 internal routes.
5. Task 6 can develop against fixture responses after Task 3, but cannot be marked complete until Task 4/5 integration tests are real.
6. Task 7 requires both WORKS resume and AVC Kernel approval receipt integration.
7. Task 8 is the release gate. No “V1 complete” claim before the exact cross-repo flow is green.

## Self-Review

- **Spec coverage:** chat, persistence, `@worker`, work creation, live execution, artifacts, evidence, approvals, stop/cancel, handoffs/events, reconnect, degraded mode and optional provider live-view boundary are covered. Human takeover remains capability-gated by the approved spec and is not made a release blocker when the selected V1 worker provider lacks `live_view`.
- **Authority check:** browser never calls WORKS directly; conversation state does not grant authority; worker selection is advisory; approval uses existing Kernel receipt authority.
- **Placeholder scan:** no implementation step relies on `TBD`, `TODO`, “similar to”, or unspecified error handling.
- **Type consistency:** event cursor is integer sequence end-to-end; conversation IDs are UUIDs; WORKS IDs remain WORKS strings; tenant ID is carried/bound on every AVC conversation read/write.
