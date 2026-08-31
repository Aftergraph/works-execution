package pipeline

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const sampleYML = `version: 1

work:
  verify:
    triggers:
      - push
      - pull_request
    requirements:
      confidence: development
      os: linux
      arch: amd64
      pool: avc-core
    nodes:
      vet:
        run: go vet ./...
        cache: true
        timeout_s: 120
      test:
        needs: [vet]
        run: go test ./... -count=1
        timeout_s: 600
`

func TestParse(t *testing.T) {
	w, err := Parse([]byte(sampleYML))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if w.Requirements.Pool != "avc-core" {
		t.Errorf("pool = %q, want avc-core", w.Requirements.Pool)
	}
	if w.Requirements.OS != "linux" || w.Requirements.Arch != "amd64" {
		t.Errorf("os/arch = %q/%q, want linux/amd64", w.Requirements.OS, w.Requirements.Arch)
	}
	if len(w.Graph.Nodes) != 2 {
		t.Fatalf("nodes = %d, want 2", len(w.Graph.Nodes))
	}
	vet := w.Graph.Nodes["vet"]
	if !vet.Cache || vet.TimeoutS != 120 || vet.Run != "go vet ./..." {
		t.Errorf("vet node = %+v, want cache+timeout+run", vet)
	}
	test := w.Graph.Nodes["test"]
	if len(test.Needs) != 1 || test.Needs[0] != "vet" {
		t.Errorf("test needs = %v, want [vet]", test.Needs)
	}
	if w.State != "CREATED" {
		t.Errorf("state = %q, want CREATED (template)", w.State)
	}
}

func TestParseEmpty(t *testing.T) {
	if _, err := Parse([]byte("version: 1\nwork: {}\n")); err == nil {
		t.Error("Parse of empty work should fail")
	}
	if _, err := Parse([]byte("not: yaml: [")); err == nil {
		t.Error("Parse of invalid yaml should fail")
	}
}

func TestParseNoRequirements(t *testing.T) {
	// A pipeline without a requirements block must parse to
	// zero-value Requirements (no pool constraint).
	w, err := Parse([]byte("version: 1\nwork:\n  verify:\n    nodes:\n      a:\n        run: echo hi\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if w.Requirements.Pool != "" {
		t.Errorf("pool = %q, want empty", w.Requirements.Pool)
	}
}

func TestMatchesTrigger(t *testing.T) {
	if !MatchesTrigger([]byte(sampleYML), "push") {
		t.Error("push should match")
	}
	if !MatchesTrigger([]byte(sampleYML), "pull_request") {
		t.Error("pull_request should match")
	}
	if MatchesTrigger([]byte(sampleYML), "release") {
		t.Error("release should not match")
	}
	// Empty triggers = match everything.
	if !MatchesTrigger([]byte("version: 1\nwork:\n  verify:\n    nodes:\n      a:\n        run: echo\n"), "anything") {
		t.Error("empty triggers should match everything")
	}
}

func TestFetchFromGitHub(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/acme/widgets/contents/works.yml" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if r.URL.Query().Get("ref") != "abc123" {
			t.Errorf("ref = %q, want abc123", r.URL.Query().Get("ref"))
		}
		if r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("auth = %q, want Bearer tok", r.Header.Get("Authorization"))
		}
		enc := base64.StdEncoding.EncodeToString([]byte(sampleYML))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"content":"` + enc + `"}`))
	}))
	defer ts.Close()

	raw, err := fetchFromGitHub(context.Background(), "tok", "acme/widgets", "abc123", ts.URL)
	if err != nil {
		t.Fatalf("fetchFromGitHub: %v", err)
	}
	if !strings.Contains(string(raw), "avc-core") {
		t.Error("fetched content missing pool line")
	}
}

func TestFetchFromGitHub404(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer ts.Close()

	raw, err := fetchFromGitHub(context.Background(), "tok", "acme/widgets", "abc", ts.URL)
	if err != nil {
		t.Fatalf("fetchFromGitHub 404: %v", err)
	}
	if raw != nil {
		t.Errorf("raw = %q, want nil on 404", raw)
	}
}

func TestFetchFromGitHubError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"bad credentials"}`))
	}))
	defer ts.Close()

	if _, err := fetchFromGitHub(context.Background(), "tok", "acme/widgets", "abc", ts.URL); err == nil {
		t.Error("403 should error")
	}
}

func TestDecodeBase64StripsNewlines(t *testing.T) {
	// GitHub wraps long content with newlines; decode must strip them.
	raw := base64.StdEncoding.EncodeToString([]byte(sampleYML))
	wrapped := raw[:40] + "\n" + raw[40:80] + "\n" + raw[80:]
	out, err := decodeBase64(wrapped)
	if err != nil {
		t.Fatalf("decodeBase64: %v", err)
	}
	if string(out) != sampleYML {
		t.Error("decoded content mismatch")
	}
}

func TestFetchFromGitHubNoToken(t *testing.T) {
	if _, err := FetchFromGitHub(context.Background(), "", "acme/widgets", "abc"); err == nil {
		t.Error("empty token should error")
	}
}
