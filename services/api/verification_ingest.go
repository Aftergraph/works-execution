package api

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/JonasAbde/works-execution/services/work/store"
)

const verifierTokenHeader = "X-WORKS-Verifier-Token"
const maxVerificationBody = 64 << 10

var domainReceiptID = regexp.MustCompile(`^dvr_[a-f0-9]{64}$`)

type verificationIngestBody struct {
	Result      string `json:"result"`
	VerifierID  string `json:"verifier_id"`
	EvidenceRef string `json:"evidence_ref"`
	VerifiedAt  string `json:"verified_at"`
}

func (s *Server) verifierTokenConfigured() bool { return len(s.VerifierToken) >= 32 }

func (s *Server) verifierTokenOK(r *http.Request) bool {
	if !s.verifierTokenConfigured() {
		return false
	}
	got := r.Header.Get(verifierTokenHeader)
	return got != "" && subtle.ConstantTimeCompare([]byte(got), s.VerifierToken) == 1
}

func decodeVerificationBody(w http.ResponseWriter, r *http.Request) (verificationIngestBody, error) {
	var body verificationIngestBody
	r.Body = http.MaxBytesReader(w, r.Body, maxVerificationBody)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		return body, err
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return body, errors.New("multiple_json_values")
	}
	return body, nil
}

func validateVerificationBody(body verificationIngestBody) (time.Time, bool) {
	if body.Result != "passed" && body.Result != "failed" {
		return time.Time{}, false
	}
	if !strings.HasPrefix(body.VerifierID, "sentinel:") || strings.TrimSpace(body.VerifierID) != body.VerifierID {
		return time.Time{}, false
	}
	if !domainReceiptID.MatchString(body.EvidenceRef) {
		return time.Time{}, false
	}
	at, err := time.Parse(time.RFC3339Nano, body.VerifiedAt)
	if err != nil || at.IsZero() {
		return time.Time{}, false
	}
	return at.UTC(), true
}
func (s *Server) workVerificationIngestHandler(w http.ResponseWriter, r *http.Request) {
	if !s.verifierTokenConfigured() {
		writeError(w, http.StatusServiceUnavailable, "verification_ingest_unavailable", "verification ingest not configured")
		return
	}
	if !s.verifierTokenOK(r) {
		writeError(w, http.StatusUnauthorized, "verification_token_invalid", "verification credential invalid")
		return
	}
	workID := r.PathValue("id")
	if workID == "" {
		writeError(w, http.StatusBadRequest, "missing_id", "work id required")
		return
	}
	work, err := s.Store.GetWork(r.Context(), workID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", workID)
			return
		}
		writeError(w, http.StatusInternalServerError, "verification_lookup_failed", "verification lookup failed")
		return
	}
	body, err := decodeVerificationBody(w, r)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeError(w, http.StatusRequestEntityTooLarge, "verification_too_large", "verification record too large")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid_verification", "invalid verification record")
		return
	}
	verifiedAt, ok := validateVerificationBody(body)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid_verification", "invalid verification record")
		return
	}
	if !work.State.IsTerminal() {
		writeError(w, http.StatusConflict, "verification_work_not_terminal", "verification requires terminal work")
		return
	}
	verdict := store.VerificationVerdict{WorkID: workID, Result: body.Result, VerifierID: body.VerifierID, EvidenceRef: body.EvidenceRef, VerifiedAt: verifiedAt}
	if err := s.Store.SaveVerificationVerdict(r.Context(), verdict); err != nil {
		if errors.Is(err, store.ErrVerificationVerdictConflict) {
			writeError(w, http.StatusConflict, "verification_conflict", "conflicting immutable verification record")
			return
		}
		writeError(w, http.StatusInternalServerError, "verification_store_failed", "verification persistence failed")
		return
	}
	writeJSON(w, http.StatusOK, verdict)
}
