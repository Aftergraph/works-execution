# Source Promotion v1 — Design

**Status:** Design review  
**Date:** 2026-10-01  
**Repository:** `Aftergraph/works-execution`  
**Depends on:** ADR-0030 and workspace/1.0 hardening in PR #200

## 1. Purpose

Agent workspaces produce source candidates. They are execution substrate, not authority.

Source Promotion v1 introduces the explicit WORKS-owned boundary that turns a verified candidate into a **proposal against canonical GitHub source** without granting the workspace provider, candidate producer, evaluator, or promoter authority to merge or deploy it.

The intended chain is:

```text
Work
  -> Workspace
  -> Candidate (immutable commit SHA)
  -> Evidence verification
  -> Promotion decision
  -> Canonical staging ref
  -> GitHub pull request
  -> human/governance merge
  -> canonical commit
```

A successful promotion operation therefore means **"a governed canonical-source proposal exists"**, not "the candidate is merged", "verified forever", or "released".

## 2. Non-goals

Source Promotion v1 does not:

- merge a pull request;
- push directly to the canonical target branch;
- deploy or advance a release ring;
- select the best candidate among several candidates;
- evaluate candidate quality;
- create new authority semantics parallel to Brain;
- turn Cloudflare Artifacts into canonical source;
- persist raw GitHub, Cloudflare, or other provider credentials;
- treat a GitHub status/check as proof of a human decision by itself.

Candidate selection remains WORKS/evaluation policy. Human/governance authority remains explicit. Deployment/release remains `packages/release`.

## 3. Existing laws reused

### ADR-0030 / workspace

Workspace providers may create isolated source state and report immutable candidate coordinates. They may not decide correctness or promote themselves.

Source Promotion consumes `workspace.Candidate`; it does not add promotion methods to `workspace.Provider`.

### Evidence

`services/evidence` already produces and verifies content-addressed evidence bundles. Promotion must depend on an independently verifiable evidence result rather than a caller-provided boolean.

Promotion records the exact evidence bundle identity used for the decision.

### Brain authority

`packages/brain` already encodes the central authority law: agent-written state cannot become authoritative without an explicit human stamp.

Source Promotion does not duplicate Brain storage, but it follows the same principle:

- automated systems may prepare a proposal;
- authoritative human/governance decisions must be explicitly referenced;
- an automated provider response cannot manufacture authority.

### Release rings

`packages/release` controls deployment/release promotion. Source Promotion only changes canonical source collaboration state by preparing a PR. A merged source change still needs the normal release path.

## 4. Architecture

### 4.1 Package boundary

Add `packages/promotion`.

The package owns:

- promotion request validation;
- provenance binding;
- idempotency identity;
- evidence/decision gates;
- canonical-source proposal orchestration;
- provider-neutral proposal result.

It does not own:

- Git workspace creation;
- evaluation;
- human authorization;
- release/deployment;
- GitHub merge.

### 4.2 Core types

```go
type Request struct {
    IdempotencyKey string

    Org    string
    WorkID string

    Workspace workspace.Workspace
    Candidate workspace.Candidate

    EvidenceBundleID string
    DecisionRef      string
    PolicyDecisionID string

    Target Target
}

type Target struct {
    Provider   string
    Repository string
    Branch     string
}

type Proposal struct {
    ID string

    Org    string
    WorkID string

    CandidateSHA string
    Target       Target

    StagingRef string

    PullRequestURL    string
    PullRequestNumber int

    EvidenceBundleID string
    DecisionRef      string
    PolicyDecisionID string

    CreatedAt time.Time
}

type Promoter interface {
    ID() string
    Propose(context.Context, Request) (Proposal, error)
    Get(context.Context, Proposal) (Proposal, error)
}
```

Names may be adjusted during implementation only where needed to fit established repository conventions; the authority split is fixed by this design.

## 5. Authority and validation laws

A promotion request fails closed unless all of these hold:

1. `Org` and `WorkID` are non-empty.
2. Workspace validates under `workspace/1.0`.
3. Workspace `Org` and `WorkID` equal request bindings.
4. Candidate `WorkspaceID` equals workspace ID.
5. Candidate repository coordinate is consistent with the workspace.
6. Candidate SHA is an immutable Git commit identifier.
7. Evidence bundle identity is non-empty.
8. Evidence verifier confirms the exact referenced bundle.
9. Evidence WorkID equals the promotion WorkID.
10. Decision reference is non-empty and resolves to an authorized promotion decision.
11. Any required policy decision identity is bound to the same Work/execution context.
12. Target repository and branch are explicit.
13. Target branch is canonical but cannot be mutated directly by the promoter.
14. No raw credential may appear in Request, Proposal, evidence, logs, or PR body.

The implementation must distinguish:

