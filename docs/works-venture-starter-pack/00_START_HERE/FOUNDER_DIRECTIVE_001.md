# Founder Directive 001 - Build the Execution Substrate

## Decision
Proceed with WORKS as a standalone venture.

## Non-negotiables
- Do not architect WORKS around AVC.
- GitHub Actions compatibility is distribution, not the core architecture.
- The durable `Work` object is the source of execution truth.
- Workers are disposable; the control plane owns state.
- V1 must work without AI.
- Security boundaries exist before privileged customer workloads.
- Every performance claim must be reproducible.
- Native execution must eventually deliver value that commodity runner substitution cannot.

## V1 objective
Prove that WORKS can move a real repository from code change to verified result with less wall time, redundant compute, and manual recovery than its existing CI baseline.
