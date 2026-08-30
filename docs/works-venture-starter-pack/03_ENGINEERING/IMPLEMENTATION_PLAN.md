# Implementation Plan

## Recommended initial stack
- Go: control plane and worker runtime
- PostgreSQL: durable metadata/state
- S3-compatible object storage: artifacts/CAS
- TypeScript + React: web product
- HTTP public API
- gRPC/streaming protocol for worker control where justified

## Milestones
1. Core schemas and state machine.
2. Local Work executor.
3. PostgreSQL persistence.
4. Worker registration/identity.
5. Heartbeat and leases.
6. Scheduler.
7. Lost-worker recovery.
8. Logs.
9. Artifacts.
10. Fingerprints/cache.
11. GitHub integration.
12. Real-repository alpha.

## Engineering rule
No milestone is complete without tests, telemetry, failure behavior and documentation.
