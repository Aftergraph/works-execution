package circuitrun

import (
	"strings"
	"testing"
	"time"
)

func TestSubjectBindsRunIDAndSpecDigest(t *testing.T) {
	run := Run{ID: "crun_0123456789abcdef0123456789abcdef", CircuitSpecSHA256: strings.Repeat("a", 64)}
	got, err := Subject(run)
	if err != nil { t.Fatal(err) }
	want := "circuit-run:" + run.ID + ":spec-sha256:" + run.CircuitSpecSHA256
	if got != want { t.Fatalf("subject=%q want=%q", got, want) }
}

func TestVerdictInputValidateFailsClosed(t *testing.T) {
	valid := VerdictInput{CircuitRunID: "crun_0123456789abcdef0123456789abcdef", Result: "ACCEPT", VerifierID: "sentinel:test", EvidenceRef: "dvr_abc", VerifiedAt: time.Now().UTC()}
	if err := valid.Validate(); err != nil { t.Fatal(err) }
	cases := []VerdictInput{
		{Result: valid.Result, VerifierID: valid.VerifierID, EvidenceRef: valid.EvidenceRef, VerifiedAt: valid.VerifiedAt},
		{CircuitRunID: valid.CircuitRunID, Result: "PASS", VerifierID: valid.VerifierID, EvidenceRef: valid.EvidenceRef, VerifiedAt: valid.VerifiedAt},
		{CircuitRunID: valid.CircuitRunID, Result: valid.Result, EvidenceRef: valid.EvidenceRef, VerifiedAt: valid.VerifiedAt},
		{CircuitRunID: valid.CircuitRunID, Result: valid.Result, VerifierID: valid.VerifierID, VerifiedAt: valid.VerifiedAt},
		{CircuitRunID: valid.CircuitRunID, Result: valid.Result, VerifierID: valid.VerifierID, EvidenceRef: valid.EvidenceRef},
	}
	for i, in := range cases { if err := in.Validate(); err == nil { t.Fatalf("case %d expected error", i) } }
}
