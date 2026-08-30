# ADR-0006: Brand and module path

**Status:** Accepted
**Date:** 2026-08-31

## Decision

The working brand for this venture is **`works-execution`**.

- **GitHub repo:** `github.com/JonasAbde/works-execution`
- **Go module path:** `github.com/JonasAbde/works-execution`
- **CLI binary name:** `works`
- **Worker binary name:** `works-worker`
- **Internal references in code, docs, and ADRs:** `works-execution`

## Rationale

- The source pack (`docs/works-venture-starter-pack/`) uses `WORKS` as a working title only. `12_LEGAL_CHECKLIST/COMPANY_AND_LEGAL_START.md` flags trademark clearance as an open item that requires lawyer review.
- The pack's central thesis — quoted verbatim from `01_PRODUCT/PRODUCT_SPEC_V1.md` — is "minimize the time, compute, and human intervention required to turn software intent into a verified result." The category name proposed in the pack's PDF report is "Autonomous Software Execution Infrastructure."
- `works-execution` preserves the `works` family name the pack uses internally while adding the differentiator that matches the thesis.
- The candidate was checked against `github.com/JonasAbde/` and was available at the time of the decision (HTTP 404 on the public repo API).
- `works` alone would collide with the dozens of unrelated OSS projects using that name and is unlikely to clear trademark review.

## Consequences

- All Go imports use `github.com/JonasAbde/works-execution/...`.
- The CLI binary keeps the short `works` name (common in developer CLIs; brand-agnostic).
- Marketing material, the eventual website, and trademark filings will use `works-execution` (or whatever final brand clears legal review).
- If a different final brand is chosen later, this ADR is superseded; the Go module path is the most expensive to change and should only be changed if trademark review forces it.

## Open follow-ups

- [ ] File a placeholder trademark search with a Danish/EU IP lawyer (per `COMPANY_AND_LEGAL_START.md`).
- [ ] Reserve the matching `works-execution.dev` (or chosen TLD) domain if it is available.
- [ ] Decide on a final brand name by the end of Day 30 of the 90-day plan (per `00_START_HERE/90_DAY_EXECUTION_PLAN.md`).
- [ ] If the brand changes, file an ADR-0007 superseding this one and migrate the module path in a single PR.

## Rollback

Until the first public release, the brand is reversible by renaming the GitHub repo (GitHub redirects for moved repos). After the first release, treat as a major-version event.