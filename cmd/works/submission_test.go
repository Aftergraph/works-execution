package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"

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
