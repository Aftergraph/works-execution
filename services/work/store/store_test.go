package store_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/JonasAbde/works-execution/packages/workgraph"
	"github.com/JonasAbde/works-execution/services/work/store"
)

func openTemp(t *testing.T) store.Store {
	t.Helper()
	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func sampleWork() *workgraph.Work {
	return &workgraph.Work{
		ID:    workgraph.NewID("wrk"),
		State: workgraph.StateCreated,
		Source: workgraph.Source{
			Type: "cli", Repository: "acme/demo", Revision: "abc123",
		},
		Objective: workgraph.Objective{Type: "verify_change"},
		Graph: workgraph.Graph{
			Nodes: map[string]workgraph.Node{
				"a": {ID: "a", Run: "echo a"},
				"b": {ID: "b", Run: "echo b", Needs: []string{"a"}},
			},
		},
		Requirements: workgraph.Requirements{OS: "linux", Arch: "amd64"},
		Policy:      workgraph.Policy{ForkPolicy: "deny", TrustClass: "standard"},
	}
}

func TestCreateAndGet_RoundTrip(t *testing.T) {
	s := openTemp(t)
	w := sampleWork()
	ctx := context.Background()
	if err := s.CreateWork(ctx, w); err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := s.GetWork(ctx, w.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.ID != w.ID {
		t.Errorf("ID mismatch: got %s want %s", got.ID, w.ID)
	}
	if got.State != workgraph.StateCreated {
		t.Errorf("state: got %s want CREATED", got.State)
	}
	if got.Source.Repository != "acme/demo" {
		t.Errorf("source.repository: got %q", got.Source.Repository)
	}
	if got.Graph.Nodes["a"].Run != "echo a" {
		t.Errorf("graph node a.run: got %q", got.Graph.Nodes["a"].Run)
	}
	if got.Policy.ForkPolicy != "deny" {
		t.Errorf("policy.fork_policy: got %q", got.Policy.ForkPolicy)
	}
}

func TestCreate_ValidationError(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	w := &workgraph.Work{ID: workgraph.NewID("wrk"), State: workgraph.StateCreated}
	// missing Objective.Type and Graph.Nodes
	if err := s.CreateWork(ctx, w); err == nil {
		t.Fatal("expected validation error")
	}
}

func TestUpdateState_HappyPath(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	w := sampleWork()
	if err := s.CreateWork(ctx, w); err != nil {
		t.Fatal(err)
	}
	got, err := s.UpdateState(ctx, w.ID, workgraph.StateQueued)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if got.State != workgraph.StateQueued {
		t.Errorf("state: got %s want QUEUED", got.State)
	}
}

func TestUpdateState_DeniesInvalidTransition(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	w := sampleWork()
	if err := s.CreateWork(ctx, w); err != nil {
		t.Fatal(err)
	}
	// CREATED -> SUCCEEDED is not allowed (must go through RUNNING -> VERIFYING)
	_, err := s.UpdateState(ctx, w.ID, workgraph.StateSucceeded)
	if err == nil {
		t.Fatal("expected error for skipping states")
	}
}

func TestUpdateState_NotFound(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	_, err := s.UpdateState(ctx, "wrk_nope", workgraph.StateQueued)
	if err != store.ErrNotFound {
		t.Errorf("got %v, want ErrNotFound", err)
	}
}

func TestAppendAttempt_AndEvidence_AndArtifact(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	w := sampleWork()
	if err := s.CreateWork(ctx, w); err != nil {
		t.Fatal(err)
	}

	attempt := workgraph.Attempt{
		ID:       workgraph.NewID("att"),
		NodeID:   "a",
		WorkerID: "wrkr_local_1",
		Status:   "succeeded",
		ExitCode: 0,
	}
	if _, err := s.AppendAttempt(ctx, w.ID, attempt); err != nil {
		t.Fatalf("append attempt: %v", err)
	}

	evidence := workgraph.Evidence{
		ID:        workgraph.NewID("evd"),
		NodeID:    "a",
		AttemptID: attempt.ID,
		Type:      "test",
		Result:    "pass",
		Signer:    "test-runner",
	}
	if _, err := s.AppendEvidence(ctx, w.ID, evidence); err != nil {
		t.Fatalf("append evidence: %v", err)
	}

	artifact := workgraph.Artifact{
		ID:       "art_abc",
		NodeID:   "a",
		MimeType: "text/plain",
		Size:     5,
		Path:     "/tmp/art_abc",
	}
	if _, err := s.AppendArtifact(ctx, w.ID, artifact); err != nil {
		t.Fatalf("append artifact: %v", err)
	}

	got, err := s.GetWork(ctx, w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Attempts) != 1 || got.Attempts[0].ID != attempt.ID {
		t.Errorf("attempts: %+v", got.Attempts)
	}
	if len(got.Evidence) != 1 || got.Evidence[0].ID != evidence.ID {
		t.Errorf("evidence: %+v", got.Evidence)
	}
	if len(got.Artifacts) != 1 || got.Artifacts[0].ID != artifact.ID {
		t.Errorf("artifacts: %+v", got.Artifacts)
	}
}

func TestIdempotency_RejectsDifferentPayload(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	w1 := sampleWork()
	w1.IdempotencyKey = "key_abc"
	if err := s.CreateWork(ctx, w1); err != nil {
		t.Fatal(err)
	}
	w2 := sampleWork()
	w2.IdempotencyKey = "key_abc" // same key
	// different ID -> conflict
	err := s.CreateWork(ctx, w2)
	if err != store.ErrIdempotencyConflict {
		t.Errorf("got %v, want ErrIdempotencyConflict", err)
	}
}

func TestIdempotency_SamePayload_Ok(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	w1 := sampleWork()
	w1.IdempotencyKey = "key_xyz"
	if err := s.CreateWork(ctx, w1); err != nil {
		t.Fatal(err)
	}
	// Same ID + same key = idempotent success
	if err := s.CreateWork(ctx, w1); err != nil {
		t.Errorf("idempotent re-create failed: %v", err)
	}
}

func TestListWorks_ReturnsRecentFirst(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		w := sampleWork()
		if err := s.CreateWork(ctx, w); err != nil {
			t.Fatal(err)
		}
	}
	list, err := s.ListWorks(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 3 {
		t.Errorf("got %d works, want 3", len(list))
	}
	// Most recent first (ordering is updated_at DESC)
	for i := 1; i < len(list); i++ {
		if list[i-1].UpdatedAt.Before(list[i].UpdatedAt) {
			t.Errorf("ordering violated at index %d", i)
		}
	}
}

// TestArtifactCollisionAcrossWorks guards the v1->v2 migration. Identical
// content hashes from different works MUST produce distinct rows.
func TestArtifactCollisionAcrossWorks(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	w1 := sampleWork()
	if err := s.CreateWork(ctx, w1); err != nil {
		t.Fatal(err)
	}
	w2 := sampleWork()
	if err := s.CreateWork(ctx, w2); err != nil {
		t.Fatal(err)
	}
	art := workgraph.Artifact{
		ID:       "sha256_same",
		NodeID:   "a",
		MimeType: "text/plain",
		Size:     5,
		Path:     "/tmp/x",
	}
	if _, err := s.AppendArtifact(ctx, w1.ID, art); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendArtifact(ctx, w2.ID, art); err != nil {
		t.Fatal(err)
	}
	got1, _ := s.GetWork(ctx, w1.ID)
	got2, _ := s.GetWork(ctx, w2.ID)
	if len(got1.Artifacts) != 1 || len(got2.Artifacts) != 1 {
		t.Errorf("expected 1 artifact per work; got %d / %d", len(got1.Artifacts), len(got2.Artifacts))
	}
}