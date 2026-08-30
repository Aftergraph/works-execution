# WORKS Product Spec V1

## Product promise
Connect a repository or submit Work. WORKS schedules the required execution across compatible compute, safely reuses valid prior computation, recovers from infrastructure failures, stores artifacts, and records structured evidence.

## Core primitive: Work
```ts
interface Work {
  id: string
  source: WorkSource
  objective: Objective
  graph: ExecutionGraph
  requirements: Requirements
  policy: Policy
  state: WorkState
  executions: Execution[]
  artifacts: Artifact[]
  evidence: Evidence[]
  approvals: Approval[]
}
```

## State machine
`CREATED -> PLANNING -> QUEUED -> RUNNING -> VERIFYING -> SUCCEEDED`

Terminal/side states: `BLOCKED`, `FAILED`, `CANCELLED`.

## V1 capabilities
- GitHub integration and compatibility wedge
- Linux worker runtime
- durable Work/DAG state
- worker pools and capabilities
- scheduling and leases
- log streaming
- artifacts/CAS
- deterministic execution fingerprints
- local + organization cache
- worker-loss recovery
- failure classification
- structured evidence
- RBAC/audit baseline
- runtime/cost/cache analytics

## Non-goals
- Git hosting
- general cloud replacement
- Kubernetes replacement
- own foundation model
- fully autonomous SDLC
- complete CD platform
- IDE replacement

## V1 success metrics
- first successful Work: <5 minutes
- GitHub migration median: <15 minutes
- P50 time-to-green improvement: >=50% on qualified workloads
- redundant compute eliminated: >=50% on repeat workloads
- recoverable infrastructure failures automatically recovered: >=80%
- control plane availability target: >99.9%
