package economic

import "testing"

const (
	legalInstrumentHash = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	rightsHash          = "sha256:2222222222222222222222222222222222222222222222222222222222222222"
	registryRecordHash  = "sha256:3333333333333333333333333333333333333333333333333333333333333333"
	custodyRecordHash   = "sha256:4444444444444444444444444444444444444444444444444444444444444444"
	reprRecordHash      = "sha256:5555555555555555555555555555555555555555555555555555555555555555"
	registryEvidence    = "sha256:6666666666666666666666666666666666666666666666666666666666666666"
	custodyEvidence     = "sha256:7777777777777777777777777777777777777777777777777777777777777777"
	reprEvidence        = "sha256:8888888888888888888888888888888888888888888888888888888888888888"
	accountFingerprint  = "sha256:9999999999999999999999999999999999999999999999999999999999999999"
)

func legalFixture() (CanonicalAssetRight, AuthoritativeRegistryRecord, CustodialRightRecord, LedgerRepresentationRecord) {
	canonical := CanonicalAssetRight{
		AssetID:"asset_1", RightID:"right_1",
		LegalInstrumentHash:legalInstrumentHash,
		RightsHash:rightsHash,
		Jurisdiction:"DK",
	}
	registry := AuthoritativeRegistryRecord{
		Schema:"aftergraph.authoritative-asset-registry-record/v1",
		RegistryID:"registry_authoritative_1",
		AssetID:"asset_1", RightID:"right_1",
		LegalInstrumentHash:legalInstrumentHash, RightsHash:rightsHash,
		RecordHash:registryRecordHash, EvidenceHash:registryEvidence,
		Status:LegalRightActive, ExternalEffects:0, Final:false,
	}
	custody := CustodialRightRecord{
		Schema:"aftergraph.custodial-right-record/v1",
		CustodianID:"custodian_1", CustodyRef:"cust_1",
		AssetID:"asset_1", RightID:"right_1",
		LegalInstrumentHash:legalInstrumentHash, RightsHash:rightsHash,
		AccountFingerprint:accountFingerprint,
		RecordHash:custodyRecordHash, EvidenceHash:custodyEvidence,
		Status:LegalRightActive, ExternalEffects:0, Final:false,
	}
	repr := LedgerRepresentationRecord{
		Schema:"aftergraph.ledger-asset-representation-record/v1",
		Network:"test-ledger", RepresentationID:"token_1",
		AssetID:"asset_1", RightID:"right_1",
		LegalInstrumentHash:legalInstrumentHash, RightsHash:rightsHash,
		RepresentationHash:reprRecordHash, EvidenceHash:reprEvidence,
		Status:LegalRightActive, ExternalEffects:0, Final:false,
	}
	return canonical, registry, custody, repr
}

func TestLegalReconciliationAlignsAllRepresentations(t *testing.T) {
	c, r, k, l := legalFixture()
	out, err := ReconcileLegalAssetRight(c, r, k, l)
	if err != nil { t.Fatal(err) }
	if out.State != "LEGAL_RIGHT_ALIGNED" { t.Fatalf("state=%s reasons=%v", out.State, out.Reasons) }
	if out.ReconciliationRequired { t.Fatal("aligned records should not require reconciliation") }
	if len(out.EvidenceSources) != 3 { t.Fatalf("sources=%v", out.EvidenceSources) }
	if out.Final { t.Fatal("alignment cannot claim legal finality") }
	if out.ExternalEffects != 0 { t.Fatal("reconciliation must be zero-effect") }
}

func TestLegalReconciliationRegistryIdentityMismatchFailsClosed(t *testing.T) {
	c, r, k, l := legalFixture()
	r.AssetID="asset_other"
	out, _ := ReconcileLegalAssetRight(c, r, k, l)
	if out.State != "LEGAL_RECONCILIATION_REQUIRED" || !out.ReconciliationRequired { t.Fatalf("out=%+v", out) }
}

func TestLegalReconciliationRightsMismatchFailsClosed(t *testing.T) {
	c, r, k, l := legalFixture()
	k.RightsHash=legalInstrumentHash
	out, _ := ReconcileLegalAssetRight(c, r, k, l)
	if out.State != "LEGAL_RECONCILIATION_REQUIRED" { t.Fatalf("out=%+v", out) }
}

func TestLegalReconciliationRepresentationMismatchFailsClosed(t *testing.T) {
	c, r, k, l := legalFixture()
	l.LegalInstrumentHash=rightsHash
	out, _ := ReconcileLegalAssetRight(c, r, k, l)
	if out.State != "LEGAL_RECONCILIATION_REQUIRED" { t.Fatalf("out=%+v", out) }
}

func TestLegalReconciliationSuspendedOrRevokedFailsClosed(t *testing.T) {
	c, r, k, l := legalFixture()
	r.Status=LegalRightSuspended
	out, _ := ReconcileLegalAssetRight(c, r, k, l)
	if out.State != "LEGAL_RECONCILIATION_REQUIRED" { t.Fatalf("out=%+v", out) }

	c, r, k, l = legalFixture()
	k.Status=LegalRightRevoked
	out, _ = ReconcileLegalAssetRight(c, r, k, l)
	if out.State != "LEGAL_RECONCILIATION_REQUIRED" { t.Fatalf("out=%+v", out) }
}

func TestLegalReconciliationRejectsEvidenceReuse(t *testing.T) {
	c, r, k, l := legalFixture()
	k.EvidenceHash=r.EvidenceHash
	out, _ := ReconcileLegalAssetRight(c, r, k, l)
	if out.State != "LEGAL_RECONCILIATION_REQUIRED" { t.Fatalf("out=%+v", out) }
}

func TestLegalReconciliationRejectsFinalityOrEffectOverclaim(t *testing.T) {
	c, r, k, l := legalFixture()
	l.Final=true
	out, _ := ReconcileLegalAssetRight(c, r, k, l)
	if out.State != "LEGAL_RECONCILIATION_REQUIRED" { t.Fatalf("out=%+v", out) }

	c, r, k, l = legalFixture()
	r.ExternalEffects=1
	out, _ = ReconcileLegalAssetRight(c, r, k, l)
	if out.State != "LEGAL_RECONCILIATION_REQUIRED" { t.Fatalf("out=%+v", out) }
}
