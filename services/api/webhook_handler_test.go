package api_test

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JonasAbde/works-execution/services/api"
	"github.com/JonasAbde/works-execution/services/work/store"
)

// newWebhookTestServer returns a test server with a webhook receiver
// enabled using the given secret. AllowedRepos is set when non-nil
// (empty map disables the check = allow-all).
func newWebhookTestServer(t *testing.T, secret string, allowed map[string]bool) (*httptest.Server, store.Store) {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	srv := &api.Server{
		Store: s,
		WebhookConfig: &api.WebhookConfig{
			Secret:           secret,
			AllowedRepos:     allowed,
			ProductionAccess: true,
		},
	}
	ts := httptest.NewServer(srv.Routes())
	t.Cleanup(ts.Close)
	return ts, s
}

// sign returns the value of the X-Hub-Signature-256 header for a body
// signed with the shared secret.
func sign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// readPushBody returns a minimal push-event payload for a real
// GitHub-shaped delivery. Tests can override fields by re-marshalling.
func pushBody(repoFull, branch, sha string) []byte {
	p := map[string]any{
		"ref":        "refs/heads/" + branch,
		"after":      sha,
		"before":     "0000000000000000000000000000000000000000",
		"repository": map[string]any{"full_name": repoFull, "html_url": "https://github.com/" + repoFull, "clone_url": "https://github.com/" + repoFull + ".git"},
	}
	b, _ := json.Marshal(p)
	return b
}

func prBody(repoFull string, number int, action, headSHA, headRef, baseRef string) []byte {
	p := map[string]any{
		"action": action,
		"number": number,
		"pull_request": map[string]any{
			"head": map[string]any{
				"sha":  headSHA,
				"ref":  headRef,
				"repo": map[string]any{"full_name": repoFull, "clone_url": "https://github.com/" + repoFull + ".git"},
			},
			"base": map[string]any{"ref": baseRef},
		},
		"repository": map[string]any{"full_name": repoFull, "html_url": "https://github.com/" + repoFull, "clone_url": "https://github.com/" + repoFull + ".git"},
	}
	b, _ := json.Marshal(p)
	return b
}

// postGitHub is a tiny helper that posts to /v1/webhook/github with
// the GitHub-required headers and a signed body.
func postGitHub(t *testing.T, ts *httptest.Server, secret, event, deliveryID string, body []byte) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/v1/webhook/github", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Event", event)
	req.Header.Set("X-GitHub-Delivery", deliveryID)
	req.Header.Set("X-Hub-Signature-256", sign(secret, body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// --- happy path -------------------------------------------------------------

func TestWebhookHandler_Push_CreatesWork(t *testing.T) {
	const secret = "shhh"
	ts, st := newWebhookTestServer(t, secret, nil)
	body := pushBody("JonasAbde/works-execution", "main", "0123456789abcdef0123456789abcdef01234567")
	resp := postGitHub(t, ts, secret, "push", "del-001", body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		bb, _ := io.ReadAll(resp.Body)
		t.Fatalf("status: got %d, want 201; body=%s", resp.StatusCode, bb)
	}
	var got map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got["status"] != "created" {
		t.Errorf("status field: got %v, want created", got["status"])
	}
	if got["delivery_id"] != "del-001" {
		t.Errorf("delivery_id: got %v, want del-001", got["delivery_id"])
	}
	workID, _ := got["work_id"].(string)
	if !strings.HasPrefix(workID, "wrk_") {
		t.Errorf("work_id %q missing wrk_ prefix", workID)
	}
	// Idempotency record persisted.
	stored, err := st.LookupWebhookDelivery(t.Context(), "del-001")
	if err != nil {
		t.Fatalf("LookupWebhookDelivery: %v", err)
	}
	if stored != workID {
		t.Errorf("LookupWebhookDelivery: got %q, want %q", stored, workID)
	}
}

// --- signature verification --------------------------------------------------

func TestWebhookHandler_BadSignature_401(t *testing.T) {
	ts, _ := newWebhookTestServer(t, "real-secret", nil)
	body := pushBody("JonasAbde/works-execution", "main", "0123456789abcdef0123456789abcdef01234567")
	// Sign with a wrong key.
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/v1/webhook/github", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Event", "push")
	req.Header.Set("X-GitHub-Delivery", "del-bad-sig")
	req.Header.Set("X-Hub-Signature-256", sign("wrong-secret", body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status: got %d, want 401", resp.StatusCode)
	}
}

func TestWebhookHandler_MissingSignature_401(t *testing.T) {
	ts, _ := newWebhookTestServer(t, "real-secret", nil)
	body := pushBody("JonasAbde/works-execution", "main", "0123456789abcdef0123456789abcdef01234567")
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/v1/webhook/github", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Event", "push")
	req.Header.Set("X-GitHub-Delivery", "del-no-sig")
	// Intentionally no signature header.
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status: got %d, want 401", resp.StatusCode)
	}
}

