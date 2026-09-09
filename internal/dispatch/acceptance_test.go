package dispatch

import (
	"errors"
	"sync"
	"testing"
	"time"
)

type memoryStore struct {
	mu  sync.Mutex
	byK map[string]*Acceptance
	byE map[string]*Acceptance
}

func newMemoryStore() *memoryStore {
	return &memoryStore{byK: map[string]*Acceptance{}, byE: map[string]*Acceptance{}}
}

func (m *memoryStore) LoadByIdempotency(key string) (*Acceptance, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.byK[key], nil
}

func (m *memoryStore) LoadByExecution(id string) (*Acceptance, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.byE[id], nil
}

func (m *memoryStore) Save(a *Acceptance) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := *a
	m.byK[a.Dispatch.IdempotencyKey] = &cp
	m.byE[a.WorksExecutionID] = &cp
	return nil
}

func testDispatch() Dispatch {
	return Dispatch{
		MissionID:         "mission/golden-001",
		AuthorityRef:      "a/value",
		AuthorityEpoch:    7,
		RuntimeDispatchID: "rdisp/1",
		AttemptID:         "attempt/1",
		EffectID:          "effect/1",
		IdempotencyKey:    "idem/1",
		BudgetRef:         "budget/1",
		BudgetCeiling:     100,
		CheckpointID:      "checkpoint/1",
		EvidenceRoot:      "evidence/1",
		VerificationSubj:  "subject/1",
		CausalID:          "causal/1",
	}
}

func newAcceptor() *Acceptor {
	return NewAcceptor(newMemoryStore(), func() time.Time {
		return time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)
	})
}

// 1. Runtime dies before WORKS accepts: no record exists, redispatch is a
// fresh accept, not a replay.
func TestAccept_FreshDispatchCreatesRecord(t *testing.T) {
	a := newAcceptor()
	acc, err := a.Accept(testDispatch(), 7)
	if err != nil {
		t.Fatal(err)
	}
	if acc.WorksExecutionID == "" || acc.Outcome != "ACCEPTED" {
		t.Fatalf("bad acceptance: %+v", acc)
	}
}

// 2/5/11. Runtime dies after accept, duplicate dispatch, replay after
// completion: same key + same causal identity returns the SAME record.
func TestAccept_DuplicateDispatchReturnsSameRecord(t *testing.T) {
	a := newAcceptor()
	d := testDispatch()
	first, err := a.Accept(d, 7)
	if err != nil {
		t.Fatal(err)
	}
	second, err := a.Accept(d, 7)
	if err != nil {
		t.Fatal(err)
	}
	if first.WorksExecutionID != second.WorksExecutionID {
		t.Fatal("duplicate dispatch created a second execution")
	}
}

// 12. Same idempotency key, different causal identity: fail closed.
func TestAccept_CausalMismatchFailsClosed(t *testing.T) {
	a := newAcceptor()
	d := testDispatch()
	if _, err := a.Accept(d, 7); err != nil {
		t.Fatal(err)
	}
	d2 := d
	d2.CausalID = "causal/EVIL"
	d2.RuntimeDispatchID = "rdisp/2"
	if _, err := a.Accept(d2, 7); !errors.Is(err, ErrCausalMismatch) {
		t.Fatalf("expected ErrCausalMismatch, got %v", err)
	}
}

// 6. Stale authority epoch at accept time: rejected.
func TestAccept_StaleAuthorityRejected(t *testing.T) {
	a := newAcceptor()
	if _, err := a.Accept(testDispatch(), 8); !errors.Is(err, ErrStaleAuthority) {
		t.Fatalf("expected ErrStaleAuthority, got %v", err)
	}
}

// 7. Mid-flight revocation blocks effect + verification.
func TestRevoke_BlocksDownstreamWork(t *testing.T) {
	a := newAcceptor()
	acc, _ := a.Accept(testDispatch(), 7)
	if err := a.Revoke(acc.WorksExecutionID); err != nil {
		t.Fatal(err)
	}
	if err := a.ApplyEffect(acc.WorksExecutionID, "effect/1", true); !errors.Is(err, ErrRevoked) {
		t.Fatalf("expected ErrRevoked on effect, got %v", err)
	}
	if err := a.RecordVerdict(acc.WorksExecutionID, "verifier/sentinel", "subject/1", true, true); !errors.Is(err, ErrRevoked) {
		t.Fatalf("expected ErrRevoked on verdict, got %v", err)
	}
}

