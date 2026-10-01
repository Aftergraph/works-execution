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

type fakeGitIssuer struct {
	mu sync.Mutex
	issued []string
	revoked []string
}
func (f *fakeGitIssuer) Issue(_ context.Context, repository, workID string, mode Mode, ttl time.Duration) (IssuedCredential, error) {
	f.mu.Lock(); defer f.mu.Unlock()
	f.issued = append(f.issued, repository+"|"+workID+"|"+string(mode))
	return IssuedCredential{ID:"gh_tok_1", Plaintext:"short-lived-git-token", ExpiresAt:time.Now().Add(ttl).UTC()}, nil
}
func (f *fakeGitIssuer) Revoke(_ context.Context, id, plaintext string) error {
	f.mu.Lock(); defer f.mu.Unlock()
	f.revoked = append(f.revoked,id+"|"+plaintext)
	return nil
}

func TestGitHubWorkspaceCreateCandidateDestroy(t *testing.T) {
	const baseline = "0123456789012345678901234567890123456789"
	const candidate = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	var branch string
	var deleted bool

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer control-secret" {
			t.Fatalf("unexpected auth %q", r.Header.Get("Authorization"))
		}
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/git/matching-refs/heads/works/"):
			writeGitJSON(w, []any{})
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/git/commits/"+baseline):
			writeGitJSON(w, map[string]any{"sha":baseline})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/git/refs"):
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			ref, _ := body["ref"].(string)
			branch = strings.TrimPrefix(ref,"refs/heads/")
			writeGitJSON(w, map[string]any{"ref":ref,"object":map[string]any{"sha":baseline,"type":"commit"}})
		case r.Method == http.MethodGet && r.URL.Path == "/repos/Aftergraph/runtime":
			writeGitJSON(w, map[string]any{"full_name":"Aftergraph/runtime","clone_url":"https://github.com/Aftergraph/runtime.git","default_branch":"main"})
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/git/ref/heads/"):
			sha := candidate
			writeGitJSON(w, map[string]any{"ref":"refs/heads/"+branch,"object":map[string]any{"sha":sha,"type":"commit"}})
		case r.Method == http.MethodDelete && strings.Contains(r.URL.Path, "/git/refs/heads/"):
			deleted = true
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	}))
	defer ts.Close()

	store := &fakeCredentialStore{}
	issuer := &fakeGitIssuer{}
	p, err := NewGitHubWorkspaceProvider(GitHubWorkspaceConfig{
		ControlTokenRef:secrets.Must("secret://github/control"),
		APIBase:ts.URL,
		TokenTTL:time.Hour,
	}, fakeResolver{value:"control-secret"}, issuer, store, ts.Client())
	if err != nil { t.Fatal(err) }

	spec := validSpec()
	spec.Name = "agent github"
	ws, err := p.Create(context.Background(), spec)
	if err != nil { t.Fatal(err) }
	if ws.ProviderID != GitHubWorkspaceProviderID || ws.DefaultBranch == "" {
		t.Fatalf("bad workspace: %#v", ws)
	}
	if ws.CredentialRef == nil || store.values[ws.CredentialRef.String()] != "short-lived-git-token" {
		t.Fatal("short-lived credential was not isolated behind secret ref")
	}

	c, err := p.Candidate(context.Background(), ws)
	if err != nil { t.Fatal(err) }
	if c.SHA != candidate || c.Ref != "refs/heads/"+ws.DefaultBranch {
		t.Fatalf("bad candidate: %#v", c)
	}

	if err := p.Destroy(context.Background(), ws); err != nil { t.Fatal(err) }
	if !deleted { t.Fatal("workspace branch not deleted") }
	if len(issuer.revoked) != 1 || issuer.revoked[0] != "gh_tok_1|short-lived-git-token" {
		t.Fatalf("credential not revoked: %#v", issuer.revoked)
	}
}

func TestGitHubWorkspaceCreateRaceRecoversExactBranch(t *testing.T) {
	const baseline = "0123456789012345678901234567890123456789"
	spec := validSpec()
	branchWant, _, err := (&GitHubWorkspaceProvider{cfg:GitHubWorkspaceConfig{BranchPrefix:"works"}}).workspaceBranch(spec)
	if err != nil { t.Fatal(err) }

	matchingCalls := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/git/matching-refs/heads/works/"):
			matchingCalls++
			if matchingCalls == 1 {
				writeGitJSON(w, []any{})
				return
			}
			writeGitJSON(w, []any{map[string]any{
				"ref":"refs/heads/"+branchWant,
				"object":map[string]any{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","type":"commit"},
			}})
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/git/commits/"+baseline):
			writeGitJSON(w, map[string]any{"sha":baseline})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/git/refs"):
			w.WriteHeader(http.StatusUnprocessableEntity)
			writeGitJSON(w, map[string]any{"message":"Reference already exists"})
		case r.Method == http.MethodGet && r.URL.Path == "/repos/Aftergraph/runtime":
			writeGitJSON(w, map[string]any{"clone_url":"https://github.com/Aftergraph/runtime.git"})
		default:
			t.Fatalf("unexpected request: %s %s",r.Method,r.URL.String())
		}
	}))
	defer ts.Close()

	p, err := NewGitHubWorkspaceProvider(GitHubWorkspaceConfig{
		ControlTokenRef:secrets.Must("secret://github/control"),
		APIBase:ts.URL, TokenTTL:time.Hour,
	}, fakeResolver{value:"control-secret"}, &fakeGitIssuer{}, &fakeCredentialStore{}, ts.Client())
	if err != nil { t.Fatal(err) }
	ws, err := p.Create(context.Background(), spec)
	if err != nil {
		t.Fatalf("create race should reconcile exact branch: %v", err)
	}
	if ws.DefaultBranch != branchWant {
		t.Fatalf("branch=%q want %q", ws.DefaultBranch, branchWant)
	}
	if matchingCalls != 2 {
		t.Fatalf("matching refs calls=%d want 2", matchingCalls)
	}
}

