package link

// k-link-01 law tests: every invariant encoded in the package header is
// executable here — positive, adversarial, and idempotency paths.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// --- in-memory DeviceStore (fake) -------------------------------------------

type memStore struct {
	devices map[string]*Device
	mounts  map[string]*MountRecord
}

func newMemStore() *memStore {
	return &memStore{devices: map[string]*Device{}, mounts: map[string]*MountRecord{}}
}

func (m *memStore) GetDevice(_ context.Context, id string) (*Device, error) {
	d, ok := m.devices[id]
	if !ok {
		return nil, ErrUnknownDevice
	}
	cp := *d
	return &cp, nil
}

func (m *memStore) PutDevice(_ context.Context, d *Device) error {
	cp := *d
	m.devices[d.DeviceID] = &cp
	return nil
}

func (m *memStore) InsertMount(_ context.Context, rec *MountRecord) (bool, error) {
	if _, exists := m.mounts[rec.ID]; exists {
		return false, nil
	}
	m.mounts[rec.ID] = rec
	return true, nil
}

func (m *memStore) GetMount(_ context.Context, id string) (*MountRecord, error) {
	rec, ok := m.mounts[id]
	if !ok {
		return nil, errors.New("mount not found")
	}
	return rec, nil
}

func (m *memStore) ListMountWorkIDs(_ context.Context, deviceID string) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	for _, rec := range m.mounts {
		if rec.DeviceID == deviceID && !seen[rec.WorkID] {
			seen[rec.WorkID] = true
			out = append(out, rec.WorkID)
		}
	}
	return out, nil
}

// --- helpers -----------------------------------------------------------------

func newTestService(t *testing.T) (*Service, *memStore) {
	t.Helper()
	ms := newMemStore()
	iss := NewTokenIssuerWithKey([]byte(strings.Repeat("k", 32)))
	return NewService(ms, iss), ms
}

// pairToToken runs the full happy pairing loop and returns the token.
func pairToToken(t *testing.T, s *Service, deviceID string, scopes []string) string {
	t.Helper()
	ctx := context.Background()
	offer, err := s.BeginPair(ctx, PairBeginRequest{DeviceID: deviceID, Scopes: scopes})
	if err != nil {
		t.Fatalf("BeginPair: %v", err)
	}
	d, token, err := s.ClaimPair(ctx, PairClaimRequest{DeviceID: deviceID, SASCode: offer.Code})
	if err != nil {
		t.Fatalf("ClaimPair: %v", err)
	}
	if d.State != StatePaired {
		t.Fatalf("state after claim = %s, want PAIRED", d.State)
	}
	return token
}

// --- L3: SAS pairing law -------------------------------------------------------

