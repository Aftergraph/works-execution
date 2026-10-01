package economic

import "testing"

func partialLegs() []SettlementLeg {
	return []SettlementLeg{
		{ID:"asset", Kind:LegAsset, Rail:"mock-asset", Outcome:OutcomeCommit},
		{ID:"cash", Kind:LegCash, Rail:"mock-cash", Outcome:OutcomeFail},
	}
}

func TestRecoveryRequiresCompensationForCommittedLeg(t *testing.T) {
	out, err := SimulateSettlementRecovery("econ_recovery_1", partialLegs(), nil, false, false)
	if err != nil { t.Fatal(err) }
	if out.State != "COMPENSATION_REQUIRED" { t.Fatalf("state=%s", out.State) }
	if out.Final { t.Fatal("recovery simulation cannot be FINAL") }
	if out.ExternalEffects != 0 { t.Fatal("recovery simulation must remain zero-effect") }
}

func TestRecoverySimulatedCompensationRemainsNonFinal(t *testing.T) {
	out, err := SimulateSettlementRecovery("econ_recovery_2", partialLegs(), []CompensationStep{
		{LegID:"asset", Outcome:CompensationApplied},
	}, false, false)
	if err != nil { t.Fatal(err) }
	if out.State != "SIMULATED_COMPENSATED" { t.Fatalf("state=%s", out.State) }
	if out.Final { t.Fatal("simulated compensation cannot be FINAL") }
}

func TestRecoveryKillSwitchFailsClosed(t *testing.T) {
	out, err := SimulateSettlementRecovery("econ_recovery_3", partialLegs(), nil, true, false)
	if err != nil { t.Fatal(err) }
	if out.State != "ABORTED_BY_KILL_SWITCH" { t.Fatalf("state=%s", out.State) }
}

func TestRecoveryRevocationFailsClosed(t *testing.T) {
	out, err := SimulateSettlementRecovery("econ_recovery_4", partialLegs(), nil, false, true)
	if err != nil { t.Fatal(err) }
	if out.State != "ABORTED_BY_REVOCATION" { t.Fatalf("state=%s", out.State) }
}

func TestRecoveryUnknownCompensationRequiresReconciliation(t *testing.T) {
	out, err := SimulateSettlementRecovery("econ_recovery_5", partialLegs(), []CompensationStep{
		{LegID:"asset", Outcome:CompensationUnknown},
	}, false, false)
	if err != nil { t.Fatal(err) }
	if out.State != "RECONCILIATION_REQUIRED" { t.Fatalf("state=%s", out.State) }
	if out.Final { t.Fatal("uncertain compensation cannot be FINAL") }
}

func TestRecoveryRejectsCompensationForUncommittedLeg(t *testing.T) {
	_, err := SimulateSettlementRecovery("econ_recovery_6", partialLegs(), []CompensationStep{
		{LegID:"cash", Outcome:CompensationApplied},
	}, false, false)
	if err == nil { t.Fatal("expected invalid compensation target") }
}
