# RFC-0007: Web Execution View

**Status:** IMPLEMENTED (2026-08-31, v2: live)
**Author:** Hermes Agent (atlas)
**Date:** 2026-08-31
**Track:** Normal
**Supersedes:** none
**Related:** RFC-0006 (self-hosted CI), RFC-0004 (BYOC pools)

## Problem

WORKS is a control plane, but before this RFC it had no human-facing
surface: everything was CLI + API. A design partner onboarding onto
WORKS needs to *see* their pipeline — works, runners, logs, evidence —
without installing anything. The starter pack's days 31–60 plan called
for a minimal web execution view; it was the last uncovered item.

## Decision

A **server-rendered, read-only HTML view** at `/v1/ui` — no build step,
no JS framework, no write surface. v1 (commit `caa6258`) was a static
dark GitHub-style dashboard; v2 (commit `fd4fb03`) made it **live** via
SSE with a vanilla-JS fragment swap.

## Design

### 1. Pages

| Route | Content |
|---|---|
| `GET /v1/ui` | live work list (SSE-driven) |
| `GET /v1/ui/works/{id}` | work detail: DAG nodes with state + log tail, attempts, evidence |
| `GET /v1/ui/runners` | runner pool + heartbeat liveness |
| `GET /v1/ui/fragments/{works,runners,work/{id}}` | HTML fragments for live swap |
| `GET /v1/ui/events` | SSE live event stream |

### 2. SSE live updates (`services/api/webui_events.go`)

`GET /v1/ui/events` streams server-sent events so the UI updates without
polling or full reloads:

```
event: work
data: {"id":"wrk_...","state":"SUCCEEDED","repo":"...","type":"github_push"}

event: runner
data: {"id":"wrkr_...","pool":"avc-core","stale":false,...}

event: ping
data: {"t":1693500000}
```

Delivery model: every `tick` (2s) the handler snapshots the work list
and runner pool, diffs against the previous snapshot, and emits only
changed records. A `ping` keeps proxies' connections open when nothing
changed; clients reconnect automatically via `EventSource`. The first
tick emits the full world so a fresh client paints immediately.
`X-Accel-Buffering: no` passes through nginx/Cloudflare buffering.

### 3. Client

Vanilla JS: `EventSource` → fragment swap for `works`/`runners`/`work/{id}`
rows, ticking ages, copy buttons. Design tokens, `sr-only`/`aria`/
`focus-visible`/`reduced-motion` for a11y. No framework, no build step.

### 4. Auth

`/v1/ui` requires a Bearer token **unless** `WebUIConfig.Public` is set.
Prod runs `WORKS_WEBUI_PUBLIC=true` (read-only pages); set it false +
token for private deployments.

## Verification (production VDS, 2026-08-31)

- 200 on all pages locally and via `works.rendetalje.dk` (Cloudflare
  tunnel).
- SSE stream confirmed live: `: stream open` + `event: work` frames.
- k-impl-031 done; `badgeFor` template panic (21:05) fixed in `fd4fb03`
  and prod binary rebuilt 21:07.

## References

- `services/api/webui.go`, `services/api/webui_events.go`
- `cmd/works-api/main.go` (WebUIConfig wiring)
- `docs/kanban/board.json` k-impl-031
