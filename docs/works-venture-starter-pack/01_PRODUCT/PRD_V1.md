# Product Requirements Document V1

## Users
### Developer
Needs fast, understandable feedback and minimal CI babysitting.

### Platform/DevOps engineer
Needs predictable execution, worker governance, capacity visibility and lower operational burden.

### Engineering leader
Needs lower cost per verified change and higher delivery throughput.

### Security engineer
Needs scoped credentials, isolation, provenance and auditability.

## Primary journeys
### A. GitHub compatibility onboarding
1. Create organization.
2. Install GitHub integration.
3. Select repository.
4. Register/choose worker pool.
5. Change compatible runner label or migration configuration.
6. Execute.
7. Compare baseline vs WORKS.

### B. Native Work
1. `works init`
2. Detect project/build graph.
3. Review generated config.
4. `works run`
5. Observe graph.
6. Retrieve evidence/artifacts.
7. Promote configuration into CI.

### C. Failure recovery
1. Node is leased to worker.
2. Worker disappears.
3. Lease expires.
4. Node returns to ready state.
5. Scheduler selects compatible worker.
6. Node resumes/re-executes according to checkpoint/cache semantics.
7. Work preserves complete attempt history.

## Required UX states
Every execution surface must support: queued, running, blocked, degraded, failed, cancelled, succeeded, retrying, cache-hit, waiting-for-worker and waiting-for-approval.

## Acceptance criteria
No UI may claim success unless the authoritative Work state and required evidence are complete.
