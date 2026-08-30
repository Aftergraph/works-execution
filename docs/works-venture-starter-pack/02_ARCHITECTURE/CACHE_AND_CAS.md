# Cache & Content-Addressed Storage

## Principle
Cache computation only when equivalence can be proven.

## Fingerprint inputs
- source tree/content hashes
- declared dependency state
- command/action identity
- environment/toolchain
- declared inputs
- relevant configuration
- executor version

## Layers
- L1 worker-local cache
- L2 organization cache
- L3 artifact/CAS store

## Security
- immutable cache objects
- tenant namespace isolation
- signed metadata
- permissioned read/write
- conservative invalidation
- no plaintext secrets in cache keys/artifacts

## Correctness > hit rate
A false cache hit is a product-integrity failure.