func TestSASCodeShape(t *testing.T) {
	s, _ := newTestService(t)
	_, err := s.BeginPair(context.Background(), PairBeginRequest{DeviceID: "dev_law", Scopes: []string{ScopeT1Read}})
	if err != nil {
		t.Fatal(err)
	}
	// law: codes are exactly 6 chars of [A-Z0-9]
	offer := s.offers[onlyCode(t, s)]
	for _, c := range offer.Code {
		if !((c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')) {
			t.Fatalf("code %q has non [A-Z0-9] char %q", offer.Code, c)
		}
	}
	if len(offer.Code) != 6 {
		t.Fatalf("code %q: want 6 chars", offer.Code)
	}
}

func onlyCode(t *testing.T, s *Service) string {
	t.Helper()
	for code := range s.offers {
		return code
	}
	t.Fatal("no offers outstanding")
	return ""
}

func TestPairingRejectsBadScopes(t *testing.T) {
	s, _ := newTestService(t)
	cases := map[string]PairBeginRequest{
		"unknown scope":  {DeviceID: "dev_a", Scopes: []string{"T9_god"}},
		"empty scopes":   {DeviceID: "dev_b", Scopes: nil},
		"dupe scopes":    {DeviceID: "dev_c", Scopes: []string{ScopeT1Read, ScopeT1Read}},
		"bad device id":  {DeviceID: "Dev-X!", Scopes: []string{ScopeT1Read}},
		"missing device": {DeviceID: "", Scopes: []string{ScopeT1Read}},
	}
	for name, req := range cases {
		_, err := s.BeginPair(context.Background(), req)
		if !errors.Is(err, ErrBadRequest) {
			t.Fatalf("%s: got %v, want ErrBadRequest", name, err)
		}
	}
}

func TestClaimWrongDeviceNeverBurnsCode(t *testing.T) {
	s, _ := newTestService(t)
	ctx := context.Background()
	offer, err := s.BeginPair(ctx, PairBeginRequest{DeviceID: "dev_real", Scopes: []string{ScopeT1Read}})
	if err != nil {
		t.Fatal(err)
	}
	// Adversary knows the code but claims with a different device id.
	_, _, err = s.ClaimPair(ctx, PairClaimRequest{DeviceID: "dev_evil", SASCode: offer.Code})
	if !errors.Is(err, ErrCodeNotIssued) {
		t.Fatalf("mismatched claim: got %v, want ErrCodeNotIssued (no leak)", err)
	}
	// The offer survives — a typo is not a burn.
	_, tok, err := s.ClaimPair(ctx, PairClaimRequest{DeviceID: "dev_real", SASCode: offer.Code})
	if err != nil || tok == "" {
		t.Fatalf("retry after failed claim must succeed: %v", err)
	}
}

func TestOfferExpiry(t *testing.T) {
	s, _ := newTestService(t)
	ctx := context.Background()
	offer, err := s.BeginPair(ctx, PairBeginRequest{DeviceID: "dev_t", Scopes: []string{ScopeT1Read}})
	if err != nil {
		t.Fatal(err)
	}
	// Fast-forward past OfferTTL via the injectable clock.
	s.now = func() time.Time { return time.Now().Add(OfferTTL + time.Second) }
	_, _, err = s.ClaimPair(ctx, PairClaimRequest{DeviceID: "dev_t", SASCode: offer.Code})
	if !errors.Is(err, ErrCodeNotIssued) {
		t.Fatalf("expired offer: got %v, want ErrCodeNotIssued", err)
	}
}

func TestRevokeThenReclaimRefused(t *testing.T) {
	s, _ := newTestService(t)
	ctx := context.Background()
	tok := pairToToken(t, s, "dev_r", []string{ScopeT1Read})
	d, err := s.Authenticate(ctx, tok)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Revoke(ctx, d, RevokeRequest{DeviceID: "dev_r"}); err != nil {
		t.Fatal(err)
	}
	// Local revoke always wins: begin-pair for a revoked device is refused.
	_, err = s.BeginPair(ctx, PairBeginRequest{DeviceID: "dev_r", Scopes: []string{ScopeT1Read}})
	if !errors.Is(err, ErrRevoked) {
		t.Fatalf("revoke-then-repair: got %v, want ErrRevoked", err)
	}
}

// --- L5: token law ------------------------------------------------------------

func TestTokenExpiryFailsClosed(t *testing.T) {
	s, _ := newTestService(t)
	ctx := context.Background()
	tok := pairToToken(t, s, "dev_e", []string{ScopeT1Read})
	s.Issuer.NowFn = func() time.Time { return time.Now().Add(TokenTTL + time.Hour) }
	if _, err := s.Authenticate(ctx, tok); !errors.Is(err, ErrExpired) {
		t.Fatalf("expired token: got %v, want ErrExpired", err)
	}
}

func TestTokenTamperRejected(t *testing.T) {
	s, _ := newTestService(t)
	tok := pairToToken(t, s, "dev_tt", []string{ScopeT1Read})
	for _, bad := range []string{"", "x", tok + "z", "abc.def"} {
		if _, err := s.Authenticate(context.Background(), bad); err == nil {
			t.Fatalf("tampered token %q accepted", bad)
		}
	}
}

func TestRepairInvalidatesOldToken(t *testing.T) {
	s, ms := newTestService(t)
	ctx := context.Background()
	oldTok := pairToToken(t, s, "dev_rp", []string{ScopeT1Read})
	// Simulate re-pair: state digest changes (paired_at moves forward).
	d, _ := ms.GetDevice(ctx, "dev_rp")
	d.PairedAt = d.PairedAt.Add(time.Minute)
	if err := ms.PutDevice(ctx, d); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Authenticate(ctx, oldTok); !errors.Is(err, ErrBadToken) {
		t.Fatalf("token across re-pair: got %v, want ErrBadToken (state binding)", err)
	}
}

// --- L1/L2: request-only + scope law -------------------------------------------

func TestWireRequestValidate(t *testing.T) {
	base := func(mut func(*WireRequest)) WireRequest {
		q := WireRequest{Endpoint: EndpointMounts, Method: "POST", Auth: "mTLS+device_token"}
		mut(&q)
		return q
	}
	ok := base(func(*WireRequest) {})
	if err := ok.Validate(); err != nil {
		t.Fatalf("valid request rejected: %v", err)
	}
	cases := map[string]struct {
		q    WireRequest
		want error
	}{
		"unknown endpoint":          {q: base(func(q *WireRequest) { q.Endpoint = "/link/v1/mission_create" }), want: ErrBadRequest},
		"PUT not in enum":           {q: base(func(q *WireRequest) { q.Method = "PUT" }), want: ErrBadRequest},
		"API token copy-paste auth": {q: base(func(q *WireRequest) { q.Auth = "api_token" }), want: ErrBadRequest},
		"T3 commands on link":       {q: base(func(q *WireRequest) { q.Endpoint = EndpointCommands; q.Scope = ScopeT3Privileged }), want: ErrBadRequest},
		"bad payload hash":          {q: base(func(q *WireRequest) { q.PayloadHash = "deadbeef" }), want: ErrBadRequest},
	}
	for name, tc := range cases {
		if err := tc.q.Validate(); !errors.Is(err, tc.want) {
			t.Fatalf("%s: got %v, want %v", name, err, tc.want)
		}
	}
}

func TestMountScopeLaw(t *testing.T) {
	s, _ := newTestService(t)
	ctx := context.Background()
	tok := pairToToken(t, s, "dev_m", []string{ScopeT1Read}) // T1 only
	d, err := s.Authenticate(ctx, tok)
	if err != nil {
		t.Fatal(err)
	}
	// T1 device cannot mount at T2 — scope ceiling is the PAIRING, not the request.
	_, err = s.Mount(ctx, d, MountRequest{WireRequest: WireRequest{Scope: ScopeT2Action}, DeviceID: "dev_m", WorkID: "wrk_1", PurposeBindings: []string{"wrk_1"}})
	if !errors.Is(err, ErrScopeDenied) {
		t.Fatalf("T1 device doing T2 mount: got %v, want ErrScopeDenied", err)
	}
	// T1 device CAN mount at T1.
	rec, err := s.Mount(ctx, d, MountRequest{WireRequest: WireRequest{Scope: ScopeT1Read}, DeviceID: "dev_m", WorkID: "wrk_1"})
	if err != nil {
		t.Fatalf("T1 mount: %v", err)
	}
	if rec.PayloadHash == "" || len(rec.PayloadHash) != 64 {
		t.Fatalf("mount record must be content-addressed: %+v", rec)
	}
	// T2 device WITHOUT purpose binding for the work: denied.
	tok2 := pairToToken(t, s, "dev_m2", []string{ScopeT1Read, ScopeT2Action})
	d2, _ := s.Authenticate(ctx, tok2)
	_, err = s.Mount(ctx, d2, MountRequest{WireRequest: WireRequest{Scope: ScopeT2Action}, DeviceID: "dev_m2", WorkID: "wrk_2"})
	if !errors.Is(err, ErrScopeDenied) || !strings.Contains(err.Error(), "purpose_bindings") {
		t.Fatalf("T2 mount without purpose binding: got %v", err)
	}
	// T2 device WITH correct binding: allowed.
	if _, err := s.Mount(ctx, d2, MountRequest{WireRequest: WireRequest{Scope: ScopeT2Action}, DeviceID: "dev_m2", WorkID: "wrk_2", PurposeBindings: []string{"wrk_2"}}); err != nil {
		t.Fatalf("lawful T2 mount failed: %v", err)
	}
	// Tokens cannot act for another device.
	_, err = s.Mount(ctx, d2, MountRequest{WireRequest: WireRequest{Scope: ScopeT1Read}, DeviceID: "dev_m", WorkID: "wrk_2"})
	if !errors.Is(err, ErrUnknownDevice) {
		t.Fatalf("cross-device mount: got %v, want ErrUnknownDevice", err)
	}
}

func TestMountIdempotentReplay(t *testing.T) {
	s, ms := newTestService(t)
	ctx := context.Background()
	tok := pairToToken(t, s, "dev_i", []string{ScopeT1Read})
	d, _ := s.Authenticate(ctx, tok)
	req := MountRequest{WireRequest: WireRequest{Scope: ScopeT1Read}, DeviceID: "dev_i", WorkID: "wrk_i"}
	first, err := s.Mount(ctx, d, req)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.Mount(ctx, d, req)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID || len(ms.mounts) != 1 {
		t.Fatalf("replay must be a no-op returning the original: %s vs %s, rows=%d", first.ID, second.ID, len(ms.mounts))
	}
}

func TestMissionsFeedNeedsT1(t *testing.T) {
	s, _ := newTestService(t)
	ctx := context.Background()
	// Device paired T2-only (no T1): the feed is a read surface, it must refuse.
	tok := pairToToken(t, s, "dev_n", []string{ScopeT2Action})
	d, _ := s.Authenticate(ctx, tok)
	if _, err := s.Missions(d, []*MissionRow{{WorkID: "wrk_x", State: "RUNNING"}}); !errors.Is(err, ErrScopeDenied) {
		t.Fatalf("missions without T1: got %v, want ErrScopeDenied", err)
	}
}

// --- L4: revoke law ------------------------------------------------------------

func TestRevokeIdempotentAndBlocksEveryCall(t *testing.T) {
	s, _ := newTestService(t)
	ctx := context.Background()
	tok := pairToToken(t, s, "dev_rv", []string{ScopeT1Read, ScopeT2Action})
	d, err := s.Authenticate(ctx, tok)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Revoke(ctx, d, RevokeRequest{DeviceID: "dev_rv"}); err != nil {
		t.Fatal(err)
	}
	// Revoke is idempotent (sync.rules/1.0).
	again, err := s.Revoke(ctx, d, RevokeRequest{DeviceID: "dev_rv"})
	if err != nil || again.Device.State != StateRevoked {
		t.Fatalf("double revoke: %v %+v", err, again)
	}
	// The old token is dead: Authenticate re-reads state (double enforcement).
	if _, err := s.Authenticate(ctx, tok); !errors.Is(err, ErrRevoked) {
		t.Fatalf("auth after revoke: got %v, want ErrRevoked", err)
	}
}

func TestRevokeCannotTargetAnotherDevice(t *testing.T) {
	s, _ := newTestService(t)
	ctx := context.Background()
	tokA := pairToToken(t, s, "dev_a1", []string{ScopeT1Read})
	pairToToken(t, s, "dev_b1", []string{ScopeT1Read})
	dA, _ := s.Authenticate(ctx, tokA)
	if _, err := s.Revoke(ctx, dA, RevokeRequest{DeviceID: "dev_b1"}); !errors.Is(err, ErrUnknownDevice) {
		t.Fatalf("cross-device revoke: got %v, want ErrUnknownDevice", err)
	}
}

// --- k-035: revoke cascade law -------------------------------------------------

// cascadeCalls records every cascade invocation to assert the once-only law.
type cascadeCalls struct {
	calls []string
	works map[string][]string
	err   error
}

func (c *cascadeCalls) fn() func(ctx context.Context, deviceID string) ([]string, error) {
	return func(_ context.Context, deviceID string) ([]string, error) {
		c.calls = append(c.calls, deviceID)
		if c.err != nil {
			return nil, c.err
		}
		return c.works[deviceID], nil
	}
}

// pairMount pairs a device and mounts workID at T1 (the link-side mount law
// needs no purpose binding at T1), returning the authenticated device.
func pairMount(t *testing.T, s *Service, deviceID, workID string) *Device {
	t.Helper()
	ctx := context.Background()
	tok := pairToToken(t, s, deviceID, []string{ScopeT1Read})
	d, err := s.Authenticate(ctx, tok)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Mount(ctx, d, MountRequest{WireRequest: WireRequest{Scope: ScopeT1Read}, DeviceID: deviceID, WorkID: workID}); err != nil {
		t.Fatalf("mount: %v", err)
	}
	return d
}

func TestRevokeCascadeRunsOnceOnFirstRevokeOnly(t *testing.T) {
	s, _ := newTestService(t)
	ctx := context.Background()
	calls := &cascadeCalls{works: map[string][]string{"dev_cas": {"wrk_a", "wrk_b"}}}
	s.Cascade = calls.fn()

	d := pairMount(t, s, "dev_cas", "wrk_a")
	d = pairMount(t, s, "dev_cas", "wrk_a") // idempotent replay: same mount twice
	res, err := s.Revoke(ctx, d, RevokeRequest{DeviceID: "dev_cas"})
	if err != nil {
		t.Fatal(err)
	}
	// Law: cascade once per FIRST revoke, distinct work ids ride back.
	if len(calls.calls) != 1 || calls.calls[0] != "dev_cas" {
		t.Fatalf("cascade calls = %v, want exactly one for dev_cas", calls.calls)
	}
	if len(res.SuspendedWorks) != 2 {
		t.Fatalf("SuspendedWorks = %v, want the mounted works", res.SuspendedWorks)
	}
	if res.CascadeErr != nil {
		t.Fatalf("unexpected cascade error: %v", res.CascadeErr)
	}
	if res.Device.State != StateRevoked {
		t.Fatalf("device state after revoke = %s, want REVOKED", res.Device.State)
	}

	// Idempotent replay: revoke again — cascade must NOT re-run and the
	// suspended list must come back empty (sync law + cascade law).
	replay, err := s.Revoke(ctx, d, RevokeRequest{DeviceID: "dev_cas"})
	if err != nil {
		t.Fatal(err)
	}
	if len(calls.calls) != 1 {
		t.Fatalf("cascade re-ran on idempotent replay (calls=%v) — replay is a no-op by law", calls.calls)
	}
	if len(replay.SuspendedWorks) != 0 {
		t.Fatalf("replay SuspendedWorks = %v, want empty", replay.SuspendedWorks)
	}
	if replay.CascadeErr != nil {
		t.Fatalf("replay cascade error: %v", replay.CascadeErr)
	}
}

func TestRevokeCascadeErrorNeverUnrevokes(t *testing.T) {
	s, ms := newTestService(t)
	ctx := context.Background()
	calls := &cascadeCalls{err: errors.New("kernel suspend path exploded")}
	s.Cascade = calls.fn()

	d := pairMount(t, s, "dev_cf", "wrk_cf")
	res, err := s.Revoke(ctx, d, RevokeRequest{DeviceID: "dev_cf"})
	if err != nil {
		t.Fatalf("revoke must succeed even when the cascade fails: %v", err)
	}
	if res.CascadeErr == nil {
		t.Fatal("cascade failure must surface on RevokeResult.CascadeErr")
	}
	if len(res.SuspendedWorks) != 0 {
		t.Fatalf("SuspendedWorks = %v on failed cascade, want empty", res.SuspendedWorks)
	}
	// The durable revoke stands: the device row is REVOKED and the token is
	// dead — best-effort semantics, never un-revoke.
	got, err := ms.GetDevice(ctx, "dev_cf")
	if err != nil || got.State != StateRevoked {
		t.Fatalf("cascade error un-revoked the device: %v %+v", err, got)
	}
}

func TestRevokeWithoutCascadeStillLawful(t *testing.T) {
	// No Cascade wired (nil): revoke behaves exactly as before k-035 —
	// empty suspended list, no error. The kernel may lag the link law.
	s, _ := newTestService(t)
	ctx := context.Background()
	d := pairMount(t, s, "dev_nc", "wrk_nc")
	res, err := s.Revoke(ctx, d, RevokeRequest{DeviceID: "dev_nc"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.SuspendedWorks) != 0 || res.CascadeErr != nil || res.Device.State != StateRevoked {
		t.Fatalf("nil-cascade revoke: %+v %v", res, err)
	}
}

// --- fail-closed plumbing --------------------------------------------------------

func TestUnconfiguredServiceRefuses(t *testing.T) {
	svc := NewService(nil, nil)
	_, err := svc.BeginPair(context.Background(), PairBeginRequest{DeviceID: "dev_z", Scopes: []string{ScopeT1Read}})
	if err == nil || !strings.Contains(err.Error(), "fail closed") {
		t.Fatalf("nil deps: got %v, want fail-closed error", err)
	}
}

func TestPayloadHashStableAndCanonical(t *testing.T) {
	a, err := PayloadHash(map[string]any{"b": 1, "a": 2})
	if err != nil {
		t.Fatal(err)
	}
	b, err := PayloadHash(map[string]any{"a": 2, "b": 1})
	if err != nil {
		t.Fatal(err)
	}
	if a != b || len(a) != 64 {
		t.Fatalf("hash not canonical: %s vs %s", a, b)
	}
}
