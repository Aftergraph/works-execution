# Identity & Authorization — Per-Standard Mapping

**Document ID:** `works-standards-identity-mapping`
**Venture:** works-execution (`github.com/JonasAbde/works-execution`)
**Generated:** 2026-08-31
**Slice context:** Slice 1 (`d3db1d1`) shipped the `Work` primitive, SQLite store, HTTP API, CLI, and polling subprocess worker. Slice 2 (`dab84f2`) added lease-based scheduling, worker-loss recovery, and log streaming. Slice 3 (this slice) introduces worker enrollment, federated identity, and role-based authorization.
**Companion documents:**
- `docs/standards/registry.json` — authoritative machine-readable registry (130 rows)
- `docs/standards/mappings/identity.md` — this document
- `docs/kanban/board.json` — Slice 3 work tracker (kanban for standards-alignment cards)
- `docs/works-venture-starter-pack/04_SECURITY/SECRETS_AND_IDENTITY.md` — pack rules cited below
- `docs/works-venture-starter-pack/04_SECURITY/SECURITY_BASELINE.md` — Zero-Secret mandate

---

## §14 Implementation Rule (binding)

Every standard in this document is processed through the five-step rule from the user-mandated standards charter:

1. **Determine applicability** — is this standard in-scope for works-execution V1?
2. **Map to system requirements** — which concrete component, contract, or test enforces it?
3. **Identify gaps** — what is missing today (Slice 1 + Slice 2)?
4. **Prioritize by risk and leverage** — score each gap on (risk-of-omission × leverage-on-platform-correctness).
5. **Recommend highest-value actionable gap with file path** — the next concrete change, where it lands, and the acceptance evidence.

---

## §1. Scope and deduplication

The registry contains **14 rows** in the `identity` domain. After deduplication and not-applicable filtering, **11 active standards** remain in scope:

| # | Standard | Registry row | Status (today) |
|---|---|---|---|
| 1 | OpenID Connect | `openid-connect` | PLANNED |
| 2 | SPIFFE (umbrella) | `spiffe` | PLANNED |
| 3 | SPIRE | `spire` | PLANNED |
| 4 | OAuth 2.0 | `oauth-2.0` | PLANNED |
| 5 | OAuth 2.1 | `oauth-2.1` | PLANNED |
| 6 | SPIFFE ID format | `spiffe-id` | PLANNED |
| 7 | SPIFFE Workload API | `spiffe-workload-api` | PLANNED |
| 8 | JWT | `jwt` | PLANNED |
| 9 | X.509 | `x509` | PLANNED |
| 10 | RBAC | `rbac` | PLANNED |
| 11 | ReBAC | `rebac` | PLANNED |
| 12 | ABAC | `abac` | PLANNED |
| 13 | Zero Trust | `zero-trust` | PARTIAL (bundled with NIST SP 800-207 row) |

**Excluded from this document (with reason):**

- `openid-connect-core` — registry carries `status: NOT_APPLICABLE` (`exceptions: ["Duplicate entry."]`). Folded into the `openid-connect` row below. Track a registry cleanup ticket separately.

> **Naming convention:** "OIDC" = OpenID Connect; "WLA" = SPIFFE Workload API; "SVID" = SPIFFE-Verified Identity Document (X.509 or JWT).

---

## §2. Per-standard mapping

### 2.1 OpenID Connect (`openid-connect`)

**Requirement (registry):** "Federated identity."

**Applicability (§14.1):** **In-scope, high leverage.** Works-execution's pack rule `04_SECURITY/SECRETS_AND_IDENTITY.md` mandates "OIDC/workload identity where providers support it." Slice 3 introduces user-facing authentication of work submitters; OIDC is the canonical federation protocol and is the entry point for the Zero-Secret mandate.

**Current status (registry):** `PLANNED`. Registry row `implementation` reads: *"Slice 3: OIDC token exchange for worker enrollment (#114 Zero-Secret)."* `enforcement_point: services/api/auth.go` — file does not exist today (Slice 1+2 API has no auth middleware).

**Gap (§14.3):**

