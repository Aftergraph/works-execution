package economic

import "testing"

func coherencePolicy() EvidenceCoherencePolicy {
	return EvidenceCoherencePolicy{
		NowUnix: 2_000_000,
		MaxAgeSeconds: 300,
		MaxSkewSeconds: 30,
		MaxFutureSkewSeconds: 5,
	}
}

func coherenceObservations() []SourceObservationMeta {
	return []SourceObservationMeta{
		{SourceClass:EvidenceRegistry, SourceID:"registry_1", EvidenceHash:"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", ObservedAtUnix:1_999_900, Generation:11},
		{SourceClass:EvidenceCustody, SourceID:"custodian_1", EvidenceHash:"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", ObservedAtUnix:1_999_910, Generation:21},
		{SourceClass:EvidenceRepresentation, SourceID:"ledger_1", EvidenceHash:"sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc", ObservedAtUnix:1_999_920, Generation:31},
	}
}

func TestEvidenceCoherenceAcceptsFreshDistinctMonotonicSources(t *testing.T) {
	out, err := EvaluateEvidenceCoherence(coherencePolicy(), coherenceObservations(), map[string]int64{
		"registry_1":10, "custodian_1":20, "ledger_1":30,
	})
	if err != nil { t.Fatal(err) }
	if out.State != "TEMPORALLY_COHERENT" { t.Fatalf("state=%s reasons=%v", out.State, out.Reasons) }
	if out.RefreshRequired { t.Fatal("fresh evidence must not require refresh") }
	if out.Final { t.Fatal("coherence cannot be FINAL") }
	if out.ExternalEffects != 0 { t.Fatal("coherence must be zero-effect") }
}

func TestEvidenceCoherenceRejectsStaleSource(t *testing.T) {
	obs:=coherenceObservations()
	obs[0].ObservedAtUnix=1_999_000
	out,_:=EvaluateEvidenceCoherence(coherencePolicy(),obs,nil)
	if out.State!="EVIDENCE_REFRESH_REQUIRED" { t.Fatalf("out=%+v",out) }
}

func TestEvidenceCoherenceRejectsObservationSkew(t *testing.T) {
	obs:=coherenceObservations()
	obs[2].ObservedAtUnix=1_999_950
	out,_:=EvaluateEvidenceCoherence(coherencePolicy(),obs,nil)
	if out.State!="EVIDENCE_REFRESH_REQUIRED" { t.Fatalf("out=%+v",out) }
}

func TestEvidenceCoherenceRejectsReplayOrGenerationRegression(t *testing.T) {
	obs:=coherenceObservations()
	out,_:=EvaluateEvidenceCoherence(coherencePolicy(),obs,map[string]int64{"registry_1":11})
	if out.State!="EVIDENCE_REFRESH_REQUIRED" { t.Fatalf("out=%+v",out) }

	obs=coherenceObservations()
	obs[0].Generation=9
	out,_=EvaluateEvidenceCoherence(coherencePolicy(),obs,map[string]int64{"registry_1":10})
	if out.State!="EVIDENCE_REFRESH_REQUIRED" { t.Fatalf("out=%+v",out) }
}

func TestEvidenceCoherenceRejectsFutureTimestampBeyondTolerance(t *testing.T) {
	obs:=coherenceObservations()
	obs[1].ObservedAtUnix=2_000_010
	out,_:=EvaluateEvidenceCoherence(coherencePolicy(),obs,nil)
	if out.State!="EVIDENCE_REFRESH_REQUIRED" { t.Fatalf("out=%+v",out) }
}

func TestEvidenceCoherenceRejectsMissingOrDuplicateClass(t *testing.T) {
	obs:=coherenceObservations()[:2]
	out,_:=EvaluateEvidenceCoherence(coherencePolicy(),obs,nil)
	if out.State!="EVIDENCE_REFRESH_REQUIRED" { t.Fatalf("out=%+v",out) }

	obs=coherenceObservations()
	obs[2].SourceClass=EvidenceCustody
	out,_=EvaluateEvidenceCoherence(coherencePolicy(),obs,nil)
	if out.State!="EVIDENCE_REFRESH_REQUIRED" { t.Fatalf("out=%+v",out) }
}

func TestEvidenceCoherenceRejectsEvidenceReuse(t *testing.T) {
	obs:=coherenceObservations()
	obs[2].EvidenceHash=obs[0].EvidenceHash
	out,_:=EvaluateEvidenceCoherence(coherencePolicy(),obs,nil)
	if out.State!="EVIDENCE_REFRESH_REQUIRED" { t.Fatalf("out=%+v",out) }
}

func TestEvidenceCoherenceRejectsInvalidPolicy(t *testing.T) {
	_,err:=EvaluateEvidenceCoherence(EvidenceCoherencePolicy{},coherenceObservations(),nil)
	if err==nil { t.Fatal("expected invalid policy") }
}
