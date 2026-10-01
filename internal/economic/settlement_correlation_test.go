package economic

import "testing"

const (
	testIntentHash = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	testLegalHash  = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	assetHash      = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	cashHash       = "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
	assetEvidence  = "sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	cashEvidence   = "sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
)

func correlationObligations() []CorrelationObligation {
	return []CorrelationObligation{
		{LegID:"asset", Kind:LegAsset, Rail:"rail-asset-a", ObligationHash:assetHash, LegalBindingHash:testLegalHash},
		{LegID:"cash", Kind:LegCash, Rail:"rail-cash-b", ObligationHash:cashHash, LegalBindingHash:testLegalHash},
	}
}

func correlationReceipts() []RailSettlementReceipt {
	return []RailSettlementReceipt{
		{
			Schema:"aftergraph.rail-settlement-receipt/v1",
			EconomicTransactionID:"econ_corr_1",
			LegID:"asset", Kind:LegAsset, Rail:"rail-asset-a",
			ObligationHash:assetHash, LegalBindingHash:testLegalHash,
			SourceEvidenceHash:assetEvidence, Outcome:OutcomeCommit,
			ExternalEffects:0, Final:false,
		},
		{
			Schema:"aftergraph.rail-settlement-receipt/v1",
			EconomicTransactionID:"econ_corr_1",
			LegID:"cash", Kind:LegCash, Rail:"rail-cash-b",
			ObligationHash:cashHash, LegalBindingHash:testLegalHash,
			SourceEvidenceHash:cashEvidence, Outcome:OutcomeCommit,
			ExternalEffects:0, Final:false,
		},
	}
}

func TestCorrelationBindsIndependentReceiptsToSameEconomicTransaction(t *testing.T) {
	out, err := CorrelateSettlementReceipts("econ_corr_1", testIntentHash, testLegalHash, correlationObligations(), correlationReceipts())
	if err != nil { t.Fatal(err) }
	if out.State != "CORRELATED" { t.Fatalf("state=%s reasons=%v", out.State, out.Reasons) }
	if out.ReconciliationRequired { t.Fatal("valid correlation should not require reconciliation") }
	if len(out.MatchedLegs) != 2 { t.Fatalf("matched=%v", out.MatchedLegs) }
	if out.Final { t.Fatal("correlation cannot establish FINAL") }
	if out.ExternalEffects != 0 { t.Fatal("correlation must be zero-effect") }
}

func TestCorrelationTransactionIDMismatchForcesUncertain(t *testing.T) {
	receipts := correlationReceipts()
	receipts[1].EconomicTransactionID = "econ_other"
	out, err := CorrelateSettlementReceipts("econ_corr_1", testIntentHash, testLegalHash, correlationObligations(), receipts)
	if err != nil { t.Fatal(err) }
	if out.State != "UNCERTAIN" || !out.ReconciliationRequired { t.Fatalf("out=%+v", out) }
	if out.Final { t.Fatal("mismatch cannot be FINAL") }
}

func TestCorrelationObligationHashMismatchForcesUncertain(t *testing.T) {
	receipts := correlationReceipts()
	receipts[0].ObligationHash = cashHash
	out, err := CorrelateSettlementReceipts("econ_corr_1", testIntentHash, testLegalHash, correlationObligations(), receipts)
	if err != nil { t.Fatal(err) }
	if out.State != "UNCERTAIN" || !out.ReconciliationRequired { t.Fatalf("out=%+v", out) }
}

func TestCorrelationLegalBindingMismatchForcesUncertain(t *testing.T) {
	receipts := correlationReceipts()
	receipts[0].LegalBindingHash = testIntentHash
	out, err := CorrelateSettlementReceipts("econ_corr_1", testIntentHash, testLegalHash, correlationObligations(), receipts)
	if err != nil { t.Fatal(err) }
	if out.State != "UNCERTAIN" || !out.ReconciliationRequired { t.Fatalf("out=%+v", out) }
}

func TestCorrelationMissingReceiptForcesUncertain(t *testing.T) {
	receipts := correlationReceipts()[:1]
	out, err := CorrelateSettlementReceipts("econ_corr_1", testIntentHash, testLegalHash, correlationObligations(), receipts)
	if err != nil { t.Fatal(err) }
	if out.State != "UNCERTAIN" || !out.ReconciliationRequired { t.Fatalf("out=%+v", out) }
}

func TestCorrelationUnknownOutcomeForcesUncertain(t *testing.T) {
	receipts := correlationReceipts()
	receipts[1].Outcome = OutcomeUnknown
	out, err := CorrelateSettlementReceipts("econ_corr_1", testIntentHash, testLegalHash, correlationObligations(), receipts)
	if err != nil { t.Fatal(err) }
	if out.State != "UNCERTAIN" || !out.ReconciliationRequired { t.Fatalf("out=%+v", out) }
}

func TestCorrelationDuplicateEvidenceCannotFakeIndependentRails(t *testing.T) {
	receipts := correlationReceipts()
	receipts[1].SourceEvidenceHash = assetEvidence
	out, err := CorrelateSettlementReceipts("econ_corr_1", testIntentHash, testLegalHash, correlationObligations(), receipts)
	if err != nil { t.Fatal(err) }
	if out.State != "UNCERTAIN" || !out.ReconciliationRequired { t.Fatalf("out=%+v", out) }
}

func TestCorrelationReceiptFinalityOverclaimForcesUncertain(t *testing.T) {
	receipts := correlationReceipts()
	receipts[0].Final = true
	out, err := CorrelateSettlementReceipts("econ_corr_1", testIntentHash, testLegalHash, correlationObligations(), receipts)
	if err != nil { t.Fatal(err) }
	if out.State != "UNCERTAIN" || !out.ReconciliationRequired { t.Fatalf("out=%+v", out) }
}

func TestCorrelationRejectsObligationsWithoutAssetCashPair(t *testing.T) {
	_, err := CorrelateSettlementReceipts("econ_corr_1", testIntentHash, testLegalHash, []CorrelationObligation{
		{LegID:"fee1", Kind:LegFee, Rail:"rail-fee", ObligationHash:assetHash, LegalBindingHash:testLegalHash},
		{LegID:"fee2", Kind:LegFee, Rail:"rail-fee2", ObligationHash:cashHash, LegalBindingHash:testLegalHash},
	}, nil)
	if err == nil { t.Fatal("expected asset/cash requirement") }
}