1. No `services/api/auth.go` exists; `/v1/*` is currently unauthenticated.
2. No issuer / client-id / audience configuration surface in `cmd/works-api/main.go`.
3. No token-validation contract (issuer URL, JWKS URI, audience, required claims).
4. No tests under `tests/auth/`.

**Next step (§14.5):**
Create `services/api/auth.go` implementing a `RequireOIDC(issuer, audience string) func(http.Handler) http.Handler` middleware that validates the `Authorization: Bearer` ID token against the configured issuer's JWKS, enforces `iss`, `aud`, `exp`, and `nbf`, and is mounted by `Server.Routes()` in `services/api/api.go`. Add `tests/auth/oidc_test.go` covering happy-path, expired-token, wrong-audience, and missing-claim cases using a self-signed RSA key fixture (no external dependency in CI).

**Acceptance evidence:** `make test` passes `tests/auth/oidc_test.go`; `services/api/auth.go` exists; `cmd/works-api/main.go` wires the middleware only when `--oidc-issuer` is set; unauthenticated `/v1/*` calls return `401` in `e2e/` test harness.

**Risk × Leverage:** High × High. Without OIDC the Zero-Secret rule is unmet; without it every later standard (JWT, RBAC claims) has nothing to bind against.

---

### 2.2 SPIFFE (`spiffe`)

**Requirement (registry):** "Workload identity."

**Applicability (§14.1):** **In-scope, foundational.** Workers are the core runtime primitive of works-execution. SPIFFE gives them a cryptographic, platform-issued identity that survives process restarts and is verifiable by any peer in the control plane. The pack's worker-identity rule requires a cryptographic identity "bound to organization/pool/trust class" with "revocation that takes effect without reinstalling the fleet." SPIFFE is the spec; SPIRE (2.3) is the runtime.

**Current status (registry):** `PLANNED`. `implementation: "Slice 3: workers carry SPIFFE IDs (#121 Runner Identity)."` No `internal/identity/` package exists; workers today identify themselves only by an opaque `worker_id` string in `internal/worker/worker.go`.

**Gap (§14.3):**

1. No SPIFFE ID type or parser.
2. Workers do not fetch / present identity documents on lease grant or heartbeat.
3. No SVID cache; no rotation policy.
4. No test that asserts the SPIFFE ID URI matches the spec format `spiffe://works-execution/ns/<tenant>/sa/<worker>`.

**Next step (§14.5):**
Create `internal/identity/spiffe.go` defining:
- `type SPIFFEID struct { TrustDomain, Namespace, ServiceAccount string }`
- `(SPIFFEID).String() string` returning the canonical URI per RFC.
- `(SPIFFEID).Parse(string) (SPIFFEID, error)` accepting the canonical form.
- `type WorkloadID struct { SPIFFEID; Class string }` encoding trust class (`public` / `restricted` / `privileged`) per the pack.

Wire `internal/identity/spiffe.go` into `internal/worker/worker.go`'s lease-grant request and add `tests/identity/spiffe_test.go` covering parse, format, round-trip, and rejection of non-`spiffe://` schemes.

**Acceptance evidence:** `internal/identity/spiffe.go` exists; `tests/identity/spiffe_test.go` passes; `internal/worker/worker.go` sends a SPIFFE ID on `/v1/leases` accept; API rejects leases with a malformed SPIFFE ID.

**Risk × Leverage:** High × High. Every downstream identity standard (WLA, X.509 SVIDs, RBAC claims) assumes a valid SPIFFE ID format.

---

### 2.3 SPIRE (`spire`)

**Requirement (registry):** "SPIFFE Runtime Environment."

**Applicability (§14.1):** **Deferred to Slice 4+.** SPIRE is the issuance agent for SVIDs. It requires a server, an agent on every host, and a node attestation plugin. Slice 1+2 runs in-process and on a single host; running a full SPIRE server + agent fleet is disproportionate to the current deployment topology. Slice 3 will use **mock SVIDs** issued by the API itself for testing; Slice 4+ will plug in SPIRE when multi-host worker pools arrive.

