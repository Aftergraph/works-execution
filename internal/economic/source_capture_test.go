package economic

import "testing"

func runtimeCaptureFixture() RuntimeEconomicRightCaptureV1 {
	return RuntimeEconomicRightCaptureV1{
		Schema:          "aftergraph.economic-right-capture/v1",
		SourceClass:     EvidenceRegistry,
		SourceID:        "registry_1",
		Generation:      11,
		Cursor:          RuntimeEconomicSourceCursorV1{Kind: "version", Value: "11"},
		ObservedAtUnix:  1_999_900,
		EvidenceHash:    "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		RecordDigest:    "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		CaptureHash:     "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
		SourceTransport: "READ_ONLY",
		Final:           false,
		ExternalEffects: 0,
	}
}

func TestSourceObservationFromRuntimeCaptureProjectsV9Shape(t *testing.T) {
	c := runtimeCaptureFixture()
	out, err := SourceObservationFromRuntimeCapture(c)
	if err != nil { t.Fatal(err) }
	if out.SourceClass != EvidenceRegistry || out.SourceID != "registry_1" {
		t.Fatalf("out=%+v", out)
	}
	if out.Generation != 11 || out.ObservedAtUnix != 1_999_900 {
		t.Fatalf("out=%+v", out)
	}
	if out.EvidenceHash != c.EvidenceHash {
		t.Fatal("evidence hash projection changed")
	}
}

func TestSourceObservationFromRuntimeCaptureRejectsWriteOrFinality(t *testing.T) {
	c := runtimeCaptureFixture()
	c.SourceTransport = "READ_WRITE"
	if _, err := SourceObservationFromRuntimeCapture(c); err == nil {
		t.Fatal("expected read-only transport rejection")
	}

	c = runtimeCaptureFixture()
	c.ExternalEffects = 1
	if _, err := SourceObservationFromRuntimeCapture(c); err == nil {
		t.Fatal("expected external effects rejection")
	}

	c = runtimeCaptureFixture()
	c.Final = true
	if _, err := SourceObservationFromRuntimeCapture(c); err == nil {
		t.Fatal("expected finality rejection")
	}
}

func TestSourceObservationFromRuntimeCaptureRejectsInvalidCursorGenerationAndHash(t *testing.T) {
	c := runtimeCaptureFixture()
	c.Cursor.Kind = "opaque"
	if _, err := SourceObservationFromRuntimeCapture(c); err == nil {
		t.Fatal("expected cursor kind rejection")
	}

	c = runtimeCaptureFixture()
	c.Generation = 0
	if _, err := SourceObservationFromRuntimeCapture(c); err == nil {
		t.Fatal("expected generation rejection")
	}

	c = runtimeCaptureFixture()
	c.CaptureHash = "bad"
	if _, err := SourceObservationFromRuntimeCapture(c); err == nil {
		t.Fatal("expected hash rejection")
	}
}

func TestProjectedRuntimeCapturesFeedEvidenceCoherence(t *testing.T) {
	base := runtimeCaptureFixture()
	custody := base
	custody.SourceClass = EvidenceCustody
	custody.SourceID = "custodian_1"
	custody.Generation = 21
	custody.Cursor = RuntimeEconomicSourceCursorV1{Kind: "sequence", Value: "21"}
	custody.ObservedAtUnix = 1_999_910
	custody.EvidenceHash = "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"

	repr := base
	repr.SourceClass = EvidenceRepresentation
	repr.SourceID = "ledger_1"
	repr.Generation = 31
	repr.Cursor = RuntimeEconomicSourceCursorV1{Kind: "offset", Value: "31"}
	repr.ObservedAtUnix = 1_999_920
	repr.EvidenceHash = "sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"

	observations := make([]SourceObservationMeta, 0, 3)
	for _, c := range []RuntimeEconomicRightCaptureV1{base, custody, repr} {
		o, err := SourceObservationFromRuntimeCapture(c)
		if err != nil { t.Fatal(err) }
		observations = append(observations, o)
	}

	out, err := EvaluateEvidenceCoherence(EvidenceCoherencePolicy{
		NowUnix: 2_000_000,
		MaxAgeSeconds: 300,
		MaxSkewSeconds: 30,
		MaxFutureSkewSeconds: 5,
	}, observations, map[string]int64{
		"registry_1": 10,
		"custodian_1": 20,
		"ledger_1": 30,
	})
	if err != nil { t.Fatal(err) }
	if out.State != "TEMPORALLY_COHERENT" {
		t.Fatalf("state=%s reasons=%v", out.State, out.Reasons)
	}
}
