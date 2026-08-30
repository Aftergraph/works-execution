# Threat Model

## Assets
Source code, secrets, artifacts, deployment credentials, customer metadata, cache contents, evidence, worker identity.

## Trust boundaries
- public internet -> API
- control plane -> worker
- worker -> untrusted repository code
- tenant -> shared services
- integration provider -> event ingestion
- artifact store -> execution runtime

## Priority threats
1. Malicious PR exfiltrates secrets.
2. Compromised worker impersonates trusted executor.
3. Cross-tenant cache poisoning/read.
4. Artifact substitution.
5. Replay of execution credentials.
6. Log-based secret leakage.
7. Untrusted fork receives privileged authority.
8. Supply-chain compromise of worker binary.
9. SSRF/network abuse from executed code.
10. Privilege escalation from container/process sandbox.

## Security acceptance
Privileged production execution is blocked until the relevant trust path has explicit policy, audit and revocation.
