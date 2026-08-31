package cache_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/JonasAbde/works-execution/packages/cache"
	"github.com/JonasAbde/works-execution/packages/workgraph"

	_ "modernc.org/sqlite"
)

func testStore(t *testing.T) *cache.Store {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+t.TempDir()+"/cache.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	s, err := cache.New(db)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func sampleNode(run string) *workgraph.Node {
	return &workgraph.Node{ID: "build", Run: run}
}

func sampleWork(repo, sha string) *workgraph.Work {
	return &workgraph.Work{
		ID:    "wrk_cache_test",
		State: workgraph.StateQueued,
		Source: workgraph.Source{
			Type:       "github_push",
			Repository: repo,
			Ref:        "refs/heads/main",
			SHA:        sha,
		},
		Objective:    workgraph.Objective{Type: "verify_change"},
		Requirements: workgraph.Requirements{OS: "linux", Arch: "amd64"},
		Graph: workgraph.Graph{Nodes: map[string]workgraph.Node{
			"build": *sampleNode("echo hello"),
		}},
	}
}

func TestFingerprint_Deterministic(t *testing.T) {
	w := sampleWork("acme/widgets", "abcdef0123456789abcdef0123456789abcdef01")
	n := sampleNode("echo hello")
	k1 := cache.KeyFromNode(w, n, "organization")
	k2 := cache.KeyFromNode(w, n, "organization")
	f1, err := k1.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	f2, err := k2.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if f1 != f2 {
		t.Fatalf("same inputs produced different fingerprints: %s vs %s", f1, f2)
	}
	if len(f1) != 64 {
		t.Fatalf("fingerprint %q is not sha256-hex", f1)
	}
}

func TestFingerprint_InputSensitivity(t *testing.T) {
	w := sampleWork("acme/widgets", "abcdef0123456789abcdef0123456789abcdef01")
	n := sampleNode("echo hello")
	base, _ := cache.KeyFromNode(w, n, "organization").Fingerprint()

	// Change run command.
	n2 := sampleNode("echo hello2")
	changed, _ := cache.KeyFromNode(w, n2, "organization").Fingerprint()
	if changed == base {
		t.Fatal("run change must change fingerprint")
	}

	// Change SHA.
	w3 := sampleWork("acme/widgets", "1111111111111111111111111111111111111111")
	shaChanged, _ := cache.KeyFromNode(w3, n, "organization").Fingerprint()
	if shaChanged == base {
		t.Fatal("sha change must change fingerprint")
	}

	// Change scope.
	scopeChanged, _ := cache.KeyFromNode(w, n, "worker").Fingerprint()
	if scopeChanged == base {
		t.Fatal("scope change must change fingerprint")
	}

	// Env map key order must not matter (json.Marshal sorts keys).
	nEnv := sampleNode("echo hello")
	nEnv.Env = map[string]string{"A": "1", "B": "2"}
	nEnv2 := sampleNode("echo hello")
	nEnv2.Env = map[string]string{"B": "2", "A": "1"}
	a, _ := cache.KeyFromNode(w, nEnv, "organization").Fingerprint()
	b, _ := cache.KeyFromNode(w, nEnv2, "organization").Fingerprint()
	if a != b {
		t.Fatal("env key order must not change fingerprint")
	}
}

func TestStore_PutLookupRoundtrip(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)

	w := sampleWork("acme/widgets", "abcdef0123456789abcdef0123456789abcdef01")
	n := sampleNode("go test ./...")
	fp, _ := cache.KeyFromNode(w, n, "organization").Fingerprint()

	// Miss first.
	if _, err := s.Lookup(ctx, fp); err != cache.ErrNotFound {
		t.Fatalf("want ErrNotFound on cold cache, got %v", err)
	}

	// Put a success.
	err := s.Put(ctx, cache.Entry{
		Fingerprint: fp,
		WorkID:      w.ID,
		NodeID:      n.ID,
		ExitCode:    0,
		LogTail:     "PASS\nok\tacme/widgets\t0.5s",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Hit.
	e, err := s.Lookup(ctx, fp)
	if err != nil {
		t.Fatalf("want hit, got %v", err)
	}
	if e.WorkID != w.ID || e.NodeID != n.ID || e.ExitCode != 0 {
		t.Fatalf("entry mismatch: %+v", e)
	}
	if e.LogTail == "" {
		t.Fatal("log tail empty on hit")
	}
}

func TestStore_RefusesFailures(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	err := s.Put(ctx, cache.Entry{
		Fingerprint: "deadbeef",
		WorkID:      "wrk_x",
		NodeID:      "build",
		ExitCode:    1,
	})
	if err == nil {
		t.Fatal("cache.Put must refuse failed executions")
	}
}

func TestStore_IdempotentPut(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	fp := "cafebabe"
	e := cache.Entry{Fingerprint: fp, WorkID: "w", NodeID: "n", ExitCode: 0, LogTail: "ok"}
	if err := s.Put(ctx, e); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(ctx, e); err != nil {
		t.Fatalf("duplicate put must be a no-op, got %v", err)
	}
}
