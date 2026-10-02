# ADR Index

Architecture Decision Records for `works-execution`.

## Accepted

- **ADR-0001** — Work is the core primitive. (Source pack, `10_ADRS/`)
- **ADR-0002** — Control plane owns authoritative state. (Source pack, `10_ADRS/`)
- **ADR-0003** — V1 does not depend on AI. (Source pack, `10_ADRS/`)
- **ADR-0004** — GitHub compatibility is a distribution wedge, not the core. (Source pack, `10_ADRS/`)
- **ADR-0005** — V1 uses SQLite for durable state. (`docs/adr/ADR-0005-sqlite-for-v1-state.md`)
- **ADR-0006** — Brand: `works-execution`. (`docs/adr/ADR-0006-brand-works-execution.md`)
- **ADR-0007** — Open-core IP carve-up (OSS substrate vs. commercial control plane). (`docs/adr/ADR-0007-open-core-ip-carve-up.md`)\n- **ADR-0030** — Agent workspace providers are execution substrate, not authority. (`docs/adr/ADR-0030-agent-workspace-provider-boundary.md`)\n- **ADR-0031** — Source promotion creates proposals, not authority. (`docs/adr/ADR-0031-governed-source-promotion.md`)
- **ADR-0032** — TIMED_OUT (terminal) and UNKNOWN (non-terminal) added to the Work state vocabulary; the vocabulary freeze guard is rebuilt so it can actually detect an addition. (`docs/adr/ADR-0032-work-state-timed-out-and-unknown.md`)
- **ADR-0033** — Lease fencing tokens enforced as a store-side compare-and-swap, and a transactional outbox replacing the fire-and-forget publish. (`docs/adr/ADR-0033-lease-fencing-and-transactional-outbox.md`)

## Proposed (none yet)

## Superseded (none yet)