// Package promotion defines the governed boundary between an immutable
// workspace candidate and a canonical-source proposal. It deliberately
// excludes merge, approval, release, and deployment authority.
package promotion

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/JonasAbde/works-execution/packages/workspace"
)

var immutableGitSHA = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)

var (
	ErrMalformed                  = errors.New("promotion: malformed request")
	ErrForeignWorkspace           = errors.New("promotion: foreign workspace")
	ErrCandidateMismatch          = errors.New("promotion: candidate does not match workspace")
	ErrImmutableCandidateRequired = errors.New("promotion: immutable candidate sha required")
	ErrEvidenceNotVerified        = errors.New("promotion: evidence not verified")
	ErrDecisionNotAuthorized      = errors.New("promotion: decision not authorized")
	ErrIdempotencyConflict        = errors.New("promotion: idempotency conflict")
	ErrUnsupportedTarget          = errors.New("promotion: unsupported target")
	ErrNotFound                   = errors.New("promotion: proposal not found")
	ErrProviderUnavailable        = errors.New("promotion: provider unavailable")
	ErrMaterializationFailed      = errors.New("promotion: materialization failed")
	ErrMaterializationMismatch    = errors.New("promotion: materialization lineage mismatch")
)

type Target struct {
	Provider   string `json:"provider"`
	Repository string `json:"repository"`
	Branch     string `json:"branch"`
}

func (t Target) Validate() error {
	if strings.TrimSpace(t.Provider) == "" || strings.TrimSpace(t.Repository) == "" || strings.TrimSpace(t.Branch) == "" {
		return fmt.Errorf("%w: target provider, repository and branch are required", ErrMalformed)
	}
	return nil
}

type Request struct {
	IdempotencyKey string `json:"idempotency_key"`
	Org            string `json:"org"`
	WorkID         string `json:"work_id"`

	Workspace workspace.Workspace `json:"workspace"`
	Candidate workspace.Candidate `json:"candidate"`

	EvidenceBundleID string `json:"evidence_bundle_id"`
	DecisionRef      string `json:"decision_ref"`
	PolicyDecisionID string `json:"policy_decision_id,omitempty"`

	Target Target `json:"target"`
}

func (r Request) Validate() error {
	if strings.TrimSpace(r.IdempotencyKey) == "" || strings.TrimSpace(r.Org) == "" || strings.TrimSpace(r.WorkID) == "" {
		return fmt.Errorf("%w: idempotency_key, org and work_id are required", ErrMalformed)
	}
	if err := r.Workspace.Validate(); err != nil {
		return fmt.Errorf("%w: workspace: %v", ErrMalformed, err)
	}
	if r.Workspace.Org != r.Org || r.Workspace.WorkID != r.WorkID {
		return ErrForeignWorkspace
	}
	if r.Candidate.WorkspaceID != r.Workspace.ID || r.Candidate.Repository != r.Workspace.Name {
		return ErrCandidateMismatch
	}
	if !immutableGitSHA.MatchString(r.Candidate.SHA) {
		return ErrImmutableCandidateRequired
	}
	if strings.TrimSpace(r.EvidenceBundleID) == "" || strings.TrimSpace(r.DecisionRef) == "" {
		return fmt.Errorf("%w: evidence_bundle_id and decision_ref are required", ErrMalformed)
	}
	return r.Target.Validate()
}

type DecisionSubject struct {
	Org              string
	WorkID           string
	CandidateSHA     string
	EvidenceBundleID string
	DecisionRef      string
	PolicyDecisionID string
}

type Proposal struct {
	ID                string    `json:"id"`
	Org               string    `json:"org"`
	WorkID            string    `json:"work_id"`
	WorkspaceID       string    `json:"workspace_id"`
	CandidateSHA      string    `json:"candidate_sha"`
	Target            Target    `json:"target"`
	StagingRef        string    `json:"staging_ref"`
	PullRequestURL    string    `json:"pull_request_url"`
	PullRequestNumber int       `json:"pull_request_number"`
	EvidenceBundleID  string    `json:"evidence_bundle_id"`
	DecisionRef       string    `json:"decision_ref"`
	PolicyDecisionID  string    `json:"policy_decision_id,omitempty"`
	CreatedAt         time.Time `json:"created_at"`
}

type EvidenceVerifier interface {
	Verify(ctx context.Context, bundleID, workID string) error
}

type DecisionVerifier interface {
	VerifyPromotionDecision(ctx context.Context, subject DecisionSubject) error
}

// AuthorizedRequest is intentionally opaque outside this package. Callers can
// receive it only through Service after evidence + decision verification.
// Backends read the immutable request through Request().
type AuthorizedRequest struct {
	request Request
	keyHash string
	fp      string
}

func (a AuthorizedRequest) Request() Request { return a.request }
func (a AuthorizedRequest) KeyHash() string  { return a.keyHash }
func (a AuthorizedRequest) Fingerprint() string { return a.fp }

// Accessors keep backend implementations usable without making authority
// construction forgeable through exported struct fields.
func (a AuthorizedRequest) Org() string { return a.request.Org }
func (a AuthorizedRequest) WorkID() string { return a.request.WorkID }
func (a AuthorizedRequest) Candidate() workspace.Candidate { return a.request.Candidate }
func (a AuthorizedRequest) EvidenceBundleID() string { return a.request.EvidenceBundleID }
func (a AuthorizedRequest) DecisionRef() string { return a.request.DecisionRef }
func (a AuthorizedRequest) PolicyDecisionID() string { return a.request.PolicyDecisionID }
func (a AuthorizedRequest) Target() Target { return a.request.Target }

type Backend interface {
	ID() string
	Propose(ctx context.Context, req AuthorizedRequest) (Proposal, error)
	Get(ctx context.Context, proposal Proposal) (Proposal, error)
}

type Service struct {
	evidence EvidenceVerifier
	decision DecisionVerifier
	backend  Backend
}

func NewService(evidence EvidenceVerifier, decision DecisionVerifier, backend Backend) (*Service, error) {
	if evidence == nil || decision == nil || backend == nil {
		return nil, fmt.Errorf("%w: evidence verifier, decision verifier and backend are required", ErrMalformed)
	}
	return &Service{evidence:evidence, decision:decision, backend:backend}, nil
}

func (s *Service) Propose(ctx context.Context, req Request) (Proposal, error) {
	if err := req.Validate(); err != nil {
		return Proposal{}, err
	}
	if err := s.evidence.Verify(ctx, req.EvidenceBundleID, req.WorkID); err != nil {
		return Proposal{}, fmt.Errorf("%w: %v", ErrEvidenceNotVerified, err)
	}
	subject := DecisionSubject{
		Org:req.Org, WorkID:req.WorkID, CandidateSHA:req.Candidate.SHA,
		EvidenceBundleID:req.EvidenceBundleID, DecisionRef:req.DecisionRef,
		PolicyDecisionID:req.PolicyDecisionID,
	}
	if err := s.decision.VerifyPromotionDecision(ctx, subject); err != nil {
		return Proposal{}, fmt.Errorf("%w: %v", ErrDecisionNotAuthorized, err)
	}
	keyHash, fp, err := promotionIdentity(req)
	if err != nil {
		return Proposal{}, err
	}
	return s.backend.Propose(ctx, AuthorizedRequest{request:req,keyHash:keyHash,fp:fp})
}
