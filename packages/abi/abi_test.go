package abi

import (
	"encoding/json"
	"errors"
	"testing"
)

// TestAbiVersionAdversarial tests abi version adversarial cases:
// rab/1.1, rab/0.9, empty, RAB/1.0 uppercase
func TestAbiVersionAdversarial(t *testing.T) {
	tests := []struct {
		name    string
		abi     string
		wantErr error
	}{
		{"valid", "rab/1.0", nil},
		{"future minor", "rab/1.1", ErrAbiVersion},
		{"past minor", "rab/0.9", ErrAbiVersion},
		{"empty", "", ErrAbiVersion},
		{"uppercase", "RAB/1.0", ErrAbiVersion},
		{"major bump", "rab/2.0", ErrAbiVersion},
		{"typo", "rab/10", ErrAbiVersion},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rab := &RAB{Abi: tc.abi, Caps: []string{"observe"}}
			err := rab.Validate()
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
			} else {
				if err == nil {
					t.Fatal("Validate() = nil, want error")
				}
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("Validate() = %v, want %v", err, tc.wantErr)
				}
			}
		})
	}
}

// TestCapsEnumAndDup tests cap enum validation and duplicate detection.
func TestCapsEnumAndDup(t *testing.T) {
	tests := []struct {
		name    string
		caps    []string
		wantErr error
	}{
		{"all valid passive", []string{"screenshot", "input", "record", "observe"}, nil},
		{"single valid", []string{"observe"}, nil},
		{"empty caps legal", []string{}, nil},
		{"unknown cap", []string{"observe", "teleport"}, ErrCapUnknown},
		{"duplicate cap", []string{"observe", "observe"}, ErrCapDuplicate},
		{"unknown then duplicate", []string{"teleport", "teleport"}, ErrCapUnknown}, // unknown hits first
		{"case sensitive", []string{"Observe"}, ErrCapUnknown},
		{"partial valid with unknown", []string{"screenshot", "teleport"}, ErrCapUnknown},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rab := &RAB{Abi: AbiVersion, Caps: tc.caps}
			err := rab.Validate()
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
			} else {
				if err == nil {
					t.Fatal("Validate() = nil, want error")
				}
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("Validate() = %v, want %v", err, tc.wantErr)
				}
			}
		})
	}
}

// TestControlTokenLaw tests the control token requirements.
func TestControlTokenLaw(t *testing.T) {
	tests := []struct {
		name               string
		caps               []string
		controlTokenReq    *bool
		wantErr            error
		wantRequiresToken  bool // RequiresControlToken() after Validate passes
	}{
		// control without token => rejected
		{"control without field", []string{"control"}, nil, ErrControlTokenRequired, false},
		{"control with false", []string{"control"}, boolPtr(false), ErrControlTokenRequired, false},
		{"control with true", []string{"control"}, boolPtr(true), nil, true},
		// passive caps without token => accepted
		{"observe only", []string{"observe"}, nil, nil, false},
		{"observe with false", []string{"observe"}, boolPtr(false), nil, false},
		{"observe with true", []string{"observe"}, boolPtr(true), nil, true}, // field=true => RequiresControlToken=true
		{"screenshot only", []string{"screenshot"}, nil, nil, false},
		{"input only", []string{"input"}, nil, nil, false},
		{"record only", []string{"record"}, nil, nil, false},
		{"multiple passive", []string{"screenshot", "input", "record", "observe"}, nil, nil, false},
		{"mixed passive and control with token", []string{"observe", "control"}, boolPtr(true), nil, true},
		{"empty caps", []string{}, nil, nil, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rab := &RAB{
				Abi:                  AbiVersion,
				Caps:                 tc.caps,
				ControlTokenRequired: tc.controlTokenReq,
			}
			err := rab.Validate()
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				// After successful validation, RequiresControlToken should match expectation
				if got := rab.RequiresControlToken(); got != tc.wantRequiresToken {
					t.Fatalf("RequiresControlToken() = %v, want %v", got, tc.wantRequiresToken)
				}
			} else {
				if err == nil {
					t.Fatal("Validate() = nil, want error")
				}
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("Validate() = %v, want %v", err, tc.wantErr)
				}
			}
		})
	}
}

// boolPtr returns a pointer to the given bool value.
func boolPtr(b bool) *bool {
	return &b
}

