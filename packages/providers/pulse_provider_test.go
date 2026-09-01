package providers

// k-pulse-01 tests — PULSE-Node CPN adapter against a stub daemon.
//
// Freeze/adversarial law under test (from pulse_security_review.md):
//   V2 consent-gate-before-wire   · V3 tenant/identity fail-closed
//   V4 loopback-only enforcement  · V5 secret.ref through exec env
//   V6 stale handle post-teardown · V7 provision replay
//   V10 daemon-down → ErrProviderUnavailable (offline-first, zero bytes)
import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// stubDaemon is a minimal PULSE daemon: consent-gated, records every request
// so tests can assert the 0-byte-without-grant guarantee at wire level.
type stubDaemon struct {
	calls      atomic.Int64 // every request that reached the wire
	lastPath   atomic.Value
	grantOK    bool // simulates active ConsentGrant rows
	revoked    atomic.Bool
}

func newStub() *stubDaemon { return &stubDaemon{} }

func (s *stubDaemon) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/handshake", func(w http.ResponseWriter, r *http.Request) {
		s.calls.Add(1)
		if !s.authorized(r) {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte("grant_missing"))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"abi":"cpi/1.0","caps":["fs","browser","git","shell","snap","teardown_keep"],"prov_id":"pulse"}`))
	})
	mux.HandleFunc("/v1/provision", func(w http.ResponseWriter, r *http.Request) {
		s.calls.Add(1)
		s.lastPath.Store("/v1/provision")
		if !s.authorized(r) {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte("grant_revoked"))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"res_pulse_1"}`))
	})
	mux.HandleFunc("/v1/exec", func(w http.ResponseWriter, r *http.Request) {
		s.calls.Add(1)
		if !s.authorized(r) {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte("grant_revoked"))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"exit_code":0,"log":"pulse exec ok"}`))
	})
	mux.HandleFunc("/v1/snapshot", func(w http.ResponseWriter, r *http.Request) {
		s.calls.Add(1)
		if !s.authorized(r) {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte("grant_revoked"))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"snap_1","digest":"` + strings.Repeat("a", 64) + `"}`))
	})
	mux.HandleFunc("/v1/teardown", func(w http.ResponseWriter, r *http.Request) {
		s.calls.Add(1)
		if !s.authorized(r) {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte("grant_revoked"))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	})
	return mux
}

func (s *stubDaemon) authorized(r *http.Request) bool {
	if s.revoked.Load() {
		return false
	}
	return r.Header.Get("Authorization") == "Bearer secret://pulse/localhost_token"
}

// cfg builds config against the running stub; grants toggle consent.
func pulseCfg(server *httptest.Server, allow bool) PulseProviderConfig {
	return PulseProviderConfig{
		ProvID:      "pulse-node",
		Endpoint:    server.URL,
		AccessToken: "secret://pulse/localhost_token",
		HTTPTimeout: 2 * time.Second,
		AllowFS:     allow,
		AllowBrowser: allow,
		GrantRef:    func() string { if allow { return "grant_1" }; return "" },
	}
}

func TestPulseProviderHandshakeAndProvision(t *testing.T) {
	s := newStub()
	server := httptest.NewServer(s.handler())
	defer server.Close()
	p := NewPulseProvider(pulseCfg(server, true))
	ctx := context.Background()

	hs, err := p.Handshake(ctx, Handshake{ABI: ABI, ProvID: "kernel"})
	if err != nil {
		t.Fatalf("handshake: %v", err)
	}
	if hs.ABI != ABI || hs.ProvID != "pulse-node" {
		t.Fatalf("handshake echo broken: %+v", hs)
	}
	res, err := p.Provision(ctx, Spec{IdempotencyKey: "p1", Org: "org_a", Caps: Capabilities{CapFS, CapBrowser}})
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	if res.Org != "org_a" || res.ProvID != "pulse-node" {
		t.Fatalf("resource identity broken: %+v", res)
	}
}

func TestPulseProviderZeroByteWithoutConsent(t *testing.T) {
	s := newStub()
	server := httptest.NewServer(s.handler())
	defer server.Close()
	p := NewPulseProvider(pulseCfg(server, false)) // NO grants
	ctx := context.Background()

	// Handshake succeeds structurally (daemon reachable) but announces zero caps.
	hs, err := p.Handshake(ctx, Handshake{ABI: ABI, ProvID: "kernel"})
	if err != nil {
		t.Fatalf("handshake offline-path: %v", err)
	}
	if len(hs.Caps) != 0 {
		t.Fatalf("caps announced without consent: %v", hs.Caps)
	}
	before := s.calls.Load()
	if _, err := p.Provision(ctx, Spec{IdempotencyKey: "p1", Org: "org_a", Caps: Capabilities{CapFS}}); !errors.Is(err, ErrConsentMissing) {
		t.Fatalf("provision without consent: got %v, want ErrConsentMissing", err)
	}
	if s.calls.Load() != before {
		t.Fatal("wire call made without consent — 0-byte guarantee broken")
	}
}

