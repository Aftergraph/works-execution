# System Architecture

## Layers
1. Experience: CLI, Web, API, SDK, integrations.
2. Control: Work Service, Graph Engine, Scheduler, Policy, Evidence.
3. Execution: Worker Gateway, Worker Runtime, sandbox/container/process executors.
4. Data: PostgreSQL metadata, queue/event system, artifact/CAS object storage.
5. Intelligence: historical duration/cost models and later failure intelligence.

## Source of truth
The Control Plane owns Work state. Workers hold leases and execution-local state only.

## Core services
- API Gateway
- Auth/Identity
- Work Service
- Graph Engine
- Scheduler
- Worker Gateway
- Artifact/CAS Service
- Evidence Service
- Audit/Metering
- Integration Service
- Web application

## Failure principle
Every service must define safe degraded behavior. Analytics failure must not stop execution. State-store failure must stop unsafe new execution rather than fabricate success.
