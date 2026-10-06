package api

import "net/http"

// createPlatformWorkV2 is the server-to-server Work creation seam for
// governed Runtime placement. It deliberately reuses createWork so WORKS
// remains the only owner of Work identity, validation, admission enrichment,
// idempotency reconciliation, and durable creation truth.
//
// Unlike /v1/works, callers authenticate as the platform bridge rather than
// impersonating a worker enrollment token.
func (s *Server) createPlatformWorkV2(w http.ResponseWriter, r *http.Request) {
	if !s.authorizeDispatchV2Platform(w, r) {
		return
	}
	s.createWork(w, r)
}
