# Changelog

## [G1] — Evidence integrity-hash (2026-09-05)
- feat: Evidence.Hash + Seal() + RecomputedHash() + HashValid() + VerifyEvidence()
- Deterministisk SHA-256 over identitet+udfald; Details ekskluderet
- Tom Hash = unsealed (legacy) — consumers viser 'unsealed' ærligt
- ci: go-test workflow (build+vet+test) — CI grøn
- evidence_hash_test.go: determinism + 9 felt-ændringer + Details-eksklusion + tamper

## [G2 — evidence-hash ved creation-paths (tamper-detektion fra birth)]

### feat: evidence fødes med integrity-hash

- worker.go (CompleteLease build-evidence), leases.go (classifier
  self-healing policy-evidence), takeover.go (takeover_event): hver
  evidence fødes med integrity-hash via Seal().
- evidence_hash_test.go: G2 Seal-protokol-test (3 creation-paths) +
  de 4 G1-tests gjenoprettet (var overskrevet ved uheld).
- CI GRØN (go build + vet + test verificerer).

### hvorfor

Tamper-detektion hidtil kun ved hash-verifikation efter Seal(); en
evidence kunne fødes uden hash (tom indtil Seal). G2 lukker hullet:
hver evidence fødes med integrity-hash — tamper-detektion fra birth.

## [G3 — hash-verify-badge i webui]

### feat: evidence viser ID + verify-verdict

- evidenceView: ID + Verify (workgraph.VerifyEvidence — kilde til sandhed).
- Template: [hash ok] grøn / [TAMPERED] rød fette / [unsealed] grå per item
  + evidence-ID vist.
- Fail/tamper ALDRIG skjult (F2-loven på WORKS-siden).
- CI GRØN.
