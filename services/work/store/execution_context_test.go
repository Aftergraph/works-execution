package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/JonasAbde/works-execution/packages/executioncontext"
	"github.com/JonasAbde/works-execution/packages/workgraph"
	"github.com/JonasAbde/works-execution/services/work/store"
)

func platformID(prefix, ch string) string {
	return prefix + "_" + string(make([]byte, 0)) + repeat(ch, 32)
}
func repeat(s string, n int) string {
	out := ""
	for i := 0; i < n; i++ {
		out += s
	}
	return out
}

func makeContext(workID, leaseID string) executioncontext.Context {
	return executioncontext.Context{
		Schema:              "execution-context/1.0",
		OrganizationID:      "org_11111111111111111111111111111111",
		TenantID:            "ten_22222222222222222222222222222222",
		PrincipalID:         "prn_33333333333333333333333333333333",
		MissionID:           "mis_example",
		AuthorityLeaseID:    "auth_44444444444444444444444444444444",
		WorkID:              workID,
		WorkerLeaseID:       leaseID,
		AdmissionDecisionID: "pdr_55555555555555555555555555555555",
		TraceID:             "trc_66666666666666666666666666666666",
	}
}

func TestCreateExecutionContextDerivesWorkerAndPersistsImmutableRecord(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	w := sampleWork()
	if err := s.CreateWork(ctx, w); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateState(ctx, w.ID, workgraph.StateQueued); err != nil {
		t.Fatal(err)
	}
	workerID := "wrkr_77777777777777777777777777777777"
	lease, _, err := s.GrantLease(ctx, w.ID, "a", workerID, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	in := makeContext(w.ID, lease.ID)
	got, err := s.CreateExecutionContext(ctx, in)
	if err != nil {
		t.Fatalf("create context: %v", err)
	}
	if got.ID == "" || got.WorkerID != workerID {
		t.Fatalf("derived fields wrong: %+v", got)
	}
	stored, err := s.GetExecutionContext(ctx, got.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ID != got.ID || stored.WorkerID != workerID {
		t.Fatalf("stored mismatch: %+v", stored)
	}
	in.TraceID = "trc_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if _, err := s.CreateExecutionContext(ctx, in); !errors.Is(err, store.ErrExecutionContextConflict) {
		t.Fatalf("expected immutable conflict, got %v", err)
	}
}

func TestCreateExecutionContextRejectsLeaseFromAnotherWork(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	w1, w2 := sampleWork(), sampleWork()
	if err := s.CreateWork(ctx, w1); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateWork(ctx, w2); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateState(ctx, w1.ID, workgraph.StateQueued); err != nil {
		t.Fatal(err)
	}
	lease, _, err := s.GrantLease(ctx, w1.ID, "a", "wrkr_77777777777777777777777777777777", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	in := makeContext(w2.ID, lease.ID)
	if _, err := s.CreateExecutionContext(ctx, in); !errors.Is(err, store.ErrExecutionContextLeaseMismatch) {
		t.Fatalf("expected lease mismatch, got %v", err)
	}
}

func TestExecutionContextReauthorizationCreatesLineageWithoutMutation(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	w := sampleWork()
	if err := s.CreateWork(ctx, w); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateState(ctx, w.ID, workgraph.StateQueued); err != nil {
		t.Fatal(err)
	}
	lease, _, err := s.GrantLease(ctx, w.ID, "a", "wrkr_77777777777777777777777777777777", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.CreateExecutionContext(ctx, makeContext(w.ID, lease.ID))
	if err != nil {
		t.Fatal(err)
	}
	secondIn := makeContext(w.ID, lease.ID)
	secondIn.AuthorityLeaseID = "auth_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	secondIn.AdmissionDecisionID = "pdr_cccccccccccccccccccccccccccccccc"
	secondIn.PriorContextID = first.ID
	second, err := s.CreateExecutionContext(ctx, secondIn)
	if err != nil {
		t.Fatalf("reauthorize: %v", err)
	}
	if second.ID == first.ID || second.PriorContextID != first.ID {
		t.Fatalf("lineage wrong: first=%+v second=%+v", first, second)
	}
	old, err := s.GetExecutionContext(ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if old.AuthorityLeaseID != "auth_44444444444444444444444444444444" {
		t.Fatalf("prior mutated: %+v", old)
	}
}
