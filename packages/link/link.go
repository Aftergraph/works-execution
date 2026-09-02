// Package link implements the WORKS-Link server side (ADR-0026, ADR-0027):
// the frozen link.wire/1.0 + pairing/1.0 materialized as the only surface a
// PULSE device may present to the works kernel.
//
// The link law encoded here (freeze contract, never relaxed at runtime):
//
//   - L1 (request-only): PULSE is never a controller. The wire vocabulary is
//     exactly the four frozen endpoints (/link/v1 pair|mounts|missions|
//     revoke); there is no mission-creation path, no privileged command path.
//     Commands tiered T3_privileged are structurally unrepresentable on the
//     link surface — a payload asking for one is rejected, not downgraded.
//   - L2 (scope law): every payload carries a scope; the device's ACTIVE
//     pairing scopes are the upper bound. mounts at T2_action requires the
//     grant's purpose_bindings to name the target work (double enforcement,
//     ADR-0026 §6: the same law is applied at PULSE before sending and here
//     at acceptance).
//   - L3 (SAS pairing): a 6-char [A-Z0-9] code displayed on BOTH ends
//     (pairing/1.0 pattern). Unknown device_id fails closed. Code collision
//     at claim → re-roll (ADR-0027 §7), never a silent takeover.
//   - L4 (revoke precedence): a revoked device is permanently refused;
//     revoking twice is a no-op (idempotent, ADR-0020 sync.rules law). A
//     mount request from a revoked device never touches a Work.
//   - L5 (short-lived tokens): pairing hands out an HMAC device token with a
//     fixed TTL (ADR-0027 §6). The kernel verifies state and expiry on every
//     request — a revoked device is refused mid-mission, and its running
//     missions surface as needing human attention (suspend is owned by the
//     kernel scheduler in a follow-up; the link layer's duty is to refuse).
//   - L6 (fail closed): unwired token secret → every authenticated route
//     503s, loudly. No default secret, no dev mode.
//
// Storage lives in the same SQLite file as the work store (table
// link_devices, migration v10). Rows are content-audited by the caller; this
// package owns the shape and the laws, not the HTTP plumbing.
package link

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"
)

// ContractVersion is the frozen link contract this package speaks.
const ContractVersion = "contract:link.wire/1.0"

// PairingContractVersion is the frozen pairing contract (ADR-0027).
const PairingContractVersion = "contract:pairing/1.0"

// TokenTTL is the device-token lifetime (ADR-0027 §6: short-lived, rotated
// at re-pair). Fixed until a policy.token-style rotation slice makes it
// configurable — configurable-now is a footgun, not a feature.
const TokenTTL = 24 * time.Hour

// Frozen endpoint vocabulary from link.wire/1.0. The schema's endpoint enum
// is law: anything else cannot even be named here.
const (
	EndpointPair    = "/link/v1/pair"
	EndpointMounts  = "/link/v1/mounts"
	EndpointMissions = "/link/v1/missions"
	EndpointCommands = "/link/v1/commands"
	EndpointRevoke  = "/link/v1/revoke"
)

// Scopes from pairing/1.0 (T1_read | T2_action | T3_privileged).
const (
	ScopeT1Read         = "T1_read"
	ScopeT2Action       = "T2_action"
	ScopeT3Privileged   = "T3_privileged"
)

// Device states mirror the frozen pairing/1.0 state enum. The kernel only
// ever persists PAIRED and REVOKED: the transient handshake states
// (PAIRING_REQUEST/DISPLAY_CODE/KEY_EXCHANGE/RE_PAIR) live on the PULSE side
// and in the offer object until claim. UNPAIRED is the absence of a row.
const (
	StatePaired  = "PAIRED"
	StateRevoked = "REVOKED"
)

