// Package api: runner RAB advertisement endpoints (k-053 / ADR-0012/0014).
//
// This file gives the runner registry its capability-advertisement leg: a
// runtime (browser RT, code RT, desktop RT) publishes a rab/1.0 RAB next to
// its identity, and callers negotiate caps against it. The RAB law itself is
// FROZEN in packages/abi — this file only wires it: it never re-implements
// validation, cap semantics, or negotiation. Every store path calls
// abi.ParseRAB / abi.RAB.Validate first; everything is fail-closed.
//
// Endpoints (additive to the existing runner surface):
//
//	POST /v1/runners/{id}/abi              body = rab/1.0 JSON (see packages/abi)
//	GET  /v1/runners/{id}/abi              -> stored advertisement + linkage
//	POST /v1/runners/{id}/abi/negotiate    body = {"caps":[...]} -> granted caps
//
// Integration-order law: a RAB requires an identity. The runner must be
// registered first via POST /v1/runners/register; all three endpoints answer
// 404 "runner_not_found" for an unknown id. This is deliberate (CPI/RAB
// split): the registry keys capability advertisements by runner identity,
// so an orphan RAB has no meaning and is never stored.
//
// Overwrite semantics: POST /abi is an idempotent upsert — re-posting a
// valid RAB replaces the previous advertisement in place and re-stamps
// registered_at (mirroring the idempotent re-registration/heartbeat style
// of POST /v1/runners/register). There is deliberately no DELETE: a runtime
// that loses capabilities re-advertises a smaller RAB; capability state is
// owned by the runtime, not the operator.
//
// Auth: the runner registration surface (POST /v1/runners/register,
// GET /v1/runners/{id}) is mounted unauthenticated in Routes(); these
// endpoints follow that existing convention unchanged. Nothing here is
// weaker: registration itself remains the gate that mints the runner id.
//
// Wire-visible law: the control-token rule (caps contains "control" =>
// control_token_required must be true) is enforced at POST with the kernel's
// own error message, and negotiate responses carry control_token_required so
// callers can gate privileged operations.
package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"time"

	"github.com/JonasAbde/works-execution/packages/abi"
)

// maxRABBodyBytes bounds the POST /abi request body. A rab/1.0 document is a
// version string, at most five caps, one bool, and N-1 tolerated extra
// fields; 64 KiB is generous and keeps a malformed client from streaming
// unbounded JSON into ParseRAB (which reads the whole body to enforce the
// trailing-token law).
const maxRABBodyBytes = 64 << 10

// RuntimeABI is a stored, already-validated RAB plus its registry linkage:
// which runner advertised it and when. The RAB law is NOT redefined here —
// RAB is the frozen kernel type and every accessor below delegates to it.
//
// MarshalJSON flattens the record on the wire: the GET response is the RAB
// document itself (abi, caps, control_token_required and any N-1 tolerated
// Extra fields) enriched with runner_id and registered_at, so readers see
// the contract-shaped advertisement with its linkage.
type RuntimeABI struct {
	// RunnerID is the registry key the advertisement hangs off.
	RunnerID string
	// RegisteredAt is server-stamped at each successful POST (overwrite
	// re-stamps it).
	RegisteredAt time.Time
	// RAB is the validated capability advertisement.
	RAB abi.RAB
}

