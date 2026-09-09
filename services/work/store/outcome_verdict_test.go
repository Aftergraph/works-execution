package store_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/JonasAbde/works-execution/services/work/store"
)

func TestVerificationVerdictPersistsAcrossRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "works.db")

	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	w := newMissionWork("v")
	if err := s.CreateWork(ctx, w); err != nil {
		t.Fatalf("create work: %v", err)
	}

	want := store.VerificationVerdict{
		WorkID:      w.ID,
		Result:      "passed",
		VerifierID:  "spiffe://aftergraph/verifier/mission-1",
		EvidenceRef: "sha256:3f4f4fca7ef63dcbf13af4d15b8d2bece1804d2b4df68b66368b231ba0a33b24",
		VerifiedAt:  time.Date(2026, 9, 9, 18, 20, 0, 123456789, time.UTC),
	}
	if err := s.SaveVerificationVerdict(ctx, want); err != nil {
		t.Fatalf("save verdict: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s, err = store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	got, err := s.GetVerificationVerdict(ctx, w.ID)
	if err != nil {
		t.Fatalf("get verdict: %v", err)
	}
	if got == nil {
		t.Fatal("get verdict returned nil")
	}
	if got.WorkID != want.WorkID || got.Result != want.Result ||
		got.VerifierID != want.VerifierID || got.EvidenceRef != want.EvidenceRef ||
		!got.VerifiedAt.Equal(want.VerifiedAt) {
		t.Fatalf("verdict mismatch: got=%+v want=%+v", *got, want)
	}
}
