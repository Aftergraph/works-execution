# ADR-0001: Work is the core primitive

**Status:** Accepted

## Decision
WORKS models execution around durable `Work`, not provider-specific workflow/job primitives.

## Rationale
This decouples repository events, compute workers, future agent workers, evidence and recovery from GitHub/GitLab semantics.

## Consequence
Compatibility layers compile external workflows/events into Work rather than becoming the internal state model.
