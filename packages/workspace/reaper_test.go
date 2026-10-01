package workspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JonasAbde/works-execution/packages/secrets"
)

func registryWorkspace(id, provider string, expires time.Time) Workspace {
	return Workspace{
		ID: id, ProviderID: provider, Org: "aftergraph",
		WorkID: "wrk_0123456789abcdef0123456789abcdef",
		Name: "repo", RemoteURL: "https://example.invalid/repo.git",
		DefaultBranch: "main",
		Baseline: SourceRef{
			Provider: "github", Repository: "Aftergraph/runtime",
			Ref: "refs/heads/main",
		},
		Mode: ModeWrite,
		CredentialRef: secrets.Must("secret://workspace/credential"),
		CredentialID: "cred_1",
		CreatedAt: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC),
		ExpiresAt: expires,
	}
}

func TestFileRegistryRoundTripIsAtomicAndSecretRefOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workspaces.json")
	r, err := NewFileRegistry(path)
	if err != nil { t.Fatal(err) }

	ws := registryWorkspace("wsp_1", "fake", time.Date(2026,10,2,12,0,0,0,time.UTC))
	if err := r.Put(context.Background(), ws); err != nil { t.Fatal(err) }

	got, err := r.Get(context.Background(), ws.ID)
	if err != nil { t.Fatal(err) }
	if got.ID != ws.ID || got.CredentialRef == nil || got.CredentialRef.String() != ws.CredentialRef.String() {
		t.Fatalf("bad roundtrip: %#v", got)
	}

	raw, err := os.ReadFile(path)
	if err != nil { t.Fatal(err) }
	text := string(raw)
	if strings.Contains(text, `"Value"`) {
		t.Fatalf("Go secret implementation leaked: %s", text)
	}
	if !strings.Contains(text, `"credential_ref": "secret://workspace/credential"`) {
		t.Fatalf("inert credential ref missing: %s", text)
	}

	list, err := r.List(context.Background())
	if err != nil { t.Fatal(err) }
	if len(list) != 1 || list[0].ID != ws.ID {
		t.Fatalf("bad list: %#v", list)
	}

	if err := r.Delete(context.Background(), ws.ID); err != nil { t.Fatal(err) }
	if _, err := r.Get(context.Background(), ws.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get after delete: %v", err)
	}
}

func TestFileRegistryFailsClosedOnCorruption(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workspaces.json")
	if err := os.WriteFile(path, []byte("{not-json"), 0o600); err != nil { t.Fatal(err) }
	r, err := NewFileRegistry(path)
	if err != nil { t.Fatal(err) }
	if _, err := r.List(context.Background()); err == nil {
		t.Fatal("corrupt registry was silently treated as empty")
	}
}

type reapProvider struct {
	mu sync.Mutex
	id string
	destroyed []string
	fail map[string]error
}
func (p *reapProvider) ID() string { return p.id }
func (p *reapProvider) Create(context.Context, Spec) (Workspace,error) { return Workspace{}, errors.New("unused") }
func (p *reapProvider) Get(context.Context, Workspace) (Workspace,error) { return Workspace{}, errors.New("unused") }
func (p *reapProvider) Candidate(context.Context, Workspace) (Candidate,error) { return Candidate{}, errors.New("unused") }
func (p *reapProvider) RevokeCredential(context.Context, Workspace) error { return errors.New("unused") }
func (p *reapProvider) Destroy(_ context.Context, ws Workspace) error {
	p.mu.Lock(); defer p.mu.Unlock()
	if err := p.fail[ws.ID]; err != nil { return err }
	for _, id := range p.destroyed {
		if id == ws.ID { return ErrNotFound }
	}
	p.destroyed = append(p.destroyed, ws.ID)
	return nil
}

func TestReaperDestroysOnlyExpiredAndIsIdempotent(t *testing.T) {
	now := time.Date(2026,10,2,12,0,0,0,time.UTC)
	r, err := NewFileRegistry(filepath.Join(t.TempDir(), "workspaces.json"))
	if err != nil { t.Fatal(err) }

	expired := registryWorkspace("wsp_expired", "fake", now.Add(-time.Minute))
	future := registryWorkspace("wsp_future", "fake", now.Add(time.Hour))
	noExpiry := registryWorkspace("wsp_no_expiry", "fake", time.Time{})
	for _, ws := range []Workspace{expired,future,noExpiry} {
		if err := r.Put(context.Background(), ws); err != nil { t.Fatal(err) }
	}

	p := &reapProvider{id:"fake",fail:map[string]error{}}
	reaper, err := NewReaper(r, map[string]Provider{"fake":p})
	if err != nil { t.Fatal(err) }

	result, err := reaper.Sweep(context.Background(), now)
	if err != nil { t.Fatal(err) }
	if result.Scanned != 3 || result.Expired != 1 || result.Reaped != 1 || result.Failed != 0 {
		t.Fatalf("bad result: %#v", result)
	}
	if len(p.destroyed) != 1 || p.destroyed[0] != expired.ID {
		t.Fatalf("bad destroyed set: %#v", p.destroyed)
	}
	if _, err := r.Get(context.Background(), expired.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired registry entry survived: %v", err)
	}
	if _, err := r.Get(context.Background(), future.ID); err != nil { t.Fatal(err) }
	if _, err := r.Get(context.Background(), noExpiry.ID); err != nil { t.Fatal(err) }

	again, err := reaper.Sweep(context.Background(), now)
	if err != nil { t.Fatal(err) }
	if again.Reaped != 0 || again.Expired != 0 {
		t.Fatalf("reaper not idempotent: %#v", again)
	}
}

func TestReaperLeavesFailuresForRetryAndTreatsProviderNotFoundAsReaped(t *testing.T) {
	now := time.Date(2026,10,2,12,0,0,0,time.UTC)
	r, err := NewFileRegistry(filepath.Join(t.TempDir(), "workspaces.json"))
	if err != nil { t.Fatal(err) }
	fail := registryWorkspace("wsp_fail", "fake", now.Add(-time.Minute))
	gone := registryWorkspace("wsp_gone", "gone", now.Add(-time.Minute))
	unknown := registryWorkspace("wsp_unknown", "missing-provider", now.Add(-time.Minute))
	for _, ws := range []Workspace{fail,gone,unknown} {
		if err := r.Put(context.Background(), ws); err != nil { t.Fatal(err) }
	}

	p := &reapProvider{id:"fake",fail:map[string]error{"wsp_fail":errors.New("transient")}}
	goneProvider := &reapProvider{id:"gone",fail:map[string]error{"wsp_gone":ErrNotFound}}
	reaper, err := NewReaper(r, map[string]Provider{"fake":p,"gone":goneProvider})
	if err != nil { t.Fatal(err) }

	result, err := reaper.Sweep(context.Background(), now)
	if err != nil { t.Fatal(err) }
	if result.Expired != 3 || result.Reaped != 1 || result.Failed != 2 {
		t.Fatalf("bad failure result: %#v", result)
	}
	if _, err := r.Get(context.Background(), gone.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("already-gone entry was not acknowledged: %v", err)
	}
	if _, err := r.Get(context.Background(), fail.ID); err != nil { t.Fatal(err) }
	if _, err := r.Get(context.Background(), unknown.ID); err != nil { t.Fatal(err) }
}
