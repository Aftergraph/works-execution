# Runbook — Disposable controller / durable Work continuity

## Purpose

A ChatGPT Work session, terminal, browser, mobile client, or other coordinator is
a **disposable controller**. It must not be the source of execution truth for a
long-running Aftergraph mission.

The continuity invariant is:

```text
controller lifetime != accepted Work lifetime
```

A controller disconnect is therefore a control-surface event, not a mission
failure.

## Scope

This runbook covers controller loss **after WORKS has durably accepted a Work**.
It does not claim to prevent ChatGPT Work, a network client, or another UI from
stopping before the mission has been submitted.

That distinction is load-bearing:

```text
before durable accept  -> plan can still be lost with the controller
after durable accept   -> WORKS owns canonical execution state
```

For long-horizon or consequential work, submit to WORKS before the first
material execution step.

## Required protocol

1. Materialize the Work/Mission definition.
2. Submit it to WORKS and require a successful durable accept plus canonical
   `work_id`.
3. Treat `work_id` as the resume handle. Do not use chat history as the
   canonical checkpoint.
4. The controller may now disconnect without terminating the accepted Work.
5. On reconnect, create a fresh client and read the canonical Work state before
   issuing any mutation.
6. If the Work is `QUEUED` or `RUNNING`, attach/observe; do not resubmit.
7. If it is terminal, inspect evidence and acceptance criteria before declaring
   completion.
8. If it is `WAITING_HUMAN` or `SUSPENDED`, resume only through the existing
   authority/checkpoint seam.
9. If the observed state is stale, indeterminate, revoked, or budget-exhausted,
   fail closed and reconcile; never bypass the boundary with a blind retry.

## ChatGPT Work usage

For Aftergraph frontier builds, ChatGPT Work should be a steering surface:

```text
ChatGPT Work / client
        |
        | submit / inspect / steer
        v
WORKS durable Work + SQLite state
        |
        v
lease / worker / runner
        |
        v
evidence + verifier
```

Direct execution in an ephemeral Work container is acceptable for bounded
inspection/bootstrap work. It is not the durable substrate for a long-running
mission whose progress must survive a UI/session termination.


## Submission ambiguity: lost acknowledgement

The controller must mint and persist a stable idempotency key **before** the
first submission. `POST /v1/works` now uses that key as a reconciliation
handle:

```text
POST accepted by WORKS
  -> response/connection lost
  -> replacement controller retries same key + same immutable intent
  -> 200 + X-Works-Idempotent-Replay:true
  -> canonical existing work_id
```

A changed immutable Work intent under the same key returns `409
idempotency_conflict`. Replay returns the Work's **current** canonical state.
The one repair action allowed during replay is closing a proven partial-submit
seam: when the original request carried `queue:true` and the durable Work is
still exactly `CREATED`, WORKS completes `CREATED -> QUEUED`. Any later state
is left untouched; replay never moves state backwards.

This closes the ambiguous-ack gap after server acceptance. It cannot recover a
mission definition that existed only in volatile controller memory and never
reached WORKS, so durable mission intent still has to exist before execution.

## Authentication continuity

Production `works run` uses the existing WORKS bearer/enrollment boundary.
When an enrollment secret is available, resilient submission attaches the
bearer token on every attempt and handles one definitive `401` by re-enrolling
and retrying.

This matters because the current dev-mode HMAC issuer intentionally rotates its
signing key when `works-api` restarts, invalidating outstanding tokens.

```text
valid CLI token
  -> works-api restarts / issuer rotates
  -> POST receives 401 before mutation
  -> CLI re-enrolls once
  -> retries the same submission
  -> canonical Work accepted/reconciled
```

A `401` retry is safe even without an idempotency key because WORKS rejects
the request in authentication middleware before the mutating handler runs.
Transport/read ambiguity and 429/5xx remain retryable **only** with a stable
idempotency key.

## Recovery rule

After any controller/UI interruption:

```text
reconnect
  -> read canonical Work by work_id
  -> inspect state + attempts + evidence
  -> reconcile actual state
  -> continue only from that state
```

Never translate "the controller stopped" directly into "rerun the last command".
The last command may already have committed an effect.

## Proof

`tests/reliability/controller_loss_test.go` exercises the real HTTP router,
real SQLite store, lease reaper, and real worker path.

The test:

1. starts WORKS without a worker;
2. controller A submits a Work and receives a durable `work_id`;
3. controller A disconnects;
4. only then does a worker come online;
5. controller B reconnects as a fresh client;
6. the same Work must reach `SUCCEEDED`;
7. exactly one attempt must exist;
8. the execution log must contain the expected effect marker.

This proves the Aftergraph property:

```text
accepted Work + controller loss -> execution can continue without that controller
```

It does **not** prove that the ChatGPT product itself will never stop a Work
session. The design goal is to reduce the impact of such stops from mission
failure to reconnect/reconcile.

## Reliability metrics

Track at least:

- `controller_loss_survival_rate`
- `resume_success_rate`
- `duplicate_attempt_rate`
- `duplicate_effect_rate`
- `unrecovered_failure_rate`
- `mean_time_to_reconcile`

For deterministic controller-loss injection, acceptance is:

```text
controller_loss_survival_rate = 100%
duplicate_effect_rate         = 0
state_corruption_rate         = 0
```

Product-level ChatGPT Work stop frequency must be measured separately; WORKS
cannot control that upstream failure rate.
