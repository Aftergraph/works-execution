# Test Strategy

## Unit
- state transitions
- graph validation
- fingerprint calculation
- scheduler eligibility/scoring
- policy evaluation

## Integration
- API -> DB
- scheduler -> worker gateway
- worker -> artifact store
- GitHub event -> Work -> check result

## Conformance
Every worker implementation runs the same protocol/execution conformance suite.

## Chaos
- kill worker mid-node
- duplicate result delivery
- delayed heartbeat
- queue redelivery
- artifact timeout
- DB failover
- cache unavailable

## Security
- malicious fork
- secret masking
- cross-tenant cache access
- replayed credential
- forged worker
- artifact substitution

## Performance
P50/P95/P99 scheduling, log streaming, artifact transfer and Work throughput.