// --- disabled ---------------------------------------------------------------

func TestWebhookHandler_NotConfigured_503(t *testing.T) {
	s, _ := store.Open(filepath.Join(t.TempDir(), "test.db"))
	t.Cleanup(func() { _ = s.Close() })
	ts := httptest.NewServer((&api.Server{Store: s}).Routes())
	t.Cleanup(ts.Close)

	body := pushBody("JonasAbde/works-execution", "main", "0123456789abcdef0123456789abcdef01234567")
	resp := postGitHub(t, ts, "anything", "push", "del-disabled", body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status: got %d, want 503", resp.StatusCode)
	}
}

// --- idempotency ------------------------------------------------------------

func TestWebhookHandler_DuplicateDelivery_200(t *testing.T) {
	const secret = "shhh"
	ts, st := newWebhookTestServer(t, secret, nil)
	body := pushBody("JonasAbde/works-execution", "main", "0123456789abcdef0123456789abcdef01234567")
	// First delivery creates the work.
	r1 := postGitHub(t, ts, secret, "push", "del-dup", body)
	r1.Body.Close()
	if r1.StatusCode != http.StatusCreated {
		t.Fatalf("first delivery: got %d, want 201", r1.StatusCode)
	}
	// Second delivery with the same delivery_id is a 200 duplicate.
	r2 := postGitHub(t, ts, secret, "push", "del-dup", body)
	defer r2.Body.Close()
	if r2.StatusCode != http.StatusOK {
		t.Errorf("duplicate delivery: got %d, want 200", r2.StatusCode)
	}
	var got map[string]any
	if err := json.NewDecoder(r2.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got["status"] != "duplicate" {
		t.Errorf("duplicate body: status=%v, want duplicate", got["status"])
	}
	// Still only one webhooks row.
	stored, _ := st.LookupWebhookDelivery(t.Context(), "del-dup")
	if stored == "" {
		t.Errorf("LookupWebhookDelivery returned empty after duplicate")
	}
}

// --- ignored events ---------------------------------------------------------

func TestWebhookHandler_Ping_Ignored_200(t *testing.T) {
	const secret = "shhh"
	ts, _ := newWebhookTestServer(t, secret, nil)
	body := []byte(`{"zen":"Speak like a human"}`)
	resp := postGitHub(t, ts, secret, "ping", "del-ping", body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status: got %d, want 200", resp.StatusCode)
	}
	var got map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got["status"] != "ignored" {
		t.Errorf("ping should be ignored: got status=%v", got["status"])
	}
}

func TestWebhookHandler_ClosedPR_Ignored_200(t *testing.T) {
	const secret = "shhh"
	ts, _ := newWebhookTestServer(t, secret, nil)
	body := prBody("JonasAbde/works-execution", 7, "closed", "0123456789abcdef0123456789abcdef01234567", "feature", "main")
	resp := postGitHub(t, ts, secret, "pull_request", "del-pr-closed", body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status: got %d, want 200", resp.StatusCode)
	}
	var got map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&got)
	if got["status"] != "ignored" {
		t.Errorf("closed PR should be ignored: got status=%v", got["status"])
	}
}

// --- bad payload ------------------------------------------------------------

func TestWebhookHandler_BadJSON_200(t *testing.T) {
	const secret = "shhh"
	ts, _ := newWebhookTestServer(t, secret, nil)
	body := []byte(`{not json`)
	resp := postGitHub(t, ts, secret, "push", "del-bad", body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status: got %d, want 200 (we ack bad payloads to stop retries)", resp.StatusCode)
	}
}

func TestWebhookHandler_UnsupportedEvent_200(t *testing.T) {
	const secret = "shhh"
	ts, _ := newWebhookTestServer(t, secret, nil)
	body := []byte(`{}`)
	resp := postGitHub(t, ts, secret, "starred", "del-star", body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status: got %d, want 200", resp.StatusCode)
	}
}

// --- repo allow-list --------------------------------------------------------

