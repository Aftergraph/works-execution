# CircuitVerdict Exact-Subject Design

**Status:** Implemented on stacked draft branch; no production deployment.

## Purpose

Persist an immutable verifier attestation over one exact CircuitRun composition subject. This does not replace the existing Work VerificationVerdict and does not promote Work state.

## Subject

`CircuitSubject = circuit-run:<CircuitRunID>:spec-sha256:<CircuitSpecSHA256>`.

The subject is derived by WORKS from the stored CircuitRun, never accepted as caller truth. A verdict input names only `circuit_run_id`, result, verifier identity, evidence reference, and verifier timestamp.

## Invariants

- Existing CircuitRun must exist; its persisted digest is authoritative.
- Result is `ACCEPT` or `REJECT`; verifier/evidence/timestamp are required.
- One immutable CircuitVerdict per CircuitRun in v0.1; exact retry is idempotent, conflicting re-attestation fails closed.
- CircuitVerdict is not Work verification truth, authority, admission, execution permission, or settlement by itself.
- The store does not infer verifier independence; upstream Witness evidence must establish it.
- T040a binds composition identity only. Current dispatch acceptance lacks a direct WorkID seam, so effect binding remains T040b and MUST NOT be claimed here.

## Persistence

Schema v15 adds `circuit_verdicts(circuit_run_id PRIMARY KEY, circuit_spec_sha256, subject, result, verifier_id, evidence_ref, verified_at)` with a foreign key to `circuit_runs`.

No API endpoint, Work-state mutation, dispatch mutation, or deployment is part of this slice.