var (
	sasCodeRe    = regexp.MustCompile(`^[A-Z0-9]{6}$`)
	deviceIDRe   = regexp.MustCompile(`^dev_[a-z0-9]+$`)
	sha256HexRe  = regexp.MustCompile(`^[a-f0-9]{64}$`)
	scopeAllowed = map[string]bool{
		ScopeT1Read: true, ScopeT2Action: true, ScopeT3Privileged: true,
	}

	// ErrUnknownDevice: pairing claim or request from an unknown device.
	ErrUnknownDevice = errors.New("link: unknown device")
	// ErrCodeNotIssued: no outstanding SAS offer for this code.
	ErrCodeNotIssued = errors.New("link: pairing code not issued")
	// ErrCodeCollision: the code is already claimed — caller re-rolls.
	ErrCodeCollision = errors.New("link: pairing code already claimed (re-roll)")
	// ErrRevoked: the device is revoked; every operation fails closed.
	ErrRevoked = errors.New("link: device revoked")
	// ErrExpired: the device token lifetime has passed.
	ErrExpired = errors.New("link: device token expired")
	// ErrBadToken: signature mismatch or malformed token.
	ErrBadToken = errors.New("link: bad device token")
	// ErrBadRequest: payload violates the frozen wire law (fail closed).
	ErrBadRequest = errors.New("link: bad request")
	// ErrScopeDenied: device scopes do not cover the request.
	ErrScopeDenied = errors.New("link: scope denied")
)

// -----------------------------------------------------------------------------
// Wire types (link.wire/1.0 + pairing/1.0 shapes)
// -----------------------------------------------------------------------------

// WireRequest is the frozen envelope every POST on the link surface carries.
// endpoint + method + auth are the schema's required trio; the rest are the
// optional-but-lawful fields. Auth is a const in the schema: the only value
// the wire accepts is "mTLS+device_token" (transport upgrade to real mTLS is
// ADR-0026's v2 path; the token half is live now).
type WireRequest struct {
	Endpoint       string `json:"endpoint"`
	Method         string `json:"method"`
	Auth           string `json:"auth"`
	Scope          string `json:"scope,omitempty"`
	IdempotencyKey string `json:"idempotency_key,omitempty"`
	PayloadHash    string `json:"payload_hash,omitempty"`
}

// Validate enforces the frozen shape plus ADR-0026 semantics: only POST/GET,
// only the five endpoints, and — the request-only law — a mounts request may
// never ask for the privileged tier.
func (q *WireRequest) Validate() error {
	if q == nil {
		return fmt.Errorf("%w: request is required", ErrBadRequest)
	}
	switch q.Endpoint {
	case EndpointPair, EndpointMounts, EndpointMissions, EndpointCommands, EndpointRevoke:
	default:
		return fmt.Errorf("%w: endpoint %q not in link.wire/1.0 enum", ErrBadRequest, q.Endpoint)
	}
	if q.Method != "POST" && q.Method != "GET" {
		return fmt.Errorf("%w: method %q not allowed (wire enum POST|GET)", ErrBadRequest, q.Method)
	}
	if q.Auth != "mTLS+device_token" {
		return fmt.Errorf("%w: auth %q violates link.wire/1.0 const", ErrBadRequest, q.Auth)
	}
	if q.PayloadHash != "" && !sha256HexRe.MatchString(q.PayloadHash) {
		return fmt.Errorf("%w: payload_hash must be 64-char sha256 hex", ErrBadRequest)
	}
	// Structural refusal of the privileged tier on the link surface: PULSE is
	// request-only. /link/v1/commands exists in the frozen enum but v1
	// refuses every request that would place a privileged command — this is
	// the PULSE read-only contradiction fix from the pre-freeze review.
	if q.Endpoint == EndpointCommands && q.Scope == ScopeT3Privileged {
		return fmt.Errorf("%w: T3_privileged is unrepresentable on the link surface (request-only law)", ErrBadRequest)
	}
	return nil
}

// PairBeginRequest is PULSE asking for a pairing offer. device_id is PULSE's
// stable local identity; scopes are the device REQUESTING them — the human
// confirms (or narrows) them by displaying and typing the SAS code.
type PairBeginRequest struct {
	DeviceID string   `json:"device_id"`
	Scopes   []string `json:"scopes"`
}

// Offer is an outstanding pairing: the code both ends display. It lives in
// memory with a TTL; an offer that is never claimed simply dies.
type Offer struct {
	Code      string
	DeviceID  string
	Scopes    []string
	ExpiresAt time.Time
}

// PairClaimRequest completes pairing: the human saw the code on PULSE and
// typed it into the kernel (or vice versa — both ends display, one side
// types, ADR-0027 §2).
type PairClaimRequest struct {
	DeviceID string `json:"device_id"`
	SASCode  string `json:"sas_code"`
}

