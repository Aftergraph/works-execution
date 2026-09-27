package store_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/JonasAbde/works-execution/packages/verifiedstate"
	"github.com/JonasAbde/works-execution/packages/workgraph"
	"github.com/JonasAbde/works-execution/services/work/store"
)

func verifiedResumeHandoff(t *testing.T, rootEdge verifiedstate.EdgeType) *workgraph.Handoff {
	t.Helper()
	proof := func(subject string) *verifiedstate.VerificationRef {
		return &verifiedstate.VerificationRef{
			VerifierID:  "sentinel:resume-test",
			EvidenceRef: "dvr_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			SubjectRef:  subject,
			VerifiedAt:  time.Date(2026, 9, 27, 9, 0, 0, 0, time.UTC),
		}
	}
	nodes := []verifiedstate.Node{
		{
			ID:           "root",
			State:        verifiedstate.StateVerified,
			Fingerprint:  "root:v1",
			Verification: proof("git:Aftergraph/example@aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"),
		},
		{
			ID:           "outcome",
			State:        verifiedstate.StateVerified,
			Fingerprint:  "outcome:v1",
			Verification: proof("git:Aftergraph/example@bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"),
		},
	}
	cp, err := verifiedstate.NewCheckpoint(
		nodes,
		[]verifiedstate.Edge{{From: "root", To: "outcome", Type: rootEdge}},
		verifiedstate.Snapshot{"root": "root:v1"},
	)
	if err != nil {
		t.Fatal(err)
	}
	h := missionHandoff("verification-aware suspend")
	if err := verifiedstate.AttachToStateSnapshot(h.StateSnapshot, cp); err != nil {
		t.Fatal(err)
	}
	return h
}

func suspendVerifiedResumeWork(t *testing.T, s *store.SQLiteStore, id string, edge verifiedstate.EdgeType) string {
	t.Helper()
	ctx := context.Background()
	w := newMissionWork(id)
	w.State = workgraph.StateRunning
	if err := s.CreateWork(ctx, w); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SuspendWork(ctx, w.ID, workgraph.StateWaitingHuman, verifiedResumeHandoff(t, edge)); err != nil {
		t.Fatal(err)
	}
	rec, err := s.LatestHandoffRecord(ctx, w.ID)
	if err != nil {
		t.Fatal(err)
	}
	return rec.PayloadHash
}

func TestVerifiedCheckpointCannotUseBlindResume(t *testing.T) {
	ctx := context.Background()
	s, _ := store.Open(t.TempDir() + "/w.db")
	defer s.Close()
	hash := suspendVerifiedResumeWork(t, s, "r", verifiedstate.EdgeData)
	_ = hash
	workID := newMissionWork("r").ID

	if _, _, err := s.ResumeFromCheckpoint(ctx, workID); !errors.Is(err, store.ErrReconciliationRequired) {
		t.Fatalf("blind resume error = %v, want ErrReconciliationRequired", err)
	}
	w, err := s.GetWork(ctx, workID)
	if err != nil {
		t.Fatal(err)
	}
	if w.State != workgraph.StateWaitingHuman {
		t.Fatalf("blind resume mutated state to %s", w.State)
	}
}

func TestVerifiedCheckpointResumesAfterUnchangedObservation(t *testing.T) {
	ctx := context.Background()
	s, _ := store.Open(t.TempDir() + "/w.db")
	defer s.Close()
	hash := suspendVerifiedResumeWork(t, s, "s", verifiedstate.EdgeData)
	workID := newMissionWork("s").ID

	w, _, result, err := s.ResumeFromCheckpointReconciled(
		ctx, workID, hash, verifiedstate.Snapshot{"root": "root:v1"},
	)
	if err != nil {
		t.Fatalf("reconciled resume: %v", err)
	}
	if len(result.Impacted) != 0 {
		t.Fatalf("unchanged observation impacted = %#v", result.Impacted)
	}
	if w.State != workgraph.StateRunning {
		t.Fatalf("state = %s, want RUNNING", w.State)
	}
}

