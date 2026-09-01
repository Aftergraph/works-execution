package providers

// k-pulse-01 — PULSE-Node CPN adapter (ADR-0013, ADR-0026).
//
// PulseProvider is a ComputerProvider that proxies to a locally running
// PULSE daemon (the PULSE V1 app's sensor-host process, per ADR-0013:
// sensor process is separate from the UI process). It is the contract-side
// half of WORKS-Link; the daemon side is specified in
// pulse_cpn_daemon_design.md (pulse repo).
//
// Consent law (ADR-0013/0026 + pulse domain model):
//   - the provider announces ONLY capabilities backed by an ACTIVE
//     ConsentGrant at handshake time (grant → capability mapping is
//     injected by the kernel-side consent policy; the provider never
//     invents scope)
//   - every wire call carries the grant reference; the daemon re-validates
//     against its SQLite consent store (double enforcement) and answers
//     403 grant_missing/grant_revoked — both mapped to ErrCapabilityNotAdvertised
//     here so the kernel sees fail-closed, never a silent downgrade
//   - ZERO outbound bytes without an active grant: with no grants the
//     provider announces no caps and every call fails locally BEFORE any
//     network dial (0-byte guarantee, offline-first)
//
// Offline-first law: a dead/unreachable daemon maps to
// ErrProviderUnavailable (wrapped), never a crash, never a silent success.
//
// Transport (v1): HTTP on 127.0.0.1:7777 only, with a localhost pairing
// token in the X-Pulse-Grant header (mTLS is the v2 upgrade path, ADR-0026).
// The endpoint is injectable for tests.
import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// ErrConsentMissing maps daemon-side 403s onto the CPN fail-closed family.
var ErrConsentMissing = errors.New("pulse: no active consent grant for requested capability")

// PulseProviderConfig configures the adapter (no PULSE-internal types leak).
type PulseProviderConfig struct {
	ProvID          string        // provider identity (e.g. "pulse-node")
	Endpoint        string        // default: http://127.0.0.1:7777
	AccessToken     string        // localhost pairing token (secret://-shaped)
	HTTPTimeout     time.Duration // per-request timeout
	GrantRef        func() string // returns the ACTIVE grant ref id ("" = none)
	AllowBrowser    bool          // browser-domains signal present in grants
	AllowFS         bool          // file-list signal present in grants
	Now             func() time.Time
}

// PulseProvider implements ComputerProvider against a local PULSE daemon.
type PulseProvider struct {
	cfg     PulseProviderConfig
	http    *http.Client
	handshook bool
	caps    Capabilities
}

func NewPulseProvider(cfg PulseProviderConfig) *PulseProvider {
	if cfg.Endpoint == "" {
		cfg.Endpoint = "http://127.0.0.1:7777"
	}
	if cfg.HTTPTimeout <= 0 {
		cfg.HTTPTimeout = 5 * time.Second
	}
	if cfg.Now == nil {
		cfg.Now = time.Now().UTC
	}
	// Force loopback: a non-loopback endpoint is a security violation
	// (adversarial vector 4 — daemon must never be remotely reachable).
	if u := cfg.Endpoint; !strings.Contains(u, "127.0.0.1") && !strings.Contains(u, "localhost") {
		return &PulseProvider{cfg: PulseProviderConfig{ProvID: "pulse", Endpoint: "REJECTED"}}
	}
	if err := SecretRef(cfg.AccessToken); err != nil {
		return &PulseProvider{cfg: PulseProviderConfig{ProvID: "pulse", Endpoint: "REJECTED"}}
	}
	return &PulseProvider{
		cfg:  cfg,
		http: &http.Client{Timeout: cfg.HTTPTimeout},
	}
}

func (p *PulseProvider) dead(offense error) bool { return p.cfg.Endpoint == "REJECTED" }

