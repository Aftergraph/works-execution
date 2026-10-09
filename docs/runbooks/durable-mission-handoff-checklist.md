# Durable mission handoff checklist

- Choose a stable `mission_id` for one immutable mission specification.
- Bind objective, purpose, budget, verification criteria, and stage DAG before submission.
- Use `secret://` references for secret-like environment variables; never persist secret values in mission YAML.
- Add read-only `reconcile` checks before consequential mutations.
- Treat reconcile exit 0 as already applied, exit 1 as proven absent, and any other code as indeterminate/fail-closed.
- Submit with `works mission run --config ...`; detached return means WORKS owns execution, not that verification passed.
- Observe with `works status` or `--follow`; observer loss must not cancel execution.
- Treat worker loss, connector timeout, and coordinator loss as continuity faults.
- Treat revocation and budget exhaustion as containment; never auto-resume them.
- Require independent verification before claiming VERIFIED.
