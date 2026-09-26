# Reliability before/after proof — 2026-09-26

## Compared revisions

- BEFORE: `89537cc376228c6eff42855d2be9e567e22eabe4`
- AFTER:  `d99a5c305562eb2e61d255bf8cc81f45e95d5a70`
- Controlled run: GitHub Actions run `36256994536`

The same black-box HTTP fault probe was copied into both exact revisions and
executed on the same GitHub-hosted runner.

## Controlled recovery matrix

| Probe | BEFORE | AFTER |
| --- | --- | --- |
| lost acknowledgement / duplicate idempotency key | FAIL (409) | PASS (200 canonical replay) |
| replay after admission-policy drift | FAIL (400 admission rejection) | PASS |
| omitted vs explicit acceptance-time defaults | FAIL (409) | PASS |
| replay returns current canonical state | FAIL (409) | PASS |
| queue escalation fails closed | PASS | PASS |

Summary:

- all probes: **1/5 -> 5/5**
- recovery-specific probes: **0/4 -> 4/4**
- safety control (queue escalation): **PASS -> PASS**

This is a deterministic invariant matrix, not a statistical estimate of all
production failure probability.

## API microbenchmarks

Three one-second Go benchmark samples were run for each exact revision.

### Normal unique create path

Median:

- BEFORE: **776,566 ns/op**
- AFTER: **832,015 ns/op**
- latency delta: **+7.14%**

Allocation median / stable count:

- bytes/op: **22,325 -> 30,303** (**+35.74%**)
- allocs/op: **284 -> 353** (**+24.30%**)

This is the direct hot-path overhead introduced by the stronger durable
reconciliation metadata/checking.

### Duplicate/idempotent pair

Median:

- BEFORE: **1,056,825 ns/op**
- AFTER: **1,816,926 ns/op**
- latency delta: **+71.92%**

But these two values do **not** represent equal semantics:

- BEFORE: **100% conflict409, 0% replay200**
- AFTER: **0% conflict409, 100% replay200**

The AFTER path performs successful canonical lookup/reconciliation and returns
the durable Work instead of failing fast.

Allocation median / stable count:

- bytes/op: **40,319 -> 82,332** (**+104.20%**)
- allocs/op: **535 -> 1,284** (**+140.00%**)

## Full GitHub CI timing

Push-run wall-clock timestamps for the exact revisions:

| Workflow | BEFORE | AFTER | Delta |
| --- | ---: | ---: | ---: |
| Go tests | 127 s | 138 s | +8.66% |
| CodeQL | 158 s | 150 s | -5.06% |

The Go test increase includes the larger post-change test surface; it is not a
clean runtime-only benchmark.

## Interpretation

The measured change is primarily a **reliability trade** rather than a raw
speed optimization:

- deterministic recovery coverage changed from 0/4 to 4/4;
- the existing queue-escalation safety invariant stayed green;
- normal create latency increased about 7% in this microbenchmark;
- reconciliation is materially more expensive because it now succeeds rather
  than terminating with a fast conflict.

No claim is made here that global ChatGPT Work stop probability fell by a
specific percentage. That requires longitudinal production telemetry. This
artifact proves the narrower Aftergraph property: the tested controller/lost-
ack recovery classes changed from unrecoverable to recoverable at the measured
cost above.
