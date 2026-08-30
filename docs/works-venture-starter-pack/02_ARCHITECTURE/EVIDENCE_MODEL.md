# Evidence Model

## Purpose
A green status is not enough. WORKS records why a result is considered verified.

## Evidence types
- build
- test
- typecheck
- lint
- security scan
- artifact
- deployment
- policy/approval

## Evidence record
Must bind:
- Work and Node
- source revision
- attempt
- environment fingerprint
- result
- timestamp
- artifact/hash references
- signer/provenance where applicable

## Future
Evidence becomes an input to deciding the minimum additional verification required for a new Work.
