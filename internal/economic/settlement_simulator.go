package economic

import "errors"

type SettlementLegKind string

const (
	LegAsset SettlementLegKind = "asset"
	LegCash  SettlementLegKind = "cash"
	LegFee   SettlementLegKind = "fee"
)

type SimulatedLegOutcome string

const (
	OutcomeReady   SimulatedLegOutcome = "READY"
	OutcomeLocked  SimulatedLegOutcome = "LOCKED"
	OutcomeCommit  SimulatedLegOutcome = "COMMITTED"
	OutcomeFail    SimulatedLegOutcome = "FAILED"
	OutcomeUnknown SimulatedLegOutcome = "UNKNOWN"
)

type SettlementLeg struct {
	ID      string
	Kind    SettlementLegKind
	Rail    string
	Outcome SimulatedLegOutcome
}

type SettlementSimulation struct {
	Schema          string
	TransactionID   string
	Semantics       string
	Legs            []SettlementLeg
	State           string
	ExternalEffects int
	Final           bool
	Reasons         []string
}

func SimulateAtomicSettlement(transactionID string, legs []SettlementLeg) (SettlementSimulation, error) {
	if transactionID == "" {
		return SettlementSimulation{}, errors.New("economic settlement requires transaction id")
	}
	if len(legs) < 2 {
		return SettlementSimulation{}, errors.New("atomic settlement requires at least two legs")
	}

	out := SettlementSimulation{
		Schema:          "aftergraph.economic-settlement-simulation/v1",
		TransactionID:   transactionID,
		Semantics:       "atomic",
		Legs:            append([]SettlementLeg(nil), legs...),
		State:           "SIMULATED",
		ExternalEffects: 0,
		Final:           false,
	}

	seen := map[string]struct{}{}
	allCommitted := true
	for _, leg := range legs {
		if leg.ID == "" || leg.Rail == "" {
			return SettlementSimulation{}, errors.New("settlement leg identity and rail are required")
		}
		if _, ok := seen[leg.ID]; ok {
			return SettlementSimulation{}, errors.New("duplicate settlement leg")
		}
		seen[leg.ID] = struct{}{}

		switch leg.Outcome {
		case OutcomeUnknown:
			out.State = "UNCERTAIN"
			out.Reasons = append(out.Reasons, leg.ID+":unknown")
			allCommitted = false
		case OutcomeFail:
			if out.State != "UNCERTAIN" {
				out.State = "ABORTED"
			}
			out.Reasons = append(out.Reasons, leg.ID+":failed")
			allCommitted = false
		case OutcomeCommit:
			// Valid simulated commit.
		case OutcomeReady, OutcomeLocked:
			if out.State != "UNCERTAIN" && out.State != "ABORTED" {
				out.State = "NOT_COMMITTED"
			}
			allCommitted = false
		default:
			return SettlementSimulation{}, errors.New("unsupported simulated leg outcome")
		}
	}

	if allCommitted {
		out.State = "SIMULATED_COMMIT"
	}

	// Frontier invariant: simulation never establishes real-world finality.
	out.Final = false
	out.ExternalEffects = 0
	return out, nil
}
