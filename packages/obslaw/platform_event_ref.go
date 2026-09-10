package obslaw

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// WorkEvent mirrors the frozen events/1.0 envelope at the adapter boundary.
// It remains observability data, not evidence.
type WorkEvent struct {
	Source     string `json:"source"`
	Seq        int64  `json:"seq"`
	Type       string `json:"type"`
	Subject    string `json:"subject"`
	PayloadRef string `json:"payload_ref"`
	Timestamp  string `json:"ts"`
	Version    string `json:"version"`
}

// PlatformCorrelation is the correlation/1.0-compatible subset required by
// platform-event-ref/0.1. Possessing this structure never grants authority.
type PlatformCorrelation struct {
	ExecutionContextID  string `json:"execution_context_id,omitempty"`
	TenantID            string `json:"tenant_id"`
	PrincipalID         string `json:"principal_id,omitempty"`
	MissionID           string `json:"mission_id"`
	AuthorityLeaseID    string `json:"authority_lease_id,omitempty"`
	WorkID              string `json:"work_id,omitempty"`
	AdmissionDecisionID string `json:"admission_decision_id,omitempty"`
	TraceID             string `json:"trace_id"`
	ActionID            string `json:"action_id"`
}

// PlatformEventRef is an experimental cross-repo projection. IntegrityRef is
// a content digest of the native WORKS event envelope, not an evidence
// attestation and not authorization.
type PlatformEventRef struct {
	Schema         string              `json:"schema"`
	EventID        string              `json:"event_id"`
	Source         string              `json:"source"`
	EventType      string              `json:"event_type"`
	OccurredAt     string              `json:"occurred_at"`
	SubjectRef     string              `json:"subject_ref"`
	Correlation    PlatformCorrelation `json:"correlation"`
	PayloadRef     string              `json:"payload_ref"`
	IntegrityRef   string              `json:"integrity_ref"`
	Classification string              `json:"classification"`
}

var canonicalIDs = map[string]*regexp.Regexp{
	"tenant_id":             regexp.MustCompile(`^ten_[a-f0-9]{32}$`),
	"principal_id":          regexp.MustCompile(`^prn_[a-f0-9]{32}$`),
	"execution_context_id":  regexp.MustCompile(`^ctx_[a-f0-9]{32}$`),
	"authority_lease_id":    regexp.MustCompile(`^auth_[a-f0-9]{32}$`),
	"work_id":               regexp.MustCompile(`^wrk_[a-f0-9]{32}$`),
	"admission_decision_id": regexp.MustCompile(`^pdr_[a-f0-9]{32}$`),
	"trace_id":              regexp.MustCompile(`^trc_[a-f0-9]{32}$`),
	"action_id":             regexp.MustCompile(`^act_[a-f0-9]{32}$`),
}

func validCanonicalID(name, value string, required bool) error {
	if value == "" && !required {
		return nil
	}
	if value == "" {
		return fmt.Errorf("%s is required", name)
	}
	rx, ok := canonicalIDs[name]
	if !ok || !rx.MatchString(value) {
		return fmt.Errorf("invalid %s", name)
	}
	return nil
}

func validateCorrelation(c PlatformCorrelation) error {
	if err := validCanonicalID("tenant_id", c.TenantID, true); err != nil {
		return err
	}
	if c.MissionID == "" || len(c.MissionID) > 256 {
		return errors.New("invalid mission_id")
	}
	if err := validCanonicalID("trace_id", c.TraceID, true); err != nil {
		return err
	}
	if err := validCanonicalID("action_id", c.ActionID, true); err != nil {
		return err
	}
	for _, optional := range []struct {
		name  string
		value string
	}{
		{"execution_context_id", c.ExecutionContextID},
		{"principal_id", c.PrincipalID},
		{"authority_lease_id", c.AuthorityLeaseID},
		{"work_id", c.WorkID},
		{"admission_decision_id", c.AdmissionDecisionID},
	} {
		if err := validCanonicalID(optional.name, optional.value, false); err != nil {
			return err
		}
	}
	return nil
}

func validateWorkEvent(event WorkEvent) (time.Time, error) {
	if event.Source != "works-org" && event.Source != "pulse-device" {
		return time.Time{}, errors.New("invalid WORKS event source")
	}
	if event.Seq < 1 {
		return time.Time{}, errors.New("invalid WORKS event seq")
	}
	if event.Type == "" || event.Subject == "" || event.Version == "" {
		return time.Time{}, errors.New("WORKS event type, subject, and version are required")
	}
	parsed, err := time.Parse(time.RFC3339Nano, event.Timestamp)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid WORKS event timestamp: %w", err)
	}
	return parsed, nil
}

func nativeEventDigest(event WorkEvent) string {
	// Length-prefixed framing: a bare NUL join is ambiguous when a
	// field itself contains NUL ("a\x00b"+"c" vs "a"+"b\x00c" collide).
	// Byte-length prefixes make the digest injective over field tuples.
	var material strings.Builder
	for _, field := range []string{
		event.Source,
		fmt.Sprintf("%d", event.Seq),
		event.Type,
		event.Subject,
		event.PayloadRef,
		event.Timestamp,
		event.Version,
	} {
		fmt.Fprintf(&material, "%d\x00%s", len(field), field)
	}
	digest := sha256.Sum256([]byte(material.String()))
	return hex.EncodeToString(digest[:])
}

// ProjectPlatformEventRef produces a correlation-only reference while keeping
// the native WORKS events/1.0 envelope authoritative. It performs no evidence
// upgrade and no authorization decision.
func ProjectPlatformEventRef(event WorkEvent, correlation PlatformCorrelation) (PlatformEventRef, error) {
	occurredAt, err := validateWorkEvent(event)
	if err != nil {
		return PlatformEventRef{}, err
	}
	if err := validateCorrelation(correlation); err != nil {
		return PlatformEventRef{}, err
	}

	digest := nativeEventDigest(event)
	payloadRef := event.PayloadRef
	if payloadRef == "" {
		payloadRef = fmt.Sprintf("works:event:%s:%d", event.Source, event.Seq)
	}

	return PlatformEventRef{
		Schema:         "platform-event-ref/0.1",
		EventID:        "evt_" + digest[:32],
		Source:         "works-execution",
		EventType:      event.Type,
		OccurredAt:     occurredAt.UTC().Format(time.RFC3339Nano),
		SubjectRef:     event.Subject,
		Correlation:    correlation,
		PayloadRef:     payloadRef,
		IntegrityRef:   "sha256:" + digest,
		Classification: "execution",
	}, nil
}
