package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/JonasAbde/works-execution/internal/dispatch"
)

type storeTestRevalidator struct {
	calls int
	proof dispatch.AuthorityProof
	err   error
}

func (r *storeTestRevalidator) Revalidate(_ context.Context, _, _ string) (dispatch.AuthorityProof, error) {
	r.calls++
	return r.proof, r.err
}

func storeTestGovernedRequest() dispatch.DispatchAuthorityRequest {
	return dispatch.DispatchAuthorityRequest{
		Dispatch: dispatch.Dispatch{
			MissionID:         "mission/golden-001",
			AuthorityRef:      "authority/lease-001",
			AuthorityEpoch:    7,
			RuntimeDispatchID: "rdisp/store-001",
			AttemptID:         "attempt/store-001",
			EffectID:          "effect/store-001",
			IdempotencyKey:    "idem/store-governed-001",
			BudgetRef:         "budget/store-001",
			BudgetCeiling:     100,
			CheckpointID:      "checkpoint/store-001",
			EvidenceRoot:      "evidence/store-001",
			VerificationSubj:  "subject/store-001",
			CausalID:          "causal/store-001",
		},
		ActionID:      "action/store-001",
		BindingDigest: "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789",
	}
}

func governedDispatchStore(t *testing.T, st *SQLiteStore) dispatch.GovernedStore {
	t.Helper()
	raw := st.DispatchAcceptanceStore()
	governed, ok := raw.(dispatch.GovernedStore)
	if !ok {
		t.Fatalf("dispatch store does not implement dispatch.GovernedStore: %T", raw)
	}
	return governed
}

func TestGovernedDispatchBindingSurvivesRestartAndBlocksActionSwap(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "works.db")
	st, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}

	rv := &storeTestRevalidator{proof: dispatch.AuthorityProof{EvidenceRef: "aie-evidence/store-001"}}
	svc, err := dispatch.NewAcceptanceService(
		governedDispatchStore(t, st),
		rv,
		func() time.Time { return time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC) },
	)
	if err != nil {
		t.Fatal(err)
	}
	req := storeTestGovernedRequest()
	first, err := svc.Accept(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if rv.calls != 1 {
		t.Fatalf("initial revalidation calls=%d want 1", rv.calls)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	restarted, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()

	// Replay must use the durable winner even when the external authority plane
	// is now unavailable; it must not mint a second acceptance or silently bind
	// a different action.
	rv2 := &storeTestRevalidator{err: errors.New("authority unavailable")}
	svc2, err := dispatch.NewAcceptanceService(governedDispatchStore(t, restarted), rv2, nil)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := svc2.Accept(context.Background(), req)
	if err != nil {
		t.Fatalf("restart replay: %v", err)
	}
	if rv2.calls != 0 {
		t.Fatalf("durable replay unexpectedly revalidated: calls=%d", rv2.calls)
	}
	if replayed.WorksExecutionID != first.WorksExecutionID ||
		replayed.ExecutionContextID != first.ExecutionContextID ||
		replayed.TraceID != first.TraceID {
		t.Fatalf("restart replay changed acceptance identity: first=%+v replay=%+v", first, replayed)
	}

	swapped := req
	swapped.ActionID = "action/store-EVIL"
	if _, err := svc2.Accept(context.Background(), swapped); !errors.Is(err, dispatch.ErrAuthorityBindingMismatch) {
		t.Fatalf("expected action swap to fail with ErrAuthorityBindingMismatch, got %v", err)
	}
}

func TestGovernedDispatchAuthorityBindingIsOutsideFrozenAcceptanceJSON(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "works.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	rv := &storeTestRevalidator{proof: dispatch.AuthorityProof{EvidenceRef: "aie-evidence/store-json"}}
	svc, err := dispatch.NewAcceptanceService(governedDispatchStore(t, st), rv, nil)
	if err != nil {
		t.Fatal(err)
	}
	req := storeTestGovernedRequest()
	if _, err := svc.Accept(context.Background(), req); err != nil {
		t.Fatal(err)
	}

	var acceptanceJSON string
	if err := st.db.QueryRow(
		`SELECT acceptance_json FROM dispatch_acceptances WHERE idempotency_key = ?`,
		req.Dispatch.IdempotencyKey,
	).Scan(&acceptanceJSON); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{req.ActionID, req.BindingDigest, "aie-evidence/store-json"} {
		if contains := stringContains(acceptanceJSON, forbidden); contains {
			t.Fatalf("frozen acceptance JSON leaked authority binding %q: %s", forbidden, acceptanceJSON)
		}
	}

	var actionID, digest, evidenceRef string
	if err := st.db.QueryRow(
		`SELECT action_id, binding_digest, evidence_ref
		 FROM dispatch_authority_bindings WHERE idempotency_key = ?`,
		req.Dispatch.IdempotencyKey,
	).Scan(&actionID, &digest, &evidenceRef); err != nil {
		t.Fatal(err)
	}
	if actionID != req.ActionID || digest != req.BindingDigest || evidenceRef != "aie-evidence/store-json" {
		t.Fatalf("authority binding row mismatch: %q %q %q", actionID, digest, evidenceRef)
	}
}

func stringContains(haystack, needle string) bool {
	if needle == "" {
		return false
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
