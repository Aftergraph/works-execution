package circuitrun

import (
	"encoding/json"
	"testing"
)

func TestCanonicalizeSpecIgnoresFormattingAndKeyOrder(t *testing.T) {
	a := json.RawMessage(`{"schema_version":"circuit-spec/0.1","circuit_id":"x","nodes":[],"edges":[],"mode":"READ_ONLY","consequential":false}`)
	b := json.RawMessage(`{ "mode":"READ_ONLY", "edges":[], "nodes":[], "circuit_id":"x", "consequential":false, "schema_version":"circuit-spec/0.1" }`)
	ca, da, err := CanonicalizeSpec(a)
	if err != nil {
		t.Fatal(err)
	}
	cb, db, err := CanonicalizeSpec(b)
	if err != nil {
		t.Fatal(err)
	}
	if string(ca) != string(cb) {
		t.Fatalf("canonical mismatch\n%s\n%s", ca, cb)
	}
	if da != db || len(da) != 64 {
		t.Fatalf("digest mismatch/length: %q %q", da, db)
	}
}

func TestCanonicalizeSpecRequiresJSONObject(t *testing.T) {
	for _, raw := range []string{`[]`, `"text"`, `null`, `1`} {
		if _, _, err := CanonicalizeSpec(json.RawMessage(raw)); err == nil {
			t.Fatalf("expected object rejection for %s", raw)
		}
	}
}

func TestInputValidateUsesCanonicalMissionIDVocabulary(t *testing.T) {
	in := Input{CircuitID: "repair", CircuitSpec: json.RawMessage(`{"schema_version":"circuit-spec/0.1","circuit_id":"repair"}`), WorkID: "wrk_0123456789abcdef0123456789abcdef", MissionID: "mission-1"}
	if err := in.Validate(); err == nil {
		t.Fatal("expected non-canonical mission_id to be rejected")
	}
}

func TestInputValidateRequiresBindingFields(t *testing.T) {
	valid := Input{
		CircuitID:   "repair",
		CircuitSpec: json.RawMessage(`{"schema_version":"circuit-spec/0.1","circuit_id":"repair"}`),
		WorkID:      "wrk_0123456789abcdef0123456789abcdef",
		MissionID:   "mis_circuit_test",
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid input: %v", err)
	}
	cases := []Input{
		{CircuitSpec: valid.CircuitSpec, WorkID: valid.WorkID, MissionID: valid.MissionID},
		{CircuitID: valid.CircuitID, WorkID: valid.WorkID, MissionID: valid.MissionID},
		{CircuitID: valid.CircuitID, CircuitSpec: valid.CircuitSpec, MissionID: valid.MissionID},
		{CircuitID: valid.CircuitID, CircuitSpec: valid.CircuitSpec, WorkID: valid.WorkID},
	}
	for i, in := range cases {
		if err := in.Validate(); err == nil {
			t.Fatalf("case %d: expected validation error", i)
		}
	}
}

func TestInputValidateBindsCircuitEnvelopeIdentity(t *testing.T) {
	base := Input{CircuitID: "repair", WorkID: "wrk_0123456789abcdef0123456789abcdef", MissionID: "mis_circuit_test"}
	base.CircuitSpec = json.RawMessage(`{"schema_version":"circuit-spec/0.1","circuit_id":"other"}`)
	if err := base.Validate(); err == nil {
		t.Fatal("expected circuit_id mismatch rejection")
	}
	base.CircuitSpec = json.RawMessage(`{"schema_version":"other/9.9","circuit_id":"repair"}`)
	if err := base.Validate(); err == nil {
		t.Fatal("expected schema_version rejection")
	}
}
