// Package abi defines the RAB/1.0 Runtime ABI capability advertisement law
// (the lower half of the CPI/RAB split per ADR-0012/0014).
//
// RAB is the capability advertisement a runtime (browser RT, code RT, desktop RT)
// publishes into the registry — WHAT it can do, not WHO owns it.
//
// RAB laws (frozen):
//   1. The 5 caps are the closed universe — a runtime cannot advertise a 6th.
//   2. Any runtime that advertises 'control' MUST require a control token
//      (control_token_required is const true in the schema: a RAB object that
//      omits it is legal schema-wise, a RAB with control-capable semantics
//      that omits it is ILLEGAL in the kernel view — Validate() closes that gap).
//   3. The abi version string is frozen at rab/1.0 — forward-compat means
//      unknown FIELDS tolerated (N-1 read per proto.charter/1.0 ADR-0021),
//      unknown ABI VERSION rejected fail-closed.
//   4. Caps order-insensitive, duplicates are a law violation (schema uniqueItems).
package abi

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

// AbiVersion is the frozen RAB ABI version string.
const AbiVersion = "rab/1.0"

// CapsAllowed is the closed enum of capability names (frozen per contract:rab/1.0).
var CapsAllowed = []string{
	"screenshot",
	"input",
	"record",
	"observe",
	"control",
}

// capSet is the fast lookup for CapsAllowed.
var capSet = map[string]bool{
	"screenshot": true,
	"input":      true,
	"record":     true,
	"observe":    true,
	"control":    true,
}

// Err* are deterministic, RAB-specific errors.
var (
	// ErrAbiVersion is returned when the abi field is not exactly "rab/1.0".
	// Fail-closed: unknown versions are rejected, never silently downgraded.
	ErrAbiVersion = errors.New("rab: abi version must be \"rab/1.0\" (fail-closed)")

	// ErrCapUnknown is returned when a capability is not in the frozen CapsAllowed enum.
	// Fail-closed: unknown caps are rejected, never silently dropped.
	ErrCapUnknown = errors.New("rab: unknown capability (fail-closed)")

	// ErrCapDuplicate is returned when caps contains duplicate entries.
	// Mirrors the schema uniqueItems constraint.
	ErrCapDuplicate = errors.New("rab: duplicate capability (schema uniqueItems)")

	// ErrControlTokenRequired is returned when a RAB advertises the "control"
	// capability but ControlTokenRequired is nil or *false.
	// This is the law the schema cannot express — the schema only says
	// control_token_required MUST be const true WHEN PRESENT, but a RAB
	// advertising "control" without the field is schema-valid yet ILLEGAL
	// in the kernel view. Validate() is the teeth that close this gap.
	ErrControlTokenRequired = errors.New("rab: control capability requires control_token_required=true")

	// ErrTrailingTokens is returned when JSON input contains trailing tokens
	// after the RAB object.
	ErrTrailingTokens = errors.New("rab: trailing tokens after JSON value")
)

// RAB is the Runtime ABI capability advertisement (contract:rab/1.0).
//
// Extra provides N-1 unknown-field tolerance (proto.charter/1.0 ADR-0021):
// unknown top-level fields round-trip and never fail validation.
//
// ControlTokenRequired semantics:
//   - nil or *false: field absent or explicitly false in JSON
//   - *true: field present and true in JSON
//   - The schema requires const true WHEN PRESENT; Validate() enforces
//     that caps containing "control" => ControlTokenRequired must be *true.
type RAB struct {
	Abi                  string         `json:"abi"`
	Caps                 []string       `json:"caps"`
	ControlTokenRequired *bool          `json:"control_token_required,omitempty"`
	Extra                map[string]any `json:"-"`
}

// Validate checks the RAB against the frozen contract:rab/1.0 law table.
//
// Law table:
//   - Abi != "rab/1.0"              => ErrAbiVersion (fail-closed)
//   - unknown cap in Caps           => ErrCapUnknown (fail-closed)
//   - duplicate cap in Caps         => ErrCapDuplicate (schema uniqueItems)
//   - empty Caps                    => LEGAL (a runtime may advertise zero caps;
//                                     it exists but does nothing)
//   - caps contains "control" AND
//     (ControlTokenRequired==nil || *ControlTokenRequired==false)
//                                   => ErrControlTokenRequired (the law the
//                                     schema cannot express — Validate's teeth)
//   - ControlTokenRequired non-nil and *true => always legal
//
// Control-only law: a RAB advertising screenshot|input|record|observe
// WITHOUT control_token_required=true is FINE. They are passive or
// token-free per ADR-0025 T1/T2 tiers; 'control' is the only privileged
// tier needing a token. This mapping is documented here so callers don't
// over-gate.
func (r *RAB) Validate() error {
	if r.Abi != AbiVersion {
		return fmt.Errorf("%w: got %q", ErrAbiVersion, r.Abi)
	}

	seen := make(map[string]bool, len(r.Caps))
	for _, cap := range r.Caps {
		if !capSet[cap] {
			return fmt.Errorf("%w: %q", ErrCapUnknown, cap)
		}
		if seen[cap] {
			return fmt.Errorf("%w: %q", ErrCapDuplicate, cap)
		}
		seen[cap] = true
	}

	// Empty caps is legal — a runtime may advertise zero capabilities.
	// It exists but does nothing.

	// Control token law: "control" cap => ControlTokenRequired must be *true.
	hasControl := seen["control"]
	if hasControl {
		if r.ControlTokenRequired == nil || !*r.ControlTokenRequired {
			return ErrControlTokenRequired
		}
	}

	// Non-nil + *true is always legal (validated above for control case).
	// ControlTokenRequired==nil or *false with NO "control" cap is legal.

	return nil
}

