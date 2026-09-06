# Evidence contract — works-execution (WORKS)

WORKS is a durable execution plane: Go services + worker + API. There is no
web UI to capture, so the UI-screenshot contract does not apply. Honest
evidence instead:

## Real terminal / execution evidence (2026-09-06)

- `go test -tags=e2e ./e2e/...` — PASS (0.67s)
  - `TestE2E_WorkSucceeds`: submitted real Work `wrk_08acbd6b47c8542183d264bb8487a8ff`,
    worker ran `hello` + `verify` steps, terminal state `SUCCEEDED`, 2 attempts,
    2 artifacts, logs streamed.
- `go test ./...` — 28/28 packages PASS (flaky `TestRunnerAuthz_RegisterMatrix`
  passed in isolation; failure only under parallel load, unrelated to branding).
- Exact HEAD tested: `820bb659cda894707182195d06ac62de58f78bee`.

Full transcript: `v2-audit/evidence/WORKS-E2E-EVIDENCE.md` (agent workspace).

`product-main.webp` (a technical product visual rendered from repository
architecture, **not** a UI screenshot) was removed — no UI surface exists to
replace it with captures, and the visual was redundant with the real
architecture SVGs.

Generated or edited mock UI must never be presented as evidence of
implemented behavior.