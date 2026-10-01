package workspace

import (
	"context"
	"encoding/json"
	"errors"
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
func (f *fakeCredentialStore) Resolve(_ context.Context, ref *secrets.Ref, _ string) (string, error) {
	f.mu.Lock(); defer f.mu.Unlock()
	return f.values[ref.String()], nil
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
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/repos") && r.URL.Query().Get("search") != "":
			writeJSON(w, map[string]any{"success":true,"result":[]any{}})
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/repos/") && strings.HasSuffix(r.URL.Path, "/import"):
			sawImport = true
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["url"] != "https://github.com/Aftergraph/runtime.git" {
				t.Fatalf("wrong import url: %#v", body)
			}
			writeJSON(w, map[string]any{
				"success": true,
				"result": map[string]any{
					"id": "repo_123", "name": strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/accounts/acct/artifacts/namespaces/default/repos/"), "/import"), "default_branch": "main",
					"remote": "https://acct.artifacts.cloudflare.net/git/default/workspace.git",
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
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/repos") && r.URL.Query().Get("search") != "":
			writeJSON(w, map[string]any{"success":true,"result":[]any{}})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/repos/baseline/fork"):
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			name, _ := body["name"].(string)
			writeJSON(w, map[string]any{"success":true,"result":map[string]any{
				"id":"repo_fork","name":name,"default_branch":"main",
				"remote":"https://acct.artifacts.cloudflare.net/git/default/"+name+".git",
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
		ProviderID:CloudflareArtifactsProviderID, Org:"aftergraph",
		WorkID:"wrk_0123456789abcdef0123456789abcdef", Name:"agent-3",
		DefaultBranch:"main",
	}
	ws.ID = workspaceHandleID(ws.ProviderID, ws.Org, ws.WorkID, ws.Name, ws.DefaultBranch)
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


func TestCloudflareArtifactsRestartSafeIdempotentReplay(t *testing.T) {
	spec := validSpec()
	spec.Name = "CF Replay"
	name, prefix, err := cloudflareWorkspaceName(spec)
	if err != nil { t.Fatal(err) }

	var importedOrForked bool
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/repos") && r.URL.Query().Get("search") == prefix:
			writeJSON(w, map[string]any{"success":true,"result":[]any{map[string]any{
				"id":"repo_existing","name":name,"default_branch":"main",
				"remote":"https://acct.artifacts.cloudflare.net/git/default/"+name+".git",
			}}})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/tokens"):
			writeJSON(w, map[string]any{"success":true,"result":map[string]any{
				"id":"tok_replay","plaintext":"replay-token","scope":"write","expires_at":"2026-10-01T19:00:00Z",
			}})
		case r.Method == http.MethodPost && (strings.HasSuffix(r.URL.Path, "/import") || strings.HasSuffix(r.URL.Path, "/fork")):
			importedOrForked = true
			t.Fatalf("idempotent replay attempted repository creation: %s", r.URL.Path)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	}))
	defer ts.Close()

	p, err := NewCloudflareArtifactsProvider(CloudflareArtifactsConfig{
		AccountID:"acct", Namespace:"default",
		ControlTokenRef:secrets.Must("secret://cloudflare/control"),
		TokenTTL:time.Hour, BaseURL:ts.URL,
	}, fakeResolver{value:"control-secret"}, &fakeCredentialStore{}, ts.Client())
	if err != nil { t.Fatal(err) }

	ws, err := p.Create(context.Background(), spec)
	if err != nil { t.Fatal(err) }
	if importedOrForked { t.Fatal("replay mutated repository set") }
	wantID := workspaceHandleID(CloudflareArtifactsProviderID, spec.Org, spec.WorkID, name, "main")
	if ws.Name != name || ws.ID != wantID {
		t.Fatalf("bad replay workspace: %#v wantID=%q", ws, wantID)
	}
}

func TestCloudflareArtifactsIdempotencyConflictFailsClosed(t *testing.T) {
	original := validSpec()
	original.Name = "original"
	_, prefix, err := cloudflareWorkspaceName(original)
	if err != nil { t.Fatal(err) }

	changed := original
	changed.Name = "changed"
	changedName, _, err := cloudflareWorkspaceName(changed)
	if err != nil { t.Fatal(err) }

	var minted bool
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/repos") && r.URL.Query().Get("search") == prefix:
			writeJSON(w, map[string]any{"success":true,"result":[]any{map[string]any{
				"id":"repo_original","name":prefix+"different-fingerprint-original","default_branch":"main",
				"remote":"https://acct.artifacts.cloudflare.net/git/default/original.git",
			}}})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/tokens"):
			minted = true
			t.Fatal("credential minted for conflicting replay")
		default:
			t.Fatalf("conflict reached unexpected operation for %q: %s %s", changedName, r.Method, r.URL.String())
		}
	}))
	defer ts.Close()

	p, err := NewCloudflareArtifactsProvider(CloudflareArtifactsConfig{
		AccountID:"acct", Namespace:"default",
		ControlTokenRef:secrets.Must("secret://cloudflare/control"),
		TokenTTL:time.Hour, BaseURL:ts.URL,
	}, fakeResolver{value:"control-secret"}, &fakeCredentialStore{}, ts.Client())
	if err != nil { t.Fatal(err) }

	if _, err := p.Create(context.Background(), changed); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("got %v want ErrIdempotencyConflict", err)
	}
	if minted { t.Fatal("minted credential on conflict") }
}
