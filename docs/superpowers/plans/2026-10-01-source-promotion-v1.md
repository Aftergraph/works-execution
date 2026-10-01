# Source Promotion v1 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a fail-closed WORKS source-promotion boundary that turns an immutable, evidence-backed, explicitly authorized workspace candidate into a deterministic GitHub pull-request proposal without granting merge or release authority.

**Architecture:** `packages/promotion` owns request validation, evidence/decision gates, provenance binding, restart-safe idempotency, and provider-neutral proposal state. A separate GitHub backend owns only canonical-source mechanics: candidate existence checks, deterministic staging refs, and PR create/reconcile. The service calls the backend only after authority gates pass; neither the workspace provider nor GitHub backend can merge, auto-merge, self-approve, update the canonical target branch, or release.

**Tech Stack:** Go 1.25 repo toolchain, existing `packages/workspace`, existing `services/evidence` verifier behind an adapter interface, existing Brain/governance authority semantics behind a verifier interface, GitHub REST via the connected Aftergraph GitHub App/connector, existing contract freeze generator + JSON Schema + CodeQL.

**Spec:** `docs/superpowers/specs/2026-10-01-source-promotion-v1-design.md`

## Global Constraints

- PR #200 / hardened `workspace/1.0` is a prerequisite for implementation merge; do not duplicate its wire/idempotency fixes in promotion.
- Workspace providers remain execution substrate only; do not add promotion/merge methods to `workspace.Provider`.
- Evidence verification is necessary but is not authority.
- Promotion decision authority must be explicitly verified; never accept caller booleans such as `Authorized` or `Verified`.
- GitHub source promotion may create/reconcile a deterministic staging branch and PR only.
- The promoter must never merge, enable auto-merge, self-approve, dismiss reviews, alter rulesets, or update the canonical target branch.
- Release/deployment remains `packages/release`; source-promotion code must not call it.
- Raw credentials must never serialize in Request, Proposal, evidence, logs, PR body, or tests.
- Same idempotency key + same canonical request returns the same proposal; same key + changed request fails with `ErrIdempotencyConflict`.
- Freeze only the durable `Proposal` wire after kernel + GitHub adapter + adversarial tests are green.
- Prefer the connected Aftergraph GitHub App/connector for GitHub reads/writes/checks. Use SentinelX `sentinel_git` only when an enrolled host is available and policy permits; never widen an existing GitHub App's permissions to make this slice easier.
- SentinelX currently reports zero enrolled hosts; no plan step may require host-side Sentinel execution for correctness.

## Review Focus

1. **Authority collapse:** a valid evidence bundle but missing/invalid decision must never reach GitHub branch or PR creation. Task 1 pins this with backend call-count assertions.
2. **Cross-work replay:** evidence/decision for another Work or workspace must fail closed even when candidate SHA and target are otherwise valid. Task 1 tests org/work/workspace/candidate bindings.
3. **Idempotency race:** concurrent/restarted retries must reconcile the same staging branch/PR and must never delete a pre-existing exact resource. Task 3 covers exact replay, race recovery, and conflict.
4. **Canonical-target mutation:** no backend path may update the target branch, merge, auto-merge, or self-approve. Task 3 uses an HTTP allowlist/negative-call fixture.
5. **Credential/provenance leakage:** Proposal/PR body must expose only immutable identifiers and typed refs; no raw token, secret value, or provider implementation object may serialize. Tasks 1, 3, and 4 pin this.

---

### Task 1: Promotion Domain Kernel and Authority Gate

**Files:**
- Create: `packages/promotion/promotion.go`
- Create: `packages/promotion/identity.go`
- Create: `packages/promotion/promotion_test.go`
- Create: `packages/promotion/conformance_test.go`

