# Changelog

## [G1] — Evidence integrity-hash (2026-09-05)
- feat: Evidence.Hash + Seal() + RecomputedHash() + HashValid() + VerifyEvidence()
- Deterministisk SHA-256 over identitet+udfald; Details ekskluderet
- Tom Hash = unsealed (legacy) — consumers viser 'unsealed' ærligt
- ci: go-test workflow (build+vet+test) — CI grøn
- evidence_hash_test.go: determinism + 9 felt-ændringer + Details-eksklusion + tamper
