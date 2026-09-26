# Durable mission handoff runbook

Use this surface when work must outlive the submitting ChatGPT, Hermes, CLI, terminal, or connector session.

## Create a mission

```yaml
version: 1
mission_id: example-2026-09-11-001
objective: produce and verify the requested outcome
purpose_bindings: [repository-maintenance]
budget:
  wall_clock_h: 1
verification:
  - criterion: expected artifact exists and matches acceptance criteria
    kind: deterministic
stages:
  apply:
    reconcile: test -f "$MARKER"
    run: printf '%s\n' done > "$MARKER"
    permissions: [read, write, execute]
    side_effects: [filesystem_write]
```

Submit it with:

```bash
works mission run --config mission.yaml --api "$WORKS_API"
```

The default is detached. A successful return means the control plane durably owns the Work; it does **not** mean the mission is verified.

## Reconcile before replay

For consequential stages, `reconcile` must be read-only and answer one narrow question about the desired external state. Exit `0` means already present, so mutation is skipped. Exit `1` means proven absent, so `run` may execute. Any other exit is indeterminate and fails closed with no mutation.

After an uncertain timeout, worker loss, or coordinator disconnect, do not blindly repeat a side effect. Observe durable WORKS state plus the external target, then continue.

## Duplicate submissions

`mission_id` is durable identity. The compiler derives a deterministic Work ID and spec fingerprint from it. Re-submitting the same mission reconciles to the existing Work. Reusing the same mission ID with changed content is rejected; create a new mission ID instead.

## Recovery versus containment

Lease expiry, worker death, connector timeout, context loss and coordinator termination are continuity faults. They may recover through ordinary WORKS lease/reaper behavior plus reconciliation. Revocation and budget exhaustion are containment conditions and must not auto-recover.

## Observe and verify

Use `works status <work_id>` or `works mission run ... --follow` to observe state. `--follow` is only an observer and may be terminated without cancelling execution. Executor completion remains distinct from independent verification; verification criteria and evidence must still be evaluated by the appropriate verifier/reviewer boundary.