// MarshalJSON renders the flattened rab/1.0 document plus linkage.
//
// k-054 finding: the previous implementation overlaid runner_id and
// registered_at on the flattened RAB map, which silently DESTROYED any
// user-supplied N-1 top-level field whose name happened to collide
// (e.g. a legal rab/1.0 document advertising a "registered_at" field
// for its own N-1 use). Linkage fields are now namespaced under a
// private "rab_runtime_meta" object so the advertised document is
// preserved bit-for-bit and the registry's bookkeeping is still
// discoverable.
func (rec RuntimeABI) MarshalJSON() ([]byte, error) {
	rab := rec.RAB
	rabJSON, err := json.Marshal(&rab)
	if err != nil {
		return nil, err
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(rabJSON, &m); err != nil {
		return nil, err
	}
	m["rab_runtime_meta"], err = json.Marshal(struct {
		RunnerID     string    `json:"runner_id"`
		RegisteredAt time.Time `json:"registered_at"`
	}{rec.RunnerID, rec.RegisteredAt})
	if err != nil {
		return nil, err
	}
	return json.Marshal(m)
}

// cloneRAB returns an independent copy of a RAB: caps slice, the
// control-token pointer, and the Extra map are all freshly allocated so a
// caller mutating a record handed out by getABI/getRuntimeABI/listABI can
// never corrupt the stored advertisement.
//
// k-054 finding: the original shallow copy of Extra aliased any nested
// map/slice value (the N-1 unknown-field JSON shape can be arbitrarily
// nested per packages/abi), so mutating a record's nested structure
// silently rewrote the stored advertisement in both directions. Extra is
// now re-canonicalised through the kernel: abi.Marshal the source and
// abi.Unmarshal into a fresh map[string]any, so the copy is a complete
// deep clone of every JSON value the kernel accepts.
func cloneRAB(r abi.RAB) abi.RAB {
	out := r
	if r.Caps != nil {
		out.Caps = append([]string(nil), r.Caps...)
	}
	if r.ControlTokenRequired != nil {
		ctr := *r.ControlTokenRequired
		out.ControlTokenRequired = &ctr
	}
	if r.Extra != nil {
		src, err := json.Marshal(r.Extra)
		if err != nil {
			// marshal of a validated, kernel-shaped map cannot fail
			// in practice; fall back to an empty map to preserve the
			// invariant rather than crash the read path.
			out.Extra = map[string]any{}
		} else {
			cp := map[string]any{}
			if err := json.Unmarshal(src, &cp); err != nil {
				out.Extra = map[string]any{}
			} else {
				out.Extra = cp
			}
		}
	}
	return out
}

// putABI stores a validated advertisement for runnerID, replacing any
// previous one (overwrite semantics — see file header). It is fail-closed:
// the RAB is validated with the frozen kernel law before anything is
// stored, so even a programmatic caller cannot insert an illegal
// advertisement. Mirrors put()'s lock discipline on the shared registry
// mutex.
func (r *runnerRegistry) putABI(runnerID string, rab *abi.RAB) error {
	if rab == nil {
		return errors.New("rab: nil advertisement")
	}
	if err := rab.Validate(); err != nil {
		return err
	}
	cp := cloneRAB(*rab)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.abiByRunner == nil {
		r.abiByRunner = map[string]*RuntimeABI{}
	}
	r.abiByRunner[runnerID] = &RuntimeABI{
		RunnerID:     runnerID,
		RegisteredAt: time.Now().UTC(),
		RAB:          cp,
	}
	return nil
}

// getABI returns a copy of the stored advertisement, or false when the
// runner has never posted one (distinct from "runner not registered" — the
// handlers map that to a 404 abi_not_found).
func (r *runnerRegistry) getABI(runnerID string) (*abi.RAB, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	rec, ok := r.abiByRunner[runnerID]
	if !ok {
		return nil, false
	}
	cp := cloneRAB(rec.RAB)
	return &cp, true
}

// getRuntimeABI returns a copy of the full stored record (RAB + linkage).
func (r *runnerRegistry) getRuntimeABI(runnerID string) (*RuntimeABI, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	rec, ok := r.abiByRunner[runnerID]
	if !ok {
		return nil, false
	}
	cp := *rec
	cp.RAB = cloneRAB(rec.RAB)
	return &cp, true
}

// listABI returns a snapshot of every stored advertisement, sorted by
// runner id for deterministic output. Same copy-out discipline as List().
func (r *runnerRegistry) listABI() []*RuntimeABI {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*RuntimeABI, 0, len(r.abiByRunner))
	for _, rec := range r.abiByRunner {
		cp := *rec
		cp.RAB = cloneRAB(rec.RAB)
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].RunnerID < out[j].RunnerID })
	return out
}

// rabABIPathID extracts the runner id from a wildcard-mounted
// /v1/runners/{id}/abi route.
func rabABIPathID(r *http.Request) string {
	return r.PathValue("id")
}

