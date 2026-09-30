package economic

import (
	"errors"
	"strings"
)

type Binding struct {
	TransactionID     string
	IntentHash        string
	GraphHash         string
	AuthorityLeaseID  string
}

type ExecutionContext struct {
	WorkID              string
	AttemptID           string
	ExecutionContextID  string
	WorkerID            string
	WorkerLeaseID       string
	AdmissionDecisionID string
	TraceID             string
	AuthorityLeaseID    string
}

type Acceptance struct {
	Schema        string
	MissionID     string
	TransactionID string
	IntentHash    string
	GraphHash     string
	Context       ExecutionContext
}

func ValidateAcceptance(binding Binding, accepted Acceptance) error {
	if accepted.Schema != "aftergraph.economic-work-acceptance/v1" {
		return errors.New("economic acceptance schema mismatch")
	}
	if accepted.TransactionID != binding.TransactionID || accepted.IntentHash != binding.IntentHash || accepted.GraphHash != binding.GraphHash {
		return errors.New("economic acceptance binding mismatch")
	}
	if accepted.Context.AuthorityLeaseID != binding.AuthorityLeaseID {
		return errors.New("economic authority lease mismatch")
	}
	checks := []struct{name,value,prefix string}{
		{"work_id", accepted.Context.WorkID, "wrk_"},
		{"execution_context_id", accepted.Context.ExecutionContextID, "ctx_"},
		{"worker_id", accepted.Context.WorkerID, "wrkr_"},
		{"worker_lease_id", accepted.Context.WorkerLeaseID, "lse_"},
		{"admission_decision_id", accepted.Context.AdmissionDecisionID, "pdr_"},
	}
	for _, c := range checks {
		if !strings.HasPrefix(c.value, c.prefix) || len(c.value) <= len(c.prefix)+8 {
			return errors.New("invalid WORKS-owned "+c.name)
		}
	}
	if accepted.Context.AttemptID == "" || accepted.Context.TraceID == "" {
		return errors.New("incomplete WORKS-owned execution lineage")
	}
	return nil
}
