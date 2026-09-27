package api_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/JonasAbde/works-execution/packages/verifiedstate"
	"github.com/JonasAbde/works-execution/packages/workgraph"
	"github.com/JonasAbde/works-execution/services/api"
	"github.com/JonasAbde/works-execution/services/work/store"
)

type stubResumeObserver struct {
	snapshot verifiedstate.Snapshot
	err      error
	calls    int
}

func (s *stubResumeObserver) ObserveResumeSnapshot(
	_ context.Context,
	_ string,
	_ verifiedstate.Checkpoint,
) (verifiedstate.Snapshot, error) {
	s.calls++
	if s.err != nil {
		return nil, s.err
	}
	out := make(verifiedstate.Snapshot, len(s.snapshot))
	for k, v := range s.snapshot {
		out[k] = v
	}
	return out, nil
}

func newVerifiedResumeServer(
	t *testing.T,
	s *store.SQLiteStore,
	observer api.ResumeWorldObserver,
) *httptest.Server {
	t.Helper()
	srv := &api.Server{Store: s, AuthEnabled: false, ResumeObserver: observer}
	mux := http.NewServeMux()
	api.WireResumeRoutes(mux, srv, testBridgeSecret)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts
}

func verifiedAPIResumeFixture(t *testing.T, s *store.SQLiteStore, id string) string {
	t.Helper()
	ctx := context.Background()
	w := &workgraph.Work{
		ID:        id,
		Objective: workgraph.Objective{Type: "custom"},
		Graph:     workgraph.Graph{Nodes: map[string]workgraph.Node{"do": {ID: "do", Run: "echo hi"}}},
		State:     workgraph.StateRunning,
		Mission: &workgraph.MissionContract{
			BudgetCeiling: &workgraph.BudgetCeiling{ComputeEUR: 5},
			Verification:  []workgraph.VerificationCriterion{{Criterion: "done"}},
			KillSwitch:    "always",
		},
	}
	if err := s.CreateWork(ctx, w); err != nil {
		t.Fatal(err)
	}
	p := func(subject string) *verifiedstate.VerificationRef {
		return &verifiedstate.VerificationRef{
			VerifierID:  "sentinel:api-resume-test",
			EvidenceRef: "dvr_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			SubjectRef:  subject,
			VerifiedAt:  time.Date(2026, 9, 27, 9, 30, 0, 0, time.UTC),
		}
	}
	cp, err := verifiedstate.NewCheckpoint(
		[]verifiedstate.Node{
			{ID: "repo-head", State: verifiedstate.StateVerified, Fingerprint: "git:a", Verification: p("git:Aftergraph/example@aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")},
			{ID: "build", State: verifiedstate.StateVerified, Fingerprint: "build:a", Verification: p("git:Aftergraph/example@bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")},
		},
		[]verifiedstate.Edge{{From: "repo-head", To: "build", Type: verifiedstate.EdgeData}},
		verifiedstate.Snapshot{"repo-head": "git:a"},
	)
	if err != nil {
		t.Fatal(err)
	}
	state := map[string]any{"node": "research"}
	if err := verifiedstate.AttachToStateSnapshot(state, cp); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SuspendWork(ctx, id, workgraph.StateWaitingHuman, &workgraph.Handoff{
		StateSnapshot: state,
		Narrative:     "verification-aware wait",
	}); err != nil {
		t.Fatal(err)
	}
	rec, err := s.LatestHandoffRecord(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	return rec.PayloadHash
}

func TestVerifiedResumeFailsClosedWithoutWorldObserver(t *testing.T) {
	s := openResumeTestStore(t)
	hash := verifiedAPIResumeFixture(t, s, "work:verified-no-observer")
	ts := newVerifiedResumeServer(t, s, nil)

	resp := postResume(t, ts, "work:verified-no-observer", testBridgeSecret,
		resumeBody("appr-1", "prin-1", "ten-1", hash, "idem-verified-no-observer"))
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", resp.StatusCode)
	}
	w, _ := s.GetWork(context.Background(), "work:verified-no-observer")
	if w.State != workgraph.StateWaitingHuman {
		t.Fatalf("missing observer mutated work to %s", w.State)
	}
}

func TestVerifiedResumeAllowsUnchangedObservedWorld(t *testing.T) {
	s := openResumeTestStore(t)
	hash := verifiedAPIResumeFixture(t, s, "work:verified-unchanged")
	observer := &stubResumeObserver{snapshot: verifiedstate.Snapshot{"repo-head": "git:a"}}
	ts := newVerifiedResumeServer(t, s, observer)

	resp := postResume(t, ts, "work:verified-unchanged", testBridgeSecret,
		resumeBody("appr-1", "prin-1", "ten-1", hash, "idem-verified-unchanged"))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if observer.calls != 1 {
		t.Fatalf("observer calls = %d, want 1", observer.calls)
	}
	w, _ := s.GetWork(context.Background(), "work:verified-unchanged")
	if w.State != workgraph.StateRunning {
		t.Fatalf("state = %s, want RUNNING", w.State)
	}
}

func TestVerifiedResumeBlocksChangedWorld(t *testing.T) {
	s := openResumeTestStore(t)
	hash := verifiedAPIResumeFixture(t, s, "work:verified-changed")
	observer := &stubResumeObserver{snapshot: verifiedstate.Snapshot{"repo-head": "git:b"}}
	ts := newVerifiedResumeServer(t, s, observer)

	resp := postResume(t, ts, "work:verified-changed", testBridgeSecret,
		resumeBody("appr-1", "prin-1", "ten-1", hash, "idem-verified-changed"))
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", resp.StatusCode)
	}
	w, _ := s.GetWork(context.Background(), "work:verified-changed")
	if w.State != workgraph.StateWaitingHuman {
		t.Fatalf("reconciliation conflict mutated work to %s", w.State)
	}
}

func TestVerifiedResumeFailsClosedWhenObservationFails(t *testing.T) {
	s := openResumeTestStore(t)
	hash := verifiedAPIResumeFixture(t, s, "work:verified-observe-fail")
	observer := &stubResumeObserver{err: errors.New("source unavailable")}
	ts := newVerifiedResumeServer(t, s, observer)

	resp := postResume(t, ts, "work:verified-observe-fail", testBridgeSecret,
		resumeBody("appr-1", "prin-1", "ten-1", hash, "idem-verified-observe-fail"))
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", resp.StatusCode)
	}
	w, _ := s.GetWork(context.Background(), "work:verified-observe-fail")
	if w.State != workgraph.StateWaitingHuman {
		t.Fatalf("observation failure mutated work to %s", w.State)
	}
}
