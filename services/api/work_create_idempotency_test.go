package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/JonasAbde/works-execution/packages/workgraph"
)

func idempotentCreateBody(id, key, run string, queue bool) []byte {
	body := map[string]any{
		"id": id,
		"idempotency_key": key,
		"queue": queue,
		"source": map[string]any{"type": "controller", "repository": "Aftergraph/reliability"},
		"objective": map[string]any{"type": "verify_change"},
		"graph": map[string]any{"nodes": map[string]any{
			"run": map[string]any{"id": "run", "run": run},
		}},
		"requirements": map[string]any{"os": "linux"},
		"policy": map[string]any{},
	}
	raw, _ := json.Marshal(body)
	return raw
}

func postWorkRaw(t *testing.T, url string, body []byte) (*http.Response, []byte) {
	t.Helper()
	resp, err := http.Post(url+"/v1/works", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	return resp, raw
}

func decodeCreatedWork(t *testing.T, raw []byte) workgraph.Work {
	t.Helper()
	var w workgraph.Work
	if err := json.Unmarshal(raw, &w); err != nil {
		t.Fatalf("decode work: %v body=%s", err, string(raw))
	}
	return w
}

func TestCreateWork_IdempotentReplayRecoversCanonicalWork(t *testing.T) {
	_, ts, st := newTestServer(t)
	key := "idem-replay-1"

	firstResp, firstRaw := postWorkRaw(t, ts.URL,
		idempotentCreateBody("wrk_replay_a", key, "echo once", true))
	if firstResp.StatusCode != http.StatusCreated {
		t.Fatalf("first submit: status=%d body=%s", firstResp.StatusCode, string(firstRaw))
	}
	first := decodeCreatedWork(t, firstRaw)

	secondResp, secondRaw := postWorkRaw(t, ts.URL,
		idempotentCreateBody("wrk_replay_b", key, "echo once", true))
	if secondResp.StatusCode != http.StatusOK {
		t.Fatalf("replay: status=%d body=%s", secondResp.StatusCode, string(secondRaw))
	}
	if got := secondResp.Header.Get("X-Works-Idempotent-Replay"); got != "true" {
		t.Fatalf("replay header=%q want true", got)
	}
	second := decodeCreatedWork(t, secondRaw)
	if second.ID != first.ID {
		t.Fatalf("replay changed work id: first=%s second=%s", first.ID, second.ID)
	}

	works, err := st.ListWorks(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(works) != 1 {
		t.Fatalf("replay created duplicate work: count=%d", len(works))
	}
}

func TestCreateWork_IdempotencyKeyChangedIntentFailsClosed(t *testing.T) {
	_, ts, _ := newTestServer(t)
	key := "idem-conflict-1"

	firstResp, firstRaw := postWorkRaw(t, ts.URL,
		idempotentCreateBody("wrk_conflict_a", key, "echo original", true))
	if firstResp.StatusCode != http.StatusCreated {
		t.Fatalf("first submit: status=%d body=%s", firstResp.StatusCode, string(firstRaw))
	}

	conflictResp, conflictRaw := postWorkRaw(t, ts.URL,
		idempotentCreateBody("wrk_conflict_b", key, "echo changed", true))
	if conflictResp.StatusCode != http.StatusConflict {
		t.Fatalf("changed intent: status=%d body=%s", conflictResp.StatusCode, string(conflictRaw))
	}
	if !bytes.Contains(conflictRaw, []byte("idempotency_conflict")) {
		t.Fatalf("missing conflict code: %s", string(conflictRaw))
	}
}