**Current status (registry):** `PLANNED`. `implementation: "Slice 4+: SPIRE agent for production enrollment."` No `enforcement_point`, no `test`, no `evidence`.

**Gap (§14.3):**

1. No production SPIRE deployment manifest (`infra/spire/`).
2. No federation trust bundle configuration.
3. No agent-vs-server split decision recorded in an ADR.

**Next step (§14.5):** Add a placeholder `infra/spire/README.md` documenting the deferred target (single-server, single-agent, filesystem-based node attestation for V1; pluggable for V2), and create ADR `docs/adr/ADR-0007-spire-architecture.md` capturing the deferral decision and the trigger that will move SPIRE from PLANNED to IMPLEMENTED (multi-host pool go-live, or any audit-driven requirement for non-mock SVIDs).

**Acceptance evidence:** `infra/spire/README.md` exists; ADR-0007 merged; registry row remains `PLANNED` but with a pointer to the ADR's `unblock_check` field.

**Risk × Leverage:** Low × Medium for Slice 3. Acceptable to defer; do not let the row drift indefinitely — the ADR is the safety net.

---

### 2.4 OAuth 2.0 (`oauth-2.0`)

**Requirement (registry):** "Authorization framework."

**Applicability (§14.1):** **In-scope, bundled with OIDC.** works-execution does not need OAuth 2.0's full authorization-code-with-PKCE surface for its first user — submitter authentication is a delegation of an existing identity provider via OIDC. The OAuth 2.0 row is in-scope only to the extent that the OIDC implementation uses OAuth 2.0 flows under the hood (Authorization Code + PKCE for first-party CLIs, Client Credentials for service-to-service). There is **no plan to be an OAuth 2.0 provider** in Slice 3.

**Current status (registry):** `PLANNED`. `implementation: "Slice 3: OAuth2/OIDC for user auth."` `enforcement_point: services/api/auth.go` (will be created per §2.1).

**Gap (§14.3):**

1. No OAuth flow documentation (`docs/security/oauth-flows.md`).
2. No PKCE enforcement in CLI token acquisition (`cmd/works/`).
3. No token-storage contract (OS keychain integration) for the CLI.

**Next step (§14.5):**
Document the Slice 3 OAuth flow in `docs/security/oauth-flows.md` (Authorization Code + PKCE for `works` CLI; Client Credentials for `works-worker` to `works-api` mTLS-or-JWT hand-off). The implementation file is the same `services/api/auth.go` from §2.1; do not duplicate the middleware.

**Acceptance evidence:** `docs/security/oauth-flows.md` exists; the diagram matches the code path in `services/api/api.go`; `tests/auth/` covers token acquisition and refresh.

**Risk × Leverage:** Low × Medium. OAuth 2.0 only matters where we sit on the protocol boundary; we are a relying party, not a provider.

---

### 2.5 OAuth 2.1 (`oauth-2.1`)

**Requirement (registry):** "OAuth 2.1 (modernized)."

**Applicability (§14.1):** **Bundled with §2.4.** OAuth 2.1 is a consolidation of OAuth 2.0 + Security BCP (mandatory PKCE, mandatory redirect-URI exact match, no implicit/hybrid, no resource-owner password). Works-execution adopts OAuth 2.1 *de facto* because we ship PKCE by default and forbid deprecated flows. The two rows (`oauth-2.0` and `oauth-2.1`) will share implementation and tests.

**Current status (registry):** `PLANNED`. `implementation: "Bundle with oauth-2.0."` No separate file path; this row is satisfied by §2.4.

**Gap (§14.3):** None independent of §2.4. PKCE enforcement is the only material addition; it is part of the §2.4 deliverable.

**Next step (§14.5):** No additional file. Verify `services/api/auth.go` rejects authorization-code flows that omit PKCE (test case in `tests/auth/oauth2_pkce_test.go`).

**Acceptance evidence:** PKCE-required test passes; `docs/security/oauth-flows.md` explicitly states "OAuth 2.1 conformance via PKCE-mandatory Authorization Code + Client Credentials; implicit/hybrid flows not supported."

