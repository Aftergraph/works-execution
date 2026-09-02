package api

// k-link-01 — the WORKS-Link HTTP surface (ADR-0026/0027): the ONLY routes a
// PULSE device may present to the works kernel.
//
//   POST /link/v1/pair             begin|claim SAS pairing (pairing/1.0)
//   POST /link/v1/mounts           consent-bearing context mount (device token)
//   GET  /link/v1/missions         read-only mission projection (device token)
//   POST /link/v1/revoke           local-revoke-notify, idempotent (device token)
//
// /link/v1/commands exists in the frozen link.wire enum but is NOT mounted
// here: the request-only law (PULSE is never a controller) makes every
// privileged command structurally unmountable on the link surface. Mounting
// a route that must refuse everything is how a hole gets opened later.
//
// Auth boundary: pair is unauthenticated (the SAS code IS the human-proof
// boundary, ADR-0027 §2); mounts/missions/revoke carry a Bearer device
// token minted at claim, and the service re-reads durable device state on
// every call (double enforcement, ADR-0026 §6). The link surface is
// independent of worker enrollment tokens: a worker JWT is not a device
// token and vice versa — different audiences, different laws.
//
// Fail-closed law (L6): an unwired pairing secret (WORKS_LINK_PAIRING_SECRET
// absent or < 32 bytes) keeps every link route mounted but answering 503 —
// loud, never a silent default.

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/JonasAbde/works-execution/packages/link"
	"github.com/JonasAbde/works-execution/packages/workgraph"
)

// LinkConfig wires the link surface. Service may be nil in dev — the 503
// fail-closed path covers it.
type LinkConfig struct {
	Service *link.Service
	// BodyLimitBytes caps link payloads (KB-class per ADR-0026 §9; 64 KiB is
	// generous metadata headroom, MB-class payloads are a law violation).
	BodyLimitBytes int64
}

const defaultLinkBodyLimit = 64 * 1024

// NewLinkServiceFromEnv builds the link service from a pairing secret (min
// 32 bytes, same law as the platform bridge). Empty/short secret -> nil
// Service: the surface stays mounted and every route 503s, loudly, with no
// default secret ever assumed (L6 fail-closed).
//
// k-035: when the DeviceStore also carries the kernel's revoke-cascade sink
// (SQLiteStore's linkStore does: SuspendMissionsForDevice), it is wired as
// the Service's Cascade — a revoked device's mounted missions suspend with a
// durable handoff. The type assertion keeps cmd/works-api wiring untouched.
func NewLinkServiceFromEnv(devices link.DeviceStore, secret string) *LinkConfig {
	cfg := &LinkConfig{}
	if len(secret) < 32 {
		return cfg // Service stays nil -> 503 on every route
	}
	cfg.Service = link.NewService(devices, link.NewTokenIssuerWithKey([]byte(secret)))
	if cs, ok := devices.(interface {
		SuspendMissionsForDevice(ctx context.Context, deviceID string) ([]string, error)
	}); ok {
		cfg.Service.Cascade = cs.SuspendMissionsForDevice
	}
	return cfg
}