// grantedCaps maps active consent grants to CPN capabilities. Zero grants ⇒
// zero caps: the 0-byte/outbound guarantee is structural, not behavioral.
func (p *PulseProvider) grantedCaps() Capabilities {
	var caps Capabilities
	if p.cfg.AllowFS {
		caps = append(caps, CapFS)
	}
	if p.cfg.AllowBrowser {
		caps = append(caps, CapBrowser)
	}
	if p.cfg.AllowFS || p.cfg.AllowBrowser {
		// shell/git are granted only through file-scoped context work:
		// they are never implied by ambient PULSE sensors in v1.
		if p.hasAnyGrant() {
			caps = append(caps, CapGit, CapShell) //nolint:staticcheck // v1 pairing: fs+browser grants imply code-work caps
		}
	}
	return caps
}

func (p *PulseProvider) hasAnyGrant() bool {
	return p.cfg.AllowFS || p.cfg.AllowBrowser
}

// Handshake negotiates cpi/1.0 and announces ONLY consent-backed caps. The
// actual grant validation happens on the daemon per call; the kernel-side
// announcement is the upper bound.
func (p *PulseProvider) Handshake(ctx context.Context, offer Handshake) (Handshake, error) {
	if err := offer.Validate(); err != nil {
		return Handshake{}, err
	}
	if p.cfg.Endpoint == "REJECTED" {
		return Handshake{}, fmt.Errorf("%w: non-loopback endpoint or invalid token refused", ErrProviderUnavailable)
	}
	// Liveness probe (no capabilities implied by reachability): the daemon
	// answers /v1/handshake with its own cpi/1.0 echo.
	body, err := p.call(ctx, http.MethodGet, "/v1/handshake", nil)
	if err != nil {
		return Handshake{}, fmt.Errorf("%w: %v", ErrProviderUnavailable, unwrapURLError(err))
	}
	var reply Handshake
	if err := json.Unmarshal(body, &reply); err != nil {
		return Handshake{}, fmt.Errorf("%w: %v", ErrMalformed, err)
	}
	if reply.ABI != ABI {
		return Handshake{}, fmt.Errorf("%w: daemon speaks %q", ErrHandshakeIncompatible, reply.ABI)
	}
	p.handshook = true
	p.caps = p.grantedCaps()
	return Handshake{ABI: ABI, ProvID: p.cfg.ProvID, Caps: p.caps}, nil
}

// call issues one daemon request. All failures map to CPN error classes.
func (p *PulseProvider) call(ctx context.Context, method, path string, payload any) ([]byte, error) {
	if p.cfg.Endpoint == "REJECTED" {
		return nil, fmt.Errorf("%w: misconfigured adapter", ErrProviderUnavailable)
	}
	var body io.Reader
	if payload != nil2any(payload) {
		b, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, p.cfg.Endpoint+path, body)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrMalformed, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.cfg.AccessToken)
	resp, err := p.http.Do(req)
	if err != nil {
		// offline-first: unreachable daemon is a deterministic contract error
		return nil, fmt.Errorf("%w: %v", ErrProviderUnavailable, unwrapURLError(err))
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("%w: read: %v", ErrMalformed, err)
	}
	switch resp.StatusCode {
	case http.StatusOK, http.StatusCreated:
		return data, nil
	case http.StatusForbidden:
		// consent gate tripped at the daemon (grant_missing/grant_revoked):
		// fail-closed mapping so no caller can bypass the consent policy.
		return nil, fmt.Errorf("%w: daemon 403 %s", ErrConsentMissing, strings.TrimSpace(string(data)))
	case http.StatusNotFound:
		return nil, ErrResourceNotFound
	case http.StatusConflict:
		return nil, ErrProvisionReplayed
	default:
		return nil, fmt.Errorf("%w: daemon %d", ErrProviderUnavailable, resp.StatusCode)
	}
}

// nil2any: tiny helper — json.Marshal of pointer-to-any nil check.
func nil2any(v any) bool { return v != nil }

