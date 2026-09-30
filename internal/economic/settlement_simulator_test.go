package economic

import "testing"

func TestAtomicSettlementSimulationNeverClaimsRealFinality(t *testing.T) {
	out, err := SimulateAtomicSettlement("econ_1", []SettlementLeg{
		{ID:"asset", Kind:LegAsset, Rail:"mock-asset", Outcome:OutcomeCommit},
		{ID:"cash", Kind:LegCash, Rail:"mock-cash", Outcome:OutcomeCommit},
	})
	if err != nil { t.Fatal(err) }
	if out.State != "SIMULATED_COMMIT" { t.Fatalf("state=%s", out.State) }
	if out.Final { t.Fatal("frontier simulation must never claim FINAL") }
	if out.ExternalEffects != 0 { t.Fatal("frontier simulation must remain zero-effect") }
}

func TestAtomicSettlementUnknownLegForcesUncertain(t *testing.T) {
	out, err := SimulateAtomicSettlement("econ_2", []SettlementLeg{
		{ID:"asset", Kind:LegAsset, Rail:"mock-asset", Outcome:OutcomeCommit},
		{ID:"cash", Kind:LegCash, Rail:"mock-cash", Outcome:OutcomeUnknown},
	})
	if err != nil { t.Fatal(err) }
	if out.State != "UNCERTAIN" { t.Fatalf("state=%s", out.State) }
	if out.Final { t.Fatal("UNCERTAIN cannot be FINAL") }
}

func TestAtomicSettlementPartialFailureCannotMasqueradeAsFinal(t *testing.T) {
	out, err := SimulateAtomicSettlement("econ_3", []SettlementLeg{
		{ID:"asset", Kind:LegAsset, Rail:"mock-asset", Outcome:OutcomeCommit},
		{ID:"cash", Kind:LegCash, Rail:"mock-cash", Outcome:OutcomeFail},
	})
	if err != nil { t.Fatal(err) }
	if out.State != "ABORTED" { t.Fatalf("state=%s", out.State) }
	if out.Final { t.Fatal("partial settlement cannot be FINAL") }
}

func TestAtomicSettlementRejectsDuplicateLegIdentity(t *testing.T) {
	_, err := SimulateAtomicSettlement("econ_4", []SettlementLeg{
		{ID:"same", Kind:LegAsset, Rail:"mock-asset", Outcome:OutcomeCommit},
		{ID:"same", Kind:LegCash, Rail:"mock-cash", Outcome:OutcomeCommit},
	})
	if err == nil { t.Fatal("expected duplicate leg rejection") }
}
