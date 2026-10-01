package economic

import (
	"errors"
	"sort"
	"strings"
)

type LegalRightStatus string

const (
	LegalRightActive    LegalRightStatus = "ACTIVE"
	LegalRightSuspended LegalRightStatus = "SUSPENDED"
	LegalRightRevoked   LegalRightStatus = "REVOKED"
	LegalRightUnknown   LegalRightStatus = "UNKNOWN"
)

type CanonicalAssetRight struct {
	AssetID             string
	RightID             string
	LegalInstrumentHash string
	RightsHash          string
	Jurisdiction        string
}

type AuthoritativeRegistryRecord struct {
	Schema              string
	RegistryID          string
	AssetID             string
	RightID             string
	LegalInstrumentHash string
	RightsHash          string
	RecordHash          string
	EvidenceHash        string
	Status              LegalRightStatus
	ExternalEffects     int
	Final               bool
}

type CustodialRightRecord struct {
	Schema              string
	CustodianID          string
	CustodyRef           string
	AssetID              string
	RightID              string
	LegalInstrumentHash  string
	RightsHash           string
	AccountFingerprint   string
	RecordHash           string
	EvidenceHash         string
	Status               LegalRightStatus
	ExternalEffects      int
	Final                bool
}

type LedgerRepresentationRecord struct {
	Schema              string
	Network              string
	RepresentationID     string
	AssetID              string
	RightID              string
	LegalInstrumentHash  string
	RightsHash           string
	RepresentationHash   string
	EvidenceHash         string
	Status               LegalRightStatus
	ExternalEffects      int
	Final                bool
}

type LegalReconciliation struct {
	Schema                 string
	AssetID                string
	RightID                string
	LegalInstrumentHash    string
	RightsHash             string
	State                  string
	Final                  bool
	ExternalEffects        int
	ReconciliationRequired bool
	EvidenceSources        []string
	Reasons                []string
}

func validHashRef(v string) bool {
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

func ReconcileLegalAssetRight(
	canonical CanonicalAssetRight,
	registry AuthoritativeRegistryRecord,
	custody CustodialRightRecord,
	representation LedgerRepresentationRecord,
) (LegalReconciliation, error) {
	if canonical.AssetID == "" || canonical.RightID == "" || canonical.Jurisdiction == "" {
		return LegalReconciliation{}, errors.New("canonical legal right identity is required")
	}
	if !validHashRef(canonical.LegalInstrumentHash) || !validHashRef(canonical.RightsHash) {
		return LegalReconciliation{}, errors.New("canonical legal right hashes are invalid")
	}

	out := LegalReconciliation{
		Schema:                 "aftergraph.economic-legal-reconciliation/v1",
		AssetID:                canonical.AssetID,
		RightID:                canonical.RightID,
		LegalInstrumentHash:    canonical.LegalInstrumentHash,
		RightsHash:             canonical.RightsHash,
		State:                  "LEGAL_RIGHT_ALIGNED",
		Final:                  false,
		ExternalEffects:        0,
		ReconciliationRequired: false,
	}

	seenEvidence := map[string]string{}
	fail := func(source, reason string) {
		out.State = "LEGAL_RECONCILIATION_REQUIRED"
		out.ReconciliationRequired = true
		out.Reasons = append(out.Reasons, source+":"+reason)
	}

	checkEvidence := func(source, hash string) {
		if !validHashRef(hash) {
			fail(source, "evidence_hash_invalid")
			return
		}
		if prev, exists := seenEvidence[hash]; exists {
			fail(source, "evidence_reused_with_"+prev)
			return
		}
		seenEvidence[hash] = source
		out.EvidenceSources = append(out.EvidenceSources, source)
	}

	if registry.Schema != "aftergraph.authoritative-asset-registry-record/v1" {
		fail("registry", "schema_mismatch")
	}
	if registry.RegistryID == "" || !validHashRef(registry.RecordHash) {
		fail("registry", "identity_invalid")
	}
	checkEvidence("registry", registry.EvidenceHash)
	if registry.AssetID != canonical.AssetID || registry.RightID != canonical.RightID {
		fail("registry", "asset_right_identity_mismatch")
	}
	if registry.LegalInstrumentHash != canonical.LegalInstrumentHash {
		fail("registry", "legal_instrument_mismatch")
	}
	if registry.RightsHash != canonical.RightsHash {
		fail("registry", "rights_mismatch")
	}
	if registry.Status != LegalRightActive {
		fail("registry", "status_not_active")
	}
	if registry.ExternalEffects != 0 || registry.Final {
		fail("registry", "receipt_overclaim")
	}

	if custody.Schema != "aftergraph.custodial-right-record/v1" {
		fail("custody", "schema_mismatch")
	}
	if custody.CustodianID == "" || custody.CustodyRef == "" ||
		!validHashRef(custody.RecordHash) || !validHashRef(custody.AccountFingerprint) {
		fail("custody", "identity_invalid")
	}
	checkEvidence("custody", custody.EvidenceHash)
	if custody.AssetID != canonical.AssetID || custody.RightID != canonical.RightID {
		fail("custody", "asset_right_identity_mismatch")
	}
	if custody.LegalInstrumentHash != canonical.LegalInstrumentHash {
		fail("custody", "legal_instrument_mismatch")
	}
	if custody.RightsHash != canonical.RightsHash {
		fail("custody", "rights_mismatch")
	}
	if custody.Status != LegalRightActive {
		fail("custody", "status_not_active")
	}
	if custody.ExternalEffects != 0 || custody.Final {
		fail("custody", "receipt_overclaim")
	}

	if representation.Schema != "aftergraph.ledger-asset-representation-record/v1" {
		fail("representation", "schema_mismatch")
	}
	if representation.Network == "" || representation.RepresentationID == "" ||
		!validHashRef(representation.RepresentationHash) {
		fail("representation", "identity_invalid")
	}
	checkEvidence("representation", representation.EvidenceHash)
	if representation.AssetID != canonical.AssetID || representation.RightID != canonical.RightID {
		fail("representation", "asset_right_identity_mismatch")
	}
	if representation.LegalInstrumentHash != canonical.LegalInstrumentHash {
		fail("representation", "legal_instrument_mismatch")
	}
	if representation.RightsHash != canonical.RightsHash {
		fail("representation", "rights_mismatch")
	}
	if representation.Status != LegalRightActive {
		fail("representation", "status_not_active")
	}
	if representation.ExternalEffects != 0 || representation.Final {
		fail("representation", "receipt_overclaim")
	}

	sort.Strings(out.EvidenceSources)
	sort.Strings(out.Reasons)

	// This proves cross-record alignment only. It does not itself establish legal title,
	// legal finality, settlement finality, or authority to transfer the right.
	out.Final = false
	out.ExternalEffects = 0
	return out, nil
}
