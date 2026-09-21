// Package dispatch implements the Runtime → WORKS dispatch acceptance seal
// (contract:dispatch.acceptance/1.0).
//
// Runtime dispatches; WORKS accepts durably. Acceptance is the exact seam
// where "Runtime sent" becomes "WORKS owns": at-most-once effect identity,
// authority freshness at accept time, budget ceiling binding, and a
// verification gate that never lets SUCCEEDED become VERIFIED without an
// independent verdict. Ambiguous consequential state resolves to
// INDETERMINATE, never to silent success.
package dispatch

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/JonasAbde/works-execution/packages/executioncontext"
)

// Sentinel failures. All are fail-closed: callers must not proceed with
// protected work when Accept or a transition returns one of these.
var (
	ErrMissingBinding         = errors.New("dispatch: missing mission/authority/dispatch/idempotency binding")
	ErrStaleAuthority         = errors.New("dispatch: authority epoch is stale")
	ErrCausalMismatch         = errors.New("dispatch: idempotency key already bound to a different causal identity")
	ErrUnknownAcceptance      = errors.New("dispatch: unknown works execution")
	ErrEffectDuplicate        = errors.New("dispatch: effect already applied")
	ErrEffectUnknown          = errors.New("dispatch: effect outcome unknown; INDETERMINATE")
	ErrBudgetExhausted        = errors.New("dispatch: budget ceiling exhausted; autonomous retry forbidden")
	ErrRevoked                = errors.New("dispatch: authority revoked mid-flight")
	ErrSelfVerification       = errors.New("dispatch: executor cannot verify itself")
	ErrStaleSubject           = errors.New("dispatch: verification subject is stale")
	ErrVerifierUnavailable    = errors.New("dispatch: verifier unavailable; outcome stays UNVERIFIED")
	ErrInvalidSpend           = errors.New("dispatch: spend amount must be positive")
	ErrInvalidBudget          = errors.New("dispatch: persisted budget state is invalid")
	ErrExecutionNotTerminal   = errors.New("dispatch: verification requires terminal execution outcome")
	ErrMissingVerdictEvidence = errors.New("dispatch: verdict result and evidence reference are required")
	ErrInvalidVerdict         = errors.New("dispatch: verdict result must be ACCEPT or REJECT")
	ErrV2StoreRequired        = errors.New("dispatch: contextual V2 store required")
	ErrContextBinding         = errors.New("dispatch: V2 execution-context binding mismatch")
	ErrWorkerLeaseUnavailable = errors.New("dispatch: V2 worker lease unavailable")
	ErrSubjectNotBound         = errors.New("dispatch: verification subject not bound")
	ErrSubjectConflict         = errors.New("dispatch: verification subject already bound to different subject")
	ErrInvalidSubject          = errors.New("dispatch: invalid verification subject")
)

// Dispatch is the Runtime-built envelope. WORKS never mints these identities;
// it only accepts and records them.
type Dispatch struct {
	MissionID         string
	AuthorityRef      string
	AuthorityEpoch    int64
	RuntimeDispatchID string
	AttemptID         string
	EffectID          string
	IdempotencyKey    string
	BudgetRef         string
	BudgetCeiling     int64
	CheckpointID      string
	EvidenceRoot      string
	VerificationSubj  string
	CausalID          string
}

// VerificationVerdict is the immutable proof reference attached to an execution
// verdict. The verifier identity remains duplicated on Acceptance for
// compatibility with the frozen 1.0 record shape.
type VerificationVerdict struct {
	Result      string
	Subject     string
	EvidenceRef string
	RecordedAt  time.Time
}

