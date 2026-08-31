package api_test

import (
	"bytes"
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