- **verification** — evidence is structurally and cryptographically valid;
- **decision** — a permitted authority selected this candidate for proposal;
- **proposal** — a GitHub PR exists;
- **merge** — deliberately outside v1;
- **release** — deliberately outside v1.

No state transition may collapse these meanings.

## 6. Decision gate

Source Promotion needs a small interface rather than direct coupling to Brain storage:

```go
type DecisionVerifier interface {
    VerifyPromotionDecision(
        ctx context.Context,
        ref string,
        org string,
        workID string,
        candidateSHA string,
        evidenceBundleID string,
    ) error
}
```

The production adapter may resolve a Brain decision object or another governance-backed decision record.

The package must not accept `Authorized bool`, `Verified bool`, or equivalent caller assertions.

A valid decision must bind at least:

- organization;
- work;
- candidate SHA;
- evidence bundle;
- decision identity;
- authority identity or stamp according to the backing governance system.

## 7. Evidence gate

Use an adapter around the existing `services/evidence` verifier so `packages/promotion` does not learn HMAC keys or evidence storage details.

Conceptually:

```go
type EvidenceVerifier interface {
    Verify(ctx context.Context, bundleID, workID string) error
}
```

The adapter owns loading the bundle, key resolution, signature verification, content-address verification, integrity-envelope verification, and correlation checks.

Promotion only receives a pass/fail result plus the immutable bundle identity it already holds.

## 8. Idempotency

Promotion must be restart-safe.

A canonical promotion identity is derived from:

- caller idempotency key;
- org;
- work ID;
- candidate SHA;
- evidence bundle ID;
- decision ref;
- policy decision ID;
- target provider/repository/branch.

Rules:

- same key + same canonical request -> same proposal;
- same key + changed canonical request -> `ErrIdempotencyConflict`;
- retries must not create duplicate staging branches or duplicate PRs;
- process restart must not erase idempotency;
- provider-local in-memory maps are insufficient.

For GitHub v1, deterministic external identity is preferred:

```text
branch:
works/promotion/<key-hash>-<request-fingerprint>

PR marker:
<!-- aftergraph-promotion:v1
id=<promotion-id>
candidate=<sha>
evidence=<bundle-id>
decision=<decision-ref>
-->
```

The adapter first searches/reconciles the deterministic branch and PR identity before creating anything.

If durable WORKS persistence is later needed for richer lifecycle state, it may be added without changing this external identity law.

## 9. GitHub promoter

GitHub is the only canonical-source promoter in v1.

### Responsibilities

The GitHub adapter may:

- verify target repository metadata;
- materialize the candidate onto a deterministic staging branch;
- create or recover a PR;
- return PR coordinates;
- read/reconcile an existing proposal.

It may not:

- merge the PR;
- enable auto-merge;
- bypass target-branch protection;
- push to the target branch;
- dismiss reviews;
- alter repository rulesets;
- self-approve the PR;
- convert successful CI into human authority.

### Candidate materialization in v1

V1 supports a GitHub workspace candidate whose immutable commit object is already reachable in the canonical GitHub repository.

The adapter:

1. verifies the candidate commit exists;
2. creates or reconciles the deterministic staging ref pointing to that exact candidate SHA;
3. creates or reconciles the PR from the staging ref to the explicit target branch.

No cherry-pick, content rewrite, merge, or direct target-branch update is permitted.

Cloudflare Artifacts and other external Git-compatible candidates require a separate materializer and are explicitly outside this v1 implementation plan. That follow-up may fetch/import immutable Git objects, but it must consume the same promotion authority boundary rather than expanding it.

## 10. Pull request evidence surface

The generated PR body should expose human-reviewable provenance without leaking secrets.

Required fields:

- Work ID;
- candidate SHA;
- source workspace/provider coordinate;
- evidence bundle ID;
- decision ref;
- policy decision ID if present;
- target branch;
- immutable promotion identity;
- statement that the PR is a proposal and not proof of merge/release.

Example structure:

```text
Aftergraph WORKS source-promotion proposal

Work: ...
Candidate: ...
Evidence: ...
Decision: ...
Policy decision: ...
Target: ...

This PR represents a governed source proposal.
Merge authority and release authority are separate.
```

The machine-readable marker is included separately for deterministic reconciliation.

## 11. Failure model

Define typed/sentinel failures including:

- malformed request;
- foreign workspace;
- candidate/workspace mismatch;
- immutable candidate required;
- evidence not verified;
- evidence/work mismatch;
- decision missing;
- decision not authorized;
- decision/candidate mismatch;
- idempotency conflict;
- target unsupported;
- materialization failed;
- proposal provider unavailable;
- proposal not found.

Provider/network failures remain retryable where safe.

Authority, binding, provenance, and idempotency conflicts are not silently retried as if they were transient.