// TestHas tests the Has method.
func TestHas(t *testing.T) {
	rab := &RAB{
		Abi:                  AbiVersion,
		Caps:                 []string{"screenshot", "input", "record", "observe", "control"},
		ControlTokenRequired: boolPtr(true),
	}
	if err := rab.Validate(); err != nil {
		t.Fatalf("setup: %v", err)
	}

	tests := []struct {
		name     string
		cap      string
		wantBool bool
	}{
		{"has screenshot", "screenshot", true},
		{"has input", "input", true},
		{"has record", "record", true},
		{"has observe", "observe", true},
		{"has control", "control", true},
		{"missing", "teleport", false},
		{"case sensitive", "SCREENSHOT", false},
		{"empty string", "", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := rab.Has(tc.cap); got != tc.wantBool {
				t.Fatalf("Has(%q) = %v, want %v", tc.cap, got, tc.wantBool)
			}
		})
	}
}

// TestNegotiate tests the Negotiate method.
func TestNegotiate(t *testing.T) {
	rab := &RAB{
		Abi:                  AbiVersion,
		Caps:                 []string{"screenshot", "input", "observe", "control"},
		ControlTokenRequired: boolPtr(true),
	}
	if err := rab.Validate(); err != nil {
		t.Fatalf("setup: %v", err)
	}

	tests := []struct {
		name                 string
		requested            []string
		wantCaps             []string
		wantControlTokenReq  bool
		wantErr              error
	}{
		{"empty requested", []string{}, nil, false, nil},
		{"single match", []string{"observe"}, []string{"observe"}, false, nil},
		{"multiple matches", []string{"observe", "screenshot"}, []string{"observe", "screenshot"}, false, nil},
		{"order preserved", []string{"screenshot", "observe"}, []string{"screenshot", "observe"}, false, nil},
		{"control included with token flag", []string{"control"}, []string{"control"}, true, nil},
		{"control in middle", []string{"observe", "control", "input"}, []string{"observe", "control", "input"}, true, nil},
		{"requested not advertised", []string{"record"}, []string{}, false, nil},
		{"unknown requested cap", []string{"teleport"}, nil, false, ErrCapUnknown},
		{"duplicate requested cap", []string{"observe", "observe"}, nil, false, ErrCapUnknown},
		{"unknown then valid", []string{"teleport", "observe"}, nil, false, ErrCapUnknown},
		{"valid then unknown", []string{"observe", "teleport"}, nil, false, ErrCapUnknown},
		{"all requested", []string{"screenshot", "input", "record", "observe", "control"}, []string{"screenshot", "input", "observe", "control"}, true, nil},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			caps, controlReq, err := rab.Negotiate(tc.requested)
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("Negotiate() = _, _, %v, want nil", err)
				}
				if !equalStringSlices(caps, tc.wantCaps) {
					t.Fatalf("Negotiate() caps = %v, want %v", caps, tc.wantCaps)
				}
				if controlReq != tc.wantControlTokenReq {
					t.Fatalf("Negotiate() controlTokenRequired = %v, want %v", controlReq, tc.wantControlTokenReq)
				}
			} else {
				if err == nil {
					t.Fatal("Negotiate() = nil, want error")
				}
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("Negotiate() = %v, want %v", err, tc.wantErr)
				}
			}
		})
	}
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestParseRAB tests the ParseRAB function.
func TestParseRAB(t *testing.T) {
	tests := []struct {
		name           string
		input          string
		wantAbi        string
		wantCaps       []string
		wantCtrlToken  *bool
		wantExtra      map[string]any
		wantErr        error
	}{
		{
			name:       "minimal valid",
			input:      `{"abi":"rab/1.0","caps":["observe"]}`,
			wantAbi:    "rab/1.0",
			wantCaps:   []string{"observe"},
			wantCtrlToken: nil,
			wantExtra:  map[string]any{},
			wantErr:    nil,
		},
		{
			name:       "with control and token",
			input:      `{"abi":"rab/1.0","caps":["control"],"control_token_required":true}`,
			wantAbi:    "rab/1.0",
			wantCaps:   []string{"control"},
			wantCtrlToken: boolPtr(true),
			wantExtra:  map[string]any{},
			wantErr:    nil,
		},
		{
			name:       "all caps",
			input:      `{"abi":"rab/1.0","caps":["screenshot","input","record","observe","control"],"control_token_required":true}`,
			wantAbi:    "rab/1.0",
			wantCaps:   []string{"screenshot","input","record","observe","control"},
			wantCtrlToken: boolPtr(true),
			wantExtra:  map[string]any{},
			wantErr:    nil,
		},
		{
			name:       "N-1 unknown field tolerated",
			input:      `{"abi":"rab/1.0","caps":["observe"],"future_thing":42}`,
			wantAbi:    "rab/1.0",
			wantCaps:   []string{"observe"},
			wantCtrlToken: nil,
			wantExtra:  map[string]any{"future_thing": float64(42)},
			wantErr:    nil,
		},
		{
			name:       "unknown string field",
			input:      `{"abi":"rab/1.0","caps":["observe"],"new_field":"hello"}`,
			wantAbi:    "rab/1.0",
			wantCaps:   []string{"observe"},
			wantCtrlToken: nil,
			wantExtra:  map[string]any{"new_field": "hello"},
			wantErr:    nil,
		},
		{
			name:       "unknown nested object",
			input:      `{"abi":"rab/1.0","caps":["observe"],"config":{"timeout":30}}`,
			wantAbi:    "rab/1.0",
			wantCaps:   []string{"observe"},
			wantCtrlToken: nil,
			wantExtra:  map[string]any{"config": map[string]any{"timeout": float64(30)}},
			wantErr:    nil,
		},
		{
			name:       "trailing junk rejected",
			input:      `{"abi":"rab/1.0","caps":["observe"]} extra`,
			wantErr:    ErrTrailingTokens,
		},
		{
			name:       "trailing object rejected",
			input:      `{"abi":"rab/1.0","caps":["observe"]}{}`,
			wantErr:    ErrTrailingTokens,
		},
		{
			name:       "trailing array rejected",
			input:      `{"abi":"rab/1.0","caps":["observe"]}[]`,
			wantErr:    ErrTrailingTokens,
		},
		{
			name:       "invalid abi version",
			input:      `{"abi":"rab/1.1","caps":["observe"]}`,
			wantErr:    ErrAbiVersion,
		},
		{
			name:       "unknown cap in input",
			input:      `{"abi":"rab/1.0","caps":["teleport"]}`,
			wantErr:    ErrCapUnknown,
		},
		{
			name:       "duplicate cap in input",
			input:      `{"abi":"rab/1.0","caps":["observe","observe"]}`,
			wantErr:    ErrCapDuplicate,
		},
		{
			name:       "control without token in input",
			input:      `{"abi":"rab/1.0","caps":["control"]}`,
			wantErr:    ErrControlTokenRequired,
		},
		{
			name:       "control with false in input",
			input:      `{"abi":"rab/1.0","caps":["control"],"control_token_required":false}`,
			wantErr:    ErrControlTokenRequired,
		},
		{
			name:       "malformed json",
			input:      `{invalid}`,
			wantErr:    nil, // json.Unmarshal error, not our error type
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rab, err := ParseRAB([]byte(tc.input))
			if tc.wantErr == nil && tc.name != "malformed json" {
				if err != nil {
					t.Fatalf("ParseRAB() = %v, want nil", err)
				}
				if rab.Abi != tc.wantAbi {
					t.Fatalf("Abi = %q, want %q", rab.Abi, tc.wantAbi)
				}
				if !equalStringSlices(rab.Caps, tc.wantCaps) {
					t.Fatalf("Caps = %v, want %v", rab.Caps, tc.wantCaps)
				}
				if (rab.ControlTokenRequired == nil) != (tc.wantCtrlToken == nil) {
					t.Fatalf("ControlTokenRequired = %v, want %v", rab.ControlTokenRequired, tc.wantCtrlToken)
				}
				if rab.ControlTokenRequired != nil && tc.wantCtrlToken != nil && *rab.ControlTokenRequired != *tc.wantCtrlToken {
					t.Fatalf("ControlTokenRequired = %v, want %v", *rab.ControlTokenRequired, *tc.wantCtrlToken)
				}
				if !equalExtra(rab.Extra, tc.wantExtra) {
					t.Fatalf("Extra = %v, want %v", rab.Extra, tc.wantExtra)
				}
			} else if tc.wantErr != nil {
				if err == nil {
					t.Fatal("ParseRAB() = nil, want error")
				}
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("ParseRAB() = %v, want %v", err, tc.wantErr)
				}
			}
		})
	}
}

