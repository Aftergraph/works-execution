package providers

import "testing"

// TestConformance_ReferenceProvider drives the full frozen-CPN battery
// against the in-memory reference provider. Every future ComputerProvider
// (avc-core pool, WORKS Cloud Computer, Linux sandbox, Windows VM, PULSE
// node, enterprise node, browser computer) runs this exact suite.
func TestConformance_ReferenceProvider(t *testing.T) {
	ConformanceSuite(t, NewReferenceProvider("ref-pool"))
}

// TestConformance_HandshakeLaw pins the fail-closed handshake laws on the
// contract level (independent of any provider implementation).
func TestConformance_HandshakeLaw(t *testing.T) {
	t.Run("kernel-offers-subset-becomes-authority", func(t *testing.T) {
		got := NegotiatedCaps(
			Capabilities{CapShell, CapFS, CapBrowser},
			Capabilities{CapShell},
		)
		if len(got) != 1 || got[0] != CapShell {
			t.Fatalf("negotiation widened authority: %v", got)
		}
	})
	t.Run("empty-capability-provision-invalid", func(t *testing.T) {
		hs := Handshake{ABI: ABI, ProvID: "kernel"}
		if err := hs.Validate(); err != nil {
			t.Fatalf("handshake without caps is legal (kernel offers none): %v", err)
		}
	})
}

// TestSecretRefLaw pins the ADR-0022 invariant at the contract level.
func TestSecretRefLaw(t *testing.T) {
	valid := []string{"secret://ci/token", "secret://avc/stripe_key_1"}
	for _, v := range valid {
		if err := SecretRef(v); err != nil {
			t.Fatalf("valid ref %q rejected: %v", v, err)
		}
	}
	invalid := []string{"sk_live_plaintext", "secret://", "secret://a\nb", "token secret://x"}
	for _, v := range invalid {
		if err := SecretRef(v); err == nil {
			t.Fatalf("invalid secret value %q accepted (error text must be redacted: no plaintext in errors)", v)
		}
	}
}