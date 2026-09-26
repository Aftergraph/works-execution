package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/JonasAbde/works-execution/internal/manifest"
	"github.com/JonasAbde/works-execution/packages/workgraph"
	"github.com/JonasAbde/works-execution/services/api"
	"github.com/JonasAbde/works-execution/services/work/store"
)

func proofServer(tb testing.TB) (*httptest.Server, store.Store) {
	tb.Helper()
	st, err := store.Open(filepath.Join(tb.TempDir(), "proof.db"))
	if err != nil {
		tb.Fatal(err)
	}
	srv := &api.Server{Store: st}
	ts := httptest.NewServer(srv.Routes())
	tb.Cleanup(func() {
		ts.Close()
		_ = st.Close()
	})
	return ts, st
}

func proofBody(id, key, run string, queue, explicitDefaults bool, permissions []string) []byte {
	node := map[string]any{
		"id":  "run",
		"run": run,
	}
	if permissions != nil {
		node["permissions"] = permissions
	}
	if explicitDefaults {
		node["timeout_s"] = manifest.DefaultTimeoutSeconds
		node["permissions"] = []string{"read"}
		node["retries"] = map[string]any{
			"max_attempts": manifest.DefaultRetryMaxAttempts,
			"backoff":      manifest.DefaultBackoff,
		}
		node["cache_spec"] = map[string]any{
			"enabled": false,
			"scope":   manifest.DefaultCacheScope,
		}
	}
	body := map[string]any{
		"id":              id,
		"idempotency_key": key,
		"queue":           queue,
		"source": map[string]any{
			"type":       "proof",
			"repository": "Aftergraph/works-execution",
		},
		"objective": map[string]any{"type": "verify_change"},
		"graph": map[string]any{
			"nodes": map[string]any{"run": node},
		},
		"requirements": map[string]any{"os": "linux"},
		"policy":       map[string]any{},
	}
	raw, _ := json.Marshal(body)
	return raw
}

func proofPost(tb testing.TB, url string, body []byte) (*http.Response, []byte) {
	tb.Helper()
	resp, err := http.Post(url+"/v1/works", "application/json", bytes.NewReader(body))
	if err != nil {
		tb.Fatal(err)
	}
	raw, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		tb.Fatal(err)
	}
	return resp, raw
}

func decodeProofWork(tb testing.TB, raw []byte) workgraph.Work {
	tb.Helper()
	var w workgraph.Work
	if err := json.Unmarshal(raw, &w); err != nil {
		tb.Fatalf("decode work: %v body=%s", err, string(raw))
	}
	return w
}

