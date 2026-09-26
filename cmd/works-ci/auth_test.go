package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/JonasAbde/works-execution/packages/protocol"
)

func TestNewAuthForUsesRegistryWorkerID(t *testing.T) {
	var workerID string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		workerID, _ = body["worker_id"].(string)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"token":"test-token"}`))
	}))
	defer server.Close()

	if _, err := newAuthFor(server.URL, "challenge"); err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^wrkr_[a-z0-9_-]{1,64}$`).MatchString(workerID) {
		t.Fatalf("worker_id %q does not match registry contract", workerID)
	}
}


type ciDropFirstResponseTransport struct {
	base   http.RoundTripper
	calls  atomic.Int32
	causes []string
}

func (t *ciDropFirstResponseTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.causes = append(t.causes, req.Header.Get(protocol.RecoveryCauseHeader))
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

func TestPostWorkResilientRecoversLostAcknowledgement(t *testing.T) {
	var accepted atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/works" {
			http.NotFound(w, r)
			return
		}
		if accepted.Swap(true) {
			w.Header().Set("X-Works-Idempotent-Replay", "true")
			w.WriteHeader(http.StatusOK)
		} else {
			w.WriteHeader(http.StatusCreated)
		}
		_, _ = w.Write([]byte(`{"id":"wrk_ci_replay","state":"QUEUED"}`))
	}))
	defer server.Close()

	transport := &ciDropFirstResponseTransport{base: http.DefaultTransport}
	oldTransport := http.DefaultTransport
	http.DefaultTransport = transport
	defer func() { http.DefaultTransport = oldTransport }()

	auth := &apiAuth{api: server.URL, token: "token"}
	var out struct {
		ID string `json:"id"`
	}
	result, err := auth.postWorkResilient("/v1/works", map[string]any{
		"idempotency_key": "idem-ci-lost-ack",
	}, "idem-ci-lost-ack", &out)
	if err != nil {
		t.Fatalf("resilient submit: %v", err)
	}
	if !result.Replay || result.StatusCode != http.StatusOK || out.ID != "wrk_ci_replay" {
		t.Fatalf("unexpected result=%+v out=%+v", result, out)
	}
	if got := transport.calls.Load(); got != 2 {
		t.Fatalf("calls=%d want 2", got)
	}
	if len(transport.causes) != 2 || transport.causes[0] != "" ||
		transport.causes[1] != protocol.RecoveryCauseAmbiguousTransport {
		t.Fatalf("causes=%v", transport.causes)
	}
}

func TestPostWorkResilientRenewsAfterDefinitive401(t *testing.T) {
	var enrollCalls atomic.Int32
	var workCalls atomic.Int32
	var seenCauses []string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/workers/enroll":
			n := enrollCalls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"token":"token-` + string(rune('0'+n)) + `"}`))
		case "/v1/works":
			seenCauses = append(seenCauses, r.Header.Get(protocol.RecoveryCauseHeader))
			n := workCalls.Add(1)
			if n == 1 {
				http.Error(w, "stale token", http.StatusUnauthorized)
				return
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"wrk_ci_auth","state":"QUEUED"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	auth := &apiAuth{api: server.URL, token: "stale", enrollSecret: "challenge"}
	var out map[string]any
	result, err := auth.postWorkResilient("/v1/works", map[string]any{
		"idempotency_key": "idem-auth",
	}, "idem-auth", &out)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if result.StatusCode != http.StatusCreated || workCalls.Load() != 2 || enrollCalls.Load() != 1 {
		t.Fatalf("result=%+v workCalls=%d enrollCalls=%d", result, workCalls.Load(), enrollCalls.Load())
	}
	if len(seenCauses) != 2 || seenCauses[0] != "" || seenCauses[1] != protocol.RecoveryCauseAuthRenewal {
		t.Fatalf("causes=%v", seenCauses)
	}
}

func TestPostWorkResilientBoundsTransientRetries(t *testing.T) {
	var calls atomic.Int32
	var causes []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		causes = append(causes, r.Header.Get(protocol.RecoveryCauseHeader))
		n := calls.Add(1)
		if n < 3 {
			http.Error(w, "transient", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"wrk_ci_transient","state":"QUEUED"}`))
	}))
	defer server.Close()

	auth := &apiAuth{api: server.URL, token: "token"}
	var out map[string]any
	start := time.Now()
	result, err := auth.postWorkResilient("/v1/works", map[string]any{
		"idempotency_key": "idem-transient",
	}, "idem-transient", &out)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if result.StatusCode != http.StatusCreated || calls.Load() != 3 {
		t.Fatalf("result=%+v calls=%d", result, calls.Load())
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("bounded retry took unexpectedly long")
	}
	if len(causes) != 3 || causes[0] != "" ||
		causes[1] != protocol.RecoveryCauseTransientStatus ||
		causes[2] != protocol.RecoveryCauseTransientStatus {
		t.Fatalf("causes=%v", causes)
	}
}

func TestPostWorkResilientDoesNotRetryPermanentConflict(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, "conflict", http.StatusConflict)
	}))
	defer server.Close()

	auth := &apiAuth{api: server.URL, token: "token"}
	_, err := auth.postWorkResilient("/v1/works", map[string]any{
		"idempotency_key": "idem-conflict",
	}, "idem-conflict", nil)
	if err == nil || !strings.Contains(err.Error(), "status=409") {
		t.Fatalf("err=%v want 409", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("permanent conflict retried: calls=%d", calls.Load())
	}
}
