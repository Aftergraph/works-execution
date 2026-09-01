# k-pulse-01 — PULSE-daemon Wire-protokol + Acceptance

**Dato:** 1. sep 2026 · **Aftaler:** ADR-0013/0026 · frozen CPN (`cpi/1.0`) · `pulse_cpn_daemon_design.md` · `pulse_security_review.md`
**Formål:** Ét dokument der lader pulse-repoet (WinUI3/.NET10, 100% in-house) implementere daemon-siden uden at se works-kode — plus acceptance-matrix for hele k-pulse-01.

---

## Del 1 — Wire-protokol (WORKS ↔ PULSE-daemon)

**Transport v1:** HTTP **kun på 127.0.0.1:7777** (Kestrel binder eksplicit IPv4 loopback — adskilt fra UI-proces, ADR-0013). Auth: `Authorization: Bearer <localhost pairing-token>` (token gemmes DPAPI i PULSE, udveksles via SAS-parring senere). mTLS = v2-opgraderingssti.

**Header-sæt (alle endpoints):**
```
Authorization: Bearer <token>     # localhost token (secret://-form i works-siden)
X-Pulse-Grant: <grant_id>         # aktivt ConsentGrant-id (daemons validerer mod SQLite)
Idempotency-Key: <key>            # provision/teardown
Content-Type: application/json
```

**Svarkoder (daemons konsent-gate kører FØR enhver payload-behandling):**
| Kode | Betydning | works-side mapping |
|---|---|---|
| 200/201 | OK (consent aktiv) | `Resource`/`ExecResult`/`SnapshotRef` |
| **403 `grant_missing`** | ingen aktiv ConsentGrant | `ErrConsentMissing` (fail-closed) |
| **403 `grant_revoked`** | revoke virkede øjeblikkeligt | `ErrConsentMissing` |
| 404 | ukendt resource_id | `ErrResourceNotFound` |
| 409 | idempotency-key replay m. anderledes spec | `ErrProvisionReplayed` |
| 401 | forkert/ugyldigt token | ingen forhandling — token roteres via re-pair |

**Endpoints:**

| Endpoint | Body (ind) | Body (ud) | Notes |
|---|---|---|---|
| `GET /v1/handshake` | — | `{abi:"cpi/1.0", caps:[...], prov_id}` | kap-cap echo; daemons caps = **kun dem dækket af aktive grants** |
| `POST /v1/provision` | `{org, caps[], idempotency_key}` | `{id}` | org er opak tenant-id; caps **skal** ⊆ grant-dækkede |
| `POST /v1/exec` | `{resource_id, capability, cmd, env{}, timeout_s}` | `{exit_code, log}` | **env-værdier KUN `secret://`-refs** — daemon resolve'r lokalt, aldrig works |
| `POST /v1/snapshot` | `{resource_id, org}` | `{id, digest(sha256-hex), res_id, prov_id}` | digest = sha256 over payload |
| `POST /v1/teardown` | `{resource_id, org, mode:"clean"\|"retain"}` | `{}` | retain kræver `teardown_keep`-grant |

**Daemons intern-lov (fra C's design + D's review):**
- Consen-validering mod SQLite `ConsentGrant{contextId, scope, workerRole, status, purposeBinding}` — makst 1 s cache-TTL, WAL
- Revoke = status-flip + `CancellationTokenSource.Cancel` på in-flight ops → ingen genstart
- Kestrel: `IPAddress.Loopback:7777` kun; statisk analyse forbyder udgående API'er i Daemon-assemblies
- GitProvider: LibGit2Sharp — kun repo/branch/dirty-count **metadata, aldrig indhold**

---

## Del 2 — Acceptance-matrix (k-pulse-01 COMPLETE = alle ✅)

| # | Kriterium | Kontrakt-clause | Test | Status |
|---|---|---|---|---|
| 1 | ConformanceSuite bestået | cpi/1.0 | `ConformanceSuite(t, pulseProvider)` (samme battery som ReferenceProvider) | ✅ planned (stub-daemon) |
| 2 | Consent-scopes håndhæves pr. scope | sync.rules/1.0, adr-0013 | `TestPulseProviderProvisionEscalationRefused`, `ZeroByteWithoutConsent` | ✅ |
| 3 | Revoke-øjeblikkelighed | pulse domain (sync.rules), J4 | `TestPulseProviderRevokeTakesEffectImmediately` | ✅ |
| 4 | Offline-first: daemon-ned | ADR-0013 | `TestPulseProviderOfflineFirstDaemonDown` → ErrProviderUnavailable | ✅ |
| 5 | **0 byte udgående uden aktiv grant** | pulse.db/1.0 consent_rule | `ZeroByteWithoutConsent` (wire-level call-counter) | ✅ |
| 6 | Cross-tenant fail-closed | identity/1.0, V3 | conformance cross-tenant + `Spec.Org==""`-afvisning | ✅ |
| 7 | secret.ref invariant | secret.ref/1.0, ADR-0022 | `PlaintextSecretRefused`, `NonLoopbackTokenRejected` | ✅ |
| 8 | Eksisterende suites grønne | — | 31/31 pakker uændrede | ✅ |
| 9 | go vet clean | DoD | vet | ✅ |
| 10 | exact-head CI SUCCESS | governance | works.yml på merge-commit | ved merge |
| 11 | Loopback-enforcement | V4 | `LoopbackOnlyEnforcement` | ✅ |
| 12 | Handshake-version-gate | proto.charter/1.0 | `HandshakeIncompatible`-klasse | ✅ |

**Deferred (dokumenteret, ikke blockers):**
- mTLS-parring (v2-path, ADR-0026 — v1 = localhost-token)
- Reel GitProvider-sensor (kræver daemon-implementering i pulse-repoet — denne slice leverer provider + kontrakt + stub-verificering)
- Snapshot/Integrity- og TeardownLeak-felter fra security-review (definerede, ikke håndhævede i reference — daemon-siden)

## Drift-log fra agent D (taget alvorligt)
- **V3 tenant-tjek caller-supplied:** acknowledged — PULSE-providerens Org håndhæves i provision+exec på kernel-siden OG daemonsiden; fuld identity-chain-bind (lease_id↔resource) er k-hal-02-arbejde (deferred med begrundelse)
- **V2 revoke-race:** 0-byte + revoke-immediacy tests på wire-level nu; TOCTOU-fuzz deferred til Hard-gate før PULSE-release
- **V9 path-traversal:** context-mount payload er aldrig filstier i v1 (kun metadata-signaler); FileList-fuzz = Hard gate i pulse-repoet før WORKS-Link-aktivering
- **SecretRef()-svaghed:** prefix-tjek skærpet i denne slice (secret:// + længde + newline-refusal); fuld regex-paritet deferred med note
- **ErrTeardownLeak/ErrSnapshotIntegrity:** definerede men daemon-ansvar (deferred, ikke kernel-drift)