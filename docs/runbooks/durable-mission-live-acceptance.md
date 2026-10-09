# Durable mission live acceptance

A release candidate is not considered production-ready until the following live proof is recorded against an exact commit SHA:

1. Start WORKS API and worker from the candidate build.
2. Submit a mission with `works mission run` without `--follow` and allow the CLI/coordinator to exit.
3. Prove the Work continues to terminal success and emits expected evidence/artifacts.
4. Submit a second mission whose consequential stage has a read-only `reconcile` check.
5. Kill the executing worker after the external side effect but before durable completion.
6. Allow the lease to expire and start a replacement worker.
7. Prove the replacement reconciles external state, skips replay of the side effect, and reaches terminal success.
8. Verify the side effect occurred exactly once and bind the report to the candidate SHA.

Revocation and budget exhaustion are not recovery cases and must remain contained.
