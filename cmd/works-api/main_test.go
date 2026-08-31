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