func TestVerifiedCheckpointBlocksStaleDataDependency(t *testing.T) {
	ctx := context.Background()
	s, _ := store.Open(t.TempDir() + "/w.db")
	defer s.Close()
	hash := suspendVerifiedResumeWork(t, s, "t", verifiedstate.EdgeData)
	workID := newMissionWork("t").ID

	_, _, result, err := s.ResumeFromCheckpointReconciled(
		ctx, workID, hash, verifiedstate.Snapshot{"root": "root:v2"},
	)
	if !errors.Is(err, store.ErrReconciliationRequired) {
		t.Fatalf("reconciled resume error = %v, want ErrReconciliationRequired", err)
	}
	want := []verifiedstate.Impact{
		{NodeID: "outcome", State: verifiedstate.StateStale},
		{NodeID: "root", State: verifiedstate.StateStale},
	}
	if !reflect.DeepEqual(result.Impacted, want) {
		t.Fatalf("impacted = %#v, want %#v", result.Impacted, want)
	}
	w, _ := s.GetWork(ctx, workID)
	if w.State != workgraph.StateWaitingHuman {
		t.Fatalf("blocked reconciliation mutated state to %s", w.State)
	}
}

func TestVerifiedCheckpointHardAuthorityInvalidatesDownstream(t *testing.T) {
	ctx := context.Background()
	s, _ := store.Open(t.TempDir() + "/w.db")
	defer s.Close()
	hash := suspendVerifiedResumeWork(t, s, "u", verifiedstate.EdgeAuthority)
	workID := newMissionWork("u").ID

	_, _, result, err := s.ResumeFromCheckpointReconciled(
		ctx, workID, hash, verifiedstate.Snapshot{"root": "root:v2"},
	)
	if !errors.Is(err, store.ErrReconciliationRequired) {
		t.Fatalf("reconciled resume error = %v, want ErrReconciliationRequired", err)
	}
	want := []verifiedstate.Impact{
		{NodeID: "outcome", State: verifiedstate.StateInvalidated},
		{NodeID: "root", State: verifiedstate.StateStale},
	}
	if !reflect.DeepEqual(result.Impacted, want) {
		t.Fatalf("impacted = %#v, want %#v", result.Impacted, want)
	}
}

func TestReconciledResumeBindsExactCheckpointHash(t *testing.T) {
	ctx := context.Background()
	s, _ := store.Open(t.TempDir() + "/w.db")
	defer s.Close()
	hash := suspendVerifiedResumeWork(t, s, "v", verifiedstate.EdgeData)
	workID := newMissionWork("v").ID

	_, _, _, err := s.ResumeFromCheckpointReconciled(
		ctx, workID, "deadbeef"+hash[8:], verifiedstate.Snapshot{"root": "root:v1"},
	)
	if !errors.Is(err, store.ErrStaleHandoff) {
		t.Fatalf("wrong checkpoint hash error = %v, want ErrStaleHandoff", err)
	}
}

func TestMalformedVerifiedCheckpointFailsClosed(t *testing.T) {
	ctx := context.Background()
	s, _ := store.Open(t.TempDir() + "/w.db")
	defer s.Close()
	w := newMissionWork("w")
	w.State = workgraph.StateRunning
	if err := s.CreateWork(ctx, w); err != nil {
		t.Fatal(err)
	}
	h := missionHandoff("malformed verified checkpoint")
	h.StateSnapshot[verifiedstate.HandoffStateKey] = map[string]any{
		"schema": "verified-state-checkpoint/999",
	}
	if _, err := s.SuspendWork(ctx, w.ID, workgraph.StateWaitingHuman, h); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.ResumeFromCheckpoint(ctx, w.ID); !errors.Is(err, store.ErrInvalidReconciliationCheckpoint) {
		t.Fatalf("malformed checkpoint resume error = %v, want ErrInvalidReconciliationCheckpoint", err)
	}
}
