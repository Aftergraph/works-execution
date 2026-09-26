package protocol

import "strings"

// RecoveryCauseHeader is carried only on retry/reconnect submissions. The
// value is deliberately low-cardinality so it is safe to persist in audit
// records and aggregate into SLOs.
const RecoveryCauseHeader = "X-Works-Recovery-Cause"

const (
	RecoveryCauseAmbiguousTransport     = "ambiguous_transport"
	RecoveryCauseAmbiguousResponseRead  = "ambiguous_response_read"
	RecoveryCauseTransientStatus        = "transient_status"
	RecoveryCauseAuthRenewal            = "auth_renewal"
	RecoveryCauseControllerReconnect    = "controller_reconnect"
)

var allowedRecoveryCauses = map[string]struct{}{
	RecoveryCauseAmbiguousTransport:    {},
	RecoveryCauseAmbiguousResponseRead: {},
	RecoveryCauseTransientStatus:       {},
	RecoveryCauseAuthRenewal:           {},
	RecoveryCauseControllerReconnect:   {},
}

// NormalizeRecoveryCause returns a known low-cardinality cause or "".
// Unknown caller-provided values are intentionally discarded.
func NormalizeRecoveryCause(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	if _, ok := allowedRecoveryCauses[v]; ok {
		return v
	}
	return ""
}

func IsAmbiguousAckCause(v string) bool {
	v = NormalizeRecoveryCause(v)
	return v == RecoveryCauseAmbiguousTransport || v == RecoveryCauseAmbiguousResponseRead
}
