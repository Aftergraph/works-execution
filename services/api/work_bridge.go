package api

// Task 8 (docs/superpowers/plans/2026-09-01-works-conversation-v1.md):
// platform-bridge-bound checkpoint surface for the Conversation V1 flow.
//
// The conversation worker (AVC) needs two WORKS endpoints that the resume
// route alone never provided:
//
//   POST /v1/works/{id}/suspend — moves a mission Work into WAITING_HUMAN
//       (or SUSPENDED) and persists its ADR-0010 handoff checkpoint in the
//       same transaction, emitting the work.waiting_human journal event.
//   GET  /v1/works/{id}/handoff — read-only checkpoint binding: the exact
//       persisted handoff payload hash the resume route validates, so the
//       platform bridge can bind an approval receipt to the CURRENT
//       checkpoint without ever guessing a hash.
//
// Same boundary as POST /v1/works/{id}/resume (fail-closed):
//   - no bridge secret wired (or shorter than 32 bytes) -> 503, always
//   - wrong/missing bridge header                   -> 401
//   - unknown work                                   -> 404 (no leak)
//   - suspend: missing/invalid handoff               -> 400
//   - suspend: non-mission work or illegal transition -> 409
//   - handoff: work has no persisted checkpoint      -> 409
//
// Both routes also sit behind requireBearer (defense in depth, matching the
// resume route: the platform bridge presents its secret, regular bearer
// authentication still applies on top).
import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/JonasAbde/works-execution/packages/workgraph"
	"github.com/JonasAbde/works-execution/services/work/store"
)

// wireCheckpointRoutes mounts the suspend + handoff endpoints on the given
// registrar with the platform bridge secret. An empty (or too-short) secret
// keeps the routes mounted but fail-closed (503 on every request), matching
// WireResumeRoutes semantics.
func WireCheckpointRoutes(reg RouteRegistrar, s *Server, bridgeSecret string) {
	bridgeOK := func(r *http.Request) bool {
		if !BridgeSecretConfigured(bridgeSecret) {
			return false
		}
		got := r.Header.Get("X-Works-Platform-Bridge")
		return got != "" && subtle.ConstantTimeCompare([]byte(got), []byte(bridgeSecret)) == 1
	}
	writeBridgeClosed := func(w http.ResponseWriter) {
		// Never reveal whether the secret is unwired vs wrong: both are 401
		// unless the route itself is not configured (503 then, loud).
		if !BridgeSecretConfigured(bridgeSecret) {
			writeError(w, http.StatusServiceUnavailable, "bridge_unavailable", "platform bridge not configured")
			return
		}
		writeError(w, http.StatusUnauthorized, "bridge_unauthorized", "missing or invalid platform bridge header")
	}

	suspender := &bridgeSuspendHandler{store: bridgeStoreAdapter{s.Store}, bridgeOK: bridgeOK, bridgeClosed: writeBridgeClosed}
	handoffer := &bridgeHandoffHandler{store: bridgeStoreAdapter{s.Store}, bridgeOK: bridgeOK, bridgeClosed: writeBridgeClosed}

	reg.Handle("POST /v1/works/{id}/suspend", s.requireBearer(suspender))
	reg.Handle("GET /v1/works/{id}/handoff", s.requireBearer(handoffer))
}

// bridgeStoreAdapter adapts the narrow store.Store interface to the
// checkpoint endpoints, type-asserting the concrete capabilities exactly
// like the resume route does (additive; fails loud when absent).
type bridgeStoreAdapter struct{ st store.Store }

func (a bridgeStoreAdapter) SuspendWorkEventful(ctx context.Context, id string, to workgraph.State, h *workgraph.Handoff) (*workgraph.Work, error) {
	type suspender interface {
		SuspendWorkEventful(ctx context.Context, id string, to workgraph.State, h *workgraph.Handoff) (*workgraph.Work, error)
	}
	s, ok := a.st.(suspender)
	if !ok {
		return nil, errors.New("bridge: store does not implement SuspendWorkEventful")
	}
	return s.SuspendWorkEventful(ctx, id, to, h)
}

func (a bridgeStoreAdapter) GetWork(ctx context.Context, id string) (*workgraph.Work, error) {
	return a.st.GetWork(ctx, id)
}

func (a bridgeStoreAdapter) LatestHandoffRecord(ctx context.Context, workID string) (*store.HandoffRecord, error) {
	type reader interface {
		LatestHandoffRecord(ctx context.Context, workID string) (*store.HandoffRecord, error)
	}
	r, ok := a.st.(reader)
	if !ok {
		return nil, errors.New("bridge: store does not implement LatestHandoffRecord")
	}
	return r.LatestHandoffRecord(ctx, workID)
}