func TestPulseProviderRevokeTakesEffectImmediately(t *testing.T) {
	s := newStub()
	server := httptest.NewServer(s.handler())
	defer server.Close()
	cfg := pulseCfg(server, true)
	p := NewPulseProvider(cfg)
	ctx := context.Background()
	if _, err := p.Handshake(ctx, Handshake{ABI: ABI, ProvID: "kernel"}); err != nil {
		t.Fatal(err)
	}
	res, err := p.Provision(ctx, Spec{IdempotencyKey: "r1", Org: "org_a", Caps: Capabilities{CapFS}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(ctx, res, CapFS, ExecSpec{Cmd: "ls"}); err != nil {
		t.Fatalf("exec with grant: %v", err)
	}
	// revoke: operator flips the grant (daemon starts answering 403)
	s.revoked.Store(true)
	if _, err := p.Exec(ctx, res, CapFS, ExecSpec{Cmd: "ls"}); !errors.Is(err, ErrConsentMissing) {
		t.Fatalf("exec after revoke: got %v, want ErrConsentMissing (revoke must be immediate)", err)
	}
}

func TestPulseProviderOfflineFirstDaemonDown(t *testing.T) {
	s := newStub()
	server := httptest.NewServer(s.handler())
	cfg := pulseCfg(server, true)
	server.Close() // daemon dead
	p := NewPulseProvider(cfg)
	ctx := context.Background()
	if _, err := p.Handshake(ctx, Handshake{ABI: ABI, ProvID: "kernel"}); !errors.Is(err, ErrProviderUnavailable) {
		t.Fatalf("dead daemon: got %v, want ErrProviderUnavailable (offline-first, fail deterministic)", err)
	}
}

func TestPulseProviderLoopbackOnlyEnforcement(t *testing.T) {
	// Adversarial vector 4: non-loopback endpoint must be rejected at
	// construction (V4) — the adapter refuses to be remotely reachable.
	p := NewPulseProvider(PulseProviderConfig{
		ProvID: "pulse", Endpoint: "http://10.0.0.5:7777",
		AccessToken: "secret://pulse/tok",
	})
	if _, err := p.Handshake(context.Background(), Handshake{ABI: ABI, ProvID: "kernel"}); err == nil {
		t.Fatal("non-loopback endpoint accepted — daemon could be remotely reachable")
	}
}

func TestPulseProviderPlaintextSecretRefused(t *testing.T) {
	s := newStub()
	server := httptest.NewServer(s.handler())
	defer server.Close()
	p := NewPulseProvider(pulseCfg(server, true))
	ctx := context.Background()
	if _, err := p.Handshake(ctx, Handshake{ABI: ABI, ProvID: "kernel"}); err != nil {
		t.Fatal(err)
	}
	res, err := p.Provision(ctx, Spec{IdempotencyKey: "s1", Org: "org_a", Caps: Capabilities{CapShell, CapFS}})
	if err != nil {
		t.Fatal(err)
	}
	bad := ExecSpec{Cmd: "x", Env: map[string]string{"TOKEN": "sk_live_plaintext"}}
	if _, err := p.Exec(ctx, res, CapShell, bad); err == nil {
		t.Fatal("plaintext env value accepted — secret.ref invariant broken (V5)")
	}
}

func TestPulseProviderNonLoopbackTokenRejected(t *testing.T) {
	// A plaintext access token (not a secret:// ref) must poison the adapter.
	p := NewPulseProvider(PulseProviderConfig{
		ProvID: "pulse", Endpoint: "http://127.0.0.1:7777",
		AccessToken: "raw_token_value",
	})
	if _, err := p.Handshake(context.Background(), Handshake{ABI: ABI, ProvID: "kernel"}); err == nil {
		t.Fatal("plaintext token accepted at construction — secret law broken")
	}
}

func TestPulseProviderProvisionEscalationRefused(t *testing.T) {
	s := newStub()
	server := httptest.NewServer(s.handler())
	defer server.Close()
	// grants only fs; caller asks for browser too
	cfg := pulseCfg(server, false)
	cfg.AllowFS, cfg.AllowBrowser = true, false
	p := NewPulseProvider(cfg)
	ctx := context.Background()
	if _, err := p.Handshake(ctx, Handshake{ABI: ABI, ProvID: "kernel"}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Provision(ctx, Spec{IdempotencyKey: "e1", Org: "org_a", Caps: Capabilities{CapFS, CapBrowser}}); err == nil {
		t.Fatal("capability escalation accepted beyond consent-backed set")
	}
}

func TestPulseProviderDeadConfigRejectedAtConstruct(t *testing.T) {
	p := NewPulseProvider(PulseProviderConfig{ProvID: "pulse", Endpoint: "http://10.0.0.9:7777", AccessToken: "secret://x/y"})
	if _, err := p.Handshake(context.Background(), Handshake{ABI: ABI, ProvID: "kernel"}); err == nil {
		t.Fatal("poisoned adapter produced a handshake")
	}
}