// Has reports whether the RAB advertises the given capability.
// This is the ONLY capability check callers should use.
func (r *RAB) Has(cap string) bool {
	for _, c := range r.Caps {
		if c == cap {
			return true
		}
	}
	return false
}

// Negotiate returns the intersection of requested capabilities with the
// RAB's advertised capabilities, preserving requested order.
//
// Law:
//   - Unknown requested cap => ErrCapUnknown (fail-closed, not silently dropped;
//     the CPN negotiation precedent).
//   - Duplicate requested cap => ErrCapUnknown (fail-closed).
//   - If "control" is in both requested and advertised, it is included in the
//     result AND the second return value (controlTokenRequired) is true.
//     This encodes the law: callers must gate control operations with this flag.
//
// Returns: (negotiatedCaps, controlTokenRequired, error)
func (r *RAB) Negotiate(requested []string) ([]string, bool, error) {
	if len(requested) == 0 {
		return nil, false, nil
	}

	requestedSeen := make(map[string]bool, len(requested))
	result := make([]string, 0, len(requested))
	controlTokenRequired := false

	for _, cap := range requested {
		// Unknown requested cap => fail-closed (CPN negotiation precedent).
		if !capSet[cap] {
			return nil, false, fmt.Errorf("%w: requested %q", ErrCapUnknown, cap)
		}

		// Duplicate requested cap => fail-closed.
		if requestedSeen[cap] {
			return nil, false, fmt.Errorf("%w: duplicate requested %q", ErrCapUnknown, cap)
		}
		requestedSeen[cap] = true

		// Intersection: only include if RAB advertises it.
		if r.Has(cap) {
			result = append(result, cap)
			if cap == "control" {
				controlTokenRequired = true
			}
		}
	}

	return result, controlTokenRequired, nil
}

// ParseRAB decodes a RAB from JSON with strict trailing-token rejection,
// then validates it.
//
// - Uses json.Decoder to ensure single-value JSON (rejects trailing tokens).
// - Extra unknown top-level fields are captured in Extra and round-trip on Marshal.
// - Returns validation error if the decoded RAB violates contract:rab/1.0.
func ParseRAB(b []byte) (*RAB, error) {
	// Pass 1: decode into a map to capture all fields including unknown ones.
	var raw map[string]json.RawMessage
	dec1 := json.NewDecoder(bytes.NewReader(b))
	if err := dec1.Decode(&raw); err != nil {
		return nil, err
	}

	// Pass 2: check for trailing tokens using a fresh decoder.
	// After the first Decode consumes the JSON value, any remaining non-whitespace
	// content (whether valid JSON or garbage) is a trailing-token violation.
	dec2 := json.NewDecoder(bytes.NewReader(b))
	var dummy any
	if err := dec2.Decode(&dummy); err != nil {
		return nil, err
	}
	// Try to decode a second value. If it returns nil, there's a second JSON value.
	// If it returns a non-EOF error, there's trailing garbage. Both are violations.
	if err := dec2.Decode(&dummy); err != nil {
		if err.Error() != "EOF" {
			return nil, ErrTrailingTokens
		}
	} else {
		return nil, ErrTrailingTokens
	}

	// Now build the RAB from raw map
	rab := &RAB{
		Extra: make(map[string]any),
	}

	// Extract known fields
	if v, ok := raw["abi"]; ok {
		if err := json.Unmarshal(v, &rab.Abi); err != nil {
			return nil, fmt.Errorf("abi field: %w", err)
		}
	}

	if v, ok := raw["caps"]; ok {
		if err := json.Unmarshal(v, &rab.Caps); err != nil {
			return nil, fmt.Errorf("caps field: %w", err)
		}
	}

	if v, ok := raw["control_token_required"]; ok {
		var val bool
		if err := json.Unmarshal(v, &val); err != nil {
			return nil, fmt.Errorf("control_token_required field: %w", err)
		}
		rab.ControlTokenRequired = &val
	}

	// Everything else goes to Extra
	for k, v := range raw {
		switch k {
		case "abi", "caps", "control_token_required":
			continue
		default:
			var val any
			if err := json.Unmarshal(v, &val); err != nil {
				return nil, fmt.Errorf("extra field %q: %w", k, err)
			}
			rab.Extra[k] = val
		}
	}

	// Validate the constructed RAB
	if err := rab.Validate(); err != nil {
		return nil, err
	}

	return rab, nil
}

// MarshalJSON implements json.Marshaler to round-trip Extra fields.
func (r *RAB) MarshalJSON() ([]byte, error) {
	// Build a map with all fields
	m := make(map[string]any, 3+len(r.Extra))
	m["abi"] = r.Abi
	m["caps"] = r.Caps
	if r.ControlTokenRequired != nil {
		m["control_token_required"] = *r.ControlTokenRequired
	}
	for k, v := range r.Extra {
		m[k] = v
	}
	return json.Marshal(m)
}

// RequiresControlToken returns true if control operations on this RAB
// require a control token.
//
// Law (nil-safe):
//   - Returns (field is *true) OR (caps contains "control").
//   - The FIELD defaults to false (nil or *false), but Validate() rejects
//     the combination of caps["control"] + field!=*true.
//   - So after Validate() passes, RequiresControlToken() == true
//     exactly when caps has "control" (since field must be *true).
//   - Callers gate control ops with this single method.
func (r *RAB) RequiresControlToken() bool {
	if r.ControlTokenRequired != nil && *r.ControlTokenRequired {
		return true
	}
	return r.Has("control")
}