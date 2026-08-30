# SLOs & SRE

## Initial SLO targets
- Control plane availability: 99.9%
- Work creation P95: <500 ms excluding external auth
- Eligible node scheduling P95: <1 s under normal load
- Lost worker detection target: <30 s
- Acknowledged audit-event loss: 0

## Degraded modes
- Analytics unavailable: execution continues.
- Remote cache unavailable: run uncached.
- Intelligent scheduler unavailable: deterministic fallback.
- Artifact store unavailable: nodes requiring durable artifacts fail closed.
- Authoritative state store unavailable: unsafe new execution stops.
