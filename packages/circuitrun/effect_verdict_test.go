package circuitrun

import (
	"testing"
	"time"
)

func TestEffectVerdictSubjectBindsExactReceipt(t *testing.T) {
	r := EffectReceipt{ID: "ercpt_0123456789abcdef0123456789abcdef", ReceiptSHA256: "a" + string(make([]byte, 63)),
		ExecutorID: "runtime-1", State: EffectApplied}
	subject, err := EffectReceiptSubject(r)
	if err == nil || subject != "" {
		t.Fatal("non-hex receipt digest must fail closed")
	}
	r.ReceiptSHA256 = "a" + repeatHex("b", 63)
	subject, err = EffectReceiptSubject(r)
	if err != nil {
		t.Fatal(err)
	}
	if subject == "" {
		t.Fatal("subject missing")
	}
}

func TestEffectVerdictInputRejectsSelfVerifier(t *testing.T) {
	in := EffectVerdictInput{EffectReceiptID: "ercpt_0123456789abcdef0123456789abcdef", Result: "ACCEPT",
		VerifierID: "runtime-1", EvidenceRef: "evidence://witness/1", VerifiedAt: time.Unix(20, 0).UTC()}
	if err := in.ValidateAgainstExecutor("runtime-1"); err == nil {
		t.Fatal("self verifier must be rejected")
	}
}

func repeatHex(s string, n int) string {
	out := ""
	for i := 0; i < n; i++ {
		out += s
	}
	return out
}
