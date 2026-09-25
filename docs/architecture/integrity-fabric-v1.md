# Integrity Fabric v1

Status: implementation candidate in PR #143. Promotion requires green tests, controlled-host benchmark evidence, merge, and post-merge verification.

## Purpose

Move evidence identity from a SHA-256-only implementation detail to an algorithm-agile integrity contract while preserving the existing `bundle_id` and N-1 readers.

A digest proves byte identity. It does **not** prove actor identity, authority, provenance, or verified outcome; those remain separate evidence/signature/verifier responsibilities.

## Before

```mermaid
flowchart TD
    W[Terminal Work] --> B[Evidence Bundle]
    B --> C[Aftergraph canonical JSON]
    C --> S[SHA-256]
    S --> ID[evb_ + first 128 bits]
    C --> H[HMAC-SHA256]
    H --> SIG[Signature record]
    ID --> V[Verifier]
    SIG --> V
```

### Before modules

| Module | Before |
|---|---|
| Content identity | Implicit SHA-256 |
| Bundle ID | `evb_` + 32 hex chars from SHA-256 |
| Digest metadata | None |
| Digest scope | Implicit |
| Alternate digest | None |
| Large-object/tree-ready scope | None |
| Canonicalization binding | Comment/code convention |
| Signature | HMAC-SHA256 over canonical projection |
| Compatibility | SHA-256 only |
| Performance evidence | No dedicated digest benchmark |

## After

```mermaid
flowchart TD
    W[Terminal Work] --> B[Evidence Bundle]
    B --> P[Immutable subject projection]
    P --> C[aftergraph-json-canonical/1]
    C --> S[SHA-256 primary]
    C --> B3[BLAKE3-256 alternate]
    S --> DS[DigestSet]
    B3 --> DS
    DS --> IE[IntegrityEnvelope]
    S --> ID[Legacy-compatible evb_ ID]
    IE --> E[Final signable envelope]
    ID --> E
    E --> H[HMAC-SHA256]
    H --> SIG[Signature record]
    E --> V[Independent verifier]
    SIG --> V
    V --> G{All gates valid?}
    G -->|yes| OK[Integrity valid]
    G -->|no| FAIL[Fail closed]
```

### After modules

| Module | After |
|---|---|
| `DigestAlgorithm` | Explicit `sha256` / `blake3` |
| `DigestScope` | `raw-bytes`, `canonical-object`, `merkle-root` |
| `DigestRef` | Algorithm + scope + encoding + value |
| `DigestSet` | SHA-256 primary + alternate digests |
| `IntegrityEnvelope` | Canonicalization + subject scope + DigestSet |
| Bundle ID | Existing SHA-256-derived `evb_` contract preserved |
| Signature | Covers the final envelope including Integrity metadata |
| Verifier | Validates bundle ID + every digest + signature + correlation |
| Compatibility | N-1 bundles without Integrity remain readable |
| Performance evidence | Same-run comparative benchmark in canonical Go CI + WORKS-native post-merge gate |

## Invariants

1. `bundle_id` remains SHA-256-derived for interoperability.
2. Every digest declares its algorithm, encoding, and semantic scope.
3. All members of a DigestSet hash the exact same subject bytes and scope.
4. Integrity metadata and signatures are excluded from the subject digest to prevent self-reference.
5. The final signature authenticates the IntegrityEnvelope.
6. A present IntegrityEnvelope is fail-closed: any malformed/missing required BLAKE3 alternate or mismatch invalidates verification.
7. Legacy bundles without Integrity remain N-1 compatible.
8. Digest validity never upgrades execution success to verified outcome.

## Performance comparison protocol

PR benchmarks execute as same-run comparative evidence on one Linux/x64 GitHub Actions host. Final promotion still requires WORKS-native main-branch dogfood and post-merge runtime verification; hosted-runner timings are comparative evidence, not production-performance evidence.

Measured sizes:

- 1 KiB
- 1 MiB
- 16 MiB

Compared paths:

- **Before:** SHA-256 only
- **Native alternate:** BLAKE3 only
- **After:** DigestSet = SHA-256 + BLAKE3

Metrics:

- ns/op
- MB/s
- B/op
- allocs/op
- dual-digest overhead vs SHA-256-only
- BLAKE3 throughput multiplier vs SHA-256

### Measured PR result

Exact candidate: `542b4e6a8e39dd3ccedb3b30dbb14c7bfb133c1b`  
CI: Go tests run 316, PASS; CodeQL run 295, PASS.  
Benchmark host: Linux/x64, 4 vCPU, Intel Xeon 6973P-C, Go 1.25.0. Five samples per case; values below use the median.

| Path | Before / reference | After / alternate | Change |
|---|---:|---:|---:|
| Full 64 KiB evidence integrity pipeline | 428,117 ns/op | 450,072 ns/op | **+5.13% latency** |
| Full pipeline bytes allocated | 239,449 B/op | 249,682 B/op | **+4.27%** |
| Full pipeline allocations | 359 allocs/op | 492 allocs/op | +37.0% |
| 1 MiB SHA-256 | 521,440 ns/op | — | 2.01 GB/s class |
| 1 MiB BLAKE3 | — | 244,088 ns/op | **2.14× faster** than SHA-256 |
| 16 MiB SHA-256 | 8,497,398 ns/op | — | ~1.97 GB/s |
| 16 MiB BLAKE3 | — | 4,104,860 ns/op | **2.07× faster** than SHA-256 |
| 16 MiB dual DigestSet | 8,497,398 ns/op SHA-only | 12,458,957 ns/op | +46.62% hashing cost for two digests |

Interpretation: adding a second cryptographic identity nearly doubles raw hashing work, but the real evidence pipeline is dominated by canonicalization/serialization; after replacing the second full canonicalization with domain-separated streaming HMAC framing, the measured end-to-end latency overhead is ~5.1%, not ~90% as in the first implementation attempt.

The 1 KiB microbenchmark is intentionally not used to claim BLAKE3 superiority: on this host BLAKE3 had higher fixed overhead than SHA-256 at 1 KiB. The advantage appears on larger payloads.

Raw benchmark output is preserved in the successful CI job log for run 316.

## Rollout

```mermaid
flowchart LR
    PR[PR #143] --> T[Tests/Vet/Build]
    T --> B[Controlled benchmark]
    B --> Q[CodeQL + Sentinel]
    Q --> M[Merge main]
    M --> D[WORKS live-main/dogfood]
    D --> P[Post-merge verification]
```

No live/deployment claim is made before the final post-merge verification.
