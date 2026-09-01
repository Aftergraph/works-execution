// Package api — platform-bridge-bound governed resume (Task 2, Conversation
// V1).
//
// POST /v1/works/{id}/resume moves a WAITING_HUMAN (or SUSPENDED) mission
// Work back to RUNNING, but only through the platform bridge: the request
// must carry X-Works-Platform-Bridge equal to the wired
// WORKS_PLATFORM_BRIDGE_SECRET (minimum 32 bytes, plan: "secret minimum 32
// bytes"; cmd/works-api is wired by the integrator, not here).
//
// Fail-closed law (plan: "missing identity, scope mapping, approval
// binding, or bridge configuration fails closed"):
//   - no secret wired (or secret shorter than 32 bytes) -> 503, always
//   - wrong/missing bridge header                       -> 401
//   - missing required body fields                      -> 400
//   - work not in WAITING_HUMAN/SUSPENDED               -> 409
//   - checkpoint_hash != persisted handoff payload hash -> 409
//   - same idempotency_key, changed payload             -> 409
//   - same idempotency_key, same payload                -> replayed result
//   - unknown work                                      -> 404 (no leak)
//
// Every successful resume persists a receipt in the work_resume_receipts
// table (file-local migration, keyed by idempotency_key) so replays are
// answerable without re-executing the transition.
package api

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/JonasAbde/works-execution/packages/workgraph"
	"github.com/JonasAbde/works-execution/services/work/store"
)

// minBridgeSecretLen is the plan-mandated floor for the platform bridge
// secret. Anything shorter fails closed: the route stays unavailable.
const minBridgeSecretLen = 32

// BridgeSecretConfigured reports whether the given secret satisfies the
// minimum-length requirement (>= 32 bytes). Shared by the handler and
// useful to integrators validating configuration at startup.
func BridgeSecretConfigured(secret string) bool {
	return len(secret) >= minBridgeSecretLen
}

// WorkResumer is the minimal surface the resume handler needs from the
// store. *store.SQLiteStore satisfies it directly.
type WorkResumer interface {
	ResumeFromCheckpoint(ctx context.Context, id string) (any, any, error)
}

// resumeReceipt is the durable record of one governed resume. Keyed by
// idempotency_key; a replay of identical input returns the stored result,
// changed input under the same key is a conflict.
type resumeReceipt struct {
	WorkID         string
	IdempotencyKey string
	PayloadHash    string // sha256 over the canonical resume payload
	ReceiptID      string // approval receipt id from the platform bridge
	PrincipalID    string
	TenantID       string
	ResultingState string
	CreatedAt      string
}

// resumeHandler serves POST /v1/works/{id}/resume.
type resumeHandler struct {
	getter   resumeStoreGetter
	resume   resumeStoreResumer
	receipts *receiptStore
	secret   string
}

// resumeStoreGetter / resumeStoreResumer are narrow adapters over the
// concrete store (defined at the bottom of this file as small interfaces
// satisfied by *store.SQLiteStore).
type resumeStoreGetter interface {
	GetWork(ctx context.Context, id string) (*workStateView, error)
	latestHandoffRecord(ctx context.Context, workID string) (*store.HandoffRecord, error)
}

type resumeStoreResumer interface {
	ResumeFromCheckpoint(ctx context.Context, id string) (*workStateView, error)
}

// WireResumeRoutes mounts POST /v1/works/{id}/resume on the given
// registrar with the platform bridge secret. An empty (or too-short)
// secret keeps the route mounted but fail-closed: every request gets 503
// (route unavailable) so misconfiguration is loud, never silently open.
//
// Wiring (integrator, in api.go Routes() after the existing registrations):
//
//	api.WireResumeRoutes(mux, s, os.Getenv("WORKS_PLATFORM_BRIDGE_SECRET"))
//
// Never log the secret. The handler also wraps itself in requireBearer —
// the platform bridge presents its secret, but regular bearer
// authentication still applies on top (defense in depth, plan: bridge is
// an additional binding, not a replacement for auth).
func WireResumeRoutes(reg RouteRegistrar, s *Server, bridgeSecret string) {
	h := &resumeHandler{
		getter:   resumeGetter{s.Store},
		resume:   resumeStore{s.Store},
		receipts: &receiptStore{st: s.Store},
		secret:   bridgeSecret,
	}
	reg.Handle("POST /v1/works/{id}/resume", s.requireBearer(h))
}

// resumeRequest is the platform-bridge resume body (plan-specified shape).
type resumeBody struct {
	ApprovalReceiptID string `json:"approval_receipt_id"`
	PrincipalID       string `json:"principal_id"`
	TenantID          string `json:"tenant_id"`
	CheckpointHash    string `json:"checkpoint_hash"`
	IdempotencyKey    string `json:"idempotency_key"`
}