// postRunnerABI handles POST /v1/runners/{id}/abi.
//
// Body is a rab/1.0 JSON document; it is parsed with abi.ParseRAB (strict
// single-value JSON with trailing-token rejection, N-1 unknown-field
// tolerance into Extra) which also runs Validate(). Law violations answer
// 400 with the kernel's own message — most importantly the control-token
// law: a RAB advertising "control" without control_token_required=true is
// rejected fail-closed.
func (s *Server) postRunnerABI(w http.ResponseWriter, r *http.Request) {
	id := rabABIPathID(r)
	if !s.rabRunnerGate(w, r, id) {
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRABBodyBytes))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	rab, err := abi.ParseRAB(body)
	if err != nil {
		if isRABLawError(err) {
			writeError(w, http.StatusBadRequest, "rab_law_violation", err.Error())
		} else {
			writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		}
		return
	}
	// putABI re-validates (defense in depth) and overwrites atomically.
	if err := s.RunnerRegistry.putABI(id, rab); err != nil {
		writeError(w, http.StatusBadRequest, "rab_law_violation", err.Error())
		return
	}
	rec, _ := s.RunnerRegistry.getRuntimeABI(id)
	s.logf("runner abi advertised: id=%s caps=%v control_token_required=%t",
		id, rec.RAB.Caps, rec.RAB.RequiresControlToken())
	writeJSON(w, http.StatusOK, rec)
}

// getRunnerABI handles GET /v1/runners/{id}/abi.
func (s *Server) getRunnerABI(w http.ResponseWriter, r *http.Request) {
	id := rabABIPathID(r)
	if !s.rabRunnerGate(w, r, id) {
		return
	}
	rec, ok := s.RunnerRegistry.getRuntimeABI(id)
	if !ok {
		writeError(w, http.StatusNotFound, "abi_not_found", id)
		return
	}
	writeJSON(w, http.StatusOK, rec)
}

// negotiateRunnerABI handles POST /v1/runners/{id}/abi/negotiate.
//
// Body: {"caps": ["observe", "control", ...]} — the caller's requested
// capability set. The response is the kernel's Negotiate triple: the
// granted caps (intersection, requested order preserved) and
// control_token_required, which is true exactly when "control" was granted.
// Unknown or duplicate requested caps are a 400 (fail-closed, never
// silently dropped). Requesting fewer/other caps than advertised is not an
// error — the intersection simply omits them.
func (s *Server) negotiateRunnerABI(w http.ResponseWriter, r *http.Request) {
	id := rabABIPathID(r)
	if !s.rabRunnerGate(w, r, id) {
		return
	}
	var req struct {
		Caps []string `json:"caps"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, maxRABBodyBytes)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	rab, ok := s.RunnerRegistry.getABI(id)
	if !ok {
		writeError(w, http.StatusNotFound, "abi_not_found", id)
		return
	}
	granted, controlTokenRequired, err := rab.Negotiate(req.Caps)
	if err != nil {
		writeError(w, http.StatusBadRequest, "rab_negotiation_failed", err.Error())
		return
	}
	if granted == nil {
		granted = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"runner_id":              id,
		"caps":                   granted,
		"control_token_required": controlTokenRequired,
	})
}

// rabRunnerGate enforces the integration-order law (RAB requires identity)
// for every /abi route: it answers 404 runner_not_found when the runner id
// is unknown, and the caller must stop when ok is false.
func (s *Server) rabRunnerGate(w http.ResponseWriter, r *http.Request, id string) bool {
	if id == "" || s.RunnerRegistry == nil {
		writeError(w, http.StatusNotFound, "runner_not_found", id)
		return false
	}
	if _, ok := s.RunnerRegistry.get(id); !ok {
		writeError(w, http.StatusNotFound, "runner_not_found", id)
		return false
	}
	return true
}

// isRABLawError reports whether err is one of the frozen kernel's
// deterministic law violations (as opposed to a JSON syntax problem).
func isRABLawError(err error) bool {
	return errors.Is(err, abi.ErrAbiVersion) ||
		errors.Is(err, abi.ErrCapUnknown) ||
		errors.Is(err, abi.ErrCapDuplicate) ||
		errors.Is(err, abi.ErrControlTokenRequired) ||
		errors.Is(err, abi.ErrTrailingTokens)
}
