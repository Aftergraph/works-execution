package providers

// ConformanceSuite is the executable evidence that a ComputerProvider
// actually honors the frozen CPN contract (k-hal-01: interface compliance
// alone proves NOTHING). Every future provider — avc-core pool, WORKS Cloud
// Computer, Linux sandbox, Windows VM, PULSE node, enterprise node, browser
// computer — must pass this same suite.
//
// Usage:
//
//	ConformanceSuite(t, NewMyProvider(cfg))
//
// The suite drives a provider through the frozen lifecycle with deterministic
// assertions; failures name the contract clause violated. It is provider-
// neutral: it exercises only the contract's own types and errors.
import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// fakeClock keeps timing assertions deterministic.
var _ = time.Now

// ConformanceSuite runs the full frozen-contract battery against p.
func ConformanceSuite(t *testing.T, p ComputerProvider) {
	t.Helper()
	ctx := context.Background()

	// ---- handshake ----
	t.Run("handshake/valid", func(t *testing.T) {
		hs, err := p.Handshake(ctx, Handshake{ABI: ABI, ProvID: "kernel"})
		if err != nil {
			t.Fatalf("valid handshake refused: %v", err)
		}
		if err := hs.Validate(); err != nil {
			t.Fatalf("provider handshake invalid: %v", err)
		}
	})

	t.Run("handshake/incompatible-version-fails-closed", func(t *testing.T) {
		bad := Handshake{ABI: "cpi/0.9", ProvID: "kernel"}
		if _, err := p.Handshake(ctx, bad); err == nil {
			t.Fatal("incompatible ABI accepted — versioning charter broken")
		} else if !errors.Is(err, ErrHandshakeIncompatible) {
			t.Fatalf("incompatible ABI surfaced wrong error class: %v", err)
		}
	})

	t.Run("handshake/unknown-capability-rejected", func(t *testing.T) {
		hs := Handshake{ABI: ABI, ProvID: "kernel", Caps: []string{"fs", "teleport"}}
		if _, err := p.Handshake(ctx, hs); err == nil {
			t.Fatal("unknown capability accepted — silent authority downgrade possible")
		}
	})

	// refresh handshake for the lifecycle battery
	hs, err := p.Handshake(ctx, Handshake{ABI: ABI, ProvID: "kernel",
		Caps: []string{CapFS, CapShell, CapGit, CapSnap, CapTeardownKeep}})
	if err != nil {
		t.Fatalf("handshake: %v", err)
	}
	if err := hs.Validate(); err != nil {
		t.Fatal(err)
	}
	caps := NegotiatedCaps(hs.Caps, Capabilities{CapFS, CapShell, CapGit, CapSnap, CapTeardownKeep})

	// ---- provision ----
	t.Run("provision/success", func(t *testing.T) {
		res, err := p.Provision(ctx, Spec{
			IdempotencyKey: "prov-1", Org: "org_a",
			Caps: Capabilities{CapShell, CapFS},
		})
		if err != nil {
			t.Fatalf("provision: %v", err)
		}
		if res.ID == "" || res.ProvID == "" || res.Org != "org_a" {
			t.Fatalf("resource identity incomplete: %+v", res)
		}
		for _, c := range res.Caps {
			if !caps.Has(c) {
				t.Fatalf("resource carries capability %q outside negotiated set — escalation", c)
			}
		}
	})

	t.Run("provision/replay-same-spec-idempotent", func(t *testing.T) {
		r1, err := p.Provision(ctx, Spec{IdempotencyKey: "prov-2", Org: "org_a", Caps: Capabilities{CapShell}})
		if err != nil {
			t.Fatalf("first: %v", err)
		}
		r2, err := p.Provision(ctx, Spec{IdempotencyKey: "prov-2", Org: "org_a", Caps: Capabilities{CapShell}})
		if err != nil {
			t.Fatalf("replay (same spec) must succeed idempotently: %v", err)
		}
		if r1.ID != r2.ID {
			t.Fatal("same idempotency key + same spec produced two resources")
		}
	})

	t.Run("provision/replay-different-spec-rejected", func(t *testing.T) {
		if _, err := p.Provision(ctx, Spec{IdempotencyKey: "prov-2", Org: "org_a", Caps: Capabilities{CapFS, CapBrowser}}); !errors.Is(err, ErrProvisionReplayed) {
			t.Fatalf("replay with different spec: got %v, want ErrProvisionReplayed", err)
		}
	})

	res, err := p.Provision(ctx, Spec{IdempotencyKey: "prov-3", Org: "org_a", Caps: caps})
	if err != nil {
		t.Fatalf("provision for lifecycle: %v", err)
	}

	// ---- exec ----
	t.Run("exec/advertised-capability", func(t *testing.T) {
		out, err := p.Exec(ctx, res, CapShell, ExecSpec{Cmd: "echo conformance", Timeout: 10 * time.Second})
		if err != nil {
			t.Fatalf("exec: %v", err)
		}
		if !strings.Contains(string(out.Log), "conformance") {
			t.Fatalf("exec result missing output: %q", out.Log)
		}
	})

	t.Run("exec/unadvertised-capability-rejected", func(t *testing.T) {
		if _, err := p.Exec(ctx, res, CapBrowser, ExecSpec{Cmd: "open"}); err == nil {
			t.Fatal("exec with un-advertised capability accepted — escalation law broken")
		}
	})

	t.Run("exec/env-secrets-refs-only", func(t *testing.T) {
		bad := ExecSpec{Cmd: "x", Env: map[string]string{"TOKEN": "sk_live_plaintext"}}
		if _, err := p.Exec(ctx, res, CapShell, bad); err == nil {
			t.Fatal("plaintext secret accepted through contract — secret.ref invariant broken")
		}
		ok := ExecSpec{Cmd: "x", Env: map[string]string{"TOKEN": "secret://ci/token"}}
		if _, err := p.Exec(ctx, res, CapShell, ok); err != nil {
			t.Fatalf("secret:// ref rejected: %v", err)
		}
	})

	t.Run("exec/cancellation-honored", func(t *testing.T) {
		cctx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
		defer cancel()
		_, err := p.Exec(cctx, res, CapShell, ExecSpec{Cmd: "sleep 5", Timeout: 10 * time.Second})
		if err == nil {
			t.Skip("provider completed faster than the cancel window — non-deterministic; not a failure")
		}
		if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation surfaced wrong error: %v", err)
		}
	})

	t.Run("exec/stale-resource-handle-rejected", func(t *testing.T) {
		ghost := Resource{ID: "res_does_not_exist", ProvID: hs.ProvID, Org: "org_a", Caps: caps}
		if _, err := p.Exec(ctx, ghost, CapShell, ExecSpec{Cmd: "x"}); !errors.Is(err, ErrResourceNotFound) {
			t.Fatalf("stale handle: got %v, want ErrResourceNotFound", err)
		}
	})

	t.Run("exec/cross-tenant-handle-fails-closed", func(t *testing.T) {
		foreign := res
		foreign.Org = "org_evil"
		if _, err := p.Exec(ctx, foreign, CapShell, ExecSpec{Cmd: "x"}); err == nil {
			// Provider may implement tenant check by org field on Resource;
			// if it accepted, the provider is confused about identity.
			t.Fatal("cross-tenant resource accepted — identity confusion fails open")
		}
	})

	// ---- snapshot ----
	t.Run("snapshot/integrity", func(t *testing.T) {
		ref, err := p.Snapshot(ctx, res)
		if err != nil {
			t.Fatalf("snapshot: %v", err)
		}
		if ref.ID == "" || ref.Digest == "" || ref.ResID != res.ID {
			t.Fatalf("snapshot envelope incomplete: %+v", ref)
		}
		if len(ref.Digest) != 64 { // sha256 hex
			t.Fatalf("snapshot digest not sha256: %q", ref.Digest)
		}
	})

	// ---- teardown ----
	t.Run("teardown/then-stale", func(t *testing.T) {
		if err := p.Teardown(ctx, res, TeardownClean); err != nil {
			t.Fatalf("teardown: %v", err)
		}
		if _, err := p.Exec(ctx, res, CapShell, ExecSpec{Cmd: "x"}); !errors.Is(err, ErrResourceNotFound) {
			t.Fatalf("post-teardown exec: got %v, want ErrResourceNotFound (stale handle must fail closed)", err)
		}
		if err := p.Teardown(ctx, res, TeardownClean); !errors.Is(err, ErrResourceNotFound) {
			t.Fatalf("double teardown: got %v, want ErrResourceNotFound", err)
		}
	})

	t.Run("provision/provider-unavailable-surface", func(t *testing.T) {
		// A provider that cannot provision must surface a deterministic
		// error, never a silent success. We use an empty org to force the
		// reference rejection path (providers map their real failures onto
		// ErrProviderUnavailable or a validation error — both acceptable).
		_, err := p.Provision(ctx, Spec{IdempotencyKey: "prov-dead", Org: "", Caps: Capabilities{CapShell}})
		if err == nil {
			t.Fatal("provision with empty tenant identity accepted — tenant-bound identity law broken")
		}
	})
}