// Acceptance is the durable WORKS-owned record.
type Acceptance struct {
	ContractVersion  string
	WorkID           string
	WorksExecutionID string
	Dispatch         Dispatch
	AcceptedAt       time.Time
	AuthorityEpochAt int64
	EffectApplied    bool
	BudgetSpent      int64
	Revoked          bool
	Outcome          string // ACCEPTED | SUCCEEDED | FAILED | INDETERMINATE
	Verified         bool
	VerifierID       string
	Verdict          *VerificationVerdict
	// ExecutionContextID and TraceID are WORKS-minted correlation identities
	// bound at accept time (execution-context/1.0). They never arrive on the
	// Runtime Dispatch envelope — clients cannot choose either field — and are
	// stable across duplicate/retry of the same idempotency key because only the
	// winning AcceptIfAbsent insert persists; every replay returns the winner's.
	ExecutionContextID string
	TraceID            string
}

// Store is the durability seam. Production uses SQLite; tests use memory.
type Store interface {
	LoadByIdempotency(key string) (*Acceptance, error)
	LoadByExecution(id string) (*Acceptance, error)
	// AcceptIfAbsent must atomically insert on the idempotency key and return
	// the winner when another request already inserted the same key.
	AcceptIfAbsent(a *Acceptance) (*Acceptance, error)
	Save(a *Acceptance) error
}

// V2Binding is correlation input for dispatch.acceptance/2.0. It carries
// references that WORKS must bind into the real execution-context/1.0.
// Possession of these references is not authority; consequential action still
// requires Trust Gateway action-time revalidation through AIE.
type V2Binding struct {
	WorkID              string
	OrganizationID      string
	TenantID            string
	PrincipalID         string
	AuthorityLeaseID    string
	WorkerLeaseID       string
	AdmissionDecisionID string
}

// V2Store extends the durable acceptance store with one atomic operation that
// inserts the acceptance and materializes the canonical execution context.
// A V2 implementation must return the committed winner on idempotent replay.
type V2Store interface {
	Store
	AcceptContextualIfAbsent(
		ctx context.Context,
		accepted *Acceptance,
		binding V2Binding,
	) (*Acceptance, *executioncontext.Context, error)
}

// VerificationSubjectBinding is an observed post-effect immutable subject.
// It is not accepted from initial dispatch because the candidate does not yet
// exist at that point.
type VerificationSubjectBinding struct {
	WorksExecutionID string
	WorkID           string
	AttemptID        string
	EffectID         string
	CausalID         string
	Subject          string
	BoundAt          time.Time
}

type VerificationSubjectStore interface {
	BindVerificationSubject(
		ctx context.Context,
		binding VerificationSubjectBinding,
	) (*VerificationSubjectBinding, error)
	LoadVerificationSubject(
		ctx context.Context,
		worksExecutionID string,
	) (*VerificationSubjectBinding, error)
}