// canonicalPayload serializes the semantically-relevant fields for payload
// binding: the idempotency key is only replayable for the exact same
// approval receipt + principal + tenant + checkpoint hash.
func (b resumeBody) canonicalPayload() string {
	type canonical struct {
		ApprovalReceiptID string `json:"approval_receipt_id"`
		PrincipalID       string `json:"principal_id"`
		TenantID          string `json:"tenant_id"`
		CheckpointHash    string `json:"checkpoint_hash"`
	}
	raw, _ := json.Marshal(canonical{
		ApprovalReceiptID: b.ApprovalReceiptID,
		PrincipalID:       b.PrincipalID,
		TenantID:          b.TenantID,
		CheckpointHash:    b.CheckpointHash,
	})
	return string(raw)
}

// sha256Hex returns the hex sha256 digest of s. Used to bind idempotency
// keys to their exact request payloads.
func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func (h *resumeHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Fail closed before anything else: an unwired or too-short secret
	// means the platform bridge cannot be trusted to be configured at all.
	if !BridgeSecretConfigured(h.secret) {
		writeError(w, http.StatusServiceUnavailable, "bridge_unavailable", "platform bridge not configured")
		return
	}

	got := r.Header.Get("X-Works-Platform-Bridge")
	if got == "" || subtle.ConstantTimeCompare([]byte(got), []byte(h.secret)) != 1 {
		writeError(w, http.StatusUnauthorized, "bridge_unauthorized", "missing or invalid platform bridge header")
		return
	}

	workID := r.PathValue("id")
	if workID == "" {
		writeError(w, http.StatusNotFound, "not_found", "not found")
		return
	}

	var body resumeBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "invalid JSON body")
		return
	}
	if body.ApprovalReceiptID == "" || body.PrincipalID == "" || body.TenantID == "" ||
		body.CheckpointHash == "" || body.IdempotencyKey == "" {
		writeError(w, http.StatusBadRequest, "validation_failed",
			"approval_receipt_id, principal_id, tenant_id, checkpoint_hash and idempotency_key are all required")
		return
	}

	ctx := r.Context()

	// Idempotent replay: same key + same payload -> stored result.
	payloadHash := sha256Hex(body.canonicalPayload())
	if rec, err := h.receipts.lookup(ctx, body.IdempotencyKey); err == nil && rec != nil {
		if rec.PayloadHash == payloadHash {
			writeJSON(w, http.StatusOK, map[string]string{
				"work_id":             rec.WorkID,
				"state":               rec.ResultingState,
				"approval_receipt_id": rec.ReceiptID,
				"replayed":            "true",
			})
			return
		}
		writeError(w, http.StatusConflict, "idempotency_conflict",
			"idempotency_key already used with a different payload")
		return
	}

	// Work existence + isolation: unknown work gets a bare 404.
	current, err := h.getter.GetWork(ctx, workID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "internal error")
		return
	}
	if workgraph.State(current.State) != workgraph.StateWaitingHuman && workgraph.State(current.State) != workgraph.StateSuspended {
		writeError(w, http.StatusConflict, "invalid_state",
			fmt.Sprintf("work is %s, resume requires WAITING_HUMAN or SUSPENDED", current.State))
		return
	}

	// Checkpoint binding: the request's hash must equal the persisted
	// handoff payload hash exactly (ADR-0010 fail-closed). A mismatch is a
	// 409, never a silent resume from a stale/different checkpoint.
	rec, err := h.getter.latestHandoffRecord(ctx, workID)
	if err != nil {
		if errors.Is(err, store.ErrNoHandoff) {
			writeError(w, http.StatusConflict, "no_checkpoint", "work has no checkpoint to resume from")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "internal error")
		return
	}
	if rec.PayloadHash != body.CheckpointHash {
		writeError(w, http.StatusConflict, "checkpoint_mismatch",
			"checkpoint_hash does not match the persisted handoff")
		return
	}

	resumed, err := h.resume.ResumeFromCheckpoint(ctx, workID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "not found")
			return
		}
		// Includes ErrStaleHandoff / ErrCorruptHandoff / non-mission:
		// the checkpoint state moved under us.
		writeError(w, http.StatusConflict, "resume_conflict", err.Error())
		return
	}

	if err := h.receipts.record(ctx, resumeReceipt{
		WorkID:         workID,
		IdempotencyKey: body.IdempotencyKey,
		PayloadHash:    payloadHash,
		ReceiptID:      body.ApprovalReceiptID,
		PrincipalID:    body.PrincipalID,
		TenantID:       body.TenantID,
		ResultingState: resumed.State,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "internal error")
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"work_id": workID,
		"state":   resumed.State,
	})
}