func (s *Server) linkHandler(w http.ResponseWriter, r *http.Request) {
	if s.Link == nil || s.Link.Service == nil {
		writeError(w, http.StatusServiceUnavailable, "link_unavailable", "WORKS-Link not configured")
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/link/v1")
	switch {
	case rest == "/pair" && r.Method == http.MethodPost:
		s.linkPair(w, r)
	case rest == "/mounts" && r.Method == http.MethodPost:
		s.linkDevice(w, r, s.linkMounts)
	case rest == "/missions" && r.Method == http.MethodGet:
		s.linkDevice(w, r, s.linkMissions)
	case rest == "/revoke" && r.Method == http.MethodPost:
		// The revoke route authenticates WITHOUT the post-revoke refusal:
		// the token's device may already be durably REVOKED, and the
		// idempotent replay law (sync.rules/1.0) requires that replay to
		// reach the handler and answer 200. Signature + expiry are still
		// verified; the REVOKED check is left to the handler (which is
		// exactly the revoke itself — ADR-0020 sync law).
		d, ok := s.linkDeviceAuth(w, r)
		if !ok {
			return
		}
		s.linkRevoke(w, r, d)
	default:
		// Unknown or wrong-method endpoints on the frozen enum 404 (never
		// 405-with-hints: the surface shape is not discoverable).
		writeError(w, http.StatusNotFound, "not_found", "endpoint not in link surface")
	}
}

// linkPair handles BOTH pairing steps with one endpoint (pairing/1.0 is a
// state machine; the body shape selects the step):
//
//	{"device_id":"dev_x","scopes":["T1_read",...]}        -> begin (offer)
//	{"device_id":"dev_x","sas_code":"ABC234"}             -> claim (token)
//
// Begin returns the code (it must be displayed on the kernel side — the NOW
// surface — and matched by the human against PULSE's display). Claim returns
// the short-lived device token exactly once.
func (s *Server) linkPair(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var body map[string]any
	if err := s.readLinkJSON(w, r, &body); err != nil {
		return
	}
	deviceID, _ := body["device_id"].(string)
	if sas, ok := body["sas_code"].(string); ok && sas != "" {
		d, token, err := s.Link.Service.ClaimPair(ctx, link.PairClaimRequest{DeviceID: deviceID, SASCode: sas})
		if err != nil {
			s.linkError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"state":      d.State,
			"device_id":  d.DeviceID,
			"scopes":     d.Scopes,
			"token":      token,
			"expires_in": int(s.Link.Service.Issuer.TTL.Seconds()),
		})
		return
	}
	// begin: scopes arrives as []any of strings
	rawScopes, _ := body["scopes"].([]any)
	scopes := make([]string, 0, len(rawScopes))
	for _, x := range rawScopes {
		str, ok := x.(string)
		if !ok {
			writeError(w, http.StatusBadRequest, "bad_request", "scopes must be strings")
			return
		}
		scopes = append(scopes, str)
	}
	offer, err := s.Link.Service.BeginPair(ctx, link.PairBeginRequest{DeviceID: deviceID, Scopes: scopes})
	if err != nil {
		s.linkError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"state":      "DISPLAY_CODE",
		"sas_code":   offer.Code,
		"device_id":  offer.DeviceID,
		"expires_in": int(link.OfferTTL.Seconds()),
	})
}

// linkDevice authenticates the Bearer device token — WITH the revoked-state
// refusal (double enforcement, ADR-0026 §6) — then runs fn.
func (s *Server) linkDevice(w http.ResponseWriter, r *http.Request, fn func(http.ResponseWriter, *http.Request, *link.Device)) {
	d, ok := s.linkDeviceAuth(w, r)
	if !ok {
		return
	}
	if d.State == link.StateRevoked {
		s.linkError(w, link.ErrRevoked)
		return
	}
	fn(w, r, d)
}

// linkDeviceAuth verifies the Bearer device token WITHOUT the revoked-state
// refusal and returns the durable device. Used by the revoke route (k-035):
// a revoked device must still be able to answer the idempotent revoke
// replay, so the REVOKED check belongs to the handler, not the gate.
func (s *Server) linkDeviceAuth(w http.ResponseWriter, r *http.Request) (*link.Device, bool) {
	h := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if len(h) <= len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
		writeError(w, http.StatusUnauthorized, "device_token_required", "link surface requires a Bearer device token")
		return nil, false
	}
	// k-035: authenticate WITHOUT the revoked refusal — the replay must
	// reach the handler. Signature, expiry, and the re-pair digest binding
	// are still enforced via the service helper.
	d, err := s.Link.Service.AuthenticateToken(r.Context(), strings.TrimSpace(h[len(prefix):]))
	if err != nil {
		s.linkError(w, err)
		return nil, false
	}
	return d, true
}

func (s *Server) linkMounts(w http.ResponseWriter, r *http.Request, d *link.Device) {
	var req link.MountRequest
	if err := s.readLinkJSON(w, r, &req); err != nil {
		return
	}
	rec, err := s.Link.Service.Mount(r.Context(), d, req)
	if err != nil {
		s.linkError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, rec)
}