**Risk × Leverage:** Low × Low. Compliance is achieved by construction.

---

### 2.6 SPIFFE ID format (`spiffe-id`)

**Requirement (registry):** "SPIFFE ID URI."

**Applicability (§14.1):** **In-scope, sub-row of §2.2.** The SPIFFE ID format is what §2.2 implements; this row exists separately in the registry because it has its own control_id (`SPIFFE-ID-FMT`) and its own test path. Treat §2.6 as the **format-compliance row** and §2.2 as the **identity-system row**.

**Current status (registry):** `PLANNED`. `implementation: "Slice 3: workers carry spiffe://works-execution/ns/<tenant>/sa/<worker>."` `enforcement_point: tests/identity/`.

**Gap (§14.3):**

1. No format-conformance test fixtures (valid IDs, invalid IDs, scheme mismatches, empty fields).
2. No documentation of the trust-domain string (`works-execution`) or its derivation.

**Next step (§14.5):**
Add `tests/identity/spiffe_id_format_test.go` with table-driven cases:
- valid: `spiffe://works-execution/ns/default/sa/worker-001`
- invalid scheme: `https://works-execution/ns/default/sa/worker-001`
- empty trust domain: `spiffe:///ns/default/sa/worker-001`
- missing `ns` segment
- non-ASCII characters in `sa`

Document the trust-domain string in `docs/standards/mappings/identity.md` (this file) under §3 — **single source of truth**.

**Acceptance evidence:** `tests/identity/spiffe_id_format_test.go` passes; trust-domain `works-execution` referenced from this file and from §2.2.

**Risk × Leverage:** Low × High. The format is small, but the test makes the contract binding.

---

### 2.7 SPIFFE Workload API (`spiffe-workload-api`)

**Requirement (registry):** "Workload identity issuance."

**Applicability (§14.1):** **Deferred to Slice 4+, bundled with §2.3.** The WLA is the runtime API that a workload (the worker) calls to fetch its SVID. SPIRE (§2.3) is what serves the WLA. Slice 3 has no SPIRE, so there is no WLA to call. Slice 3 will define a `internal/identity/svid.go` interface so the worker can be retargeted at SPIRE without touching call sites.

**Current status (registry):** `PLANNED`. `implementation: "Slice 4+."` No `enforcement_point`, no `test`, no `evidence`.

**Gap (§14.3):**

1. No `internal/identity/svid.go` interface.
2. No mock implementation to keep Slice 3 testable without SPIRE.

**Next step (§14.5):**
Define `internal/identity/svid.go`:
```go
type SVIDFetcher interface {
    FetchSVID(ctx context.Context) (SVID, error)
}
type SVID interface {
    ID() SPIFFEID
    Cert() *x509.Certificate        // populated for X.509-SVID mode (Slice 4+)
    JWT() (string, error)           // populated for JWT-SVID mode (Slice 3 mock)
    Expiry() time.Time
}
```
Provide a `MockSVIDFetcher` in `internal/identity/mock.go` for Slice 3 tests. This is the seam that lets Slice 4+ swap in `github.com/spiffe/go-spiffe/v2/workloadapi` without changing `internal/worker/`.

**Acceptance evidence:** Interface exists; mock exists; `internal/worker/worker.go` accepts an `SVIDFetcher` (constructor-injected); worker tests run without external SPIRE.

**Risk × Leverage:** Medium × High. The interface is the enabler for Slice 4+; without it the WLA retrofit will touch every call site.

---

### 2.8 JWT (`jwt`)

**Requirement (registry):** "JSON Web Token."

**Applicability (§14.1):** **In-scope, foundational for OIDC and RBAC.** OIDC ID tokens are JWTs; RBAC claims ride in JWTs; the Slice 3 mock SVID issues JWTs. We will use Go's `github.com/golang-jwt/jwt/v5` for parse / validate, and the OIDC issuer's JWKS for signature verification.

**Current status (registry):** `PLANNED`. `implementation: "Slice 3: OIDC ID tokens are JWTs."` No `enforcement_point`, no `test`, no `evidence`.

**Gap (§14.3):**