// handoffPayloadHash is the sha256 hex over the exact JSON bytes the store
// persists for a handoff — the same value SuspendWork writes as payload_hash.
func handoffPayloadHash(h *workgraph.Handoff) string {
	raw, err := json.Marshal(h)
	if err != nil {
		return ""
	}
	return sha256Hex(string(raw))
}

// bridgeSuspendStore is the minimal suspend surface the handler needs.
type bridgeSuspendStore interface {
	GetWork(ctx context.Context, id string) (*workgraph.Work, error)
	LatestHandoffRecord(ctx context.Context, workID string) (*store.HandoffRecord, error)
	SuspendWorkEventful(ctx context.Context, id string, to workgraph.State, h *workgraph.Handoff) (*workgraph.Work, error)
}

// suspendBody is POST /v1/works/{id}/suspend. state defaults to
// WAITING_HUMAN; the handoff is the ADR-0010 5-layer checkpoint payload.
type suspendBody struct {
	State   string            `json:"state,omitempty"`
	Handoff *workgraph.Handoff `json:"handoff"`
}

type bridgeSuspendHandler struct {
	store        bridgeSuspendStore
	bridgeOK     func(*http.Request) bool
	bridgeClosed func(http.ResponseWriter)
}

func (h *bridgeSuspendHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !h.bridgeOK(r) {
		h.bridgeClosed(w)
		return
	}
	workID := r.PathValue("id")
	if workID == "" {
		writeError(w, http.StatusNotFound, "not_found", "not found")
		return
	}
	var body suspendBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "invalid JSON body")
		return
	}
	to := workgraph.State(body.State)
	if body.State == "" {
		to = workgraph.StateWaitingHuman
	}
	if to != workgraph.StateWaitingHuman && to != workgraph.StateSuspended {
		writeError(w, http.StatusBadRequest, "invalid_state",
			`state must be "WAITING_HUMAN" or "SUSPENDED"`)
		return
	}
	if body.Handoff == nil {
		writeError(w, http.StatusBadRequest, "validation_failed", "handoff is required")
		return
	}
	if err := workgraph.ValidateHandoff(body.Handoff); err != nil {
		writeError(w, http.StatusBadRequest, "validation_failed", err.Error())
		return
	}
	// Idempotent retry: the work already sits in the target state with an
	// identical persisted checkpoint (same payload bytes -> same hash).
	// Mirrors the store's own idempotency guard, which only runs after the
	// transition validates (a same-state retry would otherwise 409).
	if current, err := h.store.GetWork(r.Context(), workID); err == nil && current.State == to {
		if rec, rerr := h.store.LatestHandoffRecord(r.Context(), workID); rerr == nil && rec.PayloadHash == handoffPayloadHash(body.Handoff) {
			writeJSON(w, http.StatusOK, current)
			return
		}
	}
	wk, err := h.store.SuspendWorkEventful(r.Context(), workID, to, body.Handoff)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrNotFound):
			writeError(w, http.StatusNotFound, "not_found", "not found")
		default:
			writeError(w, http.StatusConflict, "suspend_failed", err.Error())
		}
		return
	}
	writeJSON(w, http.StatusOK, wk)
}

// bridgeHandoffStore is the minimal read surface for the handoff endpoint.
type bridgeHandoffStore interface {
	GetWork(ctx context.Context, id string) (*workgraph.Work, error)
	LatestHandoffRecord(ctx context.Context, workID string) (*store.HandoffRecord, error)
}

type bridgeHandoffHandler struct {
	store        bridgeHandoffStore
	bridgeOK     func(*http.Request) bool
	bridgeClosed func(http.ResponseWriter)
}

// handoffView is the read-only checkpoint binding returned by
// GET /v1/works/{id}/handoff. checkpoint_hash is the exact persisted
// handoff payload hash — the value POST /resume validates.
type handoffView struct {
	WorkID         string `json:"work_id"`
	State          string `json:"state"`
	CheckpointHash string `json:"checkpoint_hash"`
}

func (h *bridgeHandoffHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !h.bridgeOK(r) {
		h.bridgeClosed(w)
		return
	}
	workID := r.PathValue("id")
	if workID == "" {
		writeError(w, http.StatusNotFound, "not_found", "not found")
		return
	}
	ctx := r.Context()
	// Work existence + isolation: unknown work is a bare 404 (no leak),
	// mirroring the resume route.
	current, err := h.store.GetWork(ctx, workID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "internal error")
		return
	}
	rec, err := h.store.LatestHandoffRecord(ctx, workID)
	if err != nil {
		if errors.Is(err, store.ErrNoHandoff) {
			writeError(w, http.StatusConflict, "no_checkpoint", "work has no checkpoint")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "internal error")
		return
	}
	writeJSON(w, http.StatusOK, handoffView{
		WorkID:         workID,
		State:          string(current.State),
		CheckpointHash: rec.PayloadHash,
	})
}