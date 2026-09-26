package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/JonasAbde/works-execution/packages/workgraph"
	"github.com/JonasAbde/works-execution/services/api"
	"github.com/JonasAbde/works-execution/services/work/store"
)

type dropFirstResponseTransport struct {
	base  http.RoundTripper
	calls atomic.Int32
}

func (t *dropFirstResponseTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	n := t.calls.Add(1)
	resp, err := t.base.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	if n == 1 {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		return nil, errors.New("simulated response loss after server handled request")
	}
	return resp, nil
}

func submissionTestServer(t *testing.T) (*httptest.Server, *store.SQLiteStore) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "submission.db"))
	if err != nil {
		t.Fatal(err)
	}
	srv := &api.Server{Store: st}
	ts := httptest.NewServer(srv.Routes())
	t.Cleanup(func() {
		ts.Close()
		_ = st.Close()
	})
	return ts, st
}

func submissionPayload(t *testing.T, key string) []byte {
	t.Helper()
	w := &workgraph.Work{
		ID:             workgraph.NewID("wrk"),
		State:          workgraph.StateCreated,
		IdempotencyKey: key,
		Source: workgraph.Source{
			Type:       "cli",
			Repository: "Aftergraph/reliability",
		},
		Objective: workgraph.Objective{Type: "verify_change"},
		Graph: workgraph.Graph{Nodes: map[string]workgraph.Node{
			"run": {ID: "run", Run: "echo once"},
		}},
	}
	raw, err := json.Marshal(w)
	if err != nil {
		t.Fatal(err)
	}
	return wireCreate(raw)
}

func TestSubmitWorkWithReconcile_ResponseLossRecoversCanonicalWork(t *testing.T) {
	ts, st := submissionTestServer(t)
	transport := &dropFirstResponseTransport{base: http.DefaultTransport}
	client := &http.Client{Transport: transport}

	result, err := submitWorkWithReconcile(
		client,
		ts.URL+"/v1/works",
		submissionPayload(t, "idem-cli-response-loss"),
		"idem-cli-response-loss",
		nil,
	)
	if err != nil {
		t.Fatalf("submit with reconciliation: %v", err)
	}
	if result.StatusCode != http.StatusOK {
		t.Fatalf("reconciled status=%d body=%s", result.StatusCode, string(result.Body))
	}
	if !result.Replay {
		t.Fatal("expected idempotent replay marker after lost response")
	}
	if result.Attempts != 2 {
		t.Fatalf("attempts=%d want 2", result.Attempts)
	}

	var recovered workgraph.Work
	if err := json.Unmarshal(result.Body, &recovered); err != nil {
		t.Fatal(err)
	}
	if recovered.ID == "" {
		t.Fatal("reconciled response missing canonical work id")
	}

	works, err := st.ListWorks(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(works) != 1 {
		t.Fatalf("response-loss retry created duplicate works: count=%d", len(works))
	}
	if works[0].ID != recovered.ID {
		t.Fatalf("canonical id mismatch: store=%s response=%s", works[0].ID, recovered.ID)
	}
}

func TestSubmitWorkWithReconcile_NoKeyDoesNotBlindRetry(t *testing.T) {
	ts, st := submissionTestServer(t)
	transport := &dropFirstResponseTransport{base: http.DefaultTransport}
	client := &http.Client{Transport: transport}

	_, err := submitWorkWithReconcile(
		client,
		ts.URL+"/v1/works",
		submissionPayload(t, ""),
		"",
		nil,
	)
	if err == nil {
		t.Fatal("expected ambiguous no-key submission to fail")
	}
	if got := transport.calls.Load(); got != 1 {
		t.Fatalf("unsafe blind retry count=%d want 1", got)
	}

	// The server did accept the first request. The client intentionally does
	// not guess that fact without an idempotency key.
	works, listErr := st.ListWorks(context.Background(), 10)
	if listErr != nil {
		t.Fatal(listErr)
	}
	if len(works) != 1 {
		t.Fatalf("server accepted work count=%d want 1", len(works))
	}
}


func TestSubmitWorkWithReconcile_AuthEnabled(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "auth-submission.db"))
	if err != nil {
		t.Fatal(err)
	}
	secret := "0123456789abcdef0123456789abcdef"
	srv := &api.Server{
		Store:        st,
		Auth:         api.NewHMACIssuer(),
		AuthEnabled:  true,
		EnrollSecret: secret,
	}
	ts := httptest.NewServer(srv.Routes())
	t.Cleanup(func() {
		ts.Close()
		_ = st.Close()
	})

	auth, err := newCLIAuth(ts.URL, "", secret)
	if err != nil {
		t.Fatalf("enroll cli: %v", err)
	}

	result, err := submitWorkWithReconcile(
		http.DefaultClient,
		ts.URL+"/v1/works",
		submissionPayload(t, "idem-auth-enabled"),
		"idem-auth-enabled",
		auth,
	)
	if err != nil {
		t.Fatalf("authenticated submit: %v", err)
	}
	if result.StatusCode != http.StatusCreated {
		t.Fatalf("authenticated submit status=%d body=%s", result.StatusCode, string(result.Body))
	}
}

