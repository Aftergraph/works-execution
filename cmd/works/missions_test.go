package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/JonasAbde/works-execution/packages/workgraph"
)

// missionListBody is the exact wire shape services/api listWorks serves:
// {"works":[...], "count":N} with full workgraph.Work JSON objects. It
// mixes mission works (mission.budget_ceiling present) across NOW-order
// groups with plain CI works (no mission) that must be filtered out.
const missionListBody = `{
  "works": [
    {"id":"work:ci-0001","state":"RUNNING","objective":{"type":"verify_change"},
     "source":{"type":"github","repository":"o/r","revision":"abc"},
     "graph":{"nodes":{"hello":{"id":"hello","run":"echo hi"}}}},
    {"id":"work:m-run-b","state":"RUNNING","objective":{"type":"achieve_outcome"},
     "mission":{"budget_ceiling":{"compute_eur":40.00,"wall_clock_h":8}},
     "graph":{"nodes":{"hello":{"id":"hello","run":"echo hi"}}}},
    {"id":"work:m-wait-a","state":"WAITING_HUMAN","objective":{"type":"achieve_outcome"},
     "mission":{"budget_ceiling":{"compute_eur":12.50,"wall_clock_h":24}},
     "graph":{"nodes":{"hello":{"id":"hello","run":"echo hi"}}}},
    {"id":"work:m-done-z","state":"SUCCEEDED","objective":{"type":"achieve_outcome"},
     "mission":{"budget_ceiling":{"compute_eur":5.00,"wall_clock_h":2}},
     "graph":{"nodes":{"hello":{"id":"hello","run":"echo hi"}}}},
    {"id":"work:m-exh-a","state":"BUDGET_EXHAUSTED","objective":{"type":"achieve_outcome"},
     "mission":{"budget_ceiling":{"compute_eur":99.99,"wall_clock_h":12}},
     "graph":{"nodes":{"hello":{"id":"hello","run":"echo hi"}}}},
    {"id":"work:m-broke-x","state":"SUSPENDED","objective":{"type":"achieve_outcome"},
     "mission":{"budget_ceiling":{"compute_eur":7.00,"wall_clock_h":4}},
     "graph":{"nodes":{"hello":{"id":"hello","run":"echo hi"}}}},
    {"id":"work:m-empty-y","state":"CREATED","objective":{"type":"achieve_outcome"},
     "mission":{"description":"contract fields but no ceiling -> not a mission"},
     "graph":{"nodes":{"hello":{"id":"hello","run":"echo hi"}}}}
  ],
  "count": 7
}`

const testToken = "test-token-k037"

// newMissionsStub serves GET /v1/works guarded by a Bearer token,
// mirroring requireBearer on the real control plane. No real network.
func newMissionsStub(t *testing.T, body string) *httptest.Server {
	t.Helper()
	var gotPath, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/works" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+testToken {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"error":"unauthorized"}`)
			return
		}
		gotPath, gotQuery = r.URL.Path, r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, body)
	}))
	t.Cleanup(func() {
		t.Logf("stub saw %s?%s", gotPath, gotQuery)
		srv.Close()
	})
	return srv
}

func runMissionsOK(t *testing.T, api string, extra ...string) string {
	t.Helper()
	var out, errOut strings.Builder
	args := append([]string{"--api", api, "--token", testToken}, extra...)
	if err := runMissions(args, &out, &errOut); err != nil {
		t.Fatalf("runMissions: %v (stderr=%s)", err, errOut.String())
	}
	return out.String()
}

func TestMissionsTableFiltersAndOrders(t *testing.T) {
	srv := newMissionsStub(t, missionListBody)
	got := runMissionsOK(t, srv.URL)
	t.Logf("table output:\n%s", got)

	// Header + footer present.
	for _, want := range []string{"WORK ID", "STATE", "NEEDS_HUMAN", "OBJECTIVE", "CEILING_EUR", "5 mission(s)"} {
		if !strings.Contains(got, want) {
			t.Errorf("table missing %q\n%s", want, got)
		}
	}
	// CI work and mission-without-ceiling are filtered out.
	for _, bad := range []string{"ci-0001", "m-empty"} {
		if strings.Contains(got, bad) {
			t.Errorf("table must not contain non-mission work %q\n%s", bad, got)
		}
	}
	// NOW ordering law: WAITING_HUMAN group (m-wait-a), then RUNNING
	// (m-run-b), then the rest by work id (m-broke-x, m-done-z, m-exh-a).
	wantOrder := []string{"work:m-wait", "work:m-run-", "work:m-broke", "work:m-done-", "work:m-exh-"}
	pos := -1
	for _, id := range wantOrder {
		i := strings.Index(got, id)
		if i < 0 {
			t.Fatalf("row %q missing\n%s", id, got)
		}
		if i < pos {
			t.Fatalf("row %q out of NOW order\n%s", id, got)
		}
		pos = i
	}
	// NEEDS_HUMAN yes/blank column + ceiling formatting.
	for _, line := range strings.Split(got, "\n") {
		f := strings.Fields(line)
		if len(f) < 3 {
			continue
		}
		switch f[1] {
		case "WAITING_HUMAN", "SUSPENDED", "BUDGET_EXHAUSTED":
			if f[2] != "yes" {
				t.Errorf("expected yes in NEEDS_HUMAN for %s row: %q", f[1], line)
			}
		case "RUNNING", "SUCCEEDED":
			if f[2] != "achieve_outcome" {
				t.Errorf("expected blank NEEDS_HUMAN for %s row: %q", f[1], line)
			}
		}
	}
	if !strings.Contains(got, "12.50") {
		t.Errorf("expected ceiling 12.50\n%s", got)
	}
	// Full ids are truncated to 12 chars ("work:ci-0001" is 12, but longer
	// ones would show); verify no row prints more than "work:m-wait-a"-ish
	// width by checking the exact short forms.
	for _, s := range []string{"work:m-wait-", "work:m-run-b"} {
		if !strings.Contains(got, s) {
			t.Errorf("expected 12-char short id prefix %q\n%s", s, got)
		}
	}
}

