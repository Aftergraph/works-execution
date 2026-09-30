package api

import (
	"context"
	"net/http"

	"github.com/JonasAbde/works-execution/internal/identity"
)

type workerIdentityContextKey struct{}

func workerIdentityFrom(ctx context.Context) string {
	workerID, _ := ctx.Value(workerIdentityContextKey{}).(string)
	return workerID
}

// requireWorkerMTLS requires a certificate that the HTTP server has verified
// against its configured client CA, then extracts the canonical worker URI SAN.
func (s *Server) requireWorkerMTLS(next http.Handler) http.Handler {
	if !s.RequireWorkerMTLS {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 || len(r.TLS.VerifiedChains) == 0 {
			writeError(w, http.StatusUnauthorized, "worker_certificate_required", "a verified WORKS worker client certificate is required")
			return
		}
		workerID, err := identity.WorkerIDFromCertificate(r.TLS.PeerCertificates[0])
		if err != nil {
			writeError(w, http.StatusForbidden, "worker_certificate_identity_invalid", err.Error())
			return
		}
		ctx := context.WithValue(r.Context(), workerIdentityContextKey{}, workerID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// workerEndpoint composes verified transport identity with bearer auth and
// rejects any certificate/token worker mismatch before a handler can mutate
// state. In auth-disabled local test mode, the certificate remains required
// when RequireWorkerMTLS is set, but there is no bearer claim to compare.
func (s *Server) workerEndpoint(next http.Handler) http.Handler {
	bound := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.RequireWorkerMTLS && s.AuthEnabled {
			claims := ClaimsFrom(r.Context())
			certWorkerID := workerIdentityFrom(r.Context())
			if claims == nil || certWorkerID == "" || claims.WorkerID != certWorkerID {
				writeError(w, http.StatusForbidden, "worker_identity_mismatch", "bearer worker_id must match the verified client certificate")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
	return s.requireWorkerMTLS(s.requireBearer(bound))
}
