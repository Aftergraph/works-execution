---
on:
  pull_request:
    types: [opened, synchronize, reopened]

permissions:
  contents: read
  copilot-requests: write

engine:
  id: copilot
  model: gpt-5

timeout-minutes: 15

network: defaults

tools:
  bash: ["*"]

safe-outputs:
  add-comment:
    max: 1
---

# Agentic review bound to the pull request head

You are reviewing the pull request that triggered this run. The rule in this
organisation is that a verdict is worth nothing unless it is bound to the exact
commit it was evaluated on, so every claim you make here names that commit.

## What to do

1. Record the exact head commit of this pull request in full. Everything you
   report below is about that commit and no other.
2. Work out how this repository is tested by reading what is actually there,
   such as `package.json`, `pyproject.toml`, `Makefile`, `go.mod`, or the
   existing workflows under `.github/workflows/`. Install dependencies and run
   the suite the repository itself uses. Do not assume a toolchain.
3. Read the diff. Look for changes that let a claim outrun its evidence: a
   result carried over from a different commit, a check treated as present when
   it reports another head, a failure downgraded to a pass, an error swallowed,
   or a guard removed without a replacement.

## What to report

Post a single comment with the exact head commit you evaluated, the test result
as counts of passing and failing tests rather than an adjective, each finding
with the file and line it sits on, and an explicit list of anything you could
not verify.

Never report a pass you did not observe. If the suite did not run to completion,
say that instead of producing a verdict. An unverified claim is a worse outcome
than no claim.