// AFTER expectation: retrying the same immutable creation intent under the
// same idempotency key returns the canonical accepted Work instead of 409.
func TestProofLostAckReplay(t *testing.T) {
	ts, st := proofServer(t)
	key := "proof-lost-ack"

	firstResp, firstRaw := proofPost(t, ts.URL,
		proofBody(workgraph.NewID("wrk"), key, "echo proof", true, false, nil))
	if firstResp.StatusCode != http.StatusCreated {
		t.Fatalf("first status=%d body=%s", firstResp.StatusCode, string(firstRaw))
	}
	first := decodeProofWork(t, firstRaw)

	replayResp, replayRaw := proofPost(t, ts.URL,
		proofBody(workgraph.NewID("wrk"), key, "echo proof", true, false, nil))
	if replayResp.StatusCode != http.StatusOK {
		t.Fatalf("replay status=%d body=%s want 200", replayResp.StatusCode, string(replayRaw))
	}
	if replayResp.Header.Get("X-Works-Idempotent-Replay") != "true" {
		t.Fatalf("missing replay marker")
	}
	replayed := decodeProofWork(t, replayRaw)
	if replayed.ID != first.ID {
		t.Fatalf("canonical id changed: first=%s replay=%s", first.ID, replayed.ID)
	}
	works, err := st.ListWorks(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(works) != 1 {
		t.Fatalf("duplicate durable Works=%d want 1", len(works))
	}
}

// AFTER expectation: an already-accepted Work remains recoverable even when
// today's admission policy no longer accepts a fresh equivalent submission.
func TestProofReplaySurvivesAdmissionPolicyDrift(t *testing.T) {
	ts, _ := proofServer(t)
	key := "proof-policy-drift"
	bodyA := proofBody(workgraph.NewID("wrk"), key, "echo drift", false, false, []string{"write"})

	firstResp, firstRaw := proofPost(t, ts.URL, bodyA)
	if firstResp.StatusCode != http.StatusCreated {
		t.Fatalf("first status=%d body=%s", firstResp.StatusCode, string(firstRaw))
	}
	first := decodeProofWork(t, firstRaw)

	oldAllowed := append([]string(nil), manifest.AllowedPermissions...)
	filtered := make([]string, 0, len(oldAllowed))
	for _, p := range oldAllowed {
		if p != "write" {
			filtered = append(filtered, p)
		}
	}
	manifest.AllowedPermissions = filtered
	defer func() { manifest.AllowedPermissions = oldAllowed }()

	replayResp, replayRaw := proofPost(t, ts.URL,
		proofBody(workgraph.NewID("wrk"), key, "echo drift", false, false, []string{"write"}))
	if replayResp.StatusCode != http.StatusOK {
		t.Fatalf("policy-drift replay status=%d body=%s want 200", replayResp.StatusCode, string(replayRaw))
	}
	replayed := decodeProofWork(t, replayRaw)
	if replayed.ID != first.ID {
		t.Fatalf("policy-drift replay changed id: first=%s replay=%s", first.ID, replayed.ID)
	}
}

// AFTER expectation: omission and explicit spelling of acceptance-time
// defaults reconcile to the same canonical Work.
func TestProofExplicitDefaultsReplay(t *testing.T) {
	ts, _ := proofServer(t)
	key := "proof-defaults"

	firstResp, firstRaw := proofPost(t, ts.URL,
		proofBody(workgraph.NewID("wrk"), key, "echo defaults", false, false, nil))
	if firstResp.StatusCode != http.StatusCreated {
		t.Fatalf("first status=%d body=%s", firstResp.StatusCode, string(firstRaw))
	}
	first := decodeProofWork(t, firstRaw)

	replayResp, replayRaw := proofPost(t, ts.URL,
		proofBody(workgraph.NewID("wrk"), key, "echo defaults", false, true, nil))
	if replayResp.StatusCode != http.StatusOK {
		t.Fatalf("explicit-default replay status=%d body=%s want 200", replayResp.StatusCode, string(replayRaw))
	}
	replayed := decodeProofWork(t, replayRaw)
	if replayed.ID != first.ID {
		t.Fatalf("explicit-default replay changed id: first=%s replay=%s", first.ID, replayed.ID)
	}
}

// AFTER expectation: reconciliation returns current canonical state rather
// than replaying the originally submitted projection.
func TestProofReplayReturnsCurrentCanonicalState(t *testing.T) {
	ts, st := proofServer(t)
	key := "proof-current-state"

	firstResp, firstRaw := proofPost(t, ts.URL,
		proofBody(workgraph.NewID("wrk"), key, "echo state", true, false, nil))
	if firstResp.StatusCode != http.StatusCreated {
		t.Fatalf("first status=%d body=%s", firstResp.StatusCode, string(firstRaw))
	}
	first := decodeProofWork(t, firstRaw)
	if _, err := st.UpdateState(context.Background(), first.ID, workgraph.StateRunning); err != nil {
		t.Fatalf("advance state: %v", err)
	}

	replayResp, replayRaw := proofPost(t, ts.URL,
		proofBody(workgraph.NewID("wrk"), key, "echo state", true, false, nil))
	if replayResp.StatusCode != http.StatusOK {
		t.Fatalf("current-state replay status=%d body=%s want 200", replayResp.StatusCode, string(replayRaw))
	}
	replayed := decodeProofWork(t, replayRaw)
	if replayed.State != workgraph.StateRunning {
		t.Fatalf("replay state=%s want RUNNING", replayed.State)
	}
}

// Safety should hold both before and after: a caller must not be able to turn
// an originally non-queued accepted Work into executable Work by replay alone.
func TestProofQueueEscalationFailsClosed(t *testing.T) {
	ts, st := proofServer(t)
	key := "proof-queue-escalation"

	firstResp, firstRaw := proofPost(t, ts.URL,
		proofBody(workgraph.NewID("wrk"), key, "echo queue", false, false, nil))
	if firstResp.StatusCode != http.StatusCreated {
		t.Fatalf("first status=%d body=%s", firstResp.StatusCode, string(firstRaw))
	}
	first := decodeProofWork(t, firstRaw)

	replayResp, replayRaw := proofPost(t, ts.URL,
		proofBody(workgraph.NewID("wrk"), key, "echo queue", true, false, nil))
	if replayResp.StatusCode != http.StatusConflict {
		t.Fatalf("queue escalation status=%d body=%s want 409", replayResp.StatusCode, string(replayRaw))
	}
	canonical, err := st.GetWork(context.Background(), first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if canonical.State != workgraph.StateCreated {
		t.Fatalf("queue escalation mutated state=%s want CREATED", canonical.State)
	}
}

func BenchmarkProofCreateUnique(b *testing.B) {
	ts, _ := proofServer(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		body := proofBody(workgraph.NewID("wrk"), "", "echo normal", false, false, nil)
		resp, raw := proofPost(b, ts.URL, body)
		if resp.StatusCode != http.StatusCreated {
			b.Fatalf("status=%d body=%s", resp.StatusCode, string(raw))
		}
	}
}

func BenchmarkProofIdempotentPair(b *testing.B) {
	ts, _ := proofServer(b)
	var replay200, conflict409 int
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		key := fmt.Sprintf("bench-key-%d", i)
		first, raw := proofPost(b, ts.URL,
			proofBody(workgraph.NewID("wrk"), key, "echo replay", false, false, nil))
		if first.StatusCode != http.StatusCreated {
			b.Fatalf("first status=%d body=%s", first.StatusCode, string(raw))
		}
		second, _ := proofPost(b, ts.URL,
			proofBody(workgraph.NewID("wrk"), key, "echo replay", false, false, nil))
		switch second.StatusCode {
		case http.StatusOK:
			replay200++
		case http.StatusConflict:
			conflict409++
		default:
			b.Fatalf("unexpected duplicate status=%d", second.StatusCode)
		}
	}
	b.ReportMetric(100*float64(replay200)/float64(b.N), "replay200_%")
	b.ReportMetric(100*float64(conflict409)/float64(b.N), "conflict409_%")
}
