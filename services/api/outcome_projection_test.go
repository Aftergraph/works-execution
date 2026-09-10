package api

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/JonasAbde/works-execution/services/work/store"
)

func TestProjectOutcomeVerification_ErrorFailsClosed(t *testing.T) {
	boom := errors.New("store fault")
	_, err := projectOutcomeVerification(context.Background(),
		func(context.Context, string) (*store.VerificationVerdict, error) {
			return nil, boom
		}, "wrk_x")
	if !errors.Is(err, boom) {
		t.Fatalf("expected store fault to surface, got %v", err)
	}
}

func TestProjectOutcomeVerification_NilProjectsPending(t *testing.T) {
	got, err := projectOutcomeVerification(context.Background(),
		func(context.Context, string) (*store.VerificationVerdict, error) {
			return nil, nil
		}, "wrk_x")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Status != "pending" {
		t.Errorf("status: got %q, want pending", got.Status)
	}
}

func TestProjectOutcomeVerification_RecordProjectsVerdict(t *testing.T) {
	at := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	got, err := projectOutcomeVerification(context.Background(),
		func(context.Context, string) (*store.VerificationVerdict, error) {
			return &store.VerificationVerdict{
				WorkID: "wrk_x", Result: "failed", VerifierID: "v9",
				EvidenceRef: "evb_1", VerifiedAt: at,
			}, nil
		}, "wrk_x")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Status != "failed" || got.VerifierID != "v9" ||
		got.EvidenceRef != "evb_1" || got.VerifiedAt != "2026-09-10T12:00:00Z" {
		t.Errorf("projection mismatch: %+v", got)
	}
}
