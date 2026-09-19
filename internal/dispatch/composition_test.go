package dispatch

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type governedMemoryStore struct {
	*memoryStore
	muAuthority sync.Mutex
	authority   map[string]*AuthorityBinding
}

func newGovernedMemoryStore() *governedMemoryStore {
	return &governedMemoryStore{
		memoryStore: newMemoryStore(),
		authority:   map[string]*AuthorityBinding{},
	}
}

func cloneAuthorityBinding(b *AuthorityBinding) *AuthorityBinding {
	if b == nil {
		return nil
	}
	cp := *b
	return &cp
}

func (s *governedMemoryStore) LoadAuthorityBinding(key string) (*AuthorityBinding, error) {
	s.muAuthority.Lock()
	defer s.muAuthority.Unlock()
	return cloneAuthorityBinding(s.authority[key]), nil
}

func (s *governedMemoryStore) AcceptRevalidatedIfAbsent(
	candidate *Acceptance,
	binding AuthorityBinding,
) (*Acceptance, *AuthorityBinding, error) {
	s.muAuthority.Lock()
	defer s.muAuthority.Unlock()

	key := candidate.Dispatch.IdempotencyKey
	if existingBinding := s.authority[key]; existingBinding != nil {
		existing, err := s.memoryStore.LoadByIdempotency(key)
		if err != nil {
			return nil, nil, err
		}
		if existing == nil {
			return nil, nil, errors.New("orphan authority binding")
		}
		return existing, cloneAuthorityBinding(existingBinding), nil
	}

	s.memoryStore.mu.Lock()
	defer s.memoryStore.mu.Unlock()
	if existing := s.memoryStore.byK[key]; existing != nil {
		return nil, nil, errors.New("legacy/governed idempotency collision")
	}
	b := binding
	s.authority[key] = &b
	cp := cloneAcceptance(candidate)
	s.memoryStore.byK[key] = cp
	s.memoryStore.byE[candidate.WorksExecutionID] = cp
	return cloneAcceptance(cp), cloneAuthorityBinding(&b), nil
}

type recordingRevalidator struct {
	mu       sync.Mutex
	calls    int
	proof    AuthorityProof
	err      error
	actionID string
	digest   string
}

func (r *recordingRevalidator) Revalidate(_ context.Context, actionID, digest string) (AuthorityProof, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	r.actionID = actionID
	r.digest = digest
	return r.proof, r.err
}

func (r *recordingRevalidator) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

func governedRequest() DispatchAuthorityRequest {
	return DispatchAuthorityRequest{
		Dispatch:      testDispatch(),
		ActionID:      "action/runtime-001",
		BindingDigest: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
	}
}

func newGovernedService(t *testing.T, store *governedMemoryStore, rv *recordingRevalidator) *AcceptanceService {
	t.Helper()
	svc, err := NewAcceptanceService(store, rv, func() time.Time {
		return time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC)
	})
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

func TestAcceptanceService_RevalidatesBeforeFirstDurableAccept(t *testing.T) {
	store := newGovernedMemoryStore()
	rv := &recordingRevalidator{proof: AuthorityProof{EvidenceRef: "aie-evidence/revalidate-001"}}
	svc := newGovernedService(t, store, rv)

	acc, err := svc.Accept(context.Background(), governedRequest())
	if err != nil {
		t.Fatal(err)
	}
	if acc.WorksExecutionID == "" || acc.Outcome != "ACCEPTED" {
		t.Fatalf("bad acceptance: %+v", acc)
	}
	if rv.callCount() != 1 {
		t.Fatalf("revalidation calls=%d want 1", rv.callCount())
	}
	binding, err := store.LoadAuthorityBinding(governedRequest().Dispatch.IdempotencyKey)
	if err != nil {
		t.Fatal(err)
	}
	if binding == nil || binding.ActionID != governedRequest().ActionID ||
		binding.BindingDigest != governedRequest().BindingDigest ||
		binding.EvidenceRef != "aie-evidence/revalidate-001" {
		t.Fatalf("bad persisted authority binding: %+v", binding)
	}
}

