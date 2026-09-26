package api_test

import (
	"errors"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"

	"github.com/JonasAbde/works-execution/internal/manifest"
	"github.com/JonasAbde/works-execution/packages/workgraph"
	"github.com/JonasAbde/works-execution/services/api"
	"github.com/JonasAbde/works-execution/services/work/store"
)

type failQueueOnceStore struct {
	store.Store
	fail bool
}

func (s *failQueueOnceStore) UpdateState(ctx context.Context, id string, to workgraph.State) (*workgraph.Work, error) {
	if s.fail && to == workgraph.StateQueued {
		s.fail = false
		return nil, errors.New("simulated crash seam before queue transition")
	}
	return s.Store.UpdateState(ctx, id, to)
}

func TestCreateWork_ReplayRepairsProvenOriginalQueueIntent(t *testing.T) {
	sqlite, err := store.Open(filepath.Join(t.TempDir(), "queue-repair.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer sqlite.Close()

	wrapped := &failQueueOnceStore{Store: sqlite, fail: true}
	srv := &api.Server{Store: wrapped}
	ts := httptest.NewServer(srv.Routes())
	defer ts.Close()

	key := "idem-stranded-created"
	body := idempotentCreateBody("wrk_stranded_a", key, "echo stranded", true)

	// First submission persists Work + queue_requested=true, then the injected
	// transition failure leaves canonical state CREATED.
	firstResp, firstRaw := postWorkRaw(t, ts.URL, body)
	if firstResp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("first submit: status=%d body=%s want 500", firstResp.StatusCode, string(firstRaw))
	}
	accepted, err := sqlite.GetWorkByIdempotencyKey(context.Background(), key)
	if err != nil {
		t.Fatalf("lookup accepted work: %v", err)
	}
	if accepted.State != workgraph.StateCreated || accepted.QueueRequested == nil || !*accepted.QueueRequested {
		t.Fatalf("partial accept state=%s queue_requested=%v", accepted.State, accepted.QueueRequested)
	}

	replayResp, replayRaw := postWorkRaw(t, ts.URL, body)
	if replayResp.StatusCode != http.StatusOK {
		t.Fatalf("replay: status=%d body=%s", replayResp.StatusCode, string(replayRaw))
	}
	replayed := decodeCreatedWork(t, replayRaw)
	if replayed.ID != accepted.ID {
		t.Fatalf("canonical work changed: accepted=%s replay=%s", accepted.ID, replayed.ID)
	}
	if replayed.State != workgraph.StateQueued {
		t.Fatalf("proven queue replay left work stranded: state=%s want QUEUED", replayed.State)
	}
}

func TestCreateWork_ReplayCannotChangeOriginalQueueDecision(t *testing.T) {
	_, ts, st := newTestServer(t)
	key := "idem-queue-intent-mismatch"

	firstResp, firstRaw := postWorkRaw(t, ts.URL,
		idempotentCreateBody("wrk_queue_false", key, "echo queue-intent", false))
	if firstResp.StatusCode != http.StatusCreated {
		t.Fatalf("first submit: status=%d body=%s", firstResp.StatusCode, string(firstRaw))
	}
	first := decodeCreatedWork(t, firstRaw)
	if first.State != workgraph.StateCreated {
		t.Fatalf("first state=%s want CREATED", first.State)
	}

	replayResp, replayRaw := postWorkRaw(t, ts.URL,
		idempotentCreateBody("wrk_queue_true", key, "echo queue-intent", true))
	if replayResp.StatusCode != http.StatusConflict {
		t.Fatalf("queue intent mutation: status=%d body=%s want 409", replayResp.StatusCode, string(replayRaw))
	}

	canonical, err := st.GetWork(context.Background(), first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if canonical.State != workgraph.StateCreated {
		t.Fatalf("mismatched replay launched work: state=%s want CREATED", canonical.State)
	}
}

func TestCreateWork_ReplayBypassesChangedAdmissionPolicy(t *testing.T) {
	_, ts, _ := newTestServer(t)
	key := "idem-policy-drift"

	bodyFor := func(id string) []byte {
		body := map[string]any{
			"id": id,
			"idempotency_key": key,
			"queue": true,
			"source": map[string]any{"type": "controller", "repository": "Aftergraph/reliability"},
			"objective": map[string]any{"type": "verify_change"},
			"graph": map[string]any{"nodes": map[string]any{
				"run": map[string]any{
					"id": "run", "run": "echo policy", "permissions": []string{"write"},
				},
			}},
			"requirements": map[string]any{"os": "linux"},
			"policy": map[string]any{},
		}
		raw, _ := json.Marshal(body)
		return raw
	}

	firstResp, firstRaw := postWorkRaw(t, ts.URL, bodyFor("wrk_policy_a"))
	if firstResp.StatusCode != http.StatusCreated {
		t.Fatalf("first submit: status=%d body=%s", firstResp.StatusCode, string(firstRaw))
	}
	first := decodeCreatedWork(t, firstRaw)

	oldPermissions := append([]string(nil), manifest.AllowedPermissions...)
	manifest.AllowedPermissions = []string{"read", "execute", "network", "secrets", "privileged"}
	defer func() { manifest.AllowedPermissions = oldPermissions }()

	replayResp, replayRaw := postWorkRaw(t, ts.URL, bodyFor("wrk_policy_b"))
	if replayResp.StatusCode != http.StatusOK {
		t.Fatalf("accepted work was re-admitted under changed policy: status=%d body=%s",
			replayResp.StatusCode, string(replayRaw))
	}
	replayed := decodeCreatedWork(t, replayRaw)
	if replayed.ID != first.ID {
		t.Fatalf("policy-drift replay changed canonical id: first=%s replay=%s", first.ID, replayed.ID)
	}
}

func TestCreateWork_OptionalEmptyCollectionsAreSameIntent(t *testing.T) {
	_, ts, _ := newTestServer(t)
	key := "idem-empty-equivalence"

	firstResp, firstRaw := postWorkRaw(t, ts.URL,
		idempotentCreateBody("wrk_empty_a", key, "echo empty", true))
	if firstResp.StatusCode != http.StatusCreated {
		t.Fatalf("first submit: status=%d body=%s", firstResp.StatusCode, string(firstRaw))
	}

	body := map[string]any{
		"id": "wrk_empty_b",
		"idempotency_key": key,
		"queue": true,
		"source": map[string]any{"type": "controller", "repository": "Aftergraph/reliability"},
		"objective": map[string]any{"type": "verify_change"},
		"graph": map[string]any{"nodes": map[string]any{
			"run": map[string]any{
				"id": "run", "run": "echo empty",
				"needs": []string{},
				"permissions": []string{},
				"side_effects": []string{},
				"env": map[string]string{},
			},
		}},
		"requirements": map[string]any{"os": "linux"},
		"policy": map[string]any{"secrets_scope": []string{}},
	}
	raw, _ := json.Marshal(body)
	replayResp, replayRaw := postWorkRaw(t, ts.URL, raw)
	if replayResp.StatusCode != http.StatusOK {
		t.Fatalf("representation-only replay conflicted: status=%d body=%s",
			replayResp.StatusCode, string(replayRaw))
	}
}

type barrierLookupStore struct {
	store.Store
	sqlite *store.SQLiteStore

	mu       sync.Mutex
	lookups  int
	released chan struct{}
}

func (s *barrierLookupStore) GetWorkByIdempotencyKey(ctx context.Context, key string) (*workgraph.Work, error) {
	s.mu.Lock()
	if s.lookups < 2 {
		s.lookups++
		ch := s.released
		if s.lookups == 2 {
			close(ch)
		}
		s.mu.Unlock()
		select {
		case <-ch:
			return nil, store.ErrNotFound
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	s.mu.Unlock()
	return s.sqlite.GetWorkByIdempotencyKey(ctx, key)
}

func TestCreateWork_ConcurrentSameIDChangedIntentCannotReturnUnstoredPayload(t *testing.T) {
	sqlite, err := store.Open(filepath.Join(t.TempDir(), "same-id-race.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer sqlite.Close()

	wrapped := &barrierLookupStore{
		Store: sqlite,
		sqlite: sqlite,
		released: make(chan struct{}),
	}
	srv := &api.Server{Store: wrapped}
	ts := httptest.NewServer(srv.Routes())
	defer ts.Close()

	key := "idem-same-id-race"
	sharedID := "wrk_same_id_race"

	type result struct {
		status int
		body   []byte
	}
	results := make(chan result, 2)
	post := func(run string) {
		resp, err := http.Post(ts.URL+"/v1/works", "application/json",
			bytes.NewReader(idempotentCreateBody(sharedID, key, run, true)))
		if err != nil {
			results <- result{status: 0, body: []byte(err.Error())}
			return
		}
		raw, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		results <- result{status: resp.StatusCode, body: raw}
	}
	go post("echo race-a")
	go post("echo race-b")

	a, b := <-results, <-results
	statuses := map[int]int{a.status: 1}
	statuses[b.status]++

	if statuses[http.StatusCreated] != 1 || statuses[http.StatusConflict] != 1 {
		t.Fatalf("same-ID race statuses=%d,%d bodies=%s | %s; want one 201 and one 409",
			a.status, b.status, string(a.body), string(b.body))
	}

	works, err := sqlite.ListWorks(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(works) != 1 {
		t.Fatalf("same-ID race persisted %d works want 1", len(works))
	}
}


func TestCreateWork_NestedAdmissionDefaultsAreSameIntent(t *testing.T) {
	_, ts, _ := newTestServer(t)
	key := "idem-nested-defaults"

	bodyFor := func(id string, explicitDefaults bool) []byte {
		node := map[string]any{
			"id": "run",
			"run": "echo nested-defaults",
			"retries": map[string]any{"max_attempts": 3},
			"cache_spec": map[string]any{"enabled": true},
		}
		if explicitDefaults {
			node["retries"] = map[string]any{
				"max_attempts": 3,
				"backoff": "exponential",
			}
			node["cache_spec"] = map[string]any{
				"enabled": true,
				"scope": "organization",
			}
		}
		body := map[string]any{
			"id": id,
			"idempotency_key": key,
			"queue": true,
			"source": map[string]any{"type": "controller", "repository": "Aftergraph/reliability"},
			"objective": map[string]any{"type": "verify_change"},
			"graph": map[string]any{"nodes": map[string]any{"run": node}},
			"requirements": map[string]any{"os": "linux"},
			"policy": map[string]any{},
		}
		raw, _ := json.Marshal(body)
		return raw
	}

	firstResp, firstRaw := postWorkRaw(t, ts.URL, bodyFor("wrk_nested_a", false))
	if firstResp.StatusCode != http.StatusCreated {
		t.Fatalf("first submit: status=%d body=%s", firstResp.StatusCode, string(firstRaw))
	}
	first := decodeCreatedWork(t, firstRaw)

	replayResp, replayRaw := postWorkRaw(t, ts.URL, bodyFor("wrk_nested_b", true))
	if replayResp.StatusCode != http.StatusOK {
		t.Fatalf("nested-default replay conflicted: status=%d body=%s",
			replayResp.StatusCode, string(replayRaw))
	}
	replayed := decodeCreatedWork(t, replayRaw)
	if replayed.ID != first.ID {
		t.Fatalf("nested-default replay changed canonical id: first=%s replay=%s", first.ID, replayed.ID)
	}
}
