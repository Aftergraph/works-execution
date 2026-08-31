// Package auth_test covers k-impl-003 Zero-Secret worker enrollment.
//
// Tests run against a real HTTP server (httptest.NewServer) backed by
// the production API code path, so they exercise the middleware stack,
// the enrollment handler, and the JWT verifier together. The signing
// key is supplied explicitly via NewHMACIssuerWithKey so tests are
// deterministic; production never takes this path.
package auth_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JonasAbde/works-execution/internal/worker"
	"github.com/JonasAbde/works-execution/services/api"
	"github.com/JonasAbde/works-execution/services/work/store"
)

// testKey is the deterministic HMAC key used by all tests in this
// package. It is NOT a real secret — just bytes that make signatures
// reproducible across runs.
var testKey = []byte("k-impl-003 deterministic test key 32b!!!")

const (
	testEnrollSecret = "operator-shared-challenge-do-not-leak"
)

// newServer constructs an api.Server with:
//   - a temp SQLite store
//   - a deterministic HMACIssuer
//   - WORKS_ENROLL_SECRET=testEnrollSecret
//
// and returns the live httptest.Server. Tests can hit the real HTTP
// surface (incl. middleware) end-to-end.
func newServer(t *testing.T) (*httptest.Server, *api.Server) {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "auth.db"))
	if err != nil {
		t.Fatal(err)
	}
	srv := &api.Server{
		Store:        s,
		Auth:         api.NewHMACIssuerWithKey(testKey),
		EnrollSecret: testEnrollSecret,
		AuthEnabled:  true,
	}
	ts := httptest.NewServer(srv.Routes())
	t.Cleanup(func() {
		ts.Close()
		_ = s.Close()
	})
	return ts, srv
}

// enroll performs POST /v1/workers/enroll and returns the bearer token.
func enroll(t *testing.T, ts *httptest.Server, workerID, challenge string) string {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"worker_id":   workerID,
		"challenge":   challenge,
		"ttl_seconds": 600,
	})
	resp, err := http.Post(ts.URL+"/v1/workers/enroll", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("enroll: %s: %s", resp.Status, b)
	}
	var out struct {
		Token     string `json:"token"`
		WorkerID  string `json:"worker_id"`
		Scope     string `json:"scope"`
		TokenType string `json:"token_type"`
		ExpiresIn int    `json:"expires_in"`
		KeyID     string `json:"kid"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Token == "" || out.WorkerID != workerID || out.TokenType != "Bearer" {
		t.Fatalf("enroll: bad response: %+v", out)
	}
	return out.Token
}

// -----------------------------------------------------------------------
// Enrollment tests
// -----------------------------------------------------------------------

func TestEnroll_HappyPath(t *testing.T) {
	ts, _ := newServer(t)
	tok := enroll(t, ts, "wrkr_alice", testEnrollSecret)
	if tok == "" {
		t.Fatal("empty token")
	}
	// token should be three dot-separated parts (JWS compact)
	if n := strings.Count(tok, "."); n != 2 {
		t.Fatalf("expected 2 dots in JWT, got %d in %q", n, tok)
	}
}

func TestEnroll_BadChallenge_401(t *testing.T) {
	ts, _ := newServer(t)
	body, _ := json.Marshal(map[string]any{
		"worker_id": "wrkr_alice",
		"challenge": "wrong-secret",
	})
	resp, err := http.Post(ts.URL+"/v1/workers/enroll", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status: got %d, want 401", resp.StatusCode)
	}
}

func TestEnroll_EnrollmentDisabled_503(t *testing.T) {
	// Build a server with EnrollSecret="" — enrollment must fail closed.
	s, err := store.Open(filepath.Join(t.TempDir(), "no-enroll.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	srv := &api.Server{
		Store:        s,
		Auth:         api.NewHMACIssuerWithKey(testKey),
		EnrollSecret: "", // intentionally disabled
	}
	ts := httptest.NewServer(srv.Routes())
	defer ts.Close()

	body, _ := json.Marshal(map[string]any{
		"worker_id": "wrkr_alice",
		"challenge": "anything",
	})
	resp, err := http.Post(ts.URL+"/v1/workers/enroll", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status: got %d, want 503", resp.StatusCode)
	}
}

func TestEnroll_BadWorkerID_400(t *testing.T) {
	ts, _ := newServer(t)
	for _, bad := range []string{"", " ", "wrkr with spaces", "wrkr;DROP", strings.Repeat("a", 200)} {
		body, _ := json.Marshal(map[string]any{
			"worker_id": bad,
			"challenge": testEnrollSecret,
		})
		resp, err := http.Post(ts.URL+"/v1/workers/enroll", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("worker_id=%q: status=%d want 400", bad, resp.StatusCode)
		}
	}
}

// -----------------------------------------------------------------------
// Middleware enforcement
// -----------------------------------------------------------------------

func TestMiddleware_LeasesRequireBearer(t *testing.T) {
	ts, _ := newServer(t)
	// No token.
	resp, err := http.Post(ts.URL+"/v1/leases", "application/json",
		strings.NewReader(`{"work_id":"x","node_id":"y","worker_id":"z"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no-token: status=%d want 401", resp.StatusCode)
	}

	// Wrong scheme.
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/v1/leases", nil)
	req.Header.Set("Authorization", "Basic foo:bar")
	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusUnauthorized {
		t.Fatalf("basic auth: status=%d want 401", resp2.StatusCode)
	}
}

