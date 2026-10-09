package api_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/JonasAbde/works-execution/packages/verifiedstate"
	"github.com/JonasAbde/works-execution/packages/workgraph"
	"github.com/JonasAbde/works-execution/services/work/store"
)

func observedResumeFixture(t *testing.T, s *store.SQLiteStore, id string) string {
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
			VerifierID:  "sentinel:observation-test",
			EvidenceRef: "dvr_cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
			SubjectRef:  subject,
			VerifiedAt:  time.Now().UTC().Add(-time.Minute),
		}
	}
	fingerprint := "git:Aftergraph/runtime@aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	cp, err := verifiedstate.NewObservedCheckpoint(
		[]verifiedstate.Node{
			{ID: "repo-head", State: verifiedstate.StateVerified, Fingerprint: fingerprint, Verification: p(fingerprint)},
			{ID: "build", State: verifiedstate.StateVerified, Fingerprint: "build:a", Verification: p("git:Aftergraph/runtime@bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")},
		},
		[]verifiedstate.Edge{{From: "repo-head", To: "build", Type: verifiedstate.EdgeData}},
		verifiedstate.Snapshot{"repo-head": fingerprint},
		[]verifiedstate.ObservationBinding{{
			NodeID: "repo-head", Kind: verifiedstate.ObservationGitRef, Locator: "github:Aftergraph/runtime#refs/heads/main",
		}},
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
		Narrative:     "cross-process observation wait",
	}); err != nil {
		t.Fatal(err)
	}
	rec, err := s.LatestHandoffRecord(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	return rec.PayloadHash
}

func bridgeObservationResumeBody(
	receipt, principal, tenant, hash, idem string,
	snapshot verifiedstate.Snapshot,
	observedAt time.Time,
	observationHash string,
) string {
	body := map[string]any{
		"approval_receipt_id": receipt,
		"principal_id":        principal,
		"tenant_id":           tenant,
		"checkpoint_hash":     hash,
		"idempotency_key":     idem,
		"observation": map[string]any{
			"schema":          "resume.observation/1.0",
			"observer_id":     "runtime:runtime-host",
			"checkpoint_hash": observationHash,
			"observed_at":     observedAt.UTC().Format(time.RFC3339Nano),
			"snapshot":        snapshot,
		},
	}
	raw, _ := json.Marshal(body)
	return string(raw)
}

func TestObservedHandoffExposesObservationPlanWithoutCanonicalFingerprint(t *testing.T) {
	s := openResumeTestStore(t)
	hash := observedResumeFixture(t, s, "work:observation-plan")
	ts := newBridgeTestServer(t, s, testBridgeSecret)

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/v1/works/work:observation-plan/handoff", nil)
	req.Header.Set("X-Works-Platform-Bridge", testBridgeSecret)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	rawBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(rawBody), "git:Aftergraph/runtime@aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa") {
		t.Fatal("handoff observation plan leaked prior canonical fingerprint")
	}
	var view struct {
		CheckpointHash    string `json:"checkpoint_hash"`
		VerificationAware bool   `json:"verification_aware"`
		Reconciliation    *struct {
			Schema           string `json:"schema"`
			CheckpointSchema string `json:"checkpoint_schema"`
			Subjects []struct {
				NodeID  string `json:"node_id"`
				Kind    string `json:"kind"`
				Locator string `json:"locator"`
			} `json:"subjects"`
		} `json:"reconciliation"`
	}
	if err := json.Unmarshal(rawBody, &view); err != nil {
		t.Fatal(err)
	}
	if view.CheckpointHash != hash || !view.VerificationAware || view.Reconciliation == nil {
		t.Fatalf("handoff view missing reconciliation plan: %+v", view)
	}
	if view.Reconciliation.Schema != "resume.observation-plan/1.0" ||
		view.Reconciliation.CheckpointSchema != verifiedstate.CheckpointSchemaObserved ||
		len(view.Reconciliation.Subjects) != 1 {
		t.Fatalf("reconciliation plan = %+v", view.Reconciliation)
	}
	subject := view.Reconciliation.Subjects[0]
	if subject.NodeID != "repo-head" || subject.Kind != "GIT_REF" ||
		subject.Locator != "github:Aftergraph/runtime#refs/heads/main" {
		t.Fatalf("subject plan = %+v", subject)
	}
}

