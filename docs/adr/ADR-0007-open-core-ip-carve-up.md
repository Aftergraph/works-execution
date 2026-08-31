# ADR-0007: Open-Core IP Carve-up

**Status:** Accepted
**Author:** Hermes Agent (atlas)
**Date:** 2026-08-31
**Track:** Hard
**Supersedes:** none
**Related:** ADR-0006 (brand works-execution), RFC-0001, RFC-0002

## Context

`works-execution` is a public Go monorepo at
`github.com/JonasAbde/works-execution` (since 2026-08-31). The repo
is the operating substrate of a multi-product company (ServiceOps,
FinTech, founder OS). Five slices ship with capability-aware
scheduling, OCI Docker worker sandbox, evidence bundles, zero-secret
enrollment, and a 1.6s end-to-end pilot.

Without an explicit IP carve-up, the company's potential moat — the
proprietary control plane that makes WORKS valuable to multi-tenant
customers — could leak into the OSS substrate through momentum
("we already wrote it, may as well push it").

This ADR locks the split before the M1 external repository pilot,
which is the first slice that touches customer infrastructure.

## Decision

`works-execution` ships as **open-core**. Two licenses, one repo, one
module path, explicit per-file headers where the boundary crosses.

### Open-source (MIT)

In `JonasAbde/works-execution`, MIT-licensed:

| Component | Why open |
|---|---|
| Work schema (`packages/workgraph`) | Anyone should be able to model their own work |
| Capability-aware scheduler (`internal/scheduler`) | Core algorithm, validated against `docs/standards/schemas/action-manifest.schema.json` |
| OCI Docker sandbox (`internal/sandbox/docker.go`) | Hermetic execution is table stakes; not IP |
| Hermetic execution core (`internal/sandbox/hermetic.go`) | Same as above |
| Evidence bundle producer + format (`services/evidence`) | Verification-side, format is the IP boundary — *producer* is open, *long-term storage* is not |
| Failure classifier (`services/classifier`) | Deterministic rules; not novel |
| OPA/Rego policy bundle (`policies/lease_grant.rego`) | Reference implementation, swappable |
| SBOM emit (`services/sbom`) | SPDX + CycloneDX are public standards |
| GitHub webhook receiver + check publisher (`services/webhook`) | Single-tenant, self-hosted use case |
| Reproducible build (`Makefile`, `bin/`) | Trust requires inspectable binaries |
| Self-hosted CLI/binaries (`cmd/works*`) | Self-service is the OSS promise |
| Public docs, ADRs, schemas | Documentation must be open to be trusted |
| Standards registry + mappings | Mapping is the work, not the IP |

### Commercial control-plane (proprietary)

Out-of-repo, proprietary-licensed, **not** in the public monorepo:

| Component | Why proprietary |
|---|---|
| Web UX (onboarding, repo picker, secret vault, RBAC) | UX IS the product for hosted users |
| Multi-tenant org/team/role model | Datamodel is the lock-in |
| Billing, usage metering, plans | Revenue surface |
| Long-term artifact storage (CAS) with retention | Operational IP, retention policies are the moat |
| Hosted worker mesh (M5) | Operational expertise + scale economics |
| Webhook delivery monitor + replay + at-least-once | At-least-once needs durable infra |
| Cross-repo analytics + benchmark history | Network-effect data |
| SAML/SSO, audit export, compliance reports | Enterprise gate |
| AI remediation / agent-native work (M6+) | This is where the actual moat lives |
| Cross-tenant caching + content-addressed dedup | Operational IP |
| Org-level policies + inherited trust classes | Customer-specific config |

### License boundary rules

1. **No file in the OSS repo imports, references, or depends on code that is not in the OSS repo.** Anyone cloning `main` and running `go build ./...` must succeed with zero proprietary dependencies.
2. **No customer data, secrets, PII, or tenant configuration in the OSS repo.** This includes PATs, OAuth tokens, webhook secrets, repo allow-lists.
3. **No multi-tenant primitives** (org_id, tenant_id, billing_period) leak into the public schema or DB.
4. **OSS is single-tenant-first.** A user running the OSS binary against their own GitHub App + their own machine is the happy path.
5. **Commercial extension is via separate binaries + separate repo** (e.g. `JonasAbde/works-cloud`, proprietary license). It talks to the OSS binary over a documented control-plane protocol, never the other way around.
6. **Schema vs format:** the *evidence bundle format* is open (so anyone can verify). The *evidence storage backend* with retention/dedup is proprietary.
7. **Worker protocol is open.** The wire format between worker and control plane is documented and stable. Customers can run their own workers against a hosted control plane.
8. **Policy format is open.** Rego + the `policies/` directory are reference implementations. The hosted control plane ships them but commercial customers can write their own.

### What this is NOT

- **Not dual-licensing.** The OSS repo is MIT only. Commercial users buy the hosted control plane separately. AGPL is not on the table.
- **Not "open core by accident."** Every file in the repo is audited for commercial value. If a file is needed to operate the hosted control plane, it must move out (or stay as a thin interface).
- **Not a permanent boundary.** When the company hits design-partner scale (M6) and the AI-remediation moat is real, this ADR can be revisited. Until then, it is the operating contract.

## Consequences

- **Positive:** OSS momentum builds the community + integrations + contributions; commercial control plane is the only path to multi-tenant scale.
- **Positive:** Customers can self-host for free (Rendetalje-style clean-stack tenants), generating trust.
- **Positive:** Hiring becomes easier — the OSS repo is a public artifact anyone can audit.
- **Negative:** All commercial IP must be carefully excluded from PRs against `main`. Need a CI check that rejects commits touching files outside the open-core list.
- **Negative:** Some features that feel "obvious" must be held back from the OSS repo (e.g. cached at-least-once webhook delivery) until a commercial control plane exists.
- **Risk:** If a competitor builds a multi-tenant control plane on top of our OSS, we lose the network effect. Mitigation: AI-remediation (M6+) is intentionally not in OSS.

## Enforcement

- `make opensource-audit` — scans for forbidden imports (proprietary module paths, customer-data patterns)
- `make boundary-check` — fails the build if any `services/*` file imports `cloud.*` (reserved namespace)
- CI gate: PRs touching files outside the open-core list require `@founder` approval
- Quarterly review of any new file added to `services/` against this ADR

## References

- https://en.wikipedia.org/wiki/Open-core_model (for context, not as endorsement)
- ADR-0006 (brand)
- RFC-0001 (slice 2 leases and recovery)
- RFC-0002 (slice 5 docker sandbox)
- 90-day execution plan §Days 61–90 (where the commercial packaging decision was deferred)
