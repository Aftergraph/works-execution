# WORKS API promotion — Runtime computer placement

Date: 2026-10-06

## Purpose

Promote the current canonical WORKS main to the production `works-api.service`
so Runtime governed computer placement can consume the merged WORKS-owned
platform boundaries.

The required implementation is already on main, including:

- platform Work creation;
- platform-only atomic `GrantPlacementLease`;
- `CREATED -> QUEUED -> RUNNING` committed with Attempt + WorkerLease;
- placement reservation binding to the Runtime-selected worker;
- dispatch acceptance and canonical execution-context ownership remaining in WORKS.

## Promotion contract

This file intentionally carries no secrets and changes no authority semantics.
The merge commit is eligible for the existing one-shot deployment only when its
subject includes `[deploy-works-api]`.

The native `works.yml` pipeline must execute
`scripts/ops/deploy-works-api-once.sh`, which independently requires:

- exact executing SHA equals current `origin/main`;
- non-interactive privileged operator authority;
- serialized deployment lock;
- reviewed binary revision/hash equality;
- rollback copy before replacement;
- post-restart health;
- Integrity Fabric smoke;
- durable deployment receipt.

A skipped or failed promotion is not deployment evidence.
