package api_test

// Handler tests for the Runtime → WORKS dispatch acceptance seam
// (POST /v1/works/{id}/accept, contract:dispatch.acceptance/1.0).
//
// The wire response is validated against the FROZEN contract file
// (contracts/schemas/dispatch.acceptance.schema.json), not against the DTO
// struct, so the contract is the gate: if the handler ever drifts from
// dispatch.acceptance/1.0 the schema rejects it here.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v5"

	"github.com/JonasAbde/works-execution/internal/dispatch"
	"github.com/JonasAbde/works-execution/packages/workgraph"
	"github.com/JonasAbde/works-execution/services/api"
)

// dispatchMemStore is the in-memory durability seam for the API handler test.
// It mirrors internal/dispatch's own memoryStore so the handler exercises the
// real Acceptor semantics (AcceptIfAbsent winner, correlation stability across
// replay) without a database.
type dispatchMemStore struct {
	mu    sync.Mutex
	byKey map[string]*dispatch.Acceptance
	byEx  map[string]*dispatch.Acceptance
}

func newDispatchMemStore() *dispatchMemStore {
	return &dispatchMemStore{byKey: map[string]*dispatch.Acceptance{}, byEx: map[string]*dispatch.Acceptance{}}
}

func cloneDispatchAcceptForTest(a *dispatch.Acceptance) *dispatch.Acceptance {
	if a == nil {
		return nil
	}
	cp := *a
	if a.Verdict != nil {
		v := *a.Verdict
		cp.Verdict = &v
	}
	return &cp
}

func (m *dispatchMemStore) LoadByIdempotency(key string) (*dispatch.Acceptance, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return cloneDispatchAcceptForTest(m.byKey[key]), nil
}

func (m *dispatchMemStore) LoadByExecution(id string) (*dispatch.Acceptance, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return cloneDispatchAcceptForTest(m.byEx[id]), nil
}

func (m *dispatchMemStore) AcceptIfAbsent(a *dispatch.Acceptance) (*dispatch.Acceptance, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if existing := m.byKey[a.Dispatch.IdempotencyKey]; existing != nil {
		return cloneDispatchAcceptForTest(existing), nil
	}
	cp := cloneDispatchAcceptForTest(a)
	m.byKey[a.Dispatch.IdempotencyKey] = cp
	m.byEx[a.WorksExecutionID] = cp
	return cloneDispatchAcceptForTest(cp), nil
}

func (m *dispatchMemStore) Save(a *dispatch.Acceptance) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := cloneDispatchAcceptForTest(a)
	m.byKey[a.Dispatch.IdempotencyKey] = cp
	m.byEx[a.WorksExecutionID] = cp
	return nil
}

// setupDispatchAccept wires the surface over a real Acceptor + memory store and
// returns a created work's id. currentEpoch nil leaves the resolver unset, so
// the surface fail-closes 503 — the production state until authority
// integration.
func setupDispatchAccept(t *testing.T, currentEpoch func() int64) (*api.Server, *httptest.Server, string) {
	t.Helper()
	srv, ts, st := newTestServer(t)
	w := &workgraph.Work{
		ID: workgraph.NewID("wrk"), State: workgraph.StateCreated,
		Source: workgraph.Source{Type: "cli"}, Objective: workgraph.Objective{Type: "verify_change"},
		Graph: workgraph.Graph{Nodes: map[string]workgraph.Node{"a": {ID: "a", Run: "true"}}},
	}
	if err := st.CreateWork(context.Background(), w); err != nil {
		t.Fatal(err)
	}
	srv.Dispatch = &api.DispatchConfig{
		Acceptor: dispatch.NewAcceptor(newDispatchMemStore(), func() time.Time {
			return time.Date(2026, 9, 19, 0, 0, 0, 0, time.UTC)
		}),
		CurrentEpoch: currentEpoch,
	}
	return srv, ts, w.ID
}

func dispatchAcceptBody() string {
	return `{"mission_id":"mission/headroom-001","authority_ref":"auth/value","authority_epoch":7,"runtime_dispatch_id":"rdisp/1","attempt_id":"attempt/1","effect_id":"effect/1","idempotency_key":"idem/1","budget_ref":"budget/1","budget_ceiling":100,"checkpoint_id":"checkpoint/1","evidence_root":"evidence/1","verification_subject":"subject/1","causal_id":"causal/1"}`
}

func compileDispatchAcceptanceSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate test file")
	}
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "..", "contracts", "schemas", "dispatch.acceptance.schema.json"))
	if err != nil {
		t.Fatalf("read acceptance schema: %v", err)
	}
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("contract:dispatch.acceptance/1.0", strings.NewReader(string(raw))); err != nil {
		t.Fatalf("compile acceptance schema: %v", err)
	}
	return compiler.MustCompile("contract:dispatch.acceptance/1.0")
}

// An unconfigured surface (s.Dispatch == nil — the default newTestServer state)
// must fail closed 503, never accept.
func TestDispatchAccept_FailClosedWithoutConfig(t *testing.T) {
	_, ts, st := newTestServer(t)
	w := &workgraph.Work{
		ID: workgraph.NewID("wrk"), State: workgraph.StateCreated,
		Source: workgraph.Source{Type: "cli"}, Objective: workgraph.Objective{Type: "verify_change"},
		Graph: workgraph.Graph{Nodes: map[string]workgraph.Node{"a": {ID: "a", Run: "true"}}},
	}
	if err := st.CreateWork(context.Background(), w); err != nil {
		t.Fatal(err)
	}
	resp, err := http.Post(ts.URL+"/v1/works/"+w.ID+"/accept", "application/json", strings.NewReader(dispatchAcceptBody()))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status: got %d want 503", resp.StatusCode)
	}
}