// 8. Budget exhaustion cannot autonomously retry around the ceiling.
func TestSpend_BudgetCeilingEnforced(t *testing.T) {
	a := newAcceptor()
	acc, _ := a.Accept(testDispatch(), 7)
	if err := a.Spend(acc.WorksExecutionID, 60); err != nil {
		t.Fatal(err)
	}
	if err := a.Spend(acc.WorksExecutionID, 50); !errors.Is(err, ErrBudgetExhausted) {
		t.Fatalf("expected ErrBudgetExhausted, got %v", err)
	}
	if err := a.Spend(acc.WorksExecutionID, 40); err != nil {
		t.Fatalf("exact-ceiling spend must succeed, got %v", err)
	}
}

// 3/4. Worker dies before/after external effect: effect applies exactly once;
// unknown outcome resolves INDETERMINATE, never silent success.
func TestApplyEffect_ExactlyOnceAndIndeterminate(t *testing.T) {
	a := newAcceptor()
	acc, _ := a.Accept(testDispatch(), 7)
	if err := a.ApplyEffect(acc.WorksExecutionID, "effect/1", true); err != nil {
		t.Fatal(err)
	}
	if err := a.ApplyEffect(acc.WorksExecutionID, "effect/1", true); !errors.Is(err, ErrEffectDuplicate) {
		t.Fatalf("expected ErrEffectDuplicate, got %v", err)
	}

	a2 := newAcceptor()
	d := testDispatch()
	d.IdempotencyKey = "idem/2"
	d.EffectID = "effect/2"
	acc2, _ := a2.Accept(d, 7)
	if err := a2.ApplyEffect(acc2.WorksExecutionID, "effect/2", false); err != nil {
		t.Fatal(err)
	}
	got, _ := a2.store.LoadByExecution(acc2.WorksExecutionID)
	if got.Outcome != "INDETERMINATE" {
		t.Fatalf("expected INDETERMINATE, got %q", got.Outcome)
	}
}

// SUCCEEDED cannot become VERIFIED without independent evidence.
func TestVerdict_ExecutionSuccessIsNotVerification(t *testing.T) {
	a := newAcceptor()
	acc, _ := a.Accept(testDispatch(), 7)
	if err := a.Complete(acc.WorksExecutionID, "SUCCEEDED"); err != nil {
		t.Fatal(err)
	}
	got, _ := a.store.LoadByExecution(acc.WorksExecutionID)
	if got.Verified {
		t.Fatal("execution SUCCEEDED must not imply VERIFIED")
	}
	// 9. Verifier unavailable: stays UNVERIFIED.
	if err := a.RecordVerdict(acc.WorksExecutionID, "verifier/sentinel", "subject/1", true, false); !errors.Is(err, ErrVerifierUnavailable) {
		t.Fatalf("expected ErrVerifierUnavailable, got %v", err)
	}
	// 10. Stale verification subject: fails closed.
	if err := a.RecordVerdict(acc.WorksExecutionID, "verifier/sentinel", "subject/OLD", false, true); !errors.Is(err, ErrStaleSubject) {
		t.Fatalf("expected ErrStaleSubject, got %v", err)
	}
	// Executor cannot verify itself.
	if err := a.RecordVerdict(acc.WorksExecutionID, "rdisp/1", "subject/1", true, true); !errors.Is(err, ErrSelfVerification) {
		t.Fatalf("expected ErrSelfVerification, got %v", err)
	}
	// Independent verdict over the exact subject verifies.
	if err := a.RecordVerdict(acc.WorksExecutionID, "verifier/sentinel", "subject/1", true, true); err != nil {
		t.Fatal(err)
	}
	got, _ = a.store.LoadByExecution(acc.WorksExecutionID)
	if !got.Verified || got.VerifierID != "verifier/sentinel" {
		t.Fatalf("expected independent verification, got %+v", got)
	}
}

// Restart does not widen authority: a reloaded acceptance keeps epoch,
// revocation, spend and verified flags (durability seam, not amnesia).
func TestAccept_RestartPreservesAuthorityBounds(t *testing.T) {
	st := newMemoryStore()
	a := NewAcceptor(st, nil)
	acc, err := a.Accept(testDispatch(), 7)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Spend(acc.WorksExecutionID, 60); err != nil {
		t.Fatal(err)
	}
	restarted := NewAcceptor(st, nil)
	if err := restarted.Spend(acc.WorksExecutionID, 50); !errors.Is(err, ErrBudgetExhausted) {
		t.Fatalf("restart must not reset budget, got %v", err)
	}
	if _, err := restarted.Accept(testDispatch(), 9); !errors.Is(err, ErrStaleAuthority) {
		t.Fatalf("restart must not erase epoch, got %v", err)
	}
}