func TestGitHubWorkspaceDeterministicBranchName(t *testing.T) {
	p := &GitHubWorkspaceProvider{cfg:GitHubWorkspaceConfig{BranchPrefix:"works"}}
	a := validSpec()
	a.Name = "Agent / Unsafe Name"
	b := a
	if p.branchName(a) != p.branchName(b) {
		t.Fatal("same idempotency key produced different branch")
	}
	if strings.ContainsAny(p.branchName(a), " ") {
		t.Fatalf("unsafe branch name: %q", p.branchName(a))
	}
}

func writeGitJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type","application/json")
	if w.Header().Get("Content-Type") != "" {
		_ = json.NewEncoder(w).Encode(v)
	}
}


func TestGitHubWorkspaceRestartSafeIdempotentReplay(t *testing.T) {
	spec := validSpec()
	spec.Name = "agent replay"
	keyHash, specHash, err := workspaceIdentity(spec)
	if err != nil { t.Fatal(err) }
	prefix := "works/" + keyHash + "-"
	branch := prefix + specHash + "-agent-replay"
	const existingSHA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

	var createdRef, deletedRef bool
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/git/matching-refs/heads/works/"+keyHash+"-"):
			writeGitJSON(w, []any{map[string]any{
				"ref": "refs/heads/" + branch,
				"object": map[string]any{"sha": existingSHA, "type": "commit"},
			}})
		case r.Method == http.MethodGet && r.URL.Path == "/repos/Aftergraph/runtime":
			writeGitJSON(w, map[string]any{"full_name":"Aftergraph/runtime","clone_url":"https://github.com/Aftergraph/runtime.git","default_branch":"main"})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/git/refs"):
			createdRef = true
			t.Fatalf("idempotent replay attempted to create a second branch")
		case r.Method == http.MethodDelete && strings.Contains(r.URL.Path, "/git/refs/heads/"):
			deletedRef = true
			t.Fatalf("idempotent replay attempted to delete existing branch")
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	}))
	defer ts.Close()

	issuer := &fakeGitIssuer{}
	p, err := NewGitHubWorkspaceProvider(GitHubWorkspaceConfig{
		ControlTokenRef: secrets.Must("secret://github/control"),
		APIBase: ts.URL, TokenTTL: time.Hour,
	}, fakeResolver{value:"control-secret"}, issuer, &fakeCredentialStore{}, ts.Client())
	if err != nil { t.Fatal(err) }

	ws, err := p.Create(context.Background(), spec)
	if err != nil { t.Fatal(err) }
	if ws.DefaultBranch != branch {
		t.Fatalf("replay branch=%q want %q", ws.DefaultBranch, branch)
	}
	if createdRef || deletedRef {
		t.Fatal("existing workspace was mutated during replay")
	}
	if len(issuer.issued) != 1 {
		t.Fatalf("expected fresh short-lived credential, issued=%#v", issuer.issued)
	}
}

func TestGitHubWorkspaceIdempotencyConflictFailsClosed(t *testing.T) {
	original := validSpec()
	original.Name = "original"
	keyHash, originalHash, err := workspaceIdentity(original)
	if err != nil { t.Fatal(err) }
	existing := "works/" + keyHash + "-" + originalHash + "-original"

	changed := original
	changed.Name = "changed"
	_, changedHash, err := workspaceIdentity(changed)
	if err != nil { t.Fatal(err) }
	if changedHash == originalHash {
		t.Fatal("fixture did not change spec fingerprint")
	}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/git/matching-refs/heads/works/"+keyHash+"-") {
			writeGitJSON(w, []any{map[string]any{
				"ref": "refs/heads/" + existing,
				"object": map[string]any{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","type":"commit"},
			}})
			return
		}
		t.Fatalf("conflicting replay reached unexpected provider operation: %s %s", r.Method, r.URL.String())
	}))
	defer ts.Close()

	issuer := &fakeGitIssuer{}
	p, err := NewGitHubWorkspaceProvider(GitHubWorkspaceConfig{
		ControlTokenRef: secrets.Must("secret://github/control"),
		APIBase: ts.URL, TokenTTL: time.Hour,
	}, fakeResolver{value:"control-secret"}, issuer, &fakeCredentialStore{}, ts.Client())
	if err != nil { t.Fatal(err) }

	if _, err := p.Create(context.Background(), changed); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("got %v want ErrIdempotencyConflict", err)
	}
	if len(issuer.issued) != 0 {
		t.Fatalf("credential issued for conflicting replay: %#v", issuer.issued)
	}
}
