// Package sbom emits Software Bills of Materials for works-execution
// in two formats — SPDX 3.0.1 (spdx.go, EU CRA Art. 13 + ISO/IEC
// 5962:2021) and CycloneDX 1.6 (cyclonedx.go, OWASP-led secondary
// format) — from a single Go-module snapshot produced by collect.go.
// The pipeline is wired by sbom.go and exposed as a `make sbom`
// target writing to artifacts/sbom/.
package sbom