Cleanup rules must never delete a staging branch or PR that was discovered as pre-existing during reconciliation.

## 12. Conformance suite

Add a provider-neutral promotion conformance suite.

Required laws:

- valid candidate + verified evidence + valid decision -> proposal;
- foreign org/work -> reject;
- candidate/workspace mismatch -> reject;
- mutable/non-Git candidate identity -> reject;
- missing/failed evidence -> reject;
- evidence bound to another work -> reject;
- missing/invalid decision -> reject;
- decision for another candidate -> reject;
- same idempotency key + same request -> same proposal;
- same key + changed request -> conflict;
- proposal never mutates canonical target branch;
- proposal result preserves candidate/evidence/decision provenance;
- no plaintext credential serializes;
- provider cannot return "merged" or "released" state because those states do not exist in the v1 contract.

GitHub adapter tests additionally cover:

- existing exact staging branch reconciliation;
- conflicting deterministic branch;
- existing exact PR reconciliation;
- duplicate PR race recovery;
- candidate commit existence;
- no direct target-branch update;
- no merge/automerge API calls.

## 13. Contract strategy

Do **not** freeze `promotion/1.0` in the first implementation commit.

Sequence:

1. implement package + reference promoter;
2. run conformance;
3. implement GitHub promoter;
4. run adversarial/provider tests;
5. perform independent review;
6. only then materialize JSON Schema and add it to the freeze manifest.

This repeats the lesson from `workspace/1.0`: wire form and lifecycle semantics must be proven before freeze.

The durable wire contract should freeze `Proposal`, not Go implementation details such as internal verifier interfaces.

## 14. Observability and provenance

Promotion operations should emit/record correlation data sufficient for Home OS and evidence lineage:

```text
work_id
workspace_id
candidate_sha
evidence_bundle_id
decision_ref
policy_decision_id
promotion_id
staging_ref
pull_request_number
target_repository
target_branch
```

No telemetry field becomes authority merely because it was observed.

Home OS can later visualize:

```text
Work
  -> Workspace
  -> Candidate
  -> Evaluation / Evidence
  -> Decision
  -> Promotion Proposal
  -> PR
  -> Canonical Commit
  -> Release
```

The UI must visually distinguish proposal, merged canonical source, and released state.

## 15. Security

- least-privilege GitHub App/credential scope;
- no PATs in URLs;
- no long-lived repo credentials on Proposal;
- secrets resolved only within operation scope;
- target branch never directly writable by promotion flow;
- branch/ruleset enforcement remains external defense in depth;
- reconciliation verifies identities before reusing existing external resources;
- all candidate and target coordinates are explicit;
- provider responses cannot widen org/work scope.

Where the connected Aftergraph/Sentinel GitHub App provides repository reads, checks, or governed Git operations, implementation should prefer that app path over shelling out or copying credentials. Host-side Sentinel Git operations remain optional execution mechanics and must obey the same promotion contract.

## 16. Rollout

### Slice A — domain kernel

- `packages/promotion/promotion.go`
- request/proposal validation;
- idempotency fingerprint;
- verifier interfaces;
- reference promoter;
- conformance tests.

### Slice B — GitHub adapter

- deterministic staging branch;
- candidate existence verification;
- PR create/reconcile;
- race recovery;
- negative tests proving no merge/target update.

### Slice C — freeze

- `promotion/1.0` proposal schema;
- adversarial contract tests;
- generator entry;
- manifest/hash attestation.

The implementation plan ends here. WORKS orchestration, external Git materialization, and Home OS read models are follow-up projects that consume the frozen boundary rather than enlarging this plan.

## 17. Acceptance criteria

Source Promotion v1 is ready only when:

- workspace provider still has no promotion/merge method;
- a candidate cannot reach GitHub PR creation without verified evidence;
- a candidate cannot reach GitHub PR creation without a valid authority decision;
- same-intent retries are restart-safe;
- idempotency conflicts fail closed;
- GitHub target branch is never directly updated by the promoter;
- no merge/automerge operation exists in the package;
- proposal provenance binds Work + Workspace + Candidate + Evidence + Decision + Target;
- reference conformance passes;
- GitHub adversarial tests pass;
- full Go build/vet/test/integrity and CodeQL pass;
- independent review finds no authority collapse;
- only after those gates is `promotion/1.0` frozen.

## 18. Explicit follow-up boundaries

The following remain separate future work:

- candidate ranking / multi-agent winner selection;
- FIHIM Eval Lab adapter;
- Cloudflare/external Git candidate materialization;
- private-source materialization credentials;
- WORKS orchestration hook and event/provenance integration;
- expiration/reaper for abandoned staging refs/PRs;
- Home OS promotion graph;
- merge-authority workflow;
- release/deployment linkage.

They must not be smuggled into v1 merely because they are adjacent.
