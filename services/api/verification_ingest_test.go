package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/JonasAbde/works-execution/packages/workgraph"
	"github.com/JonasAbde/works-execution/services/work/store"
)

const verifierToken = "sentinel-verifier-token-0123456789abcdef"

func seedVerificationWork(t *testing.T, st store.Store) *workgraph.Work {
	t.Helper()
	w := &workgraph.Work{ID: workgraph.NewID("wrk"), Source: workgraph.Source{Type: "cli"}, Objective: workgraph.Objective{Type: "verify_change"}, Graph: workgraph.Graph{Nodes: map[string]workgraph.Node{"a": {ID: "a", Run: "true"}}}}
	if err := st.CreateWork(context.Background(), w); err != nil {
		t.Fatal(err)
	}
	return w
}

func seedTerminalVerificationWork(t *testing.T, st store.Store) *workgraph.Work {
	t.Helper()
	w := seedVerificationWork(t, st)
	ctx := context.Background()
	if _, err := st.UpdateState(ctx, w.ID, workgraph.StateQueued); err != nil {
		t.Fatal(err)
	}
	lease, _, err := st.GrantLease(ctx, w.ID, "a", "worker:verify", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	art := &workgraph.Artifact{ID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", NodeID: "a", MimeType: "text/plain", Size: 1, Path: "/tmp/verify"}
	if _, err := st.CompleteLease(ctx, lease.ID, 0, art, nil); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetWork(ctx, w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.State.IsTerminal() {
		if _, err := st.UpdateState(ctx, w.ID, workgraph.StateSucceeded); err != nil {
			t.Fatal(err)
		}
	}
	got, err = st.GetWork(ctx, w.ID)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func postVerification(t *testing.T, base, workID, token string, body any) *http.Response {
	t.Helper()
	raw, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, base+"/v1/works/"+workID+"/verification", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("X-WORKS-Verifier-Token", token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}
func validVerificationBody() map[string]any {
	return map[string]any{
		"result":       "passed",
		"verifier_id":  "sentinel:domain-verifier",
		"evidence_ref": "dvr_" + string(bytes.Repeat([]byte("a"), 64)),
		"verified_at":  "2026-09-16T00:30:00Z",
	}
}

func TestVerificationIngest_UnconfiguredFailsClosed(t *testing.T) {
	_, ts, st := newTestServer(t)
	w := seedVerificationWork(t, st)
	resp := postVerification(t, ts.URL, w.ID, verifierToken, validVerificationBody())
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want 503", resp.StatusCode)
	}
}

func TestVerificationIngest_RequiresDedicatedToken(t *testing.T) {
	srv, ts, st := newTestServer(t)
	srv.VerifierToken = []byte(verifierToken)
	w := seedVerificationWork(t, st)
	for _, tok := range []string{"", "wrong-token"} {
		resp := postVerification(t, ts.URL, w.ID, tok, validVerificationBody())
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("token=%q status=%d want 401", tok, resp.StatusCode)
		}
	}
}
func TestVerificationIngest_PersistsImmutableSentinelVerdict(t *testing.T) {
	srv, ts, st := newTestServer(t)
	srv.VerifierToken = []byte(verifierToken)
	w := seedTerminalVerificationWork(t, st)
	body := validVerificationBody()
	resp := postVerification(t, ts.URL, w.ID, verifierToken, body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d want 200", resp.StatusCode)
	}
	got, err := st.GetVerificationVerdict(context.Background(), w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("missing stored verdict")
	}
	if got.Result != "passed" || got.VerifierID != "sentinel:domain-verifier" || got.EvidenceRef != body["evidence_ref"] {
		t.Fatalf("stored=%+v", got)
	}
	want, _ := time.Parse(time.RFC3339, "2026-09-16T00:30:00Z")
	if !got.VerifiedAt.Equal(want) {
		t.Fatalf("verified_at=%s", got.VerifiedAt)
	}

	resp2 := postVerification(t, ts.URL, w.ID, verifierToken, body)
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("idempotent retry status=%d", resp2.StatusCode)
	}

	conflict := validVerificationBody()
	conflict["result"] = "failed"
	resp3 := postVerification(t, ts.URL, w.ID, verifierToken, conflict)
	resp3.Body.Close()
	if resp3.StatusCode != http.StatusConflict {
		t.Fatalf("conflict status=%d want 409", resp3.StatusCode)
	}
}
func TestVerificationIngest_RejectsUntrustedShapes(t *testing.T) {
	srv, ts, st := newTestServer(t)
	srv.VerifierToken = []byte(verifierToken)
	w := seedVerificationWork(t, st)
	cases := []map[string]any{
		{"result": "indeterminate", "verifier_id": "sentinel:domain-verifier", "evidence_ref": "dvr_" + string(bytes.Repeat([]byte("a"), 64)), "verified_at": "2026-09-16T00:30:00Z"},
		{"result": "passed", "verifier_id": "agent:self", "evidence_ref": "dvr_" + string(bytes.Repeat([]byte("a"), 64)), "verified_at": "2026-09-16T00:30:00Z"},
		{"result": "passed", "verifier_id": "sentinel:domain-verifier", "evidence_ref": "receipt_not_sentinel", "verified_at": "2026-09-16T00:30:00Z"},
		{"result": "passed", "verifier_id": "sentinel:domain-verifier", "evidence_ref": "dvr_" + string(bytes.Repeat([]byte("a"), 64)), "verified_at": "not-time"},
	}
	for i, body := range cases {
		resp := postVerification(t, ts.URL, w.ID, verifierToken, body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("case %d status=%d want 400", i, resp.StatusCode)
		}
	}
}

func TestVerificationIngest_UnknownWork404(t *testing.T) {
	srv, ts, _ := newTestServer(t)
	srv.VerifierToken = []byte(verifierToken)
	resp := postVerification(t, ts.URL, "wrk_missing", verifierToken, validVerificationBody())
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status=%d want 404", resp.StatusCode)
	}
}

func TestVerificationIngest_RejectsNonTerminalWork(t *testing.T) {
	srv, ts, st := newTestServer(t)
	srv.VerifierToken = []byte(verifierToken)
	w := seedVerificationWork(t, st)
	resp := postVerification(t, ts.URL, w.ID, verifierToken, validVerificationBody())
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status=%d want 409", resp.StatusCode)
	}
}

func TestVerificationIngest_OversizeBody413(t *testing.T) {
	srv, ts, st := newTestServer(t)
	srv.VerifierToken = []byte(verifierToken)
	w := seedVerificationWork(t, st)
	oversize := append([]byte(`{"result":"passed","verifier_id":"`), bytes.Repeat([]byte("x"), (64<<10)+1)...)
	oversize = append(oversize, []byte(`","evidence_ref":"dvr_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","verified_at":"2026-09-16T00:30:00Z"}`)...)
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/v1/works/"+w.ID+"/verification", bytes.NewReader(oversize))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-WORKS-Verifier-Token", verifierToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status=%d want 413", resp.StatusCode)
	}
}
