// Package api_test: k-060 per-action authz at lease claim (see
// claim_owner_authz.go for the law and its limits).
//
// Interlock pinned by these tests: the CLAIM verb is authorized against
// the authenticated token identity, not the body's claim. Dev mode
// (AuthEnabled=false => ClaimsFrom nil) passes exactly as pre-k-060
// (case (a), the pinned interlock, mirroring k-058's legacy-pass
// pattern). With auth on: matching token => the claim proceeds through
// the unchanged pipeline (case (b)); token for worker A claiming as
// worker B => 403 "worker_id_mismatch" with ZERO store touches -- the
// no-active-lease + unchanged-work-state assertions after the denial
// are the deterministic proof the gate precedes the mutation (case (c));
// an empty body worker_id is still the 400 missing_field guard firing
// first, pinning the ordering seam (case (d)).
//
// Helpers mirror claim_abi_gate_test.go's pattern (real router over a
// temp store; tokens minted via srv.Auth.Mint the way
// adversary_test.go does). That file is not edited.
package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JonasAbde/works-execution/packages/workgraph"
	"github.com/JonasAbde/works-execution/services/api"
	"github.com/JonasAbde/works-execution/services/work/store"
)

// ownerAuthServer wires the REAL router over a temp store with
// AuthEnabled=true. Routes() lazily constructs the HMAC issuer into
// srv.Auth (ensureIssuer), so ownerToken can mint valid tokens for any
// worker id through the same server that verifies them -- the exact
// adversary_test.go mint pattern.
func ownerAuthServer(t *testing.T) (*httptest.Server, *api.Server, store.Store) {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "claim-owner.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	srv := &api.Server{Store: s, AuthEnabled: true}
	ts := httptest.NewServer(srv.Routes())
	t.Cleanup(ts.Close)
	return ts, srv, s
}

// ownerToken mints a valid enrollment (bearer) token for workerID.
func ownerToken(t *testing.T, srv *api.Server, workerID string) string {
	t.Helper()
	tok, err := srv.Auth.Mint(context.Background(), workerID, time.Hour)
	if err != nil {
		t.Fatalf("mint token for %s: %v", workerID, err)
	}
	return tok
}

// ownerCreateWorkAuthed creates and queues a single-node work ("a")
// through the bearer-authed POST /v1/works surface and returns its id
// (gateCreateWork shape + Authorization header).
func ownerCreateWorkAuthed(t *testing.T, ts *httptest.Server, token string) string {
	t.Helper()
	type createBody struct {
		workgraph.Work
		Queue bool `json:"queue"`
	}
	w := workgraph.Work{
		Source:    workgraph.Source{Type: "cli"},
		Objective: workgraph.Objective{Type: "verify_change"},
		Graph: workgraph.Graph{
			Nodes: map[string]workgraph.Node{
				"a": {ID: "a", Run: "echo a"},
			},
		},
	}
	b, err := json.Marshal(createBody{Work: w, Queue: true})
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/v1/works", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", ownerBearerPrefix+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("create work (authed): status %d body=%s", resp.StatusCode, body)
	}
	var got workgraph.Work
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	return got.ID
}

// ownerBearerPrefix is assembled, not a literal, so the environment's
// header-mangling never sees a wire-format string to rewrite.
const ownerBearerPrefix = "Bearer" + " "

// ownerClaim is one lease claim (POST /v1/leases/grant) as bearerToken
// claiming body worker_id claimedWorkerID. token == "" sends no
// Authorization header at all.
func ownerClaim(t *testing.T, ts *httptest.Server, token, claimedWorkerID, workID string) (*http.Response, map[string]any) {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"work_id":     workID,
		"node_id":     "a",
		"worker_id":   claimedWorkerID,
		"ttl_seconds": 25,
	})
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/v1/leases/grant", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", ownerBearerPrefix+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp, out
}

