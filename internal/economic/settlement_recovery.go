package economic

import "errors"

type CompensationOutcome string

const (
	CompensationReady   CompensationOutcome = "READY"
	CompensationApplied CompensationOutcome = "APPLIED"
	CompensationFailed  CompensationOutcome = "FAILED"
	CompensationUnknown CompensationOutcome = "UNKNOWN"
)

type CompensationStep struct {
	LegID   string
	Outcome CompensationOutcome
}

type RecoverySimulation struct {
	Schema          string
	TransactionID   string
	State           string
	Final           bool
	ExternalEffects int
	Reasons         []string
	Compensations   []CompensationStep
}

func SimulateSettlementRecovery(transactionID string, legs []SettlementLeg, compensations []CompensationStep, killSwitch bool, approvalRevoked bool) (RecoverySimulation, error) {
	if transactionID == "" {
		return RecoverySimulation{}, errors.New("economic recovery requires transaction id")
	}
	if len(legs) < 2 {
		return RecoverySimulation{}, errors.New("economic recovery requires settlement legs")
	}

	out := RecoverySimulation{
		Schema:          "aftergraph.economic-settlement-recovery-simulation/v1",
		TransactionID:   transactionID,
		State:           "NO_RECOVERY_REQUIRED",
		Final:           false,
		ExternalEffects: 0,
		Compensations:   append([]CompensationStep(nil), compensations...),
	}

	if killSwitch {
		out.State = "ABORTED_BY_KILL_SWITCH"
		out.Reasons = append(out.Reasons, "kill_switch")
		return out, nil
	}
	if approvalRevoked {
		out.State = "ABORTED_BY_REVOCATION"
		out.Reasons = append(out.Reasons, "approval_revoked")
		return out, nil
	}

	committed := map[string]bool{}
	failed := false
	uncertain := false
	for _, leg := range legs {
		switch leg.Outcome {
		case OutcomeCommit:
			committed[leg.ID] = true
		case OutcomeFail:
			failed = true
		case OutcomeUnknown:
			uncertain = true
		}
	}

	if uncertain {
		out.State = "RECONCILIATION_REQUIRED"
		out.Reasons = append(out.Reasons, "unknown_leg_outcome")
		return out, nil
	}
	if !failed {
		return out, nil
	}
	if len(committed) == 0 {
		out.State = "ABORTED_NO_COMPENSATION_REQUIRED"
		return out, nil
	}

	seen := map[string]struct{}{}
	for _, c := range compensations {
		if c.LegID == "" {
			return RecoverySimulation{}, errors.New("compensation leg id required")
		}
		if _, ok := seen[c.LegID]; ok {
			return RecoverySimulation{}, errors.New("duplicate compensation leg")
		}
		seen[c.LegID] = struct{}{}
		if !committed[c.LegID] {
			return RecoverySimulation{}, errors.New("compensation targets non-committed leg")
		}
		switch c.Outcome {
		case CompensationApplied:
			// simulated only
		case CompensationReady:
			out.State = "COMPENSATION_REQUIRED"
			out.Reasons = append(out.Reasons, c.LegID+":compensation_not_applied")
		case CompensationFailed:
			out.State = "COMPENSATION_FAILED"
			out.Reasons = append(out.Reasons, c.LegID+":compensation_failed")
		case CompensationUnknown:
			out.State = "RECONCILIATION_REQUIRED"
			out.Reasons = append(out.Reasons, c.LegID+":compensation_unknown")
		default:
			return RecoverySimulation{}, errors.New("unsupported compensation outcome")
		}
	}

	for legID := range committed {
		if _, ok := seen[legID]; !ok {
			out.State = "COMPENSATION_REQUIRED"
			out.Reasons = append(out.Reasons, legID+":missing_compensation")
		}
	}

	if out.State == "NO_RECOVERY_REQUIRED" {
		out.State = "SIMULATED_COMPENSATED"
	}

	// Recovery simulation never establishes economic or legal finality.
	out.Final = false
	out.ExternalEffects = 0
	return out, nil
}
