package executioncontext

import "testing"

func validContext() Context {
	return Context{
		Schema: "execution-context/1.0",
		ID: "ctx_11111111111111111111111111111111",
		OrganizationID: "org_22222222222222222222222222222222",
		TenantID: "ten_33333333333333333333333333333333",
		PrincipalID: "prn_44444444444444444444444444444444",
		MissionID: "mis_example",
		AuthorityLeaseID: "auth_55555555555555555555555555555555",
		WorkID: "wrk_66666666666666666666666666666666",
		WorkerID: "wrkr_77777777777777777777777777777777",
		WorkerLeaseID: "lse_88888888888888888888888888888888",
		AdmissionDecisionID: "pdr_99999999999999999999999999999999",
		TraceID: "trc_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}
}

func TestContextValidateHappyPath(t *testing.T) {
	c := validContext()
	if err := c.Validate(); err != nil { t.Fatalf("validate: %v", err) }
}

func TestContextRejectsSwappedAuthorityAndWorkerLeasePrefixes(t *testing.T) {
	c := validContext()
	c.AuthorityLeaseID = "lse_55555555555555555555555555555555"
	c.WorkerLeaseID = "auth_88888888888888888888888888888888"
	if err := c.Validate(); err == nil { t.Fatal("expected prefix validation error") }
}

func TestContextRequiresPriorContextPrefixWhenPresent(t *testing.T) {
	c := validContext()
	c.PriorContextID = "wrk_11111111111111111111111111111111"
	if err := c.Validate(); err == nil { t.Fatal("expected prior context prefix validation error") }
}