1. No JWT validator in `services/api/auth.go`.
2. No JWKS fetcher / cache.
3. No rejection path for `alg=none` or HS256 confusion attacks.

**Next step (§14.5):**
In `services/api/auth.go`, add `validateJWT(raw string, jwks *JWKSet) (*Claims, error)` using the OIDC issuer's discovered JWKS, restricting accepted algorithms to `RS256` and `ES256` (no `HS*`, no `none`). Cache the JWKS for 10 minutes with refresh-on-kid-miss. Add `tests/auth/jwt_test.go` with: valid RS256, expired, wrong issuer, wrong audience, `alg=none`, HS256-with-public-key confusion.

**Acceptance evidence:** All `tests/auth/jwt_test.go` cases pass; `services/api/auth.go` does not import `jwt-go`'s `ParseUnverified`; algorithm allow-list enforced.

**Risk × Leverage:** High × High. JWT validation is the single most common identity bug surface; ship the table-driven tests before any production deployment.

---

### 2.9 X.509 (`x509`)

**Requirement (registry):** "Public key certificates."

**Applicability (§14.1):** **Deferred to Slice 4+, bundled with SPIRE.** X.509 SVIDs are what SPIRE issues in the default deployment. Slice 3 uses JWT-SVIDs only (mock). The standard library `crypto/x509` is already used for the SQLite store's encryption-at-rest key (out of identity scope), but the identity X.509 surface is dormant.

**Current status (registry):** `PLANNED`. `implementation: "Slice 4+: SPIRE uses X.509 SVIDs."` No `enforcement_point`, no `test`, no `evidence`.

**Gap (§14.3):**

1. No `internal/identity/x509svid.go`.
2. No chain-validation policy (must SPIRE-issued, must not be expired, must include the SPIFFE ID in the SAN URI).

**Next step (§14.5):** Define the type in `internal/identity/x509svid.go` (struct holding `*x509.Certificate` and the SPIFFE ID extracted from `URI SAN`), but do not wire it into the worker yet. Leave the file as a stub with a `// Slice 4+` comment and a link to the SPIRE ADR (§2.3).

**Acceptance evidence:** File exists with the stub; `make build` succeeds; registry row remains PLANNED.

**Risk × Leverage:** Low × Low for Slice 3. Document, do not implement.

---

### 2.10 RBAC (`rbac`)

**Requirement (registry):** "Role-Based Access Control."

**Applicability (§14.1):** **In-scope, primary authorization model for Slice 3.** The registry row names the V1 role set: *"roles per organization (admin, developer, runner)."* This is the authorization layer the API checks after the JWT is validated.

**Current status (registry):** `PLANNED`. `implementation: "Slice 3: roles per organization (admin, developer, runner)."` `enforcement_point: tests/identity/rbac_test.go` (does not exist).

**Gap (§14.3):**

1. No role model in code.
2. No per-endpoint role check.
3. No per-tenant role scope (the registry says "per organization").
4. No test matrix.

**Next step (§14.5):**
Create `internal/auth/rbac.go` defining:
- `type Role string` with constants `RoleAdmin`, `RoleDeveloper`, `RoleRunner`.
- `type Policy struct { TenantID string; Role Role }`
- `func (p Policy) Can(action Action, resource Resource) bool` with an explicit allow-list:
  - `RoleAdmin`: all actions on all resources in tenant.
  - `RoleDeveloper`: create / read / cancel own Works in tenant.
  - `RoleRunner`: only the lease-grant endpoints and node-status reporting.
- `services/api/auth.go` exposes `RequireRole(role Role) func(http.Handler) http.Handler`.

Add `tests/identity/rbac_test.go` as a table-driven test: `(role, action, resource, expected)`. Cover every (role × action) pair, plus cross-tenant denial.

Wire roles from JWT claims: the OIDC token's `works_roles` claim (custom claim negotiated with the IdP) carries the per-tenant role set; absence of the claim = `RoleRunner` (least privilege).

