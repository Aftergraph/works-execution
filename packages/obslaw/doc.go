// Package obslaw is the pure-domain kernel of the observability-vs-evidence
// boundary law (ADR-0024; schema contract:obs.evidence.rules/1.0).
//
// The law, stated once:
//
//	Observability data (events, logs, metrics) is operationally disposable.
//	It may be trimmed, sampled, and is NOT signed because it is not a
//	claim about truth.
//
//	Evidence is a CLAIM about what happened. It is signed (attested) and
//	never trimmable; deleting evidence is tampering, deleting an event log
//	is routine hygiene.
//
// The boundary is a TYPE law, not a policy: an object that is trimmable
// cannot carry kind=evidence, an unsigned object cannot be evidence, and an
// event can never masquerade as signed. This package is the kernel's one
// place that law is checked.
//
// Why a kernel package, not a runtime check? Because every layer that
// touches observability or evidence needs to ask the same question in the
// same way, and the audit stream, the work journal, and the evidence bundle
// builders in this repo each have their own code paths (services/audit/,
// services/evidence/). This package centralises the invariant so an
// integrator wires it consistently. This slice DELIVERS the standalone law
// package and its tests; an integration PR wires consumers later.
//
// Reference shape (services/evidence/bundle.go): the existing evidence
// bundle uses HMAC-SHA256 over canonical JSON of the bundle minus its
// signatures, with the signature stored as a sibling struct field rather
// than a property of the bundle itself. obslaw.Verifier mirrors that
// pattern at the record level: the Record carries the shape, Attested
// carries the signature alongside.
package obslaw
