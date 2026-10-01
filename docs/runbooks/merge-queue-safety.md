# Merge queue safety

The `main` branch uses GitHub merge queue. Queue membership alone is **not** a
verification policy: the queue must have required checks configured, and the
corresponding GitHub Actions workflows must run for the synthetic merge-group
commit.

## Invariants

1. `Go tests` and `CodeQL` trigger on both `pull_request` and
   `merge_group: checks_requested`.
2. The repository ruleset targeting `refs/heads/main` must contain a
   `required_status_checks` rule for the queue checks. A merge-queue rule by
   itself is insufficient.
3. No PR is considered verified from an earlier commit SHA. The latest head (or
   the merge-group SHA when queued) is the evidence subject.
4. Auto-merge is enabled only after the PR-head checks are green; the queue then
   re-verifies the synthetic merge-group commit.
5. A failing or absent merge-group check must prevent promotion to `main`.

## Current repository ruleset

Repository ruleset `merge-queue-main` (id `22410181`) currently owns the
merge-queue rule. After the 2026-10-01 incident, an administrator must ensure the
same main-targeting ruleset (or an additional active main ruleset) explicitly
requires the check contexts emitted by:

- `Go tests / test`
- `CodeQL / Analyze (go)`

Use GitHub's ruleset UI or Rulesets REST API with repository administration
permission. Verify the effective ruleset after the change; do not infer success
from the UI save action alone.

## Verification

For any queue-safety change:

1. Open a PR whose workflows pass on the PR head.
2. Add it to the merge queue.
3. Confirm new workflow runs are created with event `merge_group` for the
   queue SHA.
4. Confirm both Go tests and CodeQL succeed on that merge-group SHA.
5. Only then accept the resulting `main` commit as promoted.

The regression test in `tests/ci/merge_queue_test.go` prevents removal of the
`merge_group` triggers from the two repository workflows.