// Device is the persisted pairing/1.0 record (PAIRED or REVOKED).
type Device struct {
	DeviceID   string    `json:"device_id"`
	Scopes     []string  `json:"scopes"`
	State      string    `json:"state"`
	PairedAt   time.Time `json:"paired_at"`
	RevokedAt  time.Time `json:"revoked_at,omitempty"`
}

// MountRequest is the consent-bearing context mount (ADR-0026 §4):
// content-addressed payload, purpose-bound to one work.
type MountRequest struct {
	WireRequest
	DeviceID        string   `json:"device_id"`
	WorkID          string   `json:"work_id"`
	PurposeBindings []string `json:"purpose_bindings"`
}

// MountRecord is the durable mount row (idempotent on the payload hash).
type MountRecord struct {
	ID             string    `json:"id"`
	DeviceID       string    `json:"device_id"`
	WorkID         string    `json:"work_id"`
	PayloadHash    string    `json:"payload_hash"`
	Scope          string    `json:"scope"`
	PurposeBinding string    `json:"purpose_binding"`
	CreatedAt      time.Time `json:"created_at"`
}

// MissionRow is the read-only projection of one mission for the PULSE NOW
// view (ADR-0026 §4 "mission-status feed", read via event fetch per ADR-0019
// — here a snapshot row is enough for the projection; the journal stays the
// source of truth).
type MissionRow struct {
	WorkID     string  `json:"work_id"`
	State      string  `json:"state"`
	NeedsHuman bool    `json:"needs_human"`
	Consumed   float64 `json:"consumed_eur,omitempty"`
	Ceiling    float64 `json:"ceiling_eur,omitempty"`
}

// RevokeRequest is the local-revoke-notify law (ADR-0020/0026 §4): PULSE
// tells the kernel it revoked; the kernel revokes authoritatively on its
// side. Idempotent by design.
type RevokeRequest struct {
	WireRequest
	DeviceID string `json:"device_id"`
}

// -----------------------------------------------------------------------------
// Helpers
// -----------------------------------------------------------------------------

// canonicalJSON marshals with sorted keys (encoding/json does) and strips
// nothing else — payloads must be JSON-round-trip safe.
func canonicalJSON(v any) ([]byte, error) { return json.Marshal(v) }

// PayloadHash is the content address of a payload (sha256 over its canonical
// JSON, hex). ADR-0026 §4: payloads content-addressed.
func PayloadHash(v any) (string, error) {
	b, err := canonicalJSON(v)
	if err != nil {
		return "", fmt.Errorf("link: payload not canonicalizable: %w", err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// NewID returns a prefixed crypto/rand id (same shape as workgraph.NewID but
// the link package keeps its own to avoid an import cycle on tests).
func NewID(prefix string) string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic("link: crypto/rand unavailable: " + err.Error())
	}
	return prefix + "_" + hex.EncodeToString(b)
}

// -----------------------------------------------------------------------------
// Pairing store (in-memory until the SQL table lands in store v10; the
// Service works against these two interfaces so the HTTP layer and laws are
// testable end-to-end without the migration)
// -----------------------------------------------------------------------------

// DeviceStore persists Devices.
type DeviceStore interface {
	GetDevice(ctx context.Context, deviceID string) (*Device, error)
	PutDevice(ctx context.Context, d *Device) error
	InsertMount(ctx context.Context, m *MountRecord) (bool, error) // false = idempotent replay
	GetMount(ctx context.Context, id string) (*MountRecord, error)
}

// -----------------------------------------------------------------------------
// Token mint/verify (HS256, in-process secret — same Zero-Secret Standard as
// enrollment: the key is freshly random per process, never persisted.)
// -----------------------------------------------------------------------------

// TokenIssuer mints and verifies short-lived device tokens.
type TokenIssuer struct {
	key    []byte
	NowFn  func() time.Time
	TTL    time.Duration
}

func NewTokenIssuer() *TokenIssuer {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		panic("link: crypto/rand unavailable: " + err.Error())
	}
	return &TokenIssuer{key: key, NowFn: time.Now, TTL: TokenTTL}
}