**Acceptance evidence:** `internal/auth/rbac.go` exists; `tests/identity/rbac_test.go` passes; `tests/auth/rbac_integration_test.go` proves a 403 is returned when a `RoleRunner` hits `POST /v1/works/{id}/cancel`.

**Risk × Leverage:** High × High. RBAC is the first line of authorization; without it, OIDC only authenticates, it does not authorize.

---

### 2.11 ReBAC (`rebac`)

**Requirement (registry):** "Relationship-Based Access Control."

**Applicability (§14.1):** **Future, not Slice 3.** The registry row says *"Future: org/team/repo graph."* ReBAC expresses *"this user is a member of this team, which owns this repo"* — necessary once works-execution supports cross-tenant delegation and per-repo policies. Slice 3 has a flat `(tenant, role)` model; introducing relationships now would be premature.

**Current status (registry):** `PLANNED`. `implementation: "Future: org/team/repo graph."` No `enforcement_point`, no `test`, no `evidence`.

**Gap (§14.3):**

1. No relationship graph data model.
2. No decision engine (e.g. SpiceDB, OpenFGA) integration.

**Next step (§14.5):** Add a §6 entry in this document (below) capturing the trigger that will promote ReBAC from PLANNED to IMPLEMENTED: at least one of (a) a second tenant in production, (b) a customer request for team-level delegation, (c) a regulatory requirement for per-repo access logs. Until one of those fires, leave the row PLANNED.

**Acceptance evidence:** This file records the trigger; no code change in Slice 3.

**Risk × Leverage:** Low × Medium. Defer cleanly; record the trigger.

---

### 2.12 ABAC (`abac`)

**Requirement (registry):** "Attribute-Based Access Control."

**Applicability (§14.1):** **Future, layered on RBAC.** The registry row says *"Future: trust_class + region + cost_center attributes."* ABAC is the right shape for runtime / policy-engine decisions (e.g. *"runner is privileged trust class AND request region matches work's data-residency constraint"*). Slice 3 ships RBAC only; ABAC will be layered as a Cedar / OPA policy file in a later slice.

**Current status (registry):** `PLANNED`. `implementation: "Future: trust_class + region + cost_center attributes."` No `enforcement_point`, no `test`, no `evidence`.

**Gap (§14.3):**

1. No attribute vocabulary.
2. No policy engine integration.

**Next step (§14.5):** Record the attribute vocabulary in §7 of this document (`trust_class ∈ {public, restricted, privileged}`, `region ∈ {eu, us, ap}`, `cost_center ∈ string`). No code in Slice 3.

**Acceptance evidence:** Vocabulary documented; registry row remains PLANNED; ABAC slice will be triggered when the first attribute other than `role` is needed to authorize an action.

**Risk × Leverage:** Low × Medium. Same posture as ReBAC.

---

### 2.13 Zero Trust (`zero-trust`)

**Requirement (registry):** "Never trust, always verify."

**Applicability (§14.1):** **In-scope, PARTIAL.** The Zero Trust row is marked `PARTIAL` today because Slice 2 already implements mTLS-like properties at the lease layer (lease tokens, expiring leases, reaper) and the API has a recoverer middleware. Slice 3 advances it from PARTIAL to IMPLEMENTED (pending verification) by adding OIDC + RBAC + per-tenant token scoping.

**Current status (registry):** `PARTIAL`. `implementation: "Bundled with nist-sp-800-207."` No `enforcement_point`, no `test`, no `evidence` row in the registry.

**Gap (§14.3):**

1. Zero Trust is a *posture*, not a single artifact; the row needs explicit sub-controls.
2. No documented boundary-trust assumptions for the worker's outbound calls.
3. No per-tenant scoping on tokens.

**Next step (§14.5):**
Create `docs/security/zero-trust-posture.md` enumerating the ZTA pillars per NIST SP 800-207 and mapping each to a works-execution component:
- **Identity** → §2.1 OIDC + §2.10 RBAC.
- **Authentication** → §2.8 JWT validation.
- **Authorization** → §2.10 RBAC + §2.11 ReBAC (future).
- **Network segmentation** → lease tokens (Slice 2) + per-tenant namespace (Slice 3).
- **Continuous verification** → lease heartbeat (Slice 2) + SVID rotation (Slice 4+).
- **Telemetry** → audit log of every auth decision (Slice 3 audit pipeline).