func TestObservedResumeSucceedsFromFreshBridgeObservationWithoutLocalObserver(t *testing.T) {
	s := openResumeTestStore(t)
	hash := observedResumeFixture(t, s, "work:observation-fresh")
	ts := newVerifiedResumeServer(t, s, nil)
	fingerprint := "git:Aftergraph/runtime@aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

	resp := postResume(t, ts, "work:observation-fresh", testBridgeSecret,
		bridgeObservationResumeBody(
			"appr-1", "prin-1", "ten-1", hash, "idem-observation-fresh",
			verifiedstate.Snapshot{"repo-head": fingerprint}, time.Now(), hash,
		))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	w, _ := s.GetWork(context.Background(), "work:observation-fresh")
	if w.State != workgraph.StateRunning {
		t.Fatalf("state = %s, want RUNNING", w.State)
	}
}

func TestObservedResumeBlocksChangedBridgeObservation(t *testing.T) {
	s := openResumeTestStore(t)
	hash := observedResumeFixture(t, s, "work:observation-changed")
	ts := newVerifiedResumeServer(t, s, nil)

	resp := postResume(t, ts, "work:observation-changed", testBridgeSecret,
		bridgeObservationResumeBody(
			"appr-1", "prin-1", "ten-1", hash, "idem-observation-changed",
			verifiedstate.Snapshot{"repo-head": "git:Aftergraph/runtime@cccccccccccccccccccccccccccccccccccccccc"},
			time.Now(), hash,
		))
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", resp.StatusCode)
	}
	w, _ := s.GetWork(context.Background(), "work:observation-changed")
	if w.State != workgraph.StateWaitingHuman {
		t.Fatalf("changed observation mutated state to %s", w.State)
	}
}

func TestObservedResumeRejectsStaleOrWrongCheckpointObservation(t *testing.T) {
	tests := []struct {
		name            string
		observedAt      time.Time
		observationHash func(string) string
	}{
		{
			name:       "stale",
			observedAt: time.Now().Add(-10 * time.Minute),
			observationHash: func(hash string) string { return hash },
		},
		{
			name:       "wrong-checkpoint",
			observedAt: time.Now(),
			observationHash: func(hash string) string { return "deadbeef" + hash[8:] },
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := openResumeTestStore(t)
			workID := "work:observation-" + tc.name
			hash := observedResumeFixture(t, s, workID)
			ts := newVerifiedResumeServer(t, s, nil)
			fingerprint := "git:Aftergraph/runtime@aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
			resp := postResume(t, ts, workID, testBridgeSecret,
				bridgeObservationResumeBody(
					"appr-1", "prin-1", "ten-1", hash, "idem-"+tc.name,
					verifiedstate.Snapshot{"repo-head": fingerprint},
					tc.observedAt, tc.observationHash(hash),
				))
			if resp.StatusCode != http.StatusConflict {
				t.Fatalf("status = %d, want 409", resp.StatusCode)
			}
			w, _ := s.GetWork(context.Background(), workID)
			if w.State != workgraph.StateWaitingHuman {
				t.Fatalf("invalid observation mutated state to %s", w.State)
			}
		})
	}
}

func TestLegacyResumeRejectsUnexpectedObservation(t *testing.T) {
	s := openResumeTestStore(t)
	hash := resumeFixture(t, s, "work:legacy-observation")
	ts := newVerifiedResumeServer(t, s, nil)

	resp := postResume(t, ts, "work:legacy-observation", testBridgeSecret,
		bridgeObservationResumeBody(
			"appr-1", "prin-1", "ten-1", hash, "idem-legacy-observation",
			verifiedstate.Snapshot{"repo-head": "git:a"}, time.Now(), hash,
		))
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", resp.StatusCode)
	}
}
