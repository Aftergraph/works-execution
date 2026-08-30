# Worker Protocol Draft

## Session lifecycle
1. Worker authenticates.
2. Capabilities are registered.
3. Persistent session established.
4. Heartbeats report health/capacity.
5. Control plane grants a lease.
6. Worker acknowledges lease.
7. Worker streams state/log events.
8. Worker uploads artifacts/evidence.
9. Worker submits terminal attempt result.
10. Control plane commits authoritative transition.

## Required messages
- HELLO
- CAPABILITIES
- HEARTBEAT
- LEASE_OFFER
- LEASE_ACCEPT
- LEASE_REJECT
- NODE_STARTED
- LOG_CHUNK
- ARTIFACT_READY
- NODE_RESULT
- CANCEL
- DRAIN

## Protocol requirements
Version negotiation, replay protection, correlation IDs, monotonic attempt sequence and bounded message sizes.