var exactGitSubjectPattern = regexp.MustCompile(
	`^git:[A-Za-z0-9._-]+/[A-Za-z0-9._-]+@[a-f0-9]{40}// Package dispatch implements the Runtime → WORKS dispatch acceptance seal
// (contract:dispatch.acceptance/1.0).
//
// Runtime dispatches; WORKS accepts durably. Acceptance is the exact seam
// where "Runtime sent" becomes "WORKS owns": at-most-once effect identity,
// authority freshness at accept time, budget ceiling binding, and a
// verification gate that never lets SUCCEEDED become VERIFIED without an
// independent verdict. Ambiguous consequential state resolves to
// INDETERMINATE, never to silent success.
package dispatch

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/JonasAbde/works-execution/packages/executioncontext"
)

// Sentinel failures. All are fail-closed: callers must not proceed with
// protected work when Accept or a transition returns one of these.
var (
	ErrMissingBinding         = errors.New("dispatch: missing mission/authority/dispatch/idempotency binding")
	ErrStaleAuthority         = errors.New("dispatch: authority epoch is stale")
	ErrCausalMismatch         = errors.New("dispatch: idempotency key already bound to a different causal identity")
	ErrUnknownAcceptance      = errors.New("dispatch: unknown works execution")
	ErrEffectDuplicate        = errors.New("dispatch: effect already applied")
	ErrEffectUnknown          = errors.New("dispatch: effect outcome unknown; INDETERMINATE")
	ErrBudgetExhausted        = errors.New("dispatch: budget ceiling exhausted; autonomous retry forbidden")
	ErrRevoked                = errors.New("dispatch: authority revoked mid-flight")
	ErrSelfVerification       = errors.New("dispatch: executor cannot verify itself")
	ErrStaleSubject           = errors.New("dispatch: verification subject is stale")
	ErrVerifierUnavailable    = errors.New("dispatch: verifier unavailable; outcome stays UNVERIFIED")
	ErrInvalidSpend           = errors.New("dispatch: spend amount must be positive")
	ErrInvalidBudget          = errors.New("dispatch: persisted budget state is invalid")
	ErrExecutionNotTerminal   = errors.New("dispatch: verification requires terminal execution outcome")
	ErrMissingVerdictEvidence = errors.New("dispatch: verdict result and evidence reference are required")
	ErrInvalidVerdict         = errors.New("dispatch: verdict result must be ACCEPT or REJECT")
	ErrV2StoreRequired        = errors.New("dispatch: contextual V2 store required")
	ErrContextBinding         = errors.New("dispatch: V2 execution-context binding mismatch")
	ErrWorkerLeaseUnavailable = errors.New("dispatch: V2 worker lease unavailable")
	ErrSubjectNotBound         = errors.New("dispatch: verification subject not bound")
	ErrSubjectConflict         = errors.New("dispatch: verification subject already bound to different subject")
	ErrInvalidSubject          = errors.New("dispatch: invalid verification subject")
)

// Dispatch is the Runtime-built envelope. WORKS never mints these identities;
// it only accepts and records them.
type Dispatch struct {
	MissionID         string
	AuthorityRef      string
	AuthorityEpoch    int64
	RuntimeDispatchID string
	AttemptID         string
	EffectID          string
	IdempotencyKey    string
	BudgetRef         string
	BudgetCeiling     int64
	CheckpointID      string
	EvidenceRoot      string
	VerificationSubj  string
	CausalID          string
}

// VerificationVerdict is the immutable proof reference attached to an execution
// verdict. The verifier identity remains duplicated on Acceptance for
// compatibility with the frozen 1.0 record shape.
type VerificationVerdict struct {
	Result      string
	Subject     string
	EvidenceRef string
	RecordedAt  time.Time
}

// Acceptance is the durable WORKS-owned record.
type Acceptance struct {
	ContractVersion  string
	WorkID           string
	WorksExecutionID string
	Dispatch         Dispatch
	AcceptedAt       time.Time
	AuthorityEpochAt int64
	EffectApplied    bool
	BudgetSpent      int64
	Revoked          bool
	Outcome          string // ACCEPTED | SUCCEEDED | FAILED | INDETERMINATE
	Verified         bool
	VerifierID       string
	Verdict          *VerificationVerdict
	// ExecutionContextID and TraceID are WORKS-minted correlation identities
	// bound at accept time (execution-context/1.0). They never arrive on the
	// Runtime Dispatch envelope — clients cannot choose either field — and are
	// stable across duplicate/retry of the same idempotency key because only the
	// winning AcceptIfAbsent insert persists; every replay returns the winner's.
	ExecutionContextID string
	TraceID            string
}

// Store is the durability seam. Production uses SQLite; tests use memory.
type Store interface {
	LoadByIdempotency(key string) (*Acceptance, error)
	LoadByExecution(id string) (*Acceptance, error)
	// AcceptIfAbsent must atomically insert on the idempotency key and return
	// the winner when another request already inserted the same key.
	AcceptIfAbsent(a *Acceptance) (*Acceptance, error)
	Save(a *Acceptance) error
}

// V2Binding is correlation input for dispatch.acceptance/2.0. It carries
// references that WORKS must bind into the real execution-context/1.0.
// Possession of these references is not authority; consequential action still
// requires Trust Gateway action-time revalidation through AIE.
type V2Binding struct {
	WorkID              string
	OrganizationID      string
	TenantID            string
	PrincipalID         string
	AuthorityLeaseID    string
	WorkerLeaseID       string
	AdmissionDecisionID string
}

// V2Store extends the durable acceptance store with one atomic operation that
// inserts the acceptance and materializes the canonical execution context.
// A V2 implementation must return the committed winner on idempotent replay.
,
)

// Clock decouples expiry/freshness checks in tests.
type Clock func() time.Time

// Acceptor owns the seal. Authority / budget / verifier truth is supplied by
// callers at each transition — never cached across restarts.
type Acceptor struct {
	store Store
	clock Clock
	now   func() string
}

func NewAcceptor(store Store, clock Clock) *Acceptor {
	if clock == nil {
		clock = time.Now
	}
	return &Acceptor{store: store, clock: clock}
}

// Accept durably records a Runtime dispatch. Same idempotency key + same
// causal identity returns the existing acceptance (safe retry / duplicate
// dispatch / Runtime-died-after-accept replay). Same key + different causal
// identity fails closed. Stale authority epoch fails closed.
func (a *Acceptor) Accept(d Dispatch, currentEpoch int64) (*Acceptance, error) {
	if d.MissionID == "" || d.AuthorityRef == "" || d.RuntimeDispatchID == "" || d.IdempotencyKey == "" {
		return nil, ErrMissingBinding
	}

	// Recovery/replay law: once WORKS has durably accepted this idempotency key,
	// the committed winner is the truth for that dispatch. A Runtime that died
	// after acceptance must be able to recover the same correlation pair even if
	// authority freshness advanced while its HTTP response was lost.
	//
	// This lookup is NOT the concurrency primitive. A concurrent first-accept
	// race still goes through AcceptIfAbsent below. It only lets an already
	// committed winner bypass a freshness check that is relevant to NEW
	// acceptance, not to readback of a completed acceptance decision.
	existing, err := a.store.LoadByIdempotency(d.IdempotencyKey)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		if existing.Dispatch.IdempotencyKey != d.IdempotencyKey ||
			existing.Dispatch.CausalID != d.CausalID ||
			existing.Dispatch.AuthorityEpoch != d.AuthorityEpoch ||
			existing.Dispatch.MissionID != d.MissionID {
			return nil, fmt.Errorf("%w: key %q", ErrCausalMismatch, d.IdempotencyKey)
		}
		return existing, nil
	}

	if d.AuthorityEpoch < currentEpoch {
		return nil, fmt.Errorf("%w: dispatch epoch %d < current %d", ErrStaleAuthority, d.AuthorityEpoch, currentEpoch)
	}
	// WORKS mints the correlation identity here. On a duplicate/retry the store
	// returns the existing winner and these freshly-minted IDs are discarded, so
	// the execution context stays stable across replay rather than being reminted.
	ctxID, trcID := executioncontext.MintCorrelationIDs()
	acc := &Acceptance{
		ContractVersion:    "dispatch.acceptance/1.0",
		WorksExecutionID:   "wexec/" + d.IdempotencyKey,
		Dispatch:           d,
		AcceptedAt:         a.clock(),
		AuthorityEpochAt:   d.AuthorityEpoch,
		Outcome:            "ACCEPTED",
		ExecutionContextID: ctxID,
		TraceID:            trcID,
	}
	// The persistence adapter owns the atomic insert/unique-key race. A
	// load-then-save sequence is not sufficient: two Runtime retries can pass
	// the load concurrently and create two effects.
	accepted, err := a.store.AcceptIfAbsent(acc)
	if err != nil {
		return nil, err
	}
	if accepted == nil {
		return nil, errors.New("dispatch: store returned nil acceptance")
	}
	if accepted.Dispatch.IdempotencyKey != d.IdempotencyKey ||
		accepted.Dispatch.CausalID != d.CausalID ||
		accepted.Dispatch.AuthorityEpoch != d.AuthorityEpoch ||
		accepted.Dispatch.MissionID != d.MissionID {
		return nil, fmt.Errorf("%w: key %q", ErrCausalMismatch, d.IdempotencyKey)
	}
	return accepted, nil
}

// AcceptV2 implements dispatch.acceptance/2.0 without inventing an authority
// epoch. Initial authority correlation is represented by AuthorityLease +
// admission PDR. Effect authorization is deliberately outside this method and
// remains Trust Gateway -> live AIE revalidation immediately before consequence.
//
// The V2 store owns the atomicity boundary: acceptance and the resolvable
// execution-context/1.0 either commit together or neither commits.
func (a *Acceptor) AcceptV2(
	ctx context.Context,
	d Dispatch,
	binding V2Binding,
) (*Acceptance, *executioncontext.Context, error) {
	if d.MissionID == "" || d.AuthorityRef == "" || d.RuntimeDispatchID == "" ||
		d.IdempotencyKey == "" || d.EffectID == "" || d.AttemptID == "" ||
		d.CausalID == "" {
		return nil, nil, ErrMissingBinding
	}
	if binding.WorkID == "" || binding.OrganizationID == "" || binding.TenantID == "" ||
		binding.PrincipalID == "" || binding.AuthorityLeaseID == "" ||
		binding.WorkerLeaseID == "" || binding.AdmissionDecisionID == "" {
		return nil, nil, ErrContextBinding
	}
	if d.AuthorityRef != binding.AuthorityLeaseID {
		return nil, nil, ErrContextBinding
	}
	v2store, ok := a.store.(V2Store)
	if !ok {
		return nil, nil, ErrV2StoreRequired
	}

	ctxID, trcID := executioncontext.MintCorrelationIDs()
	candidate := &Acceptance{
		ContractVersion:    "dispatch.acceptance/2.0",
		WorkID:             binding.WorkID,
		WorksExecutionID:   "wexec/" + d.IdempotencyKey,
		Dispatch:           d,
		AcceptedAt:         a.clock(),
		Outcome:            "ACCEPTED",
		ExecutionContextID: ctxID,
		TraceID:            trcID,
	}
	accepted, executionContext, err := v2store.AcceptContextualIfAbsent(ctx, candidate, binding)
	if err != nil {
		return nil, nil, err
	}
	if accepted == nil || executionContext == nil {
		return nil, nil, ErrContextBinding
	}
	if accepted.ContractVersion != "dispatch.acceptance/2.0" ||
		accepted.WorkID != binding.WorkID ||
		accepted.Dispatch.IdempotencyKey != d.IdempotencyKey ||
		accepted.Dispatch.CausalID != d.CausalID ||
		accepted.Dispatch.MissionID != d.MissionID ||
		accepted.Dispatch.AuthorityRef != d.AuthorityRef ||
		accepted.Dispatch.RuntimeDispatchID != d.RuntimeDispatchID ||
		accepted.Dispatch.EffectID != d.EffectID {
		return nil, nil, fmt.Errorf("%w: key %q", ErrCausalMismatch, d.IdempotencyKey)
	}
	if executionContext.ID != accepted.ExecutionContextID ||
		executionContext.TraceID != accepted.TraceID ||
		executionContext.WorkID != binding.WorkID ||
		executionContext.OrganizationID != binding.OrganizationID ||
		executionContext.TenantID != binding.TenantID ||
		executionContext.PrincipalID != binding.PrincipalID ||
		executionContext.MissionID != d.MissionID ||
		executionContext.AuthorityLeaseID != binding.AuthorityLeaseID ||
		executionContext.WorkerLeaseID != binding.WorkerLeaseID ||
		executionContext.AdmissionDecisionID != binding.AdmissionDecisionID {
		return nil, nil, ErrContextBinding
	}
	return accepted, executionContext, nil
}

// BindVerificationSubject records the immutable subject observed after the
// governed effect. The first binding wins. Replaying the same binding is
// idempotent; rebinding to another subject fails closed.
func (a *Acceptor) BindVerificationSubject(
	ctx context.Context,
	worksExecutionID string,
	workID string,
	attemptID string,
	effectID string,
	causalID string,
	subject string,
) (*VerificationSubjectBinding, error) {
	if !exactGitSubjectPattern.MatchString(subject) {
		return nil, ErrInvalidSubject
	}
	acc, err := a.get(worksExecutionID)
	if err != nil {
		return nil, err
	}
	if acc.ContractVersion != "dispatch.acceptance/2.0" {
		return nil, ErrV2StoreRequired
	}
	if acc.WorkID != workID ||
		acc.Dispatch.AttemptID != attemptID ||
		acc.Dispatch.EffectID != effectID ||
		acc.Dispatch.CausalID != causalID {
		return nil, fmt.Errorf("%w: verification subject correlation mismatch", ErrCausalMismatch)
	}
	store, ok := a.store.(VerificationSubjectStore)
	if !ok {
		return nil, ErrV2StoreRequired
	}
	winner, err := store.BindVerificationSubject(ctx, VerificationSubjectBinding{
		WorksExecutionID: worksExecutionID,
		WorkID:           workID,
		AttemptID:        attemptID,
		EffectID:         effectID,
		CausalID:         causalID,
		Subject:          subject,
	})
	if err != nil {
		return nil, err
	}
	if winner == nil {
		return nil, ErrSubjectNotBound
	}
	if winner.WorksExecutionID != worksExecutionID ||
		winner.WorkID != workID ||
		winner.AttemptID != attemptID ||
		winner.EffectID != effectID ||
		winner.CausalID != causalID {
		return nil, fmt.Errorf("%w: persisted verification subject correlation mismatch", ErrCausalMismatch)
	}
	if winner.Subject != subject {
		return nil, ErrSubjectConflict
	}
	return winner, nil
}

// Revoke marks authority revoked mid-flight. Revoked executions cannot apply
// effects or verify afterwards.
func (a *Acceptor) Revoke(worksExecutionID string) error {
	acc, err := a.get(worksExecutionID)
	if err != nil {
		return err
	}
	acc.Revoked = true
	return a.store.Save(acc)
}

// Spend charges budget. Over-ceiling spend fails closed and can never
// autonomously retry around the ceiling: only a new dispatched budget
// reference (new idempotency key) can continue.
func (a *Acceptor) Spend(worksExecutionID string, amount int64) error {
	if amount <= 0 {
		return fmt.Errorf("%w: amount %d", ErrInvalidSpend, amount)
	}
	acc, err := a.get(worksExecutionID)
	if err != nil {
		return err
	}
	if acc.Revoked {
		return ErrRevoked
	}
	if acc.Dispatch.BudgetCeiling < 0 || acc.BudgetSpent < 0 || acc.BudgetSpent > acc.Dispatch.BudgetCeiling {
		return ErrInvalidBudget
	}
	// Use subtraction rather than spent+amount so an overflowing amount cannot
	// wrap below the hard ceiling.
	if amount > acc.Dispatch.BudgetCeiling-acc.BudgetSpent {
		return fmt.Errorf("%w: spent %d + %d > ceiling %d", ErrBudgetExhausted, acc.BudgetSpent, amount, acc.Dispatch.BudgetCeiling)
	}
	acc.BudgetSpent += amount
	return a.store.Save(acc)
}

// ApplyEffect records an externally visible effect exactly once. A second
// apply of the same effect id is a duplicate (rejected, not re-executed).
// An effect whose outcome cannot be established resolves INDETERMINATE.
func (a *Acceptor) ApplyEffect(worksExecutionID, effectID string, outcomeKnown bool) error {
	acc, err := a.get(worksExecutionID)
	if err != nil {
		return err
	}
	if acc.Revoked {
		return ErrRevoked
	}
	if effectID != acc.Dispatch.EffectID {
		return fmt.Errorf("%w: %q is not the accepted effect", ErrCausalMismatch, effectID)
	}
	if acc.EffectApplied {
		return fmt.Errorf("%w: %q", ErrEffectDuplicate, effectID)
	}
	if !outcomeKnown {
		acc.Outcome = "INDETERMINATE"
		return a.store.Save(acc)
	}
	acc.EffectApplied = true
	return a.store.Save(acc)
}

// Complete records execution outcome. SUCCEEDED here is execution-complete
// only — Verified stays false until RecordVerdict with independent evidence.
func (a *Acceptor) Complete(worksExecutionID, outcome string) error {
	acc, err := a.get(worksExecutionID)
	if err != nil {
		return err
	}
	if acc.Revoked {
		return ErrRevoked
	}
	switch outcome {
	case "SUCCEEDED", "FAILED":
		acc.Outcome = outcome
		return a.store.Save(acc)
	default:
		return fmt.Errorf("dispatch: unknown outcome %q", outcome)
	}
}

// RecordVerdict records an independent verifier verdict over the exact
// accepted verification subject and a terminal execution outcome. A verifier
// must bind the verdict to an evidence reference; boolean availability alone
// is not a proof. REJECT is a recorded independent review but does not promote
// the execution to Verified.
func (a *Acceptor) RecordVerdict(worksExecutionID, verifierID, subject string, subjectCurrent, verifierAvailable bool, verdictResult, evidenceRef string) error {
	acc, err := a.get(worksExecutionID)
	if err != nil {
		return err
	}
	if verifierID == "" || verifierID == acc.Dispatch.RuntimeDispatchID {
		return ErrSelfVerification
	}
	expectedSubject := acc.Dispatch.VerificationSubj
	if acc.ContractVersion == "dispatch.acceptance/2.0" {
		store, ok := a.store.(VerificationSubjectStore)
		if !ok {
			return ErrV2StoreRequired
		}
		binding, err := store.LoadVerificationSubject(context.Background(), worksExecutionID)
		if err != nil {
			return err
		}
		if binding == nil || binding.Subject == "" {
			return ErrSubjectNotBound
		}
		expectedSubject = binding.Subject
	}
	if !subjectCurrent || subject != expectedSubject {
		return fmt.Errorf("%w: %q", ErrStaleSubject, subject)
	}
	if !verifierAvailable {
		return ErrVerifierUnavailable
	}
	if acc.Revoked {
		return ErrRevoked
	}
	if acc.Outcome != "SUCCEEDED" && acc.Outcome != "FAILED" {
		return ErrExecutionNotTerminal
	}
	if strings.TrimSpace(verdictResult) == "" || strings.TrimSpace(evidenceRef) == "" {
		return ErrMissingVerdictEvidence
	}
	if verdictResult != "ACCEPT" && verdictResult != "REJECT" {
		return fmt.Errorf("%w: %q", ErrInvalidVerdict, verdictResult)
	}
	acc.Verified = verdictResult == "ACCEPT"
	acc.VerifierID = verifierID
	acc.Verdict = &VerificationVerdict{
		Result:      verdictResult,
		Subject:     subject,
		EvidenceRef: evidenceRef,
		RecordedAt:  a.clock(),
	}
	return a.store.Save(acc)
}

func (a *Acceptor) get(id string) (*Acceptance, error) {
	acc, err := a.store.LoadByExecution(id)
	if err != nil {
		return nil, err
	}
	if acc == nil {
		return nil, fmt.Errorf("%w: %q", ErrUnknownAcceptance, id)
	}
	return acc, nil
}