**Acceptance evidence:** `docs/security/zero-trust-posture.md` exists with the table; audit log includes `auth_event`, `actor`, `tenant`, `action`, `decision`, `reason` columns; `tests/auth/audit_test.go` proves every denial is recorded.

**Risk × Leverage:** High × High. Zero Trust is the umbrella; advancing it is the headline outcome of Slice 3.

---

## §3. Trust domain (single source of truth)

Per SPIFFE spec, all SPIFFE IDs in works-execution use the trust domain:

```
works-execution
```

ID URI canonical form: `spiffe://works-execution/ns/<tenant>/sa/<worker>`

- `<tenant>` — the organization identifier; lowercase, `[a-z0-9-]{1,63}`.
- `<worker>` — the worker identifier; assigned at enrollment, immutable for the worker's lifetime.
- `trust_class` — encoded separately as a custom JWT claim (`works_trust_class ∈ {public, restricted, privileged}`), not in the SPIFFE URI itself.

This is referenced from §2.2, §2.6, §2.10, §2.13.

---

## §4. Prioritization — the three highest-leverage identity standards for Slice 3

Scoring rubric: **Risk-of-omission × Leverage-on-platform-correctness × Slice-3-deliverability**.

| Rank | Standard | Slice-3 priority rationale |
|---|---|---|
| **1** | **SPIFFE ID format (§2.6) + SPIFFE (§2.2)** | The control-plane cannot verify any peer identity without a valid SPIFFE ID. Workers are the platform; everything else (RBAC claims, JWT binding, SVIDs) hangs off a well-formed ID. Implementable today (pure Go, no external dependency). |
| **2** | **JWT (§2.8) + OpenID Connect (§2.1)** | Federates user identity via an existing IdP, satisfies the Zero-Secret rule, and is the substrate for RBAC claims. Requires only a self-signed RSA fixture for tests; production wiring is configuration. |
| **3** | **RBAC (§2.10)** | Without RBAC, OIDC only authenticates — every authenticated user can do anything. RBAC delivers the `admin / developer / runner` policy named in the registry, and is the only authorization row with a Slice-3 implementation field. |

**Deferred (with reason, not abandoned):**

- **SPIRE (§2.3)** — disproportionate to current single-host deployment; ADR-0007 captures the deferral.
- **SPIFFE Workload API (§2.7)** — depends on §2.3; interface stub is the Slice-3 deliverable.
- **X.509 SVIDs (§2.9)** — depends on §2.3; stub only.
- **OAuth 2.0 (§2.4) / OAuth 2.1 (§2.5)** — satisfied by the OIDC implementation; documented, not separately implemented.
- **ReBAC (§2.11) / ABAC (§2.12)** — no second-tenant or attribute-shaped requirements yet.
- **Zero Trust (§2.13)** — posture, advanced by §2.1 + §2.2 + §2.6 + §2.8 + §2.10 + the audit pipeline.

---

## §5. Traceability table