// NewTokenIssuerWithKey wires a deterministic key (tests only).
func NewTokenIssuerWithKey(key []byte) *TokenIssuer {
	return &TokenIssuer{key: append([]byte{}, key...), NowFn: time.Now, TTL: TokenTTL}
}

type tokenClaims struct {
	DeviceID string `json:"dev"`
	ScopeKey string `json:"sk"` // sha256(dev|scopes|paired_at) — state binding
	IssuedAt int64  `json:"iat"`
	ExpiresAt int64 `json:"exp"`
}

func (t *TokenIssuer) claimsDigest(d *Device) string {
	sum := sha256.Sum256([]byte(d.DeviceID + "|" + fmt.Sprint(d.Scopes) + "|" + d.PairedAt.UTC().Format(time.RFC3339)))
	return hex.EncodeToString(sum[:])
}

// Mint issues a device token bound to the device's CURRENT state. Rotating
// (re-pair) changes the digest, so a token minted before the re-pair stops
// verifying once the device row changes.
func (t *TokenIssuer) Mint(d *Device) (string, error) {
	c := tokenClaims{
		DeviceID:  d.DeviceID,
		ScopeKey:  t.claimsDigest(d),
		IssuedAt:  t.NowFn().Unix(),
		ExpiresAt: t.NowFn().Add(t.TTL).Unix(),
	}
	b, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	payload := base64.RawURLEncoding.EncodeToString(b)
	mac := hmac.New(sha256.New, t.key)
	mac.Write([]byte(payload))
	return payload + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

// Verify returns the claims of a correctly signed, unexpired token.
func (t *TokenIssuer) Verify(raw string) (*tokenClaims, error) {
	parts := splitToken(raw)
	if len(parts) != 2 {
		return nil, ErrBadToken
	}
	mac := hmac.New(sha256.New, t.key)
	mac.Write([]byte(parts[0]))
	want := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if subtle.ConstantTimeCompare([]byte(want), []byte(parts[1])) != 1 {
		return nil, ErrBadToken
	}
	b, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, ErrBadToken
	}
	var c tokenClaims
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, ErrBadToken
	}
	if time.Unix(c.ExpiresAt, 0).Before(t.NowFn()) {
		return nil, ErrExpired
	}
	return &c, nil
}

func splitToken(raw string) []string {
	out := []string{}
	start := 0
	for i := 0; i < len(raw); i++ {
		if raw[i] == '.' {
			out = append(out, raw[start:i])
			start = i + 1
		}
	}
	return append(out, raw[start:])
}

// -----------------------------------------------------------------------------
// Service — the laws as methods
// -----------------------------------------------------------------------------

// OfferTTL bounds an unclaimed pairing code.
const OfferTTL = 5 * time.Minute

// Service implements the link law over a DeviceStore + TokenIssuer.
type Service struct {
	Devices DeviceStore
	Issuer  *TokenIssuer

	offers   map[string]*Offer // sas code -> offer
	now      func() time.Time
}

// NewService wires a link service. Either argument may be nil in
// construction but every call fails closed without it.
func NewService(devices DeviceStore, issuer *TokenIssuer) *Service {
	return &Service{Devices: devices, Issuer: issuer, offers: map[string]*Offer{}, now: time.Now}
}

func (s *Service) ready() error {
	if s == nil || s.Devices == nil || s.Issuer == nil {
		return errors.New("link: service not configured (fail closed)")
	}
	return nil
}

