# Kanban Board — works-execution Standards & Slice 3

This directory holds the version-controlled kanban board for the works-execution venture's
standards-alignment and slice-3 workstreams. The board is JSON (`board.json`) so it's both
human-readable and machine-queryable. It covers the scope of the user-mandated 130 standards
plus 22 new internal standards.

## Conventions

- **Columns**: `backlog → ready → in_progress → review → done`.
- **Lanes** (the `.lanes` object) group cards by work type:
  - `registry` — standards registry, mappings, gap analysis (no code change).
  - `infrastructure` — schema, validators, CI gates, kanban CLI.
  - `implementation` — code that enacts a specific standard.
  - `governance` — agent declarations, audit events, evidence bundles, compliance posture.
  - `verification` — tests, chaos, conformance, attestation verification.
- **Card IDs**: `k-<short-hash>`. Stable across moves.
- **Standards linkage**: every implementation card carries a `standard_ids` array pointing
  to one or more rows in `docs/standards/registry.json`.
- **Acceptance criteria**: cards MUST carry explicit `acceptance` text. A card moves to
  `done` only when acceptance is met and `evidence` field has at least one pointer
  (test path, command output, document, or external audit reference).
- **BLOCKED items**: cards in `BLOCKED` state (a card property, not a column) carry
  a `blocked_reason` field explaining the external dependency (audit, vendor,
  certification) and a `unblock_check` describing what would change to remove the block.

## Inspection

```bash
# Show board as a markdown summary
make kanban
# or
python3 scripts/kanban.py summary

# Show by lane
python3 scripts/kanban.py lane implementation

# Show one card
python3 scripts/kanban.py card k-xxxxxx

# Move card
python3 scripts/kanban.py move k-xxxxxx in_progress
```