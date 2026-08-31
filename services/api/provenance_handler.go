// Provenance handler — GET /v1/works/{id}/provenance.

package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/JonasAbde/works-execution/services/provenance"
	"github.com/JonasAbde/works-execution/services/work/store"
)

// ProvenanceResponse is the wire shape returned by
// GET /v1/works/{id}/provenance. It bundles the canonical attestation
// envelope, the signature, and provenance metadata so consumers can
// verify integrity without a second round-trip to the control plane.
//
// Attestation is exposed as a json.RawMessage so it round-trips as a
// nested JSON object rather than a base64-encoded []byte — the envelope
// IS JSON, so consumers expect to see JSON.
type ProvenanceResponse struct {
	WorkID      string          `json:"work_id"`
	Attestation json.RawMessage `json:"attestation"`
	Signature   string          `json:"signature"`
	KeyID       string          `json:"key_id"`
	BuilderID   string          `json:"builder_id"`
	ProducedAt  string          `json:"produced_at"`
}

// workProvenanceHandler implements GET /v1/works/{id}/provenance.
//
// It produces (or replays) a workflow-provenance attestation for the Work
// following docs/standards/schemas/workflow-provenance.schema.json. If the
// Work has not been attested yet and has reached a terminal state, the
// producer is invoked inline; otherwise we return whatever has been
// previously persisted.
//
// Errors:
//   400 — missing id
//   404 — work not found
//   405 — non-GET method
//   503 — ProvenanceConfig not configured
//   500 — store / signer failure
func (s *Server) workProvenanceHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", r.Method)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/v1/works/")
	parts := strings.Split(path, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] != "provenance" {
		writeError(w, http.StatusBadRequest, "missing_id", "work id required")
		return
	}
	workID := parts[0]

	if s.ProvenanceConfig == nil {
		writeError(w, http.StatusServiceUnavailable, "provenance_unavailable", "provenance producer not configured")
		return
	}

	signer, err := provenance.NewSigner(s.ProvenanceConfig.HMACKey, s.ProvenanceConfig.KeyID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "signer_init_failed", err.Error())
		return
	}
	prod := provenance.New(signer)

	// First: try to serve an existing attestation. This is the fast path
	// for repeated reads and for verifiers that want idempotency.
	adapter := &provenance.StoreAdapter{Inner: s.Store}
	if existing, gerr := adapter.GetProvenance(r.Context(), workID); gerr == nil && existing != nil {
		writeProvenanceJSON(w, existing)
		return
	}

	// Otherwise produce one. Produce enforces all invariants (terminal
	// state check, attempts present, etc.) — let it decide.
	row, perr := prod.Produce(r.Context(), adapter, workID)
	if perr != nil {
		switch {
		case errors.Is(perr, provenance.ErrNoWork), errors.Is(perr, store.ErrNotFound):
			writeError(w, http.StatusNotFound, "not_found", workID)
		case errors.Is(perr, provenance.ErrAttestationInvalid):
			writeError(w, http.StatusConflict, "attestation_invalid", perr.Error())
		case errors.Is(perr, provenance.ErrWorkNotTerminal):
			writeError(w, http.StatusConflict, "work_not_terminal", perr.Error())
		default:
			s.logf("provenance produce: %v", perr)
			writeError(w, http.StatusInternalServerError, "provenance_failed", perr.Error())
		}
		return
	}
	writeProvenanceJSON(w, row)
}

func writeProvenanceJSON(w http.ResponseWriter, row *provenance.ProvenanceRow) {
	resp := ProvenanceResponse{
		WorkID:      row.WorkID,
		Attestation: json.RawMessage(row.Attestation),
		Signature:   string(row.Signature),
		KeyID:       row.KeyID,
		BuilderID:   row.BuilderID,
		ProducedAt:  row.ProducedAt.UTC().Format("2006-01-02T15:04:05.000000000Z07:00"),
	}
	if resp.ProducedAt == "" {
		// Belt-and-braces: row.ProducedAt was zero, fall back to a
		// best-effort label so the response is always well-formed.
		resp.ProducedAt = fmt.Sprintf("%v", row.ProducedAt)
	}
	writeJSON(w, http.StatusOK, resp)
}