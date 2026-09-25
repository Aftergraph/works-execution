package main

import (
	"strings"
	"testing"
)

func TestEvidenceConfigFromValuesDisabledWithoutKey(t *testing.T) {
	cfg, err := evidenceConfigFromValues("works-evidence-v1", "", "works-api")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg != nil {
		t.Fatalf("cfg = %#v, want nil when evidence signing key is absent", cfg)
	}
}

func TestEvidenceConfigFromValuesRejectsShortKey(t *testing.T) {
	cfg, err := evidenceConfigFromValues("works-evidence-v1", "too-short", "works-api")
	if err == nil {
		t.Fatal("expected short evidence HMAC key to fail closed")
	}
	if cfg != nil {
		t.Fatalf("cfg = %#v, want nil on invalid key", cfg)
	}
	if !strings.Contains(err.Error(), "at least 32 bytes") {
		t.Fatalf("error = %q", err)
	}
}

func TestEvidenceConfigFromValuesRejectsMissingIdentityFields(t *testing.T) {
	key := strings.Repeat("a", 32)
	for _, tc := range []struct {
		name, keyID, runnerID string
	}{
		{name: "key id", keyID: " ", runnerID: "works-api"},
		{name: "runner id", keyID: "works-evidence-v1", runnerID: " "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := evidenceConfigFromValues(tc.keyID, key, tc.runnerID)
			if err == nil {
				t.Fatal("expected configuration to fail closed")
			}
			if cfg != nil {
				t.Fatalf("cfg = %#v, want nil", cfg)
			}
		})
	}
}

func TestEvidenceConfigFromValuesWiresStableProducerIdentity(t *testing.T) {
	key := strings.Repeat("k", 32)
	cfg, err := evidenceConfigFromValues("works-evidence-v1", key, "works-api-prod")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected evidence config")
	}
	if cfg.KeyID != "works-evidence-v1" {
		t.Fatalf("KeyID = %q", cfg.KeyID)
	}
	if string(cfg.HMACKey) != key {
		t.Fatal("HMAC key mismatch")
	}
	if cfg.Runner.ID != "works-api-prod" || cfg.Runner.TrustClass != "standard" {
		t.Fatalf("Runner = %#v", cfg.Runner)
	}
}