func TestWebhookHandler_RepoAllowList_BlocksForbidden(t *testing.T) {
	const secret = "shhh"
	allow := map[string]bool{"JonasAbde/works-execution": true}
	ts, _ := newWebhookTestServer(t, secret, allow)
	body := pushBody("someone-else/repo", "main", "0123456789abcdef0123456789abcdef01234567")
	resp := postGitHub(t, ts, secret, "push", "del-foreign", body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status: got %d, want 403", resp.StatusCode)
	}
}

func TestWebhookHandler_RepoAllowList_AllowsListed(t *testing.T) {
	const secret = "shhh"
	allow := map[string]bool{"JonasAbde/works-execution": true}
	ts, _ := newWebhookTestServer(t, secret, allow)
	body := pushBody("JonasAbde/works-execution", "main", "0123456789abcdef0123456789abcdef01234567")
	resp := postGitHub(t, ts, secret, "push", "del-allowed", body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Errorf("status: got %d, want 201", resp.StatusCode)
	}
}

// --- PR creates work with correct mapping ----------------------------------

func TestWebhookHandler_PR_Opened_CreatesWork(t *testing.T) {
	const secret = "shhh"
	ts, _ := newWebhookTestServer(t, secret, nil)
	body := prBody("JonasAbde/works-execution", 42, "opened", "abcdef0123456789abcdef0123456789abcdef01", "feat/x", "main")
	resp := postGitHub(t, ts, secret, "pull_request", "del-pr-open", body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		bb, _ := io.ReadAll(resp.Body)
		t.Fatalf("status: got %d, want 201; body=%s", resp.StatusCode, bb)
	}
	var got map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&got)
	if got["status"] != "created" {
		t.Errorf("status: got %v, want created", got["status"])
	}
}

// --- source mapping on the persisted work -----------------------------------

func TestWebhookHandler_PersistedSource_HasGitHubProvenance(t *testing.T) {
	const secret = "shhh"
	ts, st := newWebhookTestServer(t, secret, nil)
	sha := "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"
	body := pushBody("JonasAbde/works-execution", "main", sha)
	resp := postGitHub(t, ts, secret, "push", "del-source", body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status: got %d, want 201", resp.StatusCode)
	}
	var got map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&got)
	workID, _ := got["work_id"].(string)
	w, err := st.GetWork(t.Context(), workID)
	if err != nil {
		t.Fatalf("GetWork: %v", err)
	}
	if w.Source.Type != "github_push" {
		t.Errorf("Source.Type: got %q, want github_push", w.Source.Type)
	}
	if w.Source.Repository != "JonasAbde/works-execution" {
		t.Errorf("Source.Repository: got %q", w.Source.Repository)
	}
	if w.Source.SHA != sha {
		t.Errorf("Source.SHA: got %q, want %q", w.Source.SHA, sha)
	}
	if w.Source.Branch != "main" {
		t.Errorf("Source.Branch: got %q, want main", w.Source.Branch)
	}
	if w.Source.CloneURL != "https://github.com/JonasAbde/works-execution.git" {
		t.Errorf("Source.CloneURL: got %q", w.Source.CloneURL)
	}
	if !w.Policy.ProductionAccess {
		t.Errorf("Policy.ProductionAccess: got %v, want true", w.Policy.ProductionAccess)
	}
}

// --- works.yml pipeline path (RFC-0006) --------------------------------------

const testPipelineYML = `version: 1

work:
  verify:
    triggers:
      - push
      - pull_request
    requirements:
      confidence: development
      os: linux
      arch: amd64
      pool: avc-core
    nodes:
      vet:
        run: go vet ./...
        cache: true
        timeout_s: 120
      test:
        needs: [vet]
        run: go test ./... -count=1
        timeout_s: 600
`

// newPipelineWebhookTestServer returns a webhook test server whose
// works.yml fetcher is a stub returning the given raw bytes (nil =
// repo has no works.yml).
func newPipelineWebhookTestServer(t *testing.T, secret string, raw []byte) (*httptest.Server, store.Store) {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	srv := &api.Server{
		Store: s,
		WebhookConfig: &api.WebhookConfig{
			Secret:           secret,
			ProductionAccess: true,
			GitHubToken:      "test-token",
		},
		PipelineFetcher: func(_ context.Context, token, repo, sha string) ([]byte, error) {
			if token != "test-token" {
				t.Errorf("fetcher token = %q, want test-token", token)
			}
			if repo != "JonasAbde/works-execution" {
				t.Errorf("fetcher repo = %q", repo)
			}
			return raw, nil
		},
	}
	ts := httptest.NewServer(srv.Routes())
	t.Cleanup(ts.Close)
	return ts, s
}

