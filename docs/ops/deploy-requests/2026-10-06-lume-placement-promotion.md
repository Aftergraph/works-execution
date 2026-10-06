# Lume placement promotion request

Date: 2026-10-06

This change records the reviewed production promotion request for the canonical
WORKS main after the atomic Runtime placement reservation implementation.

Acceptance after promotion:
- live service revision equals the promoted main SHA;
- health check passes;
- integrity smoke passes;
- a deployment receipt is persisted;
- Runtime-selected WorkerLease + Attempt remain WORKS-owned;
- atomic CREATED to RUNNING reservation remains intact.
