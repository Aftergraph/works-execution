# Pre-action evidence snapshot

Status: WORKS-native evidence record for issue #103.

## Purpose

Preserve the policy and context values that were in force **before** an execution attempt so later assurance/research consumers do not reconstruct those values from outcomes.

The record is intentionally narrow. It does not grant authority, select a policy, verify an outcome, or create a research-owned evidence plane.

## Ownership

- WORKS owns the durable execution evidence record.
- Runtime supplies orchestration/runtime facts when wiring capture into execution.
- Governance owns any future cross-repository normative registration.
- ISR may consume the record for research admission and evaluation.

## Wire identity

Evidence type:

```text
pre_action_snapshot
```

The content-addressed snapshot contains:

```text
work_id
node_id
attempt_id
run_id
execution_context_id

confidence_threshold
verification_depth
retry_ceiling

min_confidence
min_verification
min_retries

captured_at
digest
```

The execution correlation ids follow `execution-context/1.0`: `execution_context_id = ctx_<32hex>` and `trace_id = trc_<32hex>`.\n\nThe digest is SHA-256 over the canonical Go JSON encoding of every field above except `digest`.

The WORKS evidence record stores the digest in the sealed `result` field and the snapshot in `details`. Therefore:

```text
details mutation
→ recomputed snapshot digest differs from sealed result
→ DecodePreActionSnapshot fails closed
```

The underlying evidence row remains append-only through the existing store path (`INSERT OR IGNORE` by evidence id).

## evidence.schema/1.1 decision

**No in-place revision of `evidence.schema/1.1` is required for capture.**

Reason: the frozen contract already permits `records` as an array, while WORKS' native evidence model already supplies typed records plus free-form details. This implementation adds a new WORKS-native record type without tightening, renaming, or reinterpreting existing `evidence.schema/1.1` fields.

This does **not** make `pre_action_snapshot` a platform-wide normative record family automatically.

If another canonical repository must depend on the record name/shape as platform law, register/freeze that semantic contract through Governance instead of mutating `evidence.schema/1.1` silently.

## Fail-closed consumer law

Consumers requiring Headroom/research replay must reject a snapshot when:

- any identity field is empty;
- `captured_at` is absent;
- confidence values are outside [0,1];
- verification/retry values are negative;
- evidence type is not `pre_action_snapshot`;
- evidence identity differs from snapshot identity;
- the evidence seal is tampered;
- any snapshot field is missing or changed such that the content digest no longer equals the sealed result.

Consumers may not infer a missing field from condition names, model/provider identity, outcome values, or later configuration.

## Research fixture

A stable fixture is provided at:

```text
fixtures/evidence/preaction_snapshot_v1.json
```

ISR can consume this snapshot directly as the pre-action half of its Traceglass/Headroom trace admission tuple. Outcome/provenance fields remain separate execution/verifier evidence and must be joined by immutable run/work/attempt identity.

## Integration boundary

This slice defines and verifies the native record primitive. Wiring the record at the execution choke point must capture values before the action begins. A future integration must not synthesize the record at terminal bundle-production time from mutable current configuration.
