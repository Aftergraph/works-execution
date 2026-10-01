# ADR-0031: Source promotion creates proposals, not authority

**Status:** Accepted  
**Date:** 2026-10-01

## Context

ADR-0030 separates agent source workspaces from canonical-source authority.
A workspace provider can produce an immutable candidate, but WORKS still needs
an explicit boundary for presenting a verified candidate to the canonical
collaboration repository.

Collapsing candidate production, evidence verification, human/governance
decision, source merge, and release into one provider operation would make
execution infrastructure an authority system and would make provenance
ambiguous.

## Decision

WORKS owns a provider-neutral `promotion/1.0` proposal boundary in
`packages/promotion`.

A source-promotion flow must bind:

- organization and Work;
- source workspace;
- immutable candidate SHA;
- independently verified evidence bundle;
- explicit promotion decision;
- policy decision when present;
- canonical target repository and branch.

The promotion service validates evidence and decision authority **before** a
backend receives an authorized request.

The GitHub v1 backend may:

- verify that the immutable candidate commit exists;
- create or reconcile a deterministic staging ref;
- create or reconcile a pull request;
- return durable proposal coordinates.

It may **not**:

- update the canonical target branch directly;
- merge or enable auto-merge;
- approve or dismiss reviews;
- modify branch protection or repository rulesets;
- release or deploy the candidate;
- manufacture evidence or governance authority.

A successful promotion operation therefore means **a governed source proposal
exists**. It does not mean the proposal was merged or released.

## Idempotency

Promotion identity is restart-safe and separates caller retry identity from
canonical request intent.

- Same idempotency key + same canonical request returns the same proposal.
- Same idempotency key + changed canonical request fails closed.
- Reconciliation considers prior pull requests across lifecycle states, so a
  closed or merged proposal is not silently duplicated.
- Pre-existing staging refs or pull requests are never deleted merely because a
  later retry fails.

## Evidence and authority

Evidence verification and authority are separate gates.

A cryptographically valid evidence bundle is not, by itself, permission to
promote. The decision verifier must bind the authoritative decision to the same
organization, Work, candidate SHA, evidence bundle, and policy decision.

The durable proposal preserves those bindings for independent inspection.

## Contract freeze

Only the durable `Proposal` wire is frozen as `promotion/1.0`.

The following remain implementation details and are not frozen by this ADR:

- Request;
- AuthorizedRequest;
- verifier interfaces;
- backend interfaces;
- GitHub REST payloads;
- credential resolution;
- internal idempotency hashing.

Breaking changes to the durable Proposal wire require the normal contract
versioning rules from ADR-0021.

## Consequences

Canonical-source collaboration becomes explicit and auditable without granting
merge or release authority to workspace providers or repository adapters.

GitHub remains the v1 canonical collaboration backend. External Git candidate
materialization, candidate ranking, FIHIM Eval Lab integration, Home OS
visualization, merge authority, and release linkage remain separate follow-up
systems.