func TestWebhookHandler_WorksYML_UsesPipeline(t *testing.T) {
	const secret = "shhh"
	ts, st := newPipelineWebhookTestServer(t, secret, []byte(testPipelineYML))
	sha := "0123456789abcdef0123456789abcdef01234567"
	body := pushBody("JonasAbde/works-execution", "main", sha)
	resp := postGitHub(t, ts, secret, "push", "del-pipe-1", body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		bb, _ := io.ReadAll(resp.Body)
		t.Fatalf("status: got %d, want 201; body=%s", resp.StatusCode, bb)
	}
	var got map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&got)
	workID, _ := got["work_id"].(string)

	w, err := st.GetWork(t.Context(), workID)
	if err != nil {
		t.Fatalf("GetWork: %v", err)
	}
	// The pipeline's pool must be honored (RFC-0004 boundary).
	if w.Requirements.Pool != "avc-core" {
		t.Errorf("Requirements.Pool = %q, want avc-core", w.Requirements.Pool)
	}
	// The DAG must come from works.yml, not the single-node default.
	if len(w.Graph.Nodes) != 2 {
		t.Fatalf("nodes = %d, want 2 (vet, test)", len(w.Graph.Nodes))
	}
	if _, ok := w.Graph.Nodes["vet"]; !ok {
		t.Error("missing vet node")
	}
	if _, ok := w.Graph.Nodes["test"]; !ok {
		t.Error("missing test node")
	}
	if !w.Graph.Nodes["vet"].Cache {
		t.Error("vet node should have cache: true from works.yml")
	}
	// Source mapping must still be stamped.
	if w.Source.SHA != sha || w.Source.Type != "github_push" {
		t.Errorf("source = %+v", w.Source)
	}
}

func TestWebhookHandler_WorksYML_TriggerMismatch_Ignored(t *testing.T) {
	const secret = "shhh"
	// Pipeline triggers on push + pull_request; deliver a "release"
	// event — must be ignored, no work created.
	ts, _ := newPipelineWebhookTestServer(t, secret, []byte(testPipelineYML))
	body := pushBody("JonasAbde/works-execution", "main", "0123456789abcdef0123456789abcdef01234567")
	resp := postGitHub(t, ts, secret, "release", "del-pipe-2", body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d, want 200 ignored", resp.StatusCode)
	}
	var got map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&got)
	if got["status"] != "ignored" {
		t.Errorf("status = %v, want ignored", got["status"])
	}
}

func TestWebhookHandler_WorksYML_Broken_502(t *testing.T) {
	const secret = "shhh"
	ts, _ := newPipelineWebhookTestServer(t, secret, []byte("version: 1\nwork: {}\n"))
	body := pushBody("JonasAbde/works-execution", "main", "0123456789abcdef0123456789abcdef01234567")
	resp := postGitHub(t, ts, secret, "push", "del-pipe-3", body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		bb, _ := io.ReadAll(resp.Body)
		t.Fatalf("status: got %d, want 502; body=%s", resp.StatusCode, bb)
	}
}

func TestWebhookHandler_NoWorksYML_FallsBack(t *testing.T) {
	const secret = "shhh"
	// Fetcher returns nil = repo has no works.yml → default
	// single-node verify work, no pool.
	ts, st := newPipelineWebhookTestServer(t, secret, nil)
	body := pushBody("JonasAbde/works-execution", "main", "0123456789abcdef0123456789abcdef01234567")
	resp := postGitHub(t, ts, secret, "push", "del-pipe-4", body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		bb, _ := io.ReadAll(resp.Body)
		t.Fatalf("status: got %d, want 201; body=%s", resp.StatusCode, bb)
	}
	var got map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&got)
	workID, _ := got["work_id"].(string)
	w, err := st.GetWork(t.Context(), workID)
	if err != nil {
		t.Fatalf("GetWork: %v", err)
	}
	if len(w.Graph.Nodes) != 1 {
		t.Fatalf("nodes = %d, want 1 (default build)", len(w.Graph.Nodes))
	}
	if _, ok := w.Graph.Nodes["build"]; !ok {
		t.Error("missing default build node")
	}
	if w.Requirements.Pool != "" {
		t.Errorf("Requirements.Pool = %q, want empty (fallback)", w.Requirements.Pool)
	}
}