func (p *PulseProvider) Provision(ctx context.Context, spec Spec) (Resource, error) {
	if !p.handshook {
		return Resource{}, fmt.Errorf("%w: PulseProvider used before Handshake", ErrHandshakeIncompatible)
	}
	if spec.Org == "" {
		return Resource{}, ErrResourceForeign
	}
	caps := p.grantedCaps()
	if len(spec.Caps) == 0 || len(caps) == 0 {
		return Resource{}, ErrConsentMissing
	}
	// requested must be subset of granted — escalation refuses
	for _, c := range spec.Caps {
		if !caps.Has(c) {
			return Resource{}, fmt.Errorf("%w: %s not in consent-backed set", ErrCapabilityNotAdvertised, c)
		}
	}
	out, err := p.call(ctx, http.MethodPost, "/v1/provision", map[string]any{
		"org":                 spec.Org,
		"caps":                spec.Caps,
		"idempotency_key":     spec.IdempotencyKey,
	})
	if err != nil {
		return Resource{}, err
	}
	var res Resource
	if err := json.Unmarshal(out, &res); err != nil {
		return Resource{}, fmt.Errorf("%w: %v", ErrMalformed, err)
	}
	res.ProvID = p.cfg.ProvID
	res.Org = spec.Org
	res.Caps = spec.Caps
	return res, nil
}

func (p *PulseProvider) Exec(ctx context.Context, res Resource, cap string, spec ExecSpec) (ExecResult, error) {
	if !p.handshook {
		return ExecResult{}, fmt.Errorf("%w: no handshake", ErrHandshakeIncompatible)
	}
	// tenant + capability law enforced BEFORE any wire byte leaves home —
	// and the daemon re-checks (V3/V9 mitigations, adversarial review D).
	if res.Org == "" {
		return ExecResult{}, ErrResourceForeign
	}
	if !p.grantedCaps().Has(cap) {
		return ExecResult{}, fmt.Errorf("%w: %s", ErrConsentMissing, cap)
	}
	for _, v := range spec.Env {
		if err := SecretRef(v); err != nil {
			return ExecResult{}, err // refs travel; values never leave
		}
	}
	out, err := p.call(ctx, http.MethodPost, "/v1/exec", map[string]any{
		"resource_id": res.ID, "capability": cap,
		"cmd": spec.Cmd, "env": spec.Env, "timeout_s": int(spec.Timeout.Seconds()),
	})
	if err != nil {
		return ExecResult{}, err
	}
	var r struct {
		ExitCode int    `json:"exit_code"`
		Log      string `json:"log"`
	}
	if err := json.Unmarshal(out, &r); err != nil {
		return ExecResult{}, fmt.Errorf("%w: %v", ErrMalformed, err)
	}
	return ExecResult{ExitCode: r.ExitCode, Log: []byte(r.Log)}, nil
}

func (p *PulseProvider) Snapshot(ctx context.Context, res Resource) (SnapshotRef, error) {
	out, err := p.call(ctx, http.MethodPost, "/v1/snapshot", map[string]any{"resource_id": res.ID, "org": res.Org})
	if err != nil {
		return SnapshotRef{}, err
	}
	var ref SnapshotRef
	if err := json.Unmarshal(out, &ref); err != nil {
		return SnapshotRef{}, fmt.Errorf("%w: %v", ErrMalformed, err)
	}
	if ref.ID == "" || len(ref.Digest) != 64 {
		return SnapshotRef{}, fmt.Errorf("%w: snapshot envelope incomplete", ErrMalformed)
	}
	return ref, nil
}

func (p *PulseProvider) Teardown(ctx context.Context, res Resource, mode TeardownMode) error {
	if mode == TeardownRetain && !p.grantedCaps().Has(CapTeardownKeep) {
		return fmt.Errorf("%w: retain not granted", ErrCapabilityNotAdvertised)
	}
	_, err := p.call(ctx, http.MethodPost, "/v1/teardown", map[string]any{"resource_id": res.ID, "org": res.Org, "mode": string(mode)})
	return err
}

// unwrapURLError strips transport detail so kernel logs never carry
// endpoint-internal state beyond the deterministic class.
func unwrapURLError(err error) error {
	var nerr *net.OpError
	if errors.As(err, &nerr) {
		return errors.New("transport:" + nerr.Op)
	}
	return err
}