// ---------------------------------------------------------------------------
// Narrow store adapters (satisfied by *store.SQLiteStore via the bridge).
// ---------------------------------------------------------------------------

// workStateView is the minimal Work view the resume handler needs.
type workStateView struct {
	ID    string
	State string
}

type resumeGetter struct{ st store.Store }

func (g resumeGetter) GetWork(ctx context.Context, id string) (*workStateView, error) {
	type getter interface {
		GetWork(ctx context.Context, id string) (*workgraph.Work, error)
	}
	gt, ok := g.st.(getter)
	if !ok {
		return nil, errors.New("resume: store does not implement GetWork")
	}
	w, err := gt.GetWork(ctx, id)
	if err != nil {
		return nil, err
	}
	return &workStateView{ID: w.ID, State: string(w.State)}, nil
}

// latestHandoffRecord reads the persisted checkpoint record through the
// bridge accessor.
func (g resumeGetter) latestHandoffRecord(ctx context.Context, workID string) (*store.HandoffRecord, error) {
	type reader interface {
		LatestHandoffRecord(ctx context.Context, workID string) (*store.HandoffRecord, error)
	}
	rd, ok := g.st.(reader)
	if !ok {
		return nil, errors.New("resume: store does not implement LatestHandoffRecord")
	}
	return rd.LatestHandoffRecord(ctx, workID)
}

type resumeStore struct{ st store.Store }

func (s resumeStore) ResumeFromCheckpoint(ctx context.Context, id string) (*workStateView, error) {
	type resumer interface {
		ResumeFromCheckpoint(ctx context.Context, id string) (*workgraph.Work, *workgraph.Handoff, error)
	}
	rs, ok := s.st.(resumer)
	if !ok {
		return nil, errors.New("resume: store does not implement ResumeFromCheckpoint")
	}
	w, _, err := rs.ResumeFromCheckpoint(ctx, id)
	if err != nil {
		return nil, err
	}
	return &workStateView{ID: w.ID, State: string(w.State)}, nil
}

// ---------------------------------------------------------------------------
// Receipt persistence (file-local table; store.go is owned by Task 1).
// ---------------------------------------------------------------------------

// receiptStore persists resume receipts in work_resume_receipts. The table
// is created idempotently here (CREATE TABLE IF NOT EXISTS) because
// store.go's shared migration belongs to the Task 1 workstream; at
// integration this is additive and harmless.
type receiptStore struct {
	st      store.Store
	ensured bool
}

func (r *receiptStore) ensureTable(ctx context.Context) error {
	if r.ensured {
		return nil
	}
	type ensurer interface{ EnsureResumeReceiptsTable() error }
	if e, ok := r.st.(ensurer); ok {
		if err := e.EnsureResumeReceiptsTable(); err != nil {
			return err
		}
		r.ensured = true
		return nil
	}
	return errors.New("resume: store cannot provide work_resume_receipts table")
}

func (r *receiptStore) lookup(ctx context.Context, idempotencyKey string) (*resumeReceipt, error) {
	if err := r.ensureTable(ctx); err != nil {
		return nil, err
	}
	type lookup interface {
		LookupResumeReceipt(ctx context.Context, idempotencyKey string) (*store.ResumeReceipt, error)
	}
	lk, ok := r.st.(lookup)
	if !ok {
		return nil, errors.New("resume: store does not implement LookupResumeReceipt")
	}
	sr, err := lk.LookupResumeReceipt(ctx, idempotencyKey)
	if err != nil {
		return nil, err
	}
	if sr == nil {
		return nil, nil
	}
	return &resumeReceipt{
		WorkID:         sr.WorkID,
		IdempotencyKey: sr.IdempotencyKey,
		PayloadHash:    sr.PayloadHash,
		ReceiptID:      sr.ApprovalReceiptID,
		PrincipalID:    sr.PrincipalID,
		TenantID:       sr.TenantID,
		ResultingState: sr.ResultingState,
	}, nil
}

func (r *receiptStore) record(ctx context.Context, rec resumeReceipt) error {
	if err := r.ensureTable(ctx); err != nil {
		return err
	}
	type recorder interface {
		RecordResumeReceipt(ctx context.Context, rec store.ResumeReceipt) error
	}
	rd, ok := r.st.(recorder)
	if !ok {
		return errors.New("resume: store does not implement RecordResumeReceipt")
	}
	return rd.RecordResumeReceipt(ctx, store.ResumeReceipt{
		WorkID:            rec.WorkID,
		IdempotencyKey:    rec.IdempotencyKey,
		PayloadHash:       rec.PayloadHash,
		ApprovalReceiptID: rec.ReceiptID,
		PrincipalID:       rec.PrincipalID,
		TenantID:          rec.TenantID,
		ResultingState:    rec.ResultingState,
	})
}
