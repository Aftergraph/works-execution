package promotionservice

import (
	"context"
	"fmt"

	domain "github.com/JonasAbde/works-execution/packages/promotion"
)

const humanStamped = "human_stamped"

type DecisionRecord struct {
	Ref              string
	Org              string
	WorkID           string
	CandidateSHA     string
	EvidenceBundleID string
	PolicyDecisionID string
	Authoritative    bool
	Promotion        string
	HumanStamp       string
	Tombstone        bool
}

type DecisionLoader interface {
	LoadPromotionDecision(ctx context.Context, ref string) (*DecisionRecord, error)
}

type DecisionVerifier struct {
	loader DecisionLoader
}

func NewDecisionVerifier(loader DecisionLoader) (*DecisionVerifier, error) {
	if loader == nil {
		return nil, fmt.Errorf("%w: decision loader is required", domain.ErrMalformed)
	}
	return &DecisionVerifier{loader: loader}, nil
}

func (v *DecisionVerifier) VerifyPromotionDecision(ctx context.Context, subject domain.DecisionSubject) error {
	if v == nil || v.loader == nil {
		return fmt.Errorf("%w: verifier unavailable", domain.ErrDecisionNotAuthorized)
	}
	record, err := v.loader.LoadPromotionDecision(ctx, subject.DecisionRef)
	if err != nil || record == nil {
		return fmt.Errorf("%w: decision unavailable", domain.ErrDecisionNotAuthorized)
	}
	if !record.Authoritative || record.Promotion != humanStamped || record.HumanStamp == "" || record.Tombstone {
		return fmt.Errorf("%w: human-stamped authority required", domain.ErrDecisionNotAuthorized)
	}
	if record.Ref != subject.DecisionRef ||
		record.Org != subject.Org ||
		record.WorkID != subject.WorkID ||
		record.CandidateSHA != subject.CandidateSHA ||
		record.EvidenceBundleID != subject.EvidenceBundleID ||
		record.PolicyDecisionID != subject.PolicyDecisionID {
		return fmt.Errorf("%w: decision binding mismatch", domain.ErrDecisionNotAuthorized)
	}
	return nil
}
