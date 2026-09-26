package main

import (
	"os"
	"reflect"
	"testing"
)

func TestAllowedReposFromEnv(t *testing.T) {
	previous, present := os.LookupEnv("WORKS_ALLOWED_REPOS")
	t.Cleanup(func() {
		if present {
			_ = os.Setenv("WORKS_ALLOWED_REPOS", previous)
		} else {
			_ = os.Unsetenv("WORKS_ALLOWED_REPOS")
		}
	})

	_ = os.Setenv("WORKS_ALLOWED_REPOS", " JonasAbde/Renos-Control, JonasAbde/works-execution ,, ")
	got := allowedReposFromEnv()
	want := map[string]bool{"JonasAbde/Renos-Control": true, "JonasAbde/works-execution": true}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("allowed repos: got %#v, want %#v", got, want)
	}

	_ = os.Setenv("WORKS_ALLOWED_REPOS", "   ")
	if got := allowedReposFromEnv(); got != nil {
		t.Fatalf("empty allowlist: got %#v, want nil", got)
	}
}

func TestEvidenceConfigFromValues(t *testing.T) {
	if got := evidenceConfigFromValues("", "k", "r"); got != nil {
		t.Fatalf("empty key: want nil config, got %+v", got)
	}
	if got := evidenceConfigFromValues("short", "k", "r"); got != nil {
		t.Fatalf("short key: want nil config, got %+v", got)
	}
	got := evidenceConfigFromValues("0123456789abcdef0123456789abcdef", "key-v1", "runner-1")
	if got == nil {
		t.Fatal("valid key: want config, got nil")
	}
	if got.KeyID != "key-v1" {
		t.Fatalf("key id: got %q, want key-v1", got.KeyID)
	}
	if string(got.HMACKey) != "0123456789abcdef0123456789abcdef" {
		t.Fatalf("hmac key not preserved")
	}
	if got.Runner.ID != "runner-1" || got.Runner.TrustClass != "standard" {
		t.Fatalf("runner: got %+v", got.Runner)
	}
}

func TestEvidenceConfigFromValuesDefaults(t *testing.T) {
	got := evidenceConfigFromValues("0123456789abcdef0123456789abcdef", "", "")
	if got == nil {
		t.Fatal("valid key with empty ids: want config, got nil")
	}
	if got.KeyID != "works-api-evidence-v1" {
		t.Fatalf("default key id: got %q", got.KeyID)
	}
	if got.Runner.ID != "works-api" {
		t.Fatalf("default runner id: got %q", got.Runner.ID)
	}
}
