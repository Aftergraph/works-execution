// Package providers — avc-core adapter mapping (agent C, k-hal-01).
//
// This file documents (and type-checks) how the EXISTING execution
// infrastructure maps onto the frozen CPN contract without leaking
// implementation into it. Per k-hal-01 law: existing infrastructure
// adapts BEHIND the contract; it never defines it.
//
//   - internal/sandbox.Docker.Run  → CPN Exec (shell capability; hermetic
//     defaults — read-only fs, cap-drop ALL, no network — become the
//     provider's internal implementation detail, invisible to the kernel)
//   - internal/sandbox.Hermetic    → CPN Exec (host-subprocess path)
//   - services/runner (Plan/Run/Step, stack detection, build fingerprint)
//     → stays kernel-side: it is mission logic, NOT provider logic. The
//     runner calls providers.Exec per step; it never touches Docker/shell
//     details through anything but the contract.
//   - avc-core pool runner (BYOC, RFC-0004) → a future remote
//     ComputerProvider speaking CPN over its lease API. Nothing in this
//     slice needs it: the contract is transport-neutral
package providers

// AdapterNotes documents the mapping decisions frozen for k-hal-01
// review. No code here — the adapter is the EXISTING runner path plus the
// capability gate below; it compiles against the contract, not the reverse.
//
// Kernel side: services/runner keeps calling sandbox runtimes for its
// local pipeline today; when the kernel switches to CPN, it talks to
// `providers.ComputerProvider` only. The pool of providers is looked up by
// ReqRequirements.Pool name (existing runner naming) at the API edge — the
// mapping lives there, not in kernel packages.
//
// Capability mapping (frozen enum → existing machinery):
//
//	CapShell  → internal/sandbox (hermetic + docker backends)
//	CapFS     → sandbox mount controls (read-only root, /work rw)
//	CapGit    → shell commands (git is a shell program today)
//	CapSnap   → workgraph handoff checkpoints (kernel-side; provider
//	            snapshot lands with k-pulse-01 providers)
//	CapTeardownKeep → lease retain path (store/leases ReleaseLease)
//
// Nothing in this mapping names a vendor, Docker as a contract concept, a
// specific VM, an OS, or a runner implementation inside packages/providers.
const _ = "adapter mapping note (k-hal-01): kernel talks CPN only"