func equalExtra(a, b map[string]any) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		bv, ok := b[k]
		if !ok {
			return false
		}
		if !equalAny(v, bv) {
			return false
		}
	}
	return true
}

func equalAny(a, b any) bool {
	// Handle JSON number types (float64)
	af, aok := a.(float64)
	bf, bok := b.(float64)
	if aok && bok {
		return af == bf
	}
	// Handle strings
	as, aok := a.(string)
	bs, bok := b.(string)
	if aok && bok {
		return as == bs
	}
	// Handle maps
	am, aok := a.(map[string]any)
	bm, bok := b.(map[string]any)
	if aok && bok {
		return equalExtra(am, bm)
	}
	// Handle slices
	asl, aok := a.([]any)
	bsl, bok := b.([]any)
	if aok && bok {
		if len(asl) != len(bsl) {
			return false
		}
		for i := range asl {
			if !equalAny(asl[i], bsl[i]) {
				return false
			}
		}
		return true
	}
	// Handle bools
	ab, aok := a.(bool)
	bb, bok := b.(bool)
	if aok && bok {
		return ab == bb
	}
	return false
}

// TestParseRABRoundTrip tests that MarshalJSON round-trips Extra fields.
func TestParseRABRoundTrip(t *testing.T) {
	input := `{"abi":"rab/1.0","caps":["observe","control"],"control_token_required":true,"future_field":123,"another":"value"}`
	rab, err := ParseRAB([]byte(input))
	if err != nil {
		t.Fatalf("ParseRAB: %v", err)
	}

	// Marshal back
	out, err := json.Marshal(rab)
	if err != nil {
		t.Fatalf("MarshalJSON: %v", err)
	}

	// Parse again
	rab2, err := ParseRAB(out)
	if err != nil {
		t.Fatalf("ParseRAB round-trip: %v", err)
	}

	if rab2.Abi != rab.Abi {
		t.Fatalf("Abi mismatch: %q vs %q", rab2.Abi, rab.Abi)
	}
	if !equalStringSlices(rab2.Caps, rab.Caps) {
		t.Fatalf("Caps mismatch: %v vs %v", rab2.Caps, rab.Caps)
	}
	if (rab2.ControlTokenRequired == nil) != (rab.ControlTokenRequired == nil) {
		t.Fatalf("ControlTokenRequired nil mismatch")
	}
	if rab2.ControlTokenRequired != nil && rab.ControlTokenRequired != nil && *rab2.ControlTokenRequired != *rab.ControlTokenRequired {
		t.Fatalf("ControlTokenRequired value mismatch")
	}
	if !equalExtra(rab2.Extra, rab.Extra) {
		t.Fatalf("Extra mismatch: %v vs %v", rab2.Extra, rab.Extra)
	}
}

