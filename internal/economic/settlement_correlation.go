package economic

import (
	"errors"
	"sort"
	"strings"
)

type CorrelationObligation struct {
	LegID            string
	Kind             SettlementLegKind
	Rail             string
	ObligationHash   string
	LegalBindingHash string
}

type RailSettlementReceipt struct {
	Schema             string
	EconomicTransactionID string
	LegID              string
	Kind               SettlementLegKind
	Rail               string
	ObligationHash     string
	LegalBindingHash   string
	SourceEvidenceHash string
	Outcome            SimulatedLegOutcome
	ExternalEffects    int
	Final              bool
}

type SettlementCorrelation struct {
	Schema                string
	EconomicTransactionID string
	IntentHash            string
	LegalBindingHash      string
	State                 string
	Final                 bool
	ExternalEffects       int
	ReconciliationRequired bool
	MatchedLegs           []string
	Reasons               []string
}

func isSHA256Ref(v string) bool {
	if len(v) != len("sha256:")+64 || !strings.HasPrefix(v, "sha256:") {
		return false
	}
	for _, c := range v[len("sha256:"):] {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

func CorrelateSettlementReceipts(
	economicTransactionID string,
	intentHash string,
	legalBindingHash string,
	obligations []CorrelationObligation,
	receipts []RailSettlementReceipt,
) (SettlementCorrelation, error) {
	if economicTransactionID == "" {
		return SettlementCorrelation{}, errors.New("economic correlation requires transaction id")
	}
	if !isSHA256Ref(intentHash) {
		return SettlementCorrelation{}, errors.New("economic correlation requires sha256 intent hash")
	}
	if !isSHA256Ref(legalBindingHash) {
		return SettlementCorrelation{}, errors.New("economic correlation requires sha256 legal binding hash")
	}
	if len(obligations) < 2 {
		return SettlementCorrelation{}, errors.New("economic correlation requires at least two obligations")
	}

	out := SettlementCorrelation{
		Schema:                 "aftergraph.economic-settlement-correlation/v1",
		EconomicTransactionID:  economicTransactionID,
		IntentHash:             intentHash,
		LegalBindingHash:       legalBindingHash,
		State:                  "CORRELATED",
		Final:                  false,
		ExternalEffects:        0,
		ReconciliationRequired: false,
	}

	expected := map[string]CorrelationObligation{}
	hasAsset := false
	hasCash := false
	for _, obligation := range obligations {
		if obligation.LegID == "" || obligation.Rail == "" {
			return SettlementCorrelation{}, errors.New("economic obligation identity and rail are required")
		}
		if !isSHA256Ref(obligation.ObligationHash) {
			return SettlementCorrelation{}, errors.New("economic obligation requires sha256 obligation hash")
		}
		if obligation.LegalBindingHash != legalBindingHash {
			return SettlementCorrelation{}, errors.New("economic obligation legal binding mismatch")
		}
		if _, exists := expected[obligation.LegID]; exists {
			return SettlementCorrelation{}, errors.New("duplicate economic obligation leg")
		}
		switch obligation.Kind {
		case LegAsset:
			hasAsset = true
		case LegCash:
			hasCash = true
		case LegFee:
			// Fee legs may be correlated in addition to the required asset/cash pair.
		default:
			return SettlementCorrelation{}, errors.New("unsupported economic obligation kind")
		}
		expected[obligation.LegID] = obligation
	}
	if !hasAsset || !hasCash {
		return SettlementCorrelation{}, errors.New("economic correlation requires asset and cash obligations")
	}

	seenLeg := map[string]struct{}{}
	seenEvidence := map[string]struct{}{}
	for _, receipt := range receipts {
		if receipt.Schema != "aftergraph.rail-settlement-receipt/v1" {
			out.State = "UNCERTAIN"
			out.ReconciliationRequired = true
			out.Reasons = append(out.Reasons, receipt.LegID+":receipt_schema_mismatch")
			continue
		}
		if receipt.EconomicTransactionID != economicTransactionID {
			out.State = "UNCERTAIN"
			out.ReconciliationRequired = true
			out.Reasons = append(out.Reasons, receipt.LegID+":transaction_id_mismatch")
			continue
		}
		if receipt.ExternalEffects != 0 || receipt.Final {
			out.State = "UNCERTAIN"
			out.ReconciliationRequired = true
			out.Reasons = append(out.Reasons, receipt.LegID+":receipt_overclaim")
			continue
		}
		expectedLeg, ok := expected[receipt.LegID]
		if !ok {
			out.State = "UNCERTAIN"
			out.ReconciliationRequired = true
			out.Reasons = append(out.Reasons, receipt.LegID+":unexpected_leg")
			continue
		}
		if _, exists := seenLeg[receipt.LegID]; exists {
			out.State = "UNCERTAIN"
			out.ReconciliationRequired = true
			out.Reasons = append(out.Reasons, receipt.LegID+":duplicate_receipt")
			continue
		}
		seenLeg[receipt.LegID] = struct{}{}

		if !isSHA256Ref(receipt.SourceEvidenceHash) {
			out.State = "UNCERTAIN"
			out.ReconciliationRequired = true
			out.Reasons = append(out.Reasons, receipt.LegID+":source_evidence_invalid")
			continue
		}
		if _, exists := seenEvidence[receipt.SourceEvidenceHash]; exists {
			out.State = "UNCERTAIN"
			out.ReconciliationRequired = true
			out.Reasons = append(out.Reasons, receipt.LegID+":source_evidence_reused")
			continue
		}
		seenEvidence[receipt.SourceEvidenceHash] = struct{}{}

		if receipt.Kind != expectedLeg.Kind ||
			receipt.Rail != expectedLeg.Rail ||
			receipt.ObligationHash != expectedLeg.ObligationHash ||
			receipt.LegalBindingHash != expectedLeg.LegalBindingHash {
			out.State = "UNCERTAIN"
			out.ReconciliationRequired = true
			out.Reasons = append(out.Reasons, receipt.LegID+":obligation_binding_mismatch")
			continue
		}
		if receipt.Outcome != OutcomeCommit {
			out.State = "UNCERTAIN"
			out.ReconciliationRequired = true
			out.Reasons = append(out.Reasons, receipt.LegID+":not_committed")
			continue
		}
		out.MatchedLegs = append(out.MatchedLegs, receipt.LegID)
	}

	for legID := range expected {
		if _, ok := seenLeg[legID]; !ok {
			out.State = "UNCERTAIN"
			out.ReconciliationRequired = true
			out.Reasons = append(out.Reasons, legID+":missing_receipt")
		}
	}

	sort.Strings(out.MatchedLegs)
	sort.Strings(out.Reasons)

	// Correlation proves common transaction binding only.
	// It never establishes legal or real-world settlement finality.
	out.Final = false
	out.ExternalEffects = 0
	return out, nil
}