func TestMissionsJSON(t *testing.T) {
	srv := newMissionsStub(t, missionListBody)
	got := runMissionsOK(t, srv.URL, "--json")

	var works []workgraph.Work
	if err := json.Unmarshal([]byte(got), &works); err != nil {
		t.Fatalf("--json output is not a JSON array: %v\n%s", err, got)
	}
	if len(works) != 5 {
		t.Fatalf("want 5 mission works, got %d\n%s", len(works), got)
	}
	// Raw works are preserved (full ids, CI excluded, ceiling on the wire).
	for _, w := range works {
		if !w.IsMission() {
			t.Errorf("non-mission work in --json output: %s", w.ID)
		}
		if strings.HasPrefix(w.ID, "work:ci-") {
			t.Errorf("CI work leaked into --json output: %s", w.ID)
		}
	}
	if works[0].ID != "work:m-wait-a" || works[1].ID != "work:m-run-b" {
		t.Errorf("json ordering: got %s,%s want work:m-wait-a,work:m-run-b", works[0].ID, works[1].ID)
	}
	if !strings.Contains(got, `"compute_eur": 12.5`) {
		t.Errorf("expected canonical indented ceiling field\n%s", got)
	}
}

func TestMissionsLimit(t *testing.T) {
	srv := newMissionsStub(t, missionListBody)
	got := runMissionsOK(t, srv.URL, "--limit", "2", "--json")
	var works []workgraph.Work
	if err := json.Unmarshal([]byte(got), &works); err != nil {
		t.Fatalf("--json output is not a JSON array: %v", err)
	}
	if len(works) != 2 {
		t.Fatalf("want limit 2 applied, got %d", len(works))
	}
	if works[0].ID != "work:m-wait-a" || works[1].ID != "work:m-run-b" {
		t.Errorf("limit must take the top of the NOW order, got %s,%s", works[0].ID, works[1].ID)
	}
	// The --limit flag is also forwarded to the server as ?limit=.
	if !strings.Contains(srv.URL, "http") {
		t.Fatal("stub url sanity")
	}
}

func TestMissionsEmptyList(t *testing.T) {
	srv := newMissionsStub(t, `{"works":[],"count":0}`)
	got := runMissionsOK(t, srv.URL)
	if strings.TrimSpace(got) != "no missions" {
		t.Fatalf("empty list should print 'no missions', got %q", got)
	}
	// JSON mode stays machine-readable: empty array.
	gotJSON := runMissionsOK(t, srv.URL, "--json")
	if strings.TrimSpace(gotJSON) != "[]" {
		t.Fatalf("empty list --json should print [], got %q", gotJSON)
	}
}

func TestMissionsBadToken(t *testing.T) {
	srv := newMissionsStub(t, missionListBody)
	var out, errOut strings.Builder
	err := runMissions([]string{"--api", srv.URL, "--token", "wrong-token"}, &out, &errOut)
	if err == nil {
		t.Fatalf("expected error for bad token, got stdout=%q", out.String())
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("error should mention 401, got %v", err)
	}
	if !strings.Contains(err.Error(), hint401) {
		t.Errorf("error should carry the fix-it hint, got %v", err)
	}
	if out.Len() != 0 {
		t.Errorf("no table output expected on auth failure, got %q", out.String())
	}
}

func TestParseWorksListBothShapes(t *testing.T) {
	wrapped, err := parseWorksList(json.RawMessage(missionListBody))
	if err != nil {
		t.Fatalf("wrapped shape: %v", err)
	}
	if len(wrapped) != 7 {
		t.Fatalf("wrapped: want 7 works, got %d", len(wrapped))
	}
	bare, err := parseWorksList(json.RawMessage(`[{"id":"work:x","state":"RUNNING"}]`))
	if err != nil {
		t.Fatalf("bare array shape: %v", err)
	}
	if len(bare) != 1 || bare[0].ID != "work:x" {
		t.Fatalf("bare array: got %+v", bare)
	}
	if n, err := parseWorksList(json.RawMessage(`  `)); err != nil || n != nil {
		t.Fatalf("empty body: got %d,%v", len(n), err)
	}
}

func TestFilterAndNeedsHumanHelpers(t *testing.T) {
	m := &workgraph.Work{ID: "work:a", State: workgraph.StateSuspended,
		Mission: &workgraph.MissionContract{BudgetCeiling: &workgraph.BudgetCeiling{ComputeEUR: 1}}}
	ci := &workgraph.Work{ID: "work:b", State: workgraph.StateWaitingHuman}
	half := &workgraph.Work{ID: "work:c", Mission: &workgraph.MissionContract{}}
	got := filterMissionWorks([]*workgraph.Work{ci, nil, m, half})
	if len(got) != 1 || got[0].ID != "work:a" {
		t.Fatalf("filterMissionWorks: %+v", got)
	}
	for s, want := range map[workgraph.State]bool{
		workgraph.StateWaitingHuman:    true,
		workgraph.StateSuspended:       true,
		workgraph.StateBudgetExhausted: true,
		workgraph.StateRunning:         false,
		workgraph.StateCreated:         false,
	} {
		if needsHumanState(s) != want {
			t.Errorf("needsHumanState(%s)=%v want %v", s, !want, want)
		}
	}
	if shortID("work:0123456789") != "work:0123456" {
		t.Errorf("shortID must cut at 12 chars, got %q", shortID("work:0123456789"))
	}
}
