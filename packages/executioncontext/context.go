package executioncontext

import (
	"fmt"
	"regexp"
)

type Context struct {
	Schema              string `json:"schema"`
	ID                  string `json:"execution_context_id"`
	PriorContextID      string `json:"prior_execution_context_id,omitempty"`
	OrganizationID      string `json:"organization_id"`
	TenantID            string `json:"tenant_id"`
	PrincipalID         string `json:"principal_id"`
	MissionID           string `json:"mission_id"`
	AuthorityLeaseID    string `json:"authority_lease_id"`
	WorkID              string `json:"work_id"`
	WorkerID            string `json:"worker_id"`
	WorkerLeaseID       string `json:"worker_lease_id"`
	AdmissionDecisionID string `json:"admission_decision_id"`
	TraceID             string `json:"trace_id"`
}

var idPatterns = map[string]*regexp.Regexp{
	"execution_context_id":  regexp.MustCompile(`^ctx_[a-f0-9]{32}$`),
	"organization_id":       regexp.MustCompile(`^org_[a-f0-9]{32}$`),
	"tenant_id":             regexp.MustCompile(`^ten_[a-f0-9]{32}$`),
	"principal_id":          regexp.MustCompile(`^prn_[a-f0-9]{32}$`),
	"authority_lease_id":    regexp.MustCompile(`^auth_[a-f0-9]{32}$`),
	"work_id":               regexp.MustCompile(`^wrk_[a-f0-9]{32}$`),
	"worker_id":             regexp.MustCompile(`^wrkr_[a-f0-9]{32}$`),
	"worker_lease_id":       regexp.MustCompile(`^lse_[a-f0-9]{32}$`),
	"admission_decision_id": regexp.MustCompile(`^pdr_[a-f0-9]{32}$`),
	"trace_id":              regexp.MustCompile(`^trc_[a-f0-9]{32}$`),
}

func (c Context) Validate() error {
	if c.Schema != "execution-context/1.0" {
		return fmt.Errorf("schema must be execution-context/1.0")
	}
	values := map[string]string{
		"execution_context_id": c.ID, "organization_id": c.OrganizationID, "tenant_id": c.TenantID,
		"principal_id": c.PrincipalID, "authority_lease_id": c.AuthorityLeaseID, "work_id": c.WorkID,
		"worker_id": c.WorkerID, "worker_lease_id": c.WorkerLeaseID,
		"admission_decision_id": c.AdmissionDecisionID, "trace_id": c.TraceID,
	}
	for name, value := range values {
		if !idPatterns[name].MatchString(value) {
			return fmt.Errorf("%s has invalid format", name)
		}
	}
	if len(c.MissionID) < 5 || c.MissionID[:4] != "mis_" {
		return fmt.Errorf("mission_id has invalid format")
	}
	if c.PriorContextID != "" && !idPatterns["execution_context_id"].MatchString(c.PriorContextID) {
		return fmt.Errorf("prior_execution_context_id has invalid format")
	}
	return nil
}