func TestMiddleware_WorkersRequireBearer(t *testing.T) {
	ts, _ := newServer(t)
	// /v1/workers/ready with no token → 401
	resp, err := http.Get(ts.URL + "/v1/workers/ready")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status=%d want 401", resp.StatusCode)
	}

	// /v1/workers/ready with a valid token → 200 (no ready items → still 200)
	tok := enroll(t, ts, "wrkr_bob", testEnrollSecret)
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/v1/workers/ready", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp2.Body)
		t.Fatalf("with-token: status=%d want 200; body=%s", resp2.StatusCode, b)
	}
}

func TestMiddleware_HealthzUnauthenticated(t *testing.T) {
	// /healthz and /v1/works/* are operator surface and stay open in V1.
	ts, _ := newServer(t)
	resp, err := http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("healthz: status=%d want 200", resp.StatusCode)
	}
}

// -----------------------------------------------------------------------
// Token verification edge cases (low-level; no HTTP)
// -----------------------------------------------------------------------

func TestVerify_BadSignature(t *testing.T) {
	// Mint with one key, verify with another — must reject.
	mint := api.NewHMACIssuerWithKey([]byte("key-a-mint-32-bytes-please!"))
	verify := api.NewHMACIssuerWithKey([]byte("key-b-verify-32-bytes!!!!!"))
	tok, err := mint.Mint(context.Background(), "wrkr_carol", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := verify.Verify(context.Background(), tok); err == nil {
		t.Fatal("expected verification to fail with mismatched key")
	}
}

func TestVerify_Expired(t *testing.T) {
	iss := api.NewHMACIssuerWithKey(testKey)
	// Force the clock to the past.
	iss.SetClock(func() time.Time { return time.Now().Add(-2 * time.Hour) })
	tok, err := iss.Mint(context.Background(), "wrkr_dave", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	iss.SetClock(time.Now) // restore for Verify
	if _, err := iss.Verify(context.Background(), tok); err == nil {
		t.Fatal("expected expired-token rejection")
	}
}

func TestVerify_MalformedTokens(t *testing.T) {
	iss := api.NewHMACIssuerWithKey(testKey)
	for _, bad := range []string{
		"",
		"not-a-jwt",
		"only.two",
		"a.b.c.d",
		// header says alg=none — must be rejected
		"eyJhbGciOiJub25lIiwidHlwIjoiSldUIn0.eyJ3b3JrZXJfaWQiOiJ4In0.",
	} {
		if _, err := iss.Verify(context.Background(), bad); err == nil {
			t.Fatalf("expected reject for %q", bad)
		}
	}
}

// -----------------------------------------------------------------------
// End-to-end: worker client enroll → poll with bearer → 200
// -----------------------------------------------------------------------

func TestWorkerClient_EnrollThenReady(t *testing.T) {
	ts, _ := newServer(t)
	cli := &worker.Client{
		BaseURL: ts.URL,
		HTTP:    &http.Client{Timeout: 5 * time.Second},
	}
	tok, err := cli.Enroll(context.Background(), "wrkr_eve", testEnrollSecret, 5*time.Minute)
	if err != nil {
		t.Fatalf("enroll: %v", err)
	}
	if tok == "" {
		t.Fatal("empty token")
	}
	cli.Token = tok
	items, err := cli.Ready(context.Background())
	if err != nil {
		t.Fatalf("ready: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("expected 0 ready items on empty store, got %d", len(items))
	}
}

func TestWorkerClient_NoTokenIsRejected(t *testing.T) {
	ts, _ := newServer(t)
	cli := &worker.Client{
		BaseURL: ts.URL,
		HTTP:    &http.Client{Timeout: 5 * time.Second},
		// no Token — should hit 401
	}
	_, err := cli.Ready(context.Background())
	if err == nil {
		t.Fatal("expected error from unauthenticated /v1/workers/ready")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Fatalf("expected 401 in error, got: %v", err)
	}
}

// -----------------------------------------------------------------------
// AuthZ on leases: invalid bearer for POST /v1/leases
// -----------------------------------------------------------------------

func TestLeases_Grant_RejectsTamperedToken(t *testing.T) {
	ts, _ := newServer(t)
	tok := enroll(t, ts, "wrkr_frank", testEnrollSecret)
	// Tamper with the signature segment.
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatal("bad token shape")
	}
	tampered := parts[0] + "." + parts[1] + "." + "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/v1/leases",
		strings.NewReader(`{"work_id":"x","node_id":"y","worker_id":"z"}`))
	req.Header.Set("Authorization", "Bearer "+tampered)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status=%d want 401; body=%s", resp.StatusCode, b)
	}
}

// -----------------------------------------------------------------------
// Smoke: a fresh process gets a fresh signing key (Zero-Secret rotation)
// -----------------------------------------------------------------------

func TestZeroSecret_PerProcessKeyRotation(t *testing.T) {
	a := api.NewHMACIssuer()
	b := api.NewHMACIssuer()
	if a.KeyID() == b.KeyID() {
		t.Fatal("two issuers produced the same key id; per-process rotation broken")
	}
	tok, err := a.Mint(context.Background(), "wrkr_grace", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Verify(context.Background(), tok); err == nil {
		t.Fatal("issuer B accepted a token minted by issuer A; keys are not isolated")
	}
	// sanity: issuer A does accept its own token
	if _, err := a.Verify(context.Background(), tok); err != nil {
		t.Fatalf("issuer A rejected its own token: %v", err)
	}
	_ = fmt.Sprintf // keep fmt import warm under -trimpath builds
}