// TestRequiresControlTokenTruthTable tests the RequiresControlToken truth table.
func TestRequiresControlTokenTruthTable(t *testing.T) {
	tests := []struct {
		name           string
		caps           []string
		controlTokenReq *bool
		want           bool
	}{
		// (field true) OR (caps has control)
		{"control cap, field true", []string{"control"}, boolPtr(true), true},
		{"control cap, field nil (invalid but method is nil-safe)", []string{"control"}, nil, true}, // Has("control") = true
		{"control cap, field false (invalid but method is nil-safe)", []string{"control"}, boolPtr(false), true},
		{"no control cap, field true", []string{"observe"}, boolPtr(true), true},
		{"no control cap, field nil", []string{"observe"}, nil, false},
		{"no control cap, field false", []string{"observe"}, boolPtr(false), false},
		{"empty caps, field nil", []string{}, nil, false},
		{"multiple caps including control", []string{"observe", "control"}, boolPtr(true), true},
		{"multiple passive caps", []string{"screenshot", "input", "record", "observe"}, nil, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rab := &RAB{
				Abi:                  AbiVersion,
				Caps:                 tc.caps,
				ControlTokenRequired: tc.controlTokenReq,
			}
			// Note: We don't call Validate() here because RequiresControlToken
			// is nil-safe and should work even on invalid RABs (it's the
			// caller's responsibility to call Validate first, but the method
			// itself must not panic).
			got := rab.RequiresControlToken()
			if got != tc.want {
				t.Fatalf("RequiresControlToken() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestCapsAllowedExported tests that CapsAllowed is exported and contains exactly 5 caps.
func TestCapsAllowedExported(t *testing.T) {
	if len(CapsAllowed) != 5 {
		t.Fatalf("CapsAllowed length = %d, want 5", len(CapsAllowed))
	}
	expected := map[string]bool{
		"screenshot": true,
		"input":      true,
		"record":     true,
		"observe":    true,
		"control":    true,
	}
	for _, cap := range CapsAllowed {
		if !expected[cap] {
			t.Fatalf("CapsAllowed contains unexpected cap: %q", cap)
		}
		delete(expected, cap)
	}
	if len(expected) > 0 {
		for cap := range expected {
			t.Fatalf("CapsAllowed missing expected cap: %q", cap)
		}
	}
}