// BeginPair issues a fresh SAS code for a device's requested scopes. Codes
// are generated from crypto/rand over the 32-char unambiguous alphabet; a
// collision with a live offer is re-rolled (ADR-0027 §7). The returned code
// is what BOTH ends must display; nothing is persisted yet.
func (s *Service) BeginPair(ctx context.Context, req PairBeginRequest) (*Offer, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	if !deviceIDRe.MatchString(req.DeviceID) {
		return nil, fmt.Errorf("%w: device_id must match dev_[a-z0-9]+", ErrBadRequest)
	}
	if len(req.Scopes) == 0 {
		return nil, fmt.Errorf("%w: scopes must be non-empty", ErrBadRequest)
	}
	seen := map[string]bool{}
	for _, sc := range req.Scopes {
		if !scopeAllowed[sc] {
			return nil, fmt.Errorf("%w: scope %q not in pairing/1.0 enum", ErrBadRequest, sc)
		}
		if seen[sc] {
			return nil, fmt.Errorf("%w: duplicate scope %q", ErrBadRequest, sc)
		}
		seen[sc] = true
	}
	// A revoked device may re-pair only through RE_PAIR: BeginPair refuses
	// known-revoked device ids outright (local revoke always wins until a
	// human re-enables, ADR-0020/0027).
	existing, err := s.Devices.GetDevice(ctx, req.DeviceID)
	if err != nil && !errors.Is(err, ErrUnknownDevice) {
		return nil, err
	}
	if existing != nil && existing.State == StateRevoked {
		return nil, ErrRevoked
	}
	s.gcOffers()
	for i := 0; i < 5; i++ {
		code := randomCode()
		if _, taken := s.offers[code]; taken {
			continue // re-roll
		}
		offer := &Offer{Code: code, DeviceID: req.DeviceID, Scopes: append([]string{}, req.Scopes...), ExpiresAt: s.now().Add(OfferTTL)}
		s.offers[code] = offer
		return offer, nil
	}
	return nil, errors.New("link: code space saturated (re-roll exhausted)")
}

const sasAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789" // no I,O,0,1 — human-readable codes

func randomCode() string {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		panic("link: crypto/rand unavailable: " + err.Error())
	}
	for i := range b {
		b[i] = sasAlphabet[int(b[i])%len(sasAlphabet)]
	}
	return string(b)
}

// ClaimPair exchanges a displayed code for a device token — the one moment
// a human is provably involved on the kernel side. Wrong code for the
// device → fail closed, offer survives (a typo is not a burn).
func (s *Service) ClaimPair(ctx context.Context, req PairClaimRequest) (*Device, string, error) {
	if err := s.ready(); err != nil {
		return nil, "", err
	}
	if !sasCodeRe.MatchString(req.SASCode) {
		return nil, "", fmt.Errorf("%w: sas_code must be 6 chars [A-Z0-9]", ErrBadRequest)
	}
	s.gcOffers()
	offer, ok := s.offers[req.SASCode]
	if !ok {
		return nil, "", ErrCodeNotIssued
	}
	if offer.DeviceID != req.DeviceID {
		return nil, "", ErrCodeNotIssued // never reveal WHICH device issued the code
	}
	delete(s.offers, req.SASCode)
	now := s.now().UTC()
	d := &Device{DeviceID: req.DeviceID, Scopes: offer.Scopes, State: StatePaired, PairedAt: now}
	token, err := s.Issuer.Mint(d)
	if err != nil {
		return nil, "", err
	}
	if err := s.Devices.PutDevice(ctx, d); err != nil {
		return nil, "", err
	}
	return d, token, nil
}

// Authenticate verifies a device token AND re-reads the durable device —
// double enforcement (ADR-0026 §6): a revoked or re-paired device is
// refused even with a still-signed token.
func (s *Service) Authenticate(ctx context.Context, rawToken string) (*Device, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	claims, err := s.Issuer.Verify(rawToken)
	if err != nil {
		return nil, err
	}
	d, err := s.Devices.GetDevice(ctx, claims.DeviceID)
	if err != nil {
		return nil, err
	}
	if d.State == StateRevoked {
		return nil, ErrRevoked
	}
	if s.Issuer.claimsDigest(d) != claims.ScopeKey {
		return nil, ErrBadToken // state moved under the token (re-pair, scope change)
	}
	return d, nil
}