func TestSubmitWorkWithReconcile_APIRestartRenewsTokenAndKeepsIdentity(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "auth-restart.db"))
	if err != nil {
		t.Fatal(err)
	}
	secret := "0123456789abcdef0123456789abcdef"
	srv := &api.Server{
		Store:        st,
		Auth:         api.NewHMACIssuer(),
		AuthEnabled:  true,
		EnrollSecret: secret,
	}
	ts := httptest.NewServer(srv.Routes())
	t.Cleanup(func() {
		ts.Close()
		_ = st.Close()
	})

	auth, err := newCLIAuth(ts.URL, "", secret)
	if err != nil {
		t.Fatalf("initial enroll: %v", err)
	}
	oldToken := auth.token

	// Model a replacement controller that persisted an explicit token and also
	// has the enrollment challenge available as a renewal fallback.
	auth, err = newCLIAuth(ts.URL, oldToken, secret)
	if err != nil {
		t.Fatalf("reconstruct explicit auth: %v", err)
	}
	if !auth.canRenew() {
		t.Fatal("explicit token dropped enrollment renewal fallback")
	}

	// API restart semantics: the dev-mode issuer key rotates. The old token
	// is now invalid, but the enrollment challenge remains available.
	srv.Auth = api.NewHMACIssuer()

	result, err := submitWorkWithReconcile(
		http.DefaultClient,
		ts.URL+"/v1/works",
		submissionPayload(t, "idem-auth-restart"),
		"idem-auth-restart",
		auth,
	)
	if err != nil {
		t.Fatalf("submit across auth restart: %v", err)
	}
	if result.StatusCode != http.StatusCreated {
		t.Fatalf("restart recovery status=%d body=%s", result.StatusCode, string(result.Body))
	}
	if result.Attempts != 2 {
		t.Fatalf("restart recovery attempts=%d want 2", result.Attempts)
	}
	if auth.token == oldToken {
		t.Fatal("expected token renewal after issuer rotation")
	}

	works, err := st.ListWorks(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(works) != 1 {
		t.Fatalf("auth recovery created duplicate works: count=%d", len(works))
	}
}


func TestSubmitWorkWithReconcile_NoKeyTransportAmbiguityDoesNotRetryEvenWhenAuthCanRenew(t *testing.T) {
	ts, st := submissionTestServer(t)
	transport := &dropFirstResponseTransport{base: http.DefaultTransport}
	client := &http.Client{Transport: transport}
	auth := &cliAuth{
		api:          ts.URL,
		enrollSecret: "renewal-is-configured-but-must-not-authorize-transport-retry",
	}

	_, err := submitWorkWithReconcile(
		client,
		ts.URL+"/v1/works",
		submissionPayload(t, ""),
		"",
		auth,
	)
	if err == nil {
		t.Fatal("expected ambiguous no-key submission to fail")
	}
	if got := transport.calls.Load(); got != 1 {
		t.Fatalf("auth capability accidentally enabled blind transport retry: calls=%d want 1", got)
	}

	works, listErr := st.ListWorks(context.Background(), 10)
	if listErr != nil {
		t.Fatal(listErr)
	}
	if len(works) != 1 {
		t.Fatalf("server accepted work count=%d want 1", len(works))
	}
}


type stallFirstResponseTransport struct {
	base  http.RoundTripper
	calls atomic.Int32
}

func (t *stallFirstResponseTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	n := t.calls.Add(1)
	resp, err := t.base.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	if n != 1 {
		return resp, nil
	}

	// The server has already handled the request and produced a response, but
	// the controller never receives it. Hold the transport until the client's
	// deadline proves every attempt is actually bounded.
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	<-req.Context().Done()
	return nil, req.Context().Err()
}

