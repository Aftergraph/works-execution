package economic

import "testing"

func validAcceptance() Acceptance {
	return Acceptance{
		Schema:"aftergraph.economic-work-acceptance/v1",
		MissionID:"mis_economic_1", TransactionID:"econ_1", IntentHash:"intent-hash", GraphHash:"graph-hash",
		Context:ExecutionContext{
			WorkID:"wrk_11111111111111111111111111111111",
			AttemptID:"att_22222222222222222222222222222222",
			ExecutionContextID:"ctx_33333333333333333333333333333333",
			WorkerID:"wrkr_44444444444444444444444444444444",
			WorkerLeaseID:"lse_55555555555555555555555555555555",
			AdmissionDecisionID:"pdr_66666666666666666666666666666666",
			TraceID:"trc_77777777777777777777777777777777",
			AuthorityLeaseID:"auth_88888888888888888888888888888888",
		},
	}
}

func TestValidateAcceptanceBindsEconomicHashesAndWorksIdentity(t *testing.T) {
	a:=validAcceptance()
	b:=Binding{TransactionID:a.TransactionID,IntentHash:a.IntentHash,GraphHash:a.GraphHash,AuthorityLeaseID:a.Context.AuthorityLeaseID}
	if err:=ValidateAcceptance(b,a);err!=nil{t.Fatalf("unexpected: %v",err)}
}

func TestValidateAcceptanceRejectsSubstitutedContext(t *testing.T) {
	a:=validAcceptance(); a.Context.ExecutionContextID="ctx_bad"
	b:=Binding{TransactionID:a.TransactionID,IntentHash:a.IntentHash,GraphHash:a.GraphHash,AuthorityLeaseID:a.Context.AuthorityLeaseID}
	if err:=ValidateAcceptance(b,a);err==nil{t.Fatal("expected invalid WORKS-owned context")}
}