// Mount accepts a consent-bearing context mount. Law: T2_action requires the
// purpose_bindings to name the work (no ambient T2 — a device may not mount
// arbitrary work context); payload_hash is verified against the canonical
// hash of the provided payload fields; the (work_id, payload_hash) pair is
// idempotent.
func (s *Service) Mount(ctx context.Context, d *Device, req MountRequest) (*MountRecord, error) {
	req.Endpoint = EndpointMounts
	req.Method = "POST"
	req.Auth = "mTLS+device_token"
	if err := req.WireRequest.Validate(); err != nil {
		return nil, err
	}
	if req.DeviceID != d.DeviceID {
		return nil, ErrUnknownDevice // tokens cannot act for another device
	}
	if req.WorkID == "" {
		return nil, fmt.Errorf("%w: mounts require work_id", ErrBadRequest)
	}
	if req.Scope != ScopeT1Read && req.Scope != ScopeT2Action {
		return nil, fmt.Errorf("%w: scope %q not allowed for mounts", ErrScopeDenied, req.Scope)
	}
	if !hasScope(d.Scopes, req.Scope) {
		return nil, fmt.Errorf("%w: device lacks %s", ErrScopeDenied, req.Scope)
	}
	if req.Scope == ScopeT2Action && !containsStr(req.PurposeBindings, req.WorkID) {
		return nil, fmt.Errorf("%w: purpose_bindings must name work %s", ErrScopeDenied, req.WorkID)
	}
	hash, err := PayloadHash(map[string]any{"device_id": req.DeviceID, "work_id": req.WorkID, "scope": req.Scope, "purpose_bindings": req.PurposeBindings})
	if err != nil {
		return nil, err
	}
	if req.PayloadHash != "" && req.PayloadHash != hash {
		return nil, fmt.Errorf("%w: payload_hash does not match canonical content", ErrBadRequest)
	}
	rec := &MountRecord{
		ID: mountIdempotencyID(d.DeviceID, hash), DeviceID: d.DeviceID, WorkID: req.WorkID,
		PayloadHash: hash, Scope: req.Scope,
		PurposeBinding: req.WorkID, CreatedAt: s.now().UTC(),
	}
	created, err := s.Devices.InsertMount(ctx, rec)
	if err != nil {
		return nil, err
	}
	if !created { // idempotent replay: return the stored original
		existing, err := s.Devices.GetMount(ctx, mountIdempotencyID(req.DeviceID, hash))
		if err != nil {
			return nil, err
		}
		return existing, nil
	}
	return rec, nil
}

// Missions is the read-only status feed (ADR-0026 §4): only a T1 device may
// even ask; rows are projected by the caller (who owns BudgetLedger access).
func (s *Service) Missions(d *Device, rows []*MissionRow) ([]*MissionRow, error) {
	q := &WireRequest{Endpoint: EndpointMissions, Method: "GET", Auth: "mTLS+device_token"}
	if err := q.Validate(); err != nil {
		return nil, err
	}
	if !hasScope(d.Scopes, ScopeT1Read) {
		return nil, fmt.Errorf("%w: missions feed requires %s", ErrScopeDenied, ScopeT1Read)
	}
	out := make([]*MissionRow, 0, len(rows))
	out = append(out, rows...)
	return out, nil
}

// Revoke marks the device REVOKED on the kernel side (notify-only from
// PULSE; local revoke always wins, ADR-0020). Idempotent: revoking twice is
// a no-op that still answers 200 (sync law).
func (s *Service) Revoke(ctx context.Context, caller *Device, req RevokeRequest) (*Device, error) {
	req.Endpoint = EndpointRevoke
	req.Method = "POST"
	req.Auth = "mTLS+device_token"
	if err := req.WireRequest.Validate(); err != nil {
		return nil, err
	}
	// A device may only revoke itself through the link surface — revoking
	// another device is a kernel/operator action, never a PULSE one.
	if req.DeviceID != caller.DeviceID {
		return nil, ErrUnknownDevice
	}
	d, err := s.Devices.GetDevice(ctx, req.DeviceID)
	if err != nil {
		return nil, err
	}
	if d.State == StateRevoked {
		return d, nil // idempotent replay (sync.rules/1.0)
	}
	d.State = StateRevoked
	d.RevokedAt = s.now().UTC()
	if err := s.Devices.PutDevice(ctx, d); err != nil {
		return nil, err
	}
	return d, nil
}

func (s *Service) gcOffers() {
	now := s.now()
	for code, o := range s.offers {
		if o.ExpiresAt.Before(now) {
			delete(s.offers, code)
		}
	}
}

func hasScope(scopes []string, want string) bool { return containsStr(scopes, want) }

func containsStr(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// mountIdempotencyID derives the deterministic row id for an idempotent
// replay: sha256(device|payload_hash) — mount records are content-addressed.
func mountIdempotencyID(deviceID, payloadHash string) string {
	sum := sha256.Sum256([]byte(deviceID + "|" + payloadHash))
	return "mnt_" + hex.EncodeToString(sum[:])
}