// A configured Acceptor but a nil authority-epoch resolver must ALSO fail closed
// 503 — this is the exact production state (WORKS has no resolver today), and
// the seam refuses to run the staleness guard against a fiction.
func TestDispatchAccept_FailClosedWithoutEpochResolver(t *testing.T) {
	_, ts, workID := setupDispatchAccept(t, nil)
	resp, err := http.Post(ts.URL+"/v1/works/"+workID+"/accept", "application/json", strings.NewReader(dispatchAcceptBody()))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status: got %d want 503", resp.StatusCode)
	}
}

// Happy path: with a resolver present, accept mints the correlation pair and the
// wire response conforms to the frozen dispatch.acceptance/1.0 contract.
func TestDispatchAccept_HappyPathMintsCorrelation(t *testing.T) {
	_, ts, workID := setupDispatchAccept(t, func() int64 { return 0 })
	resp, err := http.Post(ts.URL+"/v1/works/"+workID+"/accept", "application/json", strings.NewReader(dispatchAcceptBody()))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d want 200", resp.StatusCode)
	}
	var dto map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&dto); err != nil {
		t.Fatal(err)
	}
	// The contract is the gate — not the DTO struct.
	if err := compileDispatchAcceptanceSchema(t).Validate(dto); err != nil {
		t.Fatalf("response does not conform to dispatch.acceptance/1.0: %v", err)
	}
	ctxID, _ := dto["execution_context_id"].(string)
	trcID, _ := dto["trace_id"].(string)
	if !regexp.MustCompile(`^ctx_[a-f0-9]{32}$`).MatchString(ctxID) {
		t.Fatalf("execution_context_id malformed: %q", ctxID)
	}
	if !regexp.MustCompile(`^trc_[a-f0-9]{32}$`).MatchString(trcID) {
		t.Fatalf("trace_id malformed: %q", trcID)
	}
}

// Idempotent replay of the same key must return the SAME correlation pair — the
// seal never remints on retry, so the execution context is stable across replay.
func TestDispatchAccept_IdempotentReplaySameCorrelation(t *testing.T) {
	_, ts, workID := setupDispatchAccept(t, func() int64 { return 0 })
	post := func() map[string]any {
		resp, err := http.Post(ts.URL+"/v1/works/"+workID+"/accept", "application/json", strings.NewReader(dispatchAcceptBody()))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status=%d", resp.StatusCode)
		}
		var dto map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&dto); err != nil {
			t.Fatal(err)
		}
		return dto
	}
	first := post()
	second := post()
	if first["execution_context_id"] != second["execution_context_id"] || first["trace_id"] != second["trace_id"] {
		t.Fatalf("replay reminted correlation: first=%v second=%v", first, second)
	}
}

// WORKS mints the correlation identity; clients cannot choose it. An injected
// execution_context_id must be rejected at the boundary (DisallowUnknownFields),
// not silently discarded.
func TestDispatchAccept_RejectsClientInjectedCorrelation(t *testing.T) {
	_, ts, workID := setupDispatchAccept(t, func() int64 { return 0 })
	body := strings.TrimSuffix(dispatchAcceptBody(), "}") + `,"execution_context_id":"ctx_11111111111111111111111111111111"}`
	resp, err := http.Post(ts.URL+"/v1/works/"+workID+"/accept", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status: got %d want 400 (client cannot choose correlation)", resp.StatusCode)
	}
}

// A dispatch whose authority epoch is below the server's current epoch fails
// closed 409 — the staleness guard is real once a resolver is wired.
func TestDispatchAccept_StaleAuthorityEpoch409(t *testing.T) {
	_, ts, workID := setupDispatchAccept(t, func() int64 { return 10 })
	resp, err := http.Post(ts.URL+"/v1/works/"+workID+"/accept", "application/json", strings.NewReader(dispatchAcceptBody()))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status: got %d want 409", resp.StatusCode)
	}
}

// The {id} route scope is validated: accepting against a nonexistent work 404s
// rather than orphaning a dispatch.
func TestDispatchAccept_WorkNotFound404(t *testing.T) {
	_, ts, _ := setupDispatchAccept(t, func() int64 { return 0 })
	resp, err := http.Post(ts.URL+"/v1/works/wrk_ffffffffffffffffffffffffffffffff/accept", "application/json", strings.NewReader(dispatchAcceptBody()))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status: got %d want 404", resp.StatusCode)
	}
}

// A dispatch missing its binding fails closed 400.
func TestDispatchAccept_MissingBinding400(t *testing.T) {
	_, ts, workID := setupDispatchAccept(t, func() int64 { return 0 })
	body := strings.Replace(dispatchAcceptBody(), `"mission_id":"mission/headroom-001"`, `"mission_id":""`, 1)
	resp, err := http.Post(ts.URL+"/v1/works/"+workID+"/accept", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status: got %d want 400", resp.StatusCode)
	}
}

// Non-POST on the accept route is 405, not a fall-through to the work-item handler.
func TestDispatchAccept_NonPost405(t *testing.T) {
	_, ts, workID := setupDispatchAccept(t, func() int64 { return 0 })
	resp, err := http.Get(ts.URL + "/v1/works/" + workID + "/accept")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("status: got %d want 405", resp.StatusCode)
	}
}