**Interfaces:**
- Consumes: `workspace.Workspace`, `workspace.Candidate`.
- Produces:
  - `type Request struct`
  - `type Target struct`
  - `type Proposal struct`
  - `type EvidenceVerifier interface { Verify(context.Context, string, string) error }`
  - `type DecisionVerifier interface { VerifyPromotionDecision(context.Context, DecisionSubject) error }`
  - `type Backend interface { ID() string; Propose(context.Context, AuthorizedRequest) (Proposal, error); Get(context.Context, Proposal) (Proposal, error) }`
  - `type Service struct`
  - `func NewService(EvidenceVerifier, DecisionVerifier, Backend) (*Service, error)`
  - `func (s *Service) Propose(context.Context, Request) (Proposal, error)`
  - sentinel errors including `ErrMalformed`, `ErrForeignWorkspace`, `ErrCandidateMismatch`, `ErrImmutableCandidateRequired`, `ErrEvidenceNotVerified`, `ErrDecisionNotAuthorized`, `ErrIdempotencyConflict`, `ErrUnsupportedTarget`.

- [ ] **Step 1: Write failing validation and authority-gate tests**

Add tests asserting:
- valid workspace/candidate/request passes structural validation;
- request org/work mismatch returns `ErrForeignWorkspace`;
- candidate workspace ID mismatch returns `ErrCandidateMismatch`;
- non-40/64-hex candidate SHA returns `ErrImmutableCandidateRequired`;
- empty evidence bundle / decision / target fail closed;
- evidence verifier failure prevents any backend call;
- decision verifier failure prevents any backend call;
- decision subject receives exact org, work ID, candidate SHA, evidence bundle, decision ref, and policy decision ID;
- successful service call invokes backend exactly once with an `AuthorizedRequest`, not the original unauthenticated `Request`.

- [ ] **Step 2: Run the focused tests and confirm RED**

Run:
```bash
go test ./packages/promotion -run 'Test(Request|Service|Authority)' -count=1
```

Expected: FAIL because `packages/promotion` does not exist.

- [ ] **Step 3: Implement the domain types, sentinel errors, and validation**

Implement exact public types from the Interfaces block. Keep `AuthorizedRequest` package-visible or unexported so external callers cannot fabricate post-gate authority state.

Use one immutable Git SHA validator accepting exactly 40 or 64 lowercase hex characters, matching `workspace/1.0`.

- [ ] **Step 4: Implement restart-safe promotion identity**

In `identity.go`, derive:
- a key hash from `IdempotencyKey`;
- a canonical request fingerprint from org, work ID, workspace ID, candidate SHA, evidence bundle, decision ref, policy decision ID, target provider/repository/branch.

Exclude `IdempotencyKey` itself from the request fingerprint.

Add tests proving deterministic output, same-key changed-request fingerprint drift, and different-key prefix drift.

- [ ] **Step 5: Implement `Service.Propose` authority ordering**

Order must be:
1. structural/request binding validation;
2. evidence verification;
3. decision verification;
4. backend proposal.

No backend call is allowed before both gates pass.

- [ ] **Step 6: Add provider-neutral conformance**

`ConformanceSuite(t, serviceFactory)` must pin:
- valid proposal provenance;
- cross-org/work rejection;
- candidate/workspace mismatch;
- invalid candidate SHA;
- failed evidence;
- invalid decision;
- exact replay;
- idempotency conflict;
- proposal has no merged/released state;
- serialized proposal contains no `secret://` credential value object or raw token-shaped field.

- [ ] **Step 7: Run Task 1 tests**