func TestAcceptanceService_ReplayDoesNotReauthorizeOrRemint(t *testing.T) {
	store := newGovernedMemoryStore()
	rv := &recordingRevalidator{proof: AuthorityProof{EvidenceRef: "aie-evidence/revalidate-001"}}
	svc := newGovernedService(t, store, rv)

	first, err := svc.Accept(context.Background(), governedRequest())
	if err != nil {
		t.Fatal(err)
	}
	rv.err = errors.New("authority now unavailable")
	second, err := svc.Accept(context.Background(), governedRequest())
	if err != nil {
		t.Fatalf("accepted replay must return durable winner without reauthorization: %v", err)
	}
	if rv.callCount() != 1 {
		t.Fatalf("replay revalidated authority: calls=%d", rv.callCount())
	}
	if second.WorksExecutionID != first.WorksExecutionID ||
		second.ExecutionContextID != first.ExecutionContextID ||
		second.TraceID != first.TraceID {
		t.Fatalf("replay changed durable identity: first=%+v second=%+v", first, second)
	}
}

func TestAcceptanceService_SameIdempotencyDifferentActionFailsClosed(t *testing.T) {
	store := newGovernedMemoryStore()
	rv := &recordingRevalidator{proof: AuthorityProof{EvidenceRef: "aie-evidence/revalidate-001"}}
	svc := newGovernedService(t, store, rv)
	req := governedRequest()
	if _, err := svc.Accept(context.Background(), req); err != nil {
		t.Fatal(err)
	}

	req.ActionID = "action/evil"
	if _, err := svc.Accept(context.Background(), req); !errors.Is(err, ErrAuthorityBindingMismatch) {
		t.Fatalf("expected ErrAuthorityBindingMismatch, got %v", err)
	}
	if rv.callCount() != 1 {
		t.Fatalf("mismatched replay must fail before revalidation, calls=%d", rv.callCount())
	}
}

func TestAcceptanceService_FailsClosedWithoutEvidenceOrOnRevalidationFailure(t *testing.T) {
	for _, tc := range []struct {
		name string
		rv   *recordingRevalidator
		want error
	}{
		{
			name: "missing evidence",
			rv:   &recordingRevalidator{proof: AuthorityProof{}},
			want: ErrAuthorityEvidenceMissing,
		},
		{
			name: "revalidation failure",
			rv:   &recordingRevalidator{err: errors.New("revoked")},
			want: ErrAuthorityRevalidationFailed,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newGovernedMemoryStore()
			svc := newGovernedService(t, store, tc.rv)
			if _, err := svc.Accept(context.Background(), governedRequest()); !errors.Is(err, tc.want) {
				t.Fatalf("expected %v, got %v", tc.want, err)
			}
			acc, err := store.LoadByIdempotency(governedRequest().Dispatch.IdempotencyKey)
			if err != nil {
				t.Fatal(err)
			}
			if acc != nil {
				t.Fatalf("failed authority check persisted acceptance: %+v", acc)
			}
		})
	}
}

func TestAcceptanceService_RejectsMalformedBindingDigest(t *testing.T) {
	store := newGovernedMemoryStore()
	rv := &recordingRevalidator{proof: AuthorityProof{EvidenceRef: "aie-evidence/revalidate-001"}}
	svc := newGovernedService(t, store, rv)
	req := governedRequest()
	req.BindingDigest = "not-a-sha256"
	if _, err := svc.Accept(context.Background(), req); !errors.Is(err, ErrAuthorityBindingMismatch) {
		t.Fatalf("expected ErrAuthorityBindingMismatch, got %v", err)
	}
	if rv.callCount() != 0 {
		t.Fatalf("malformed identity reached external authority: calls=%d", rv.callCount())
	}
}