func TestSubmitWorkWithReconcile_HalfOpenAcceptedResponseTimesOutAndReconciles(t *testing.T) {
	ts, st := submissionTestServer(t)
	transport := &stallFirstResponseTransport{base: http.DefaultTransport}
	client := &http.Client{Transport: transport, Timeout: 50 * time.Millisecond}

	start := time.Now()
	result, err := submitWorkWithReconcile(
		client,
		ts.URL+"/v1/works",
		submissionPayload(t, "idem-half-open"),
		"idem-half-open",
		nil,
	)
	if err != nil {
		t.Fatalf("half-open reconciliation: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("submission was not bounded: elapsed=%s", elapsed)
	}
	if result.StatusCode != http.StatusOK || !result.Replay {
		t.Fatalf("half-open replay status=%d replay=%v body=%s",
			result.StatusCode, result.Replay, string(result.Body))
	}
	if result.Attempts != 2 {
		t.Fatalf("half-open attempts=%d want 2", result.Attempts)
	}
	if got := transport.calls.Load(); got != 2 {
		t.Fatalf("transport calls=%d want 2", got)
	}

	works, err := st.ListWorks(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(works) != 1 {
		t.Fatalf("half-open recovery created duplicate works: count=%d", len(works))
	}
}


type transientThenRealTransport struct {
	base  http.RoundTripper
	calls atomic.Int32
}

func (t *transientThenRealTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	n := t.calls.Add(1)
	if n <= 2 {
		return &http.Response{
			StatusCode: http.StatusServiceUnavailable,
			Status:     "503 Service Unavailable",
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("transient")),
			Request:    req,
		}, nil
	}
	return t.base.RoundTrip(req)
}

func TestSubmitWorkWithReconcile_AuthRenewalHasSeparateRetrySlot(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "auth-retry-slot.db"))
	if err != nil {
		t.Fatal(err)
	}
	secret := "0123456789abcdef0123456789abcdef"
	srv := &api.Server{
		Store:        st,
		Auth:         api.NewHMACIssuer(),
		AuthEnabled:  true,
		EnrollSecret: secret,
	}
	ts := httptest.NewServer(srv.Routes())
	t.Cleanup(func() {
		ts.Close()
		_ = st.Close()
	})

	auth, err := newCLIAuth(ts.URL, "", secret)
	if err != nil {
		t.Fatalf("initial enroll: %v", err)
	}
	staleToken := auth.token

	// Rotate issuer after enrollment. The first two HTTP attempts are synthetic
	// 503s; the third reaches WORKS with the stale token and gets a definitive
	// pre-mutation 401. Renewal must still have its own fourth HTTP slot.
	srv.Auth = api.NewHMACIssuer()
	transport := &transientThenRealTransport{base: http.DefaultTransport}
	client := &http.Client{Transport: transport, Timeout: 2 * time.Second}

	result, err := submitWorkWithReconcile(
		client,
		ts.URL+"/v1/works",
		submissionPayload(t, "idem-auth-separate-slot"),
		"idem-auth-separate-slot",
		auth,
	)
	if err != nil {
		t.Fatalf("combined transient+401 recovery: %v", err)
	}
	if result.StatusCode != http.StatusCreated {
		t.Fatalf("final status=%d body=%s", result.StatusCode, string(result.Body))
	}
	if got := transport.calls.Load(); got != 4 {
		t.Fatalf("HTTP attempts=%d want 4 (2 transient + 401 + renewed)", got)
	}
	if auth.token == staleToken {
		t.Fatal("stale token was not renewed")
	}
	works, err := st.ListWorks(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(works) != 1 {
		t.Fatalf("combined recovery created %d works want 1", len(works))
	}
}


type captureRecoveryCauseTransport struct {
	base   http.RoundTripper
	calls  atomic.Int32
	causes []string
}

func (t *captureRecoveryCauseTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.causes = append(t.causes, req.Header.Get(api.RecoveryCauseHeader))
	n := t.calls.Add(1)
	resp, err := t.base.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	if n == 1 {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		return nil, errors.New("simulated lost acknowledgement")
	}
	return resp, nil
}

func TestSubmitWorkWithReconcile_AutomaticallyAttributesAmbiguousTransport(t *testing.T) {
	ts, _ := submissionTestServer(t)
	transport := &captureRecoveryCauseTransport{base: http.DefaultTransport}
	client := &http.Client{Transport: transport, Timeout: 2 * time.Second}

	result, err := submitWorkWithReconcile(
		client,
		ts.URL+"/v1/works",
		submissionPayload(t, "idem-auto-cause"),
		"idem-auto-cause",
		nil,
	)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if !result.Replay || result.StatusCode != http.StatusOK {
		t.Fatalf("result=%+v want canonical replay", result)
	}
	if len(transport.causes) != 2 {
		t.Fatalf("captured causes=%v want 2 requests", transport.causes)
	}
	if transport.causes[0] != "" {
		t.Fatalf("first request unexpectedly attributed cause=%q", transport.causes[0])
	}
	if transport.causes[1] != "ambiguous_transport" {
		t.Fatalf("second cause=%q want ambiguous_transport", transport.causes[1])
	}
}

func TestSubmitWorkWithReconcile_ExplicitControllerReconnectCause(t *testing.T) {
	ts, _ := submissionTestServer(t)
	var causes []string
	base := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		causes = append(causes, req.Header.Get(api.RecoveryCauseHeader))
		return http.DefaultTransport.RoundTrip(req)
	})
	client := &http.Client{Transport: base, Timeout: 2 * time.Second}

	result, err := submitWorkWithReconcileCause(
		client,
		ts.URL+"/v1/works",
		submissionPayload(t, "idem-controller-cause"),
		"idem-controller-cause",
		nil,
		"controller_reconnect",
	)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if result.StatusCode != http.StatusCreated {
		t.Fatalf("status=%d body=%s", result.StatusCode, string(result.Body))
	}
	if len(causes) != 1 || causes[0] != "controller_reconnect" {
		t.Fatalf("causes=%v want controller_reconnect", causes)
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}
