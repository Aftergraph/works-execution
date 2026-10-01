# ADR-0030: Agent workspace providers are execution substrate, not authority

**Status:** Accepted  
**Date:** 2026-10-01

## Context

Agentic software execution needs many concurrent, isolated source workspaces. GitHub
branches alone are not a sufficient execution primitive at high fan-out, while coupling
WORKS directly to one repository host would violate ADR-0001/0004 provider neutrality.

Cloudflare Artifacts now exposes programmable Git-compatible repositories that can be
created and forked per task/agent, with repo-scoped credentials and repository events.
GitHub, local Git, and future source systems remain valid backends.

## Decision

WORKS owns a frozen provider-neutral `workspace/1.0` boundary in
`packages/workspace`.

A workspace provider may:

- create/fork an isolated source workspace from an explicit baseline;
- return a Git-compatible remote;
- expose only a `secret://` credential ref to WORKS;
- report an immutable candidate coordinate;
- revoke credentials and destroy the workspace.

A workspace provider may **not**:

- decide whether a candidate is correct;
- merge/promote a candidate to canonical source on its own;
- widen policy or capability scope;
- become the source of Work truth;
- persist raw provider credentials in Work/evidence/audit payloads.

Promotion remains an explicit WORKS/governance decision backed by verification evidence.

## Provider mapping

- **GitHub:** canonical collaboration/upstream source where appropriate.
- **Cloudflare Artifacts:** high-fanout ephemeral agent workspace backend.
- **Local Git:** development/offline backend.
- **Future providers:** adapters behind `workspace/1.0`.

Cloudflare Artifacts is therefore optional infrastructure, not a platform dependency.

## Security invariants

1. Every workspace is bound to `org + work_id`.
2. The baseline is explicit (`provider/repository/ref|sha`).
3. Idempotency keys prevent duplicate workspace creation.
4. Credentials cross the boundary only as `contract:secret.ref/1.0`.
5. Candidate production and canonical promotion are separate operations.
6. Destroy/revoke is explicit and provider-scoped.

## Consequences

This gives WORKS an agent-native isolation primitive without adding a new top-level
Aftergraph product or duplicating WorkGraph. It also allows Cloudflare Artifacts to be
adopted incrementally while preserving GitHub compatibility and Aftergraph provenance
laws.