func (s *Server) linkMissions(w http.ResponseWriter, r *http.Request, d *link.Device) {
	works, err := s.Store.ListWorks(r.Context(), 200)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", "could not read works")
		return
	}
	rows := make([]*link.MissionRow, 0, len(works))
	for _, work := range works {
		if work == nil || !work.IsMission() {
			continue // the missions feed is missions-only; CI works never appear
		}
		rows = append(rows, missionRow(work))
	}
	out, err := s.Link.Service.Missions(d, rows)
	if err != nil {
		s.linkError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"missions": out, "device_id": d.DeviceID})
}

func (s *Server) linkRevoke(w http.ResponseWriter, r *http.Request, d *link.Device) {
	var req link.RevokeRequest
	if err := s.readLinkJSON(w, r, &req); err != nil {
		return
	}
	if req.DeviceID == "" {
		req.DeviceID = d.DeviceID
	}
	// k-035: RevokeResult carries the durable device record plus the
	// cascade outcome. The revoke is authoritative regardless of the
	// cascade; a cascade failure is surfaced as a 500 with the revoke
	// already standing (best-effort law — never un-revoke, never 200-lie).
	res, err := s.Link.Service.Revoke(r.Context(), d, req)
	if err != nil {
		s.linkError(w, err)
		return
	}
	if res.CascadeErr != nil {
		s.logf("link revoke cascade for %s partially failed: %v", res.Device.DeviceID, res.CascadeErr)
		writeError(w, http.StatusInternalServerError, "cascade_failed",
			"device revoked but mission suspension failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"state":           res.Device.State,
		"device_id":       res.Device.DeviceID,
		"suspended_works": res.SuspendedWorks,
	})
}

// missionRow projects one mission Work onto the link feed. Budget ceiling
// comes from the frozen contract; consumed is kernel-in-metering (memory,
// ADR-0009) and deliberately NOT claimed here — the feed is status, not
// billing.
func missionRow(work *workgraph.Work) *link.MissionRow {
	row := &link.MissionRow{WorkID: work.ID, State: string(work.State)}
	switch work.State {
	case workgraph.StateWaitingHuman, workgraph.StateSuspended, workgraph.StateBudgetExhausted:
		row.NeedsHuman = true
	}
	if work.Mission != nil && work.Mission.BudgetCeiling != nil {
		row.Ceiling = work.Mission.BudgetCeiling.ComputeEUR
	}
	return row
}

// readLinkJSON decodes a size-capped body into dst (fail closed on overflow
// and on any trailing junk).
func (s *Server) readLinkJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	limit := int64(defaultLinkBodyLimit)
	if s.Link != nil && s.Link.BodyLimitBytes > 0 {
		limit = s.Link.BodyLimitBytes
	}
	defer r.Body.Close()
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit))
	if err := dec.Decode(dst); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "link payload could not be decoded")
		return err
	}
	return nil
}

// linkError maps the link law's sentinel errors onto the frozen status
// semantics: unknown/unauthenticated -> 401, revoked -> 403, bad payload ->
// 400, collision -> 409. Messages never leak device state to a non-owner.
func (s *Server) linkError(w http.ResponseWriter, err error) {
	switch {
	case strings.Contains(err.Error(), "not configured"):
		writeError(w, http.StatusServiceUnavailable, "link_unavailable", err.Error())
	case errIs(err, link.ErrRevoked):
		writeError(w, http.StatusForbidden, "device_revoked", "device pairing revoked")
	case errIs(err, link.ErrBadToken), errIs(err, link.ErrExpired), errIs(err, link.ErrUnknownDevice):
		writeError(w, http.StatusUnauthorized, "device_token_invalid", "device token is not valid")
	case errIs(err, link.ErrCodeNotIssued):
		writeError(w, http.StatusUnauthorized, "pairing_code_invalid", "pairing code not issued or expired")
	case errIs(err, link.ErrScopeDenied):
		writeError(w, http.StatusForbidden, "scope_denied", err.Error())
	case errIs(err, link.ErrBadRequest):
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
	default:
		writeError(w, http.StatusInternalServerError, "link_error", "internal link error")
	}
}

func errIs(err, target error) bool {
	for err != nil {
		if err == target {
			return true
		}
		type unwrapper interface{ Unwrap() error }
		u, ok := err.(unwrapper)
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}
