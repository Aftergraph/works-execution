# AIE Revalidation Integration for WORKS Execution Plane

This document specifies how to wire the AuthorityRevalidateFn interface into the WORKS execution flow to perform AIE revalidation before evidence is finalized.

## AuthorityRevalidateFn Go Interface

```go
// AuthorityRevalidateFn is the function signature for AIE revalidation
// callbacks. It is invoked after node execution completes but before
// evidence is committed to the store.
type AuthorityRevalidateFn func(ctx context.Context, workID, nodeID string, evidence []workgraph.Evidence) ([]workgraph.Evidence, error)
```

**Contract:**
- **Input:** Context, work/node IDs, and the evidence slice that the worker just produced
- **Output:** May augment or replace the evidence; returns an error if revalidation fails
- **Error handling:** Must follow fail-closed semantics (see below)

## Wiring Location

Add the revalidation hook at **line 670** in `internal/worker/worker.go`, immediately before evidence is constructed and submitted via `CompleteLease`.

```go
// Line 670: Before evidence construction
// Insert:
if revalidator != nil {
    revalidated, err := revalidator(ctx, item.WorkID, item.NodeID, []workgraph.Evidence{})
    if err != nil {
        // Fail-closed: mark execution as failed
        res.Status = "failed"
        res.ExitCode = -1
        evidenceDetails["revalidation_error"] = err.Error()
        // Continue to evidence build and CompleteLease
    } else if len(revalidated) > 0 {
        evidenceDetails["revalidated_by"] = "authority"
    }
}

// Lines 671-679: Existing evidence construction
evidence := []workgraph.Evidence{{
    ID:          workgraph.NewID("evd"),
    NodeID:      item.NodeID,
    Type:        "build",
    Result:      evidenceResult(res.Status),
    Signer:      w.ID,
    Environment: fmt.Sprintf("worker=%s", w.ID),
    Details:     evidenceDetails,
}}
```

**Note:** For the initial integration, place the revalidator call **after** the evidence is constructed but **before** `CompleteLease` at line 680:

```go
// Line 680: Before CompleteLease
// Insert:
if revalidator != nil {
    revalidated, err := revalidator(ctx, item.WorkID, item.NodeID, evidence)
    if err != nil {
        // Fail-closed: still submit but mark the error
        evidenceDetails["revalidation_error"] = err.Error()
        // Update evidence Result to indicate revalidation failure
        if len(revalidated) > 0 {
            evidence = revalidated
        }
    } else {
        evidenceDetails["revalidated"] = true
        evidence = revalidated
    }
}
```

## Fail-Closed Error Handling Contract

All revalidation errors **must** fail closed:

| Scenario | Required Behavior |
|----------|-------------------|
| Revalidator returns error | Execution **must not** be marked as SUCCEEDED. Set `res.Status = "failed"` and `res.ExitCode = -1`. Log the error. |
| Revalidator times out | Treat as error → fail closed. Use context timeout wrapping around revalidator call. |
| Revalidator returns empty evidence when augmentation expected | Log warning; preserve original evidence with `revalidation_warning` in Details. |
| Revalidator returns evidence with Result = "fail" | Honor it → execution fails. |

**Timeout policy:**
- Default timeout: 5 seconds
- Configurable via Worker config
- Wrapped with `context.WithTimeout`

```go
ctx, cancel := context.WithTimeout(ctx, revalidationTimeout)
defer cancel()
revalidated, err := revalidator(ctx, workID, nodeID, evidence)
```

## Evidence Bundle Extension for Revalidation Results

Extend the `Components.Evidence` entry in the evidence bundle (`services/evidence/bundle.go`) to record revalidation metadata:

```go
// EvidenceRef already exists; add to Details map:
//
// Keys added to EvidenceRef.Details when revalidation runs:
// - "revalidated": bool (true if revalidator accepted)
// - "revalidated_by": string (authority identifier)
// - "revalidation_error": string (if fail-closed triggered)
// - "revalidation_duration_ms": int64 (execution time of revalidator)
```

The `EvidenceRef` struct (line 131-141 in bundle.go) already supports `Details map[string]any`, so no structural changes are needed—just document the convention.

### Example Bundle Evidence Entry

```json
{
  "id": "evd_xxx",
  "node_id": "node_123",
  "type": "build",
  "result": "pass",
  "signer": "worker_abc",
  "details": {
    "exit_code": 0,
    "duration_ms": 1500,
    "revalidated": true,
    "revalidated_by": "aie-authority",
    "revalidation_duration_ms": 420
  }
}
```

## Integration Checklist

- [ ] Add `Revalidator` field to `Worker` struct (`internal/worker/worker.go` ~line 420-440)
- [ ] Wire revalidator in `execute()` at line 670-680
- [ ] Implement fail-closed error handling
- [ ] Add timeout policy (default 5s)
- [ ] Document evidence extension conventions
- [ ] Update `Produce` in `services/evidence/bundle.go` if bundle validation needs to check revalidation fields

## Scheduling Integration (Optional Phase 2)

To inform scheduler decisions with revalidation capability:

1. Extend `Scheduler.Runner` (`internal/scheduler/scheduler.go` ~line 52-76) with:
   ```go
   RevalidationSupported bool `json:"revalidation_supported,omitempty"`
   ```

2. Add soft signal score for revalidation-ready runners (~line 337-388)

3. Update `Select()` hard constraints to filter by revalidation requirements if a work specifies them

This enables the scheduler to route works requiring AIE revalidation only to capable runners.