| Standard | Registry row | Status | Enforcement point (file) | Test (file) | Evidence pointer | Slice |
|---|---|---|---|---|---|---|
| OpenID Connect | `openid-connect` | PLANNED | `services/api/auth.go` (new) | `tests/auth/oidc_test.go` (new) | this doc + test output | 3 |
| SPIFFE | `spiffe` | PLANNED | `internal/identity/spiffe.go` (new), wired in `internal/worker/worker.go` | `tests/identity/spiffe_test.go` (new) | this doc + test output | 3 |
| SPIRE | `spire` | PLANNED | `infra/spire/README.md` (new), ADR-0007 (new) | n/a (deferred) | ADR-0007 | 4+ |
| OAuth 2.0 | `oauth-2.0` | PLANNED | `services/api/auth.go` (shared with OIDC) | `tests/auth/oauth2_pkce_test.go` (new) | this doc + `docs/security/oauth-flows.md` | 3 |
| OAuth 2.1 | `oauth-2.1` | PLANNED | bundled with OAuth 2.0 | bundled with OAuth 2.0 | this doc | 3 |
| SPIFFE ID format | `spiffe-id` | PLANNED | `internal/identity/spiffe.go` (shared) | `tests/identity/spiffe_id_format_test.go` (new) | this doc + test output | 3 |
| SPIFFE Workload API | `spiffe-workload-api` | PLANNED | `internal/identity/svid.go` (new interface) | mock in `internal/identity/mock.go` | this doc | 3 (interface), 4+ (impl) |
| JWT | `jwt` | PLANNED | `services/api/auth.go` (shared) | `tests/auth/jwt_test.go` (new) | this doc + test output | 3 |
| X.509 | `x509` | PLANNED | `internal/identity/x509svid.go` (new stub) | n/a (deferred) | stub + ADR-0007 | 4+ |
| RBAC | `rbac` | PLANNED | `internal/auth/rbac.go` (new), mounted in `services/api/api.go` | `tests/identity/rbac_test.go` + `tests/auth/rbac_integration_test.go` (new) | this doc + test output | 3 |
| ReBAC | `rebac` | PLANNED | n/a (deferred) | n/a (deferred) | §6 below | future |
| ABAC | `abac` | PLANNED | n/a (deferred) | n/a (deferred) | §7 below | future |
| Zero Trust | `zero-trust` | PARTIAL | `docs/security/zero-trust-posture.md` (new) | `tests/auth/audit_test.go` (new) | this doc + posture doc | 3 (advance PARTIAL → IMPLEMENTED) |

---

## §6. ReBAC promotion trigger (future)

Promote `rebac` from PLANNED to IMPLEMENTED when **any one** of the following fires:

1. A second tenant exists in production with cross-tenant delegation requirements.
2. A customer requests team-scoped Works ("only the `platform-team` group in tenant X may submit Works against repo Y").
3. A regulatory or audit requirement mandates per-repo access logs.

The ReBAC engine will be SpiceDB or OpenFGA; the data model will be a `subject → relation → object` graph with `tenant` as the root namespace. Until one trigger fires, RBAC (§2.10) covers the Slice-3 needs.

---

## §7. ABAC promotion trigger and vocabulary (future)

Promote `abac` from PLANNED to IMPLEMENTED when **any one** of the following fires:

1. A policy decision cannot be expressed in RBAC alone (e.g. region/data-residency constraints on Works).
2. An external compliance regime (SOC 2, ISO 27001) requires attribute-based policy.
3. A customer requests cost-center-scoped Works.

**Vocabulary (reserved in this document as the single source of truth):**

| Attribute | Type | Values | Source |
|---|---|---|---|
| `works_trust_class` | enum | `public`, `restricted`, `privileged` | JWT custom claim |
| `works_region` | enum | `eu`, `us`, `ap` | JWT custom claim |
| `works_cost_center` | string | any | JWT custom claim |
| `works_data_classification` | enum | `internal`, `confidential`, `regulated` | Work spec (not yet enforced) |

The policy engine will be OPA (Rego) or Cedar; decision call goes through `internal/auth/policy.go` after the RBAC check returns `true`.

---

## §8. Acceptance for this mapping document

- [x] All 11 active identity/authorization standards mapped (§§2.1–2.13).
- [x] Duplicates and NOT_APPLICABLE rows handled (§1).
- [x] Per-standard fields complete: applicability, status, gap, next step, file path (§2).
- [x] §14 five-step rule applied to every row.
- [x] Top-3 highest-leverage Slice-3 standards identified (§4).
- [x] Traceability table links each standard to enforcement point, test, and slice (§5).
- [x] Deferred items (SPIRE, WLA, X.509, ReBAC, ABAC) carry explicit promotion triggers (§§3, 6, 7) and an ADR where applicable.
- [x] Cross-references to `docs/standards/registry.json`, `docs/kanban/board.json`, and the pack's `SECRETS_AND_IDENTITY.md`.

This document will be updated whenever a row moves between status values in `docs/standards/registry.json`, and at minimum once per slice close.