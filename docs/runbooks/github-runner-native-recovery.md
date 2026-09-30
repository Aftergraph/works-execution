# GitHub runner native recovery

This procedure recovers the GitHub compatibility runner through the Aftergraph WORKS execution plane. It is not a GitHub-side truth override.

## Contract

Run on the Linux VDS worker that owns the runner service:

```bash
bash scripts/ops/recover-github-runner.sh vps-ci-01
```

The script is idempotent and fail-closed:

- resolves exactly one `actions.runner.*.<runner>.service`;
- does not restart an already-active listener;
- uses non-interactive operator authority for restart;
- requires systemd active state plus recent `Connected to GitHub` or `Listening for Jobs` evidence;
- returns `INFRA_UNAVAILABLE` if service identity or listener evidence cannot be proven.

A `RECOVERED` receipt proves runner listener recovery only. It does not prove any queued CI job passed. CI truth still comes from the exact-head evaluation/receipt/Sentinel chain.
