package workspace

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JonasAbde/works-execution/packages/secrets"
)

type fakeResolver struct{ value string }
func (f fakeResolver) Resolve(context.Context, *secrets.Ref, string) (string, error) { return f.value, nil }

type fakeCredentialStore struct {
	mu sync.Mutex
	values map[string]string
}
func (f *fakeCredentialStore) Put(_ context.Context, workID, provider, name, value string, _ time.Time) (*secrets.Ref, error) {
	f.mu.Lock(); defer f.mu.Unlock()
	if f.values == nil { f.values = map[string]string{} }
	ref := secrets.Must("secret://workspace/" + name)
	f.values[ref.String()] = value
	return ref, nil
}
func (f *fakeCredentialStore) Delete(_ context.Context, ref *secrets.Ref) error {
	f.mu.Lock(); defer f.mu.Unlock()
	delete(f.values, ref.String())
	return nil
}

func TestCloudflareArtifactsCreateImportsAndMintsRevocableToken(t *testing.T) {
	var sawImport, sawVerify, sawMint bool
	var authLeak bool

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer control-secret" {
			t.Fatalf("wrong control auth: %q", r.Header.Get("Authorization"))
		}
		if strings.Contains(r.URL.String(), "control-secret") {
			authLeak = true
		}
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/repos/agent-1/import"):
			sawImport = true
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["url"] != "https://github.com/Aftergraph/runtime.git" {
				t.Fatalf("wrong import url: %#v", body)
			}
			writeJSON(w, map[string]any{
				"success": true,
				"result": map[string]any{
					"id": "repo_123", "name": "agent-1", "default_branch": "main",
					"remote": "https://acct.artifacts.cloudflare.net/git/default/agent-1.git",
					"token": "initial-token-must-not-be-persisted",
				},
			})
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/commit/0123456789012345678901234567890123456789"):
			sawVerify = true
			writeJSON(w, map[string]any{"success": true, "result": map[string]any{"hash": "0123456789012345678901234567890123456789"}})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/tokens"):
			sawMint = true
			writeJSON(w, map[string]any{
				"success": true,
				"result": map[string]any{
					"id": "tok_123",
					"plaintext": "agent-git-token",
					"scope": "write",
					"expires_at": "2026-10-01T19:00:00Z",
				},
			})
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	}))
	defer ts.Close()

	store := &fakeCredentialStore{}
	p, err := NewCloudflareArtifactsProvider(CloudflareArtifactsConfig{
		AccountID: "acct",
		Namespace: "default",
		ControlTokenRef: secrets.Must("secret://cloudflare/control"),
		TokenTTL: time.Hour,
		BaseURL: ts.URL,
	}, fakeResolver{value:"control-secret"}, store, ts.Client())
	if err != nil { t.Fatal(err) }

	spec := validSpec()
	spec.Name = "agent-1"
	ws, err := p.Create(context.Background(), spec)
	if err != nil { t.Fatal(err) }
	if !sawImport || !sawVerify || !sawMint {
		t.Fatalf("expected import=%v verify=%v mint=%v", sawImport, sawVerify, sawMint)
	}
	if authLeak {
		t.Fatal("control token leaked into request URL")
	}
	if ws.CredentialID != "tok_123" {
		t.Fatalf("missing revocable token id: %#v", ws)
	}
	if ws.CredentialRef == nil {
		t.Fatal("missing credential ref")
	}
	if got := store.values[ws.CredentialRef.String()]; got != "agent-git-token" {
		t.Fatalf("wrong stored credential value: %q", got)
	}
	for _, v := range store.values {
		if v == "initial-token-must-not-be-persisted" {
			t.Fatal("initial Cloudflare token was persisted")
		}
	}
}

func TestCloudflareArtifactsForkAndRevoke(t *testing.T) {
	var revoked string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/repos/baseline/fork"):
			writeJSON(w, map[string]any{"success":true,"result":map[string]any{
				"id":"repo_fork","name":"agent-2","default_branch":"main",
				"remote":"https://acct.artifacts.cloudflare.net/git/default/agent-2.git",
				"token":"ignored-initial",
			}})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/tokens"):
			writeJSON(w, map[string]any{"success":true,"result":map[string]any{
				"id":"tok_revoke","plaintext":"repo-token","scope":"write","expires_at":"2026-10-01T19:00:00Z",
			}})
		case r.Method == http.MethodDelete && strings.Contains(r.URL.Path, "/tokens/"):
			revoked = r.URL.Path
			writeJSON(w, map[string]any{"success":true,"result":map[string]any{"id":"tok_revoke"}})
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	}))
	defer ts.Close()

	store := &fakeCredentialStore{}
	p, err := NewCloudflareArtifactsProvider(CloudflareArtifactsConfig{
		AccountID:"acct", Namespace:"default",
		ControlTokenRef:secrets.Must("secret://cloudflare/control"),
		TokenTTL:time.Hour, BaseURL:ts.URL,
	}, fakeResolver{value:"control-secret"}, store, ts.Client())
	if err != nil { t.Fatal(err) }

	spec := validSpec()
	spec.Name = "agent-2"
	spec.Baseline.Provider = CloudflareArtifactsProviderID
	spec.Baseline.Repository = "baseline"
	ws, err := p.Create(context.Background(), spec)
	if err != nil { t.Fatal(err) }
	if err := p.RevokeCredential(context.Background(), ws); err != nil { t.Fatal(err) }
	if !strings.HasSuffix(revoked, "/tokens/tok_revoke") {
		t.Fatalf("wrong revoke endpoint: %q", revoked)
	}
	if _, ok := store.values[ws.CredentialRef.String()]; ok {
		t.Fatal("credential store entry survived revocation")
	}
}

func TestCloudflareArtifactsCandidateUsesImmutableSHA(t *testing.T) {
	const sha = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || !strings.HasSuffix(r.URL.Path, "/repos/agent-3/log") {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
		writeJSON(w, map[string]any{"success":true,"result":map[string]any{
			"commits":[]any{map[string]any{"hash":sha}},
		}})
	}))
	defer ts.Close()

	p, err := NewCloudflareArtifactsProvider(CloudflareArtifactsConfig{
		AccountID:"acct", Namespace:"default",
		ControlTokenRef:secrets.Must("secret://cloudflare/control"),
		TokenTTL:time.Hour, BaseURL:ts.URL,
	}, fakeResolver{value:"control-secret"}, &fakeCredentialStore{}, ts.Client())
	if err != nil { t.Fatal(err) }

	ws := Workspace{
		ID:"repo_3", ProviderID:CloudflareArtifactsProviderID, Org:"aftergraph",
		WorkID:"wrk_0123456789abcdef0123456789abcdef", Name:"agent-3",
		DefaultBranch:"main",
	}
	c, err := p.Candidate(context.Background(), ws)
	if err != nil { t.Fatal(err) }
	if c.SHA != sha || c.Ref != "refs/heads/main" {
		t.Fatalf("bad candidate: %#v", c)
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
