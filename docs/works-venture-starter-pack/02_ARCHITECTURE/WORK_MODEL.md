# Work Model

## Entities
- Work
- Node
- Attempt
- Worker
- WorkerPool
- Lease
- Artifact
- Evidence
- Approval
- AuditEvent

## Node readiness
A node is READY only when:
- all required dependencies are satisfied;
- policy permits execution;
- required inputs are resolvable;
- no active valid lease exists;
- Work is in an executable state.

## Attempt semantics
Each execution attempt is immutable after completion. Retries create new attempts. This preserves failure history and prevents a retry from rewriting evidence.

## Idempotency
Work creation and external event ingestion require idempotency keys.
