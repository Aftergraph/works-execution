package api

import "net/http"

// Platform runner snapshot endpoints are the read-only Runtime placement seam.
// They deliberately do not reuse worker JWT authentication: Runtime is a
// platform scheduler, not a worker. The same platform bearer + bridge secret
// used by Work creation/reservation authenticates these reads.
//
// No mutation is exposed here. Runner registration and ABI publication remain
// worker-owned /v1 surfaces.
func (s *Server) listPlatformRunnersV2(w http.ResponseWriter, r *http.Request) {
	if !s.authorizeDispatchV2Platform(w, r) {
		return
	}
	s.listRunners(w, r)
}

func (s *Server) getPlatformRunnerABIV2(w http.ResponseWriter, r *http.Request) {
	if !s.authorizeDispatchV2Platform(w, r) {
		return
	}
	s.getRunnerABI(w, r)
}
