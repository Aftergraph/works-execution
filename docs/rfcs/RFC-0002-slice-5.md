# RFC-0002: Slice 5 — Docker Sandbox, BYOC Workers, Real OPA, Pilot CLI

**Status:** IMPLEMENTED (2026-08-31)
**Author:** Hermes Agent (atlas)
**Date:** 2026-08-31
**Track:** Normal (architectural change but contained; no new external dependencies beyond `moby/moby` client + `github.com/open-policy-agent/opa`)
**Depends on:** slices 1–4

## Source

The user asked for "Slice 5" after slice 4. This document scopes that slice against:

- `docs/works-venture-starter-pack/00_START_HERE/90_DAY_EXECUTION_PLAN.md` §Days 61–90 (the third bucket).
- Slice-4 PARTIAL rows in `docs/standards/registry.json` that explicitly call out slice 5+ as the resolution path (e.g. `platform-runtime-isolation` #126, `opa` #96 hand-translation noted as V1-only).
- The user-mandated standards charter, particularly `platform-runtime-isolation`, `platform-universal-compat`, and the **Hermetic Execution Standard** (`platform-hermetic-execution` #111) which the slice-4 implementation marked PARTIAL because we only have env-scrub + default-deny, not full netns.

## Goals

1. **Docker worker sandbox.** `internal/sandbox/docker.go` wraps the existing hermetic core in an OCI `docker run` invocation. Real `image` field on `Node` is honored end-to-end (admission → scheduler → worker).
2. **BYOC worker pool.** `Runner.Runtime` field distinguishes `host` (current subprocess path) from `docker` (sandbox path). Runner self-declares on registration. Scheduler honors both.
3. **Real OPA runtime.** Replace the slice-4 hand-translated Go engine with `github.com/open-policy-agent/opa` v1 SDK. Keep `policies/lease_grant.rego` as the single source of truth.
4. **E2E Docker smoke.** A test submits a Work with `Node.Runtime.Image = "alpine:3.20"`, runs the worker, asserts `SUCCEEDED` with image-pull evidence recorded.
5. **Pilot CLI.** `works-pilot` measures `time-to-first-successful-work` end-to-end against a local API, so design partners (or future us) can objectively measure the pack's `<5 min` goal.

## Non-goals

- **GitHub Actions compatibility (`platform-universal-compat` #120).** Real value requires a GitHub App, OAuth, and a GitHub test fixture. The pack's wedge is conceptually validated by the slice 2 lease protocol; full compat is slice 6+.
- **External security review (`CONTROLS_TO_TESTS.md` PACK fix).** Requires a paid auditor.
- **Paid-beta packaging.** Requires a billing system + Stripe + decisions I shouldn't make.
- **OCI Distribution Spec (`oci-distribution-spec`).** Sliding into slice 6 with the image-registry work.

## Design

### 1. `workgraph.Node.Runtime.Image` and `Runtime.Command`

Currently a node only carries `Run string`. Slice 5 adds:

```go
type Runtime struct {
    Image   string `json:"image,omitempty"`   // OCI image ref; empty => host subprocess
    Command string `json:"command,omitempty"` // exec inside image; default = node.Run
    User    string `json:"user,omitempty"`   // UID inside image; default = 0:0
    Workdir string `json:"workdir,omitempty"` // default = /work
}
```

The admission layer (slice 4's `ValidateAndEnrich`) defaults `Runtime` to `{Command: node.Run}` (host) when the caller doesn't supply it, preserving slice 1+2+3+4 behavior.

### 2. `Runner.Runtime` field

```go
type Runner struct {
    // ... existing fields ...
    Runtime string `json:"runtime"` // "host" | "docker"
}
```

If `runtime=docker`, the scheduler will only assign this runner to nodes with a non-empty `Runtime.Image`. If `runtime=host`, only nodes WITHOUT an image. This is a hard constraint, no soft scoring.

### 3. Docker sandbox `internal/sandbox/docker.go`

```go
// Run executes a node under an OCI container. Pulls the image if not
// present, mounts the work directory + evidence directory, applies
// hermetic defaults (no network unless allow-listed, no extra caps),
// and streams stdout/stderr to the caller's CombinedLog buffer.
func Run(ctx context.Context, image, command string, opts RunOptions) (*Result, error)
```

The Docker daemon is reached via the standard `DOCKER_HOST` env var (defaults to `unix:///var/run/docker.sock`). For tests we use the `testcontainers-go/docker` Go client, but for production the runner talks to a daemon the customer controls. Slice 5 uses the `github.com/moby/moby/client` package (official Docker SDK).

Hard hermetic defaults:
- `--network=none` by default; `Runtime.Network` field overrides with an allow-list
- `--read-only` root filesystem with tmpfs at `/tmp`, `/work`
- `--cap-drop=ALL` then add only what's required (none by default)
- `--security-opt=no-new-privileges`
- `--pids-limit=256`
- `--memory=512m` (overridable via node.Resources)
- `--cpus=2.0` (overridable)
- No `--privileged`, ever

### 4. Real OPA runtime

Slice 4 had a hand-rolled Rego interpreter in `services/api/policy.go` that I replaced after the subagent's interpreter was broken. Slice 5 swaps that for `github.com/open-policy-agent/opa`:

```go
// loadBundle compiles the .rego file and returns an opa.NamedEvaluationEngine
// ready for repeated Evaluate calls.
func loadBundle(src string) (opa.NamedEvaluationEngine, error)
```

The public surface (`Engine`, `NewEngine`, `LoadBundle`, `Evaluate`, `EvaluateOrError`, `Decision`) is preserved so the existing tests, command flags, and API handlers don't change. Only the implementation swaps.

### 5. `works-pilot` CLI

```
$ works-pilot run-demo
Submitting a 2-node work (alpine:3.20 + host) and timing end-to-end.
t=0.00s  POST /v1/works             201
t=0.10s  GET  /v1/works/{id}        202 (CREATED, auto-queued)
t=0.50s  worker picks up node 1
t=2.30s  node 1 done, evidence written
t=2.50s  worker picks up node 2
t=3.10s  node 2 done
t=3.20s  work reached SUCCEEDED

Time-to-first-successful-work: 3.20s
Target (pack §V1 metrics): <5min  ✓
```

The CLI is a measurement tool, not a control plane. It uses the existing `works` CLI for actual submission.

## Risk assessment

- **Docker dependency.** The host must have a Docker daemon reachable. CI environments without Docker (most of them) will skip the e2e docker test. Mark with `//go:build docker` build tag and a runtime check for the daemon.
- **OPA SDK version.** The `open-policy-agent/opa` package has been pre-1.0. We'll pin to a specific version (v0.59.0 or whatever's stable) and document the upgrade path.
- **The "subagent integration collision" problem from slice 4.** Subagents tend to land code that compiles but breaks at integration. I will write the slice-5 subagent dispatches in **two phases of 3 + 2** to bound the integration damage and give me time to reconcile.
- **Slice 5 budget.** Per slice 4 data: ~1,500 LOC per subagent, ~3 subagents per 8-hour WebUI turn. Slice 5 is 5 cards, so budget-allowing 2-3 subagents with manual reconciliation for the rest.

## Test plan

| Layer | Test |
|---|---|
| Unit | Docker sandbox: image pull, run, env scrub, network isolation (where Docker-in-Docker allows), resource limit enforcement |
| Unit | BYOC scheduler: capability matrix (host runner rejects docker node, vice versa) |
| Unit | Real OPA: same 4 tests as slice 4 (TestEngine_NonProductionAllowed, etc.) — proves the swap is behavior-preserving |
| Integration | Live Docker smoke: submit alpine work, walk through state machine, verify SUCCEEDED + evidence recorded |
| Pilot | `works-pilot run-demo` end-to-end against local API |

## Rollout

1. Land `internal/sandbox/docker.go` + tests.
2. Land `workgraph.Node.Runtime` + admission update.
3. Land `Runner.Runtime` + scheduler hard filter.
4. Land real OPA replacement.
5. Land e2e docker smoke.
6. Land `works-pilot` CLI.
7. Verify all gates green.
8. Commit + memory update.

## Rollback

Slice 5 is purely additive. The `Runtime` field is optional on both `Node` and `Runner`. Removing it reverts the codebase to slice 4 behavior in one revert.

## Alternatives considered

- **Pure containerd (no Docker).** Better for k8s, but heavier integration and not where design partners will start. Rejected for slice 5; re-evaluate when we have a k8s path.
- **gVisor (`runsc`).** Strongest sandbox; rejected for slice 5 because it requires kernel support and most design partners don't have it set up.
- **Podman instead of Docker.** Similar to Docker but no daemon. Rejected because Docker is what most design partners have already.