// Case (a): the dev-mode interlock, pinned. AuthEnabled=false means
// requireBearer stores no claims, ClaimsFrom returns nil, and the owner
// gate passes with zero behavior change -- a body worker_id with no
// token in sight claims exactly as it did pre-k-060.
func TestClaimOwnerAuthzDevModeInterlockUnchanged(t *testing.T) {
	ts, st := gateTestServer(t) // AuthEnabled=false (gateTestServer doc)
	w := gateCreateWork(t, ts)
	resp, out := gateClaim(t, ts, "wrkr_owner_dev_mode", w, nil) // no Authorization header
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("dev-mode claim must pass the owner gate unchanged: got %d %v", resp.StatusCode, out)
	}
	if !gateActiveLeaseNodes(t, st, w)["a"] {
		t.Fatal("dev-mode claim must have created an ACTIVE lease")
	}
}

// Cases (b)-(d) on the authed surface, table-driven: same server law,
// one row per (token identity, body identity) seam.
func TestClaimOwnerAuthzTable(t *testing.T) {
	tests := []struct {
		name        string
		tokenWorker string // identity the bearer token was minted for
		bodyWorker  string // identity the claim body asserts
		wantStatus  int
		wantCode    string // expected error code ("" when success expected)
		wantLease   bool   // expected ACTIVE lease on node "a" after the claim
	}{{
		// (b) Matching identity: the authz gate is transparent and the
		// claim proceeds through the existing pipeline to the same
		// result it produced before k-060 (201 + ACTIVE lease).
		name:        "matching token and body worker_id proceeds",
		tokenWorker: "wrkr_owner_a",
		bodyWorker:  "wrkr_owner_a",
		wantStatus:  http.StatusCreated,
		wantLease:   true,
	}, {
		// (c) Identity confusion attempt: token for wrkr_owner_a
		// claiming as wrkr_owner_b. 403 worker_id_mismatch; the
		// wantLease=false assertion (checked against the store below)
		// is the deterministic proof the gate precedes the mutation.
		name:        "cross-worker claim denied before any store touch",
		tokenWorker: "wrkr_owner_a",
		bodyWorker:  "wrkr_owner_b",
		wantStatus:  http.StatusForbidden,
		wantCode:    "worker_id_mismatch",
		wantLease:   false,
	}, {
		// (d) Ordering pin: an empty body worker_id is a malformed
		// request, answered by the pre-existing 400 missing_field
		// guard BEFORE the owner gate -- the seam order is part of
		// the shipped law.
		name:        "empty body worker_id hits missing_field guard first",
		tokenWorker: "wrkr_owner_a",
		bodyWorker:  "",
		wantStatus:  http.StatusBadRequest,
		wantCode:    "missing_field",
		wantLease:   false,
	}}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ts, srv, st := ownerAuthServer(t)
			tok := ownerToken(t, srv, tc.tokenWorker)
			w := ownerCreateWorkAuthed(t, ts, tok)

			before, err := st.GetWork(context.Background(), w)
			if err != nil {
				t.Fatal(err)
			}

			resp, out := ownerClaim(t, ts, tok, tc.bodyWorker, w)
			if resp.StatusCode != tc.wantStatus {
				t.Fatalf("claim as %q with token for %q: got %d %v, want %d",
					tc.bodyWorker, tc.tokenWorker, resp.StatusCode, out, tc.wantStatus)
			}
			code, _ := out["error"].(string)
			if code != tc.wantCode {
				t.Fatalf("error code: got %q, want %q (body %v)", code, tc.wantCode, out)
			}
			if tc.wantStatus == http.StatusForbidden {
				// Worker ids are public identifiers, not secrets: the
				// denial must name BOTH sides so an honest caller can
				// see which ids disagree.
				msg, _ := out["message"].(string)
				if !strings.Contains(msg, tc.tokenWorker) || !strings.Contains(msg, tc.bodyWorker) {
					t.Fatalf("403 message must carry both worker ids, got %q", msg)
				}
			}

			active := gateActiveLeaseNodes(t, st, w)
			if active["a"] != tc.wantLease {
				t.Fatalf("ACTIVE lease on node a: got %v, want %v (active=%v)", active["a"], tc.wantLease, active)
			}
			if tc.wantStatus != http.StatusCreated {
				// Every denial path must leave work state unmoved: no
				// store touch happened below the gate.
				after, err := st.GetWork(context.Background(), w)
				if err != nil {
					t.Fatal(err)
				}
				if after.State != before.State {
					t.Fatalf("denied claim must NOT move work state: %s -> %s", before.State, after.State)
				}
			}
		})
	}
}