Run:
```bash
go test ./packages/promotion -count=1
go vet ./packages/promotion
```

Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add packages/promotion
git commit -m "feat(promotion): add governed source promotion kernel"
```

---

### Task 2: Evidence and Decision Adapters

**Files:**
- Create: `services/promotion/evidence_verifier.go`
- Create: `services/promotion/evidence_verifier_test.go`
- Create: `services/promotion/decision_verifier.go`
- Create: `services/promotion/decision_verifier_test.go`

**Interfaces:**
- Consumes: Task 1 `promotion.EvidenceVerifier`, `promotion.DecisionVerifier`, existing `evidence.Bundle`, `evidence.VerifyBundle`, existing Brain/governance object semantics.
- Produces:
  - `type BundleLoader interface { LoadEvidenceBundle(context.Context, string) (*evidence.Bundle, error) }`
  - `type EvidenceKeyResolver interface { ResolveEvidenceVerificationKey(context.Context, string) (keyID string, key []byte, err error) }`
  - `type EvidenceVerifier struct` implementing the Task 1 interface.
  - `type DecisionLoader interface { LoadPromotionDecision(context.Context, string) (*DecisionRecord, error) }`
  - `type DecisionRecord struct` containing the exact authority bindings needed by Task 1.
  - `type DecisionVerifier struct` implementing the Task 1 interface.

- [ ] **Step 1: Write failing evidence-adapter tests**

Pin:
- missing bundle -> `promotion.ErrEvidenceNotVerified`;
- bundle ID mismatch -> reject;
- bundle WorkID mismatch -> reject;
- signature/content/integrity/correlation failure from `evidence.VerifyBundle` -> reject;
- fully valid bundle -> PASS;
- no verification key bytes or signatures are exposed in returned errors.

- [ ] **Step 2: Implement evidence adapter**

Load by immutable bundle ID, verify `bundle.BundleID == requested ID` and `bundle.WorkID == requested WorkID`, resolve verification key internally, then call existing `evidence.VerifyBundle`.

Never accept a precomputed caller boolean.

- [ ] **Step 3: Write failing decision-adapter tests**

Pin:
- missing decision -> `promotion.ErrDecisionNotAuthorized`;
- non-authoritative decision -> reject;
- no human/governance stamp -> reject;
- tombstoned decision -> reject;
- org/work/candidate/evidence mismatch -> reject;
- exact authoritative binding -> PASS.

- [ ] **Step 4: Implement decision adapter without duplicating Brain authority law**

The adapter translates the backing governance record into `DecisionRecord` and verifies the same central semantics already used by Brain: authority requires explicit stamp/decision provenance. Do not create a second promotion enum or a caller-set `Authorized bool`.

If the existing persistent Brain store does not expose a clean read interface for this exact lookup, define the small `DecisionLoader` seam here and leave storage wiring to the later WORKS-integration project rather than expanding this plan.

- [ ] **Step 5: Run Task 2 tests**

```bash
go test ./services/promotion -count=1
go vet ./services/promotion
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add services/promotion
git commit -m "feat(promotion): bind evidence and authority verification"
```

---

### Task 3: GitHub Canonical-Source Backend

**Files:**
- Create: `packages/promotion/github.go`
- Create: `packages/promotion/github_test.go`
- Modify only if reuse is clean and dependency-safe: `packages/workspace/github.go` helper extraction; otherwise keep promotion GitHub mechanics local.

**Interfaces:**
- Consumes: Task 1 `Backend`, `AuthorizedRequest`, `Proposal`.
- Produces:
  - `type GitHubConfig struct { ControlTokenRef *secrets.Ref; ControlScope string; APIBase string; WebBase string; BranchPrefix string }`
  - `type GitHubBackend struct`
  - `func NewGitHubBackend(GitHubConfig, secrets.Resolver, *http.Client) (*GitHubBackend, error)`
  - `GitHubBackend.Propose` and `Get`.

- [ ] **Step 1: Write failing GitHub happy-path test**

Mock only these allowed endpoint classes:
- GET candidate commit;
- GET matching staging refs;
- POST staging ref when absent;
- GET/search open PR by deterministic head/base;
- POST PR when absent.

Assert staging branch is deterministic:
```text
works/promotion/<key-hash>-<request-fingerprint>
```

Assert POST ref SHA equals the immutable candidate SHA.

Assert PR base equals explicit target branch.

- [ ] **Step 2: Add a negative API-call allowlist test**

The mock server must fail the test immediately if code calls:
- merge endpoint;
- auto-merge mutation/path;
- review approval endpoint;
- target-branch ref update/force update;
- repository rules/ruleset endpoint;
- branch protection mutation.

This test is the executable proof that the backend cannot silently expand authority.

- [ ] **Step 3: Implement candidate existence and target validation**

V1 supports only target provider `github` and candidate commits already reachable in the target GitHub repository.

Verify candidate commit exists before staging ref creation.

Do not cherry-pick or rewrite content in v1.

- [ ] **Step 4: Implement deterministic staging-ref reconciliation**

Before create:
- query exact/matching deterministic staging ref;
- exact SHA -> reuse;
- deterministic ref with different SHA/request fingerprint -> `ErrIdempotencyConflict`.

On create race:
- re-read;
- exact identity -> reuse;
- conflict -> fail closed.

Cleanup may delete only a resource proven to have been created by the current operation; never delete a pre-existing exact ref.

- [ ] **Step 5: Implement PR marker + body**

Human-readable body includes Work, Candidate, Evidence, Decision, optional policy decision, Target, and the statement that merge/release authority are separate.

Machine marker:
```text
<!-- aftergraph-promotion:v1
id=<promotion-id>
candidate=<sha>
evidence=<bundle-id>
decision=<decision-ref>
-->
```

No credential refs or raw values are included in the PR body.

- [ ] **Step 6: Implement PR reconciliation and race recovery**

Search by deterministic head/base and verify machine marker before reuse.

Exact existing PR -> return same Proposal.

Same deterministic branch with mismatched marker/bindings -> `ErrIdempotencyConflict`.

POST race -> re-search and reconcile.

- [ ] **Step 7: Add restart/race/foreign-resource tests**

Cover:
- exact staging branch + exact PR after process restart;
- staging branch conflict;
- existing PR with wrong marker;
- duplicate-PR race recovery;
- provider/network 429/5xx retryable error mapping;
- 404 candidate commit -> fail closed;
- no deletion of pre-existing branch/PR on later failure.

- [ ] **Step 8: Run Task 3 tests**

```bash
go test ./packages/promotion -run 'TestGitHub' -count=1
go test ./packages/promotion -count=1
go vet ./packages/promotion
```

Expected: PASS.

- [ ] **Step 9: Commit**

```bash
git add packages/promotion
git commit -m "feat(promotion): add GitHub source proposal backend"
```

---

### Task 4: Independent Verification Before Freeze

**Files:**
- Modify tests only as justified by review findings.
- No schema/manifest edits in this task.

**Interfaces:**
- Consumes: Tasks 1–3.
- Produces: review evidence that the implementation is safe enough to freeze.

- [ ] **Step 1: Run focused package/service tests**

```bash
go test ./packages/promotion ./services/promotion -count=1
```

Expected: PASS.

- [ ] **Step 2: Run full repository gates**

```bash
go build ./...
go vet ./...
go test ./... -count=1
```

Expected: PASS.

- [ ] **Step 3: Run the repository integrity benchmark/gate exactly as `.github/workflows/go-test.yml` does**

Expected: PASS with no before/after integrity drift.

- [ ] **Step 4: Use GitHub App/connector exact-head evidence**

Confirm the implementation PR head has:
- Go tests / Build PASS;
- Vet PASS;
- Test PASS;
- Integrity PASS;
- CodeQL PASS.

Do not infer latest-head success from an older SHA.

- [ ] **Step 5: Run fresh independent review**

Use the strongest available read-only reviewer/harness against:
- authority ordering;
- negative GitHub API allowlist;
- credential leakage;
- restart-safe idempotency;
- cleanup ownership;
- evidence/work/decision bindings.

Any material finding returns to the owning task; do not proceed to freeze on an unresolved finding.

- [ ] **Step 6: Sentinel/Aftergraph operational check**

If SentinelX has an enrolled host at execution time:
- use `sentinel_git diff` / read-only verification where policy permits;
- optionally run focused tests on the authorized repo checkout;
- record exact SHA and evidence.

If no host is enrolled, record `NOT_RUN: no SentinelX host enrolled` and continue with GitHub App + native CI. This is not a correctness failure because Sentinel is an additional verifier, not the source-promotion authority.

- [ ] **Step 7: Commit any test-only repairs from review**

Use a narrowly scoped `test(promotion): ...` or `fix(promotion): ...` commit and rerun Steps 1–6.

---

### Task 5: Freeze proposal wire as promotion/1.0

**Files:**
- Create: `contracts/schemas/promotion.schema.json`
- Modify: `contracts/gen_freeze.py`
- Modify: `contracts/manifest.json` only via generator output
- Modify: `contracts/manifest.sha256` only via generator output
- Modify: `tests/contracts/contracts_test.go`

**Interfaces:**
- Consumes: verified Task 1 `Proposal` wire.
- Produces: `contract:promotion/1.0` for durable proposal state only.

- [ ] **Step 1: Write failing contract tests before schema**

Valid fixture must include:
- proposal ID;
- org/work ID;
- candidate SHA;
- target provider/repository/branch;
- staging ref;
- PR URL/number;
- evidence bundle ID;
- decision ref;
- optional policy decision ID;
- created_at.

Adversarial fixtures must reject:
- raw token/credential fields;
- non-Git candidate SHA;
- missing evidence;
- missing decision;
- empty target;
- fields such as `merged`, `released`, `auto_merge`, or `approved` that would collapse later authority states into Proposal;
- unknown fields.

- [ ] **Step 2: Run contract test and confirm RED**

```bash
go test ./tests/contracts -run TestPromotionV1 -count=1
```

Expected: FAIL because promotion schema is absent.

- [ ] **Step 3: Add generator schema entry**

Add `promotion` with:
- version `1.0`;
- owner `works-execution`;
- source ADR/design reference to the accepted source-promotion design/ADR produced from this implementation;
- draft-07 schema matching only durable `Proposal`.

Do not freeze Request, verifier interfaces, backend interfaces, HTTP response types, or GitHub implementation details.

- [ ] **Step 4: Regenerate contracts using `contracts/gen_freeze.py`**

```bash
python3 contracts/gen_freeze.py
```

Expected:
- all prior schema hashes unchanged;
- exactly one new promotion entry;
- manifest entry count increments by one;
- `manifest.sha256` matches exact manifest bytes.

- [ ] **Step 5: Run contract + full gates**

```bash
go test ./tests/contracts -count=1
go build ./...
go vet ./...
go test ./... -count=1
```

Expected: PASS.

- [ ] **Step 6: Verify exact-head GitHub checks**

Confirm Go tests and CodeQL PASS on the freeze head.

- [ ] **Step 7: Commit**

```bash
git add contracts tests/contracts
git commit -m "contracts(promotion): freeze promotion/1.0 proposal wire"
```

---

### Task 6: Final Branch Review and Handoff

**Files:**
- Modify: implementation PR body only; code changes only if review finds a defect.

**Interfaces:**
- Consumes: Tasks 1–5.
- Produces: merge-ready branch evidence without exercising merge authority.

- [ ] **Step 1: Compare branch to base**

Confirm expected scope only:
- `packages/promotion/**`;
- `services/promotion/**`;
- promotion schema/freeze files;
- promotion contract tests;
- accepted design/ADR references as needed.

No workspace provider merge method, release call, Home OS code, FIHIM code, or Cloudflare materializer belongs in this branch.

- [ ] **Step 2: Run source-integrity scan**

Check for accidental duplicate package/type/function blocks in all string/tool-edited files, repeating the safeguard learned from #196.

- [ ] **Step 3: Run final exact-head gates**

Require:
- Build PASS;
- Vet PASS;
- Test PASS;
- Integrity PASS;
- CodeQL PASS.

- [ ] **Step 4: Update PR evidence ledger**

PR body must list:
- exact head SHA;
- focused test result;
- full gate results;
- promotion schema hash;
- freeze manifest hash;
- independent-review result;
- Sentinel verification result or explicit `NOT_RUN: no enrolled host`.

- [ ] **Step 5: Do not merge automatically**

Because repository ruleset issue #199 remains a separate governance defect until independently verified resolved, this plan stops at a merge-ready PR. Source-promotion code itself must not be used to merge its own implementation.

