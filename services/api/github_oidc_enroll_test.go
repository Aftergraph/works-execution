package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

type stubGitHubOIDCVerifier struct {
	claims GitHubActionsOIDCClaims
	err error
}
func (s stubGitHubOIDCVerifier) Verify(context.Context, string) (GitHubActionsOIDCClaims, error) {
	return s.claims, s.err
}

func oidcFixture() (*Server, *httptest.Server) {
	s := &Server{
		Auth: NewHMACIssuerWithKey([]byte("01234567890123456789012345678901")),
		AuthEnabled: true,
		GitHubOIDCVerifier: stubGitHubOIDCVerifier{claims: GitHubActionsOIDCClaims{
			Repository: "Aftergraph/intelligence-systems-research",
			RepositoryID: "1356862124",
			Ref: "refs/heads/bootstrap/lenovo-works-native",
			WorkflowRef: "Aftergraph/intelligence-systems-research/.github/workflows/bootstrap-lenovo-works-native.yml@refs/heads/bootstrap/lenovo-works-native",
			RunnerEnvironment: "self-hosted",
			EventName: "push",
		}},
		GitHubOIDCPolicy: &GitHubActionsOIDCPolicy{
			Repository: "Aftergraph/intelligence-systems-research",
			RepositoryID: "1356862124",
			Ref: "refs/heads/bootstrap/lenovo-works-native",
			WorkflowRef: "Aftergraph/intelligence-systems-research/.github/workflows/bootstrap-lenovo-works-native.yml@refs/heads/bootstrap/lenovo-works-native",
			RunnerEnvironment: "self-hosted",
			EventName: "push",
		},
	}
	return s, httptest.NewServer(s.Routes())
}

func postOIDC(t *testing.T, base string, body map[string]any) *http.Response {
	t.Helper()
	b, _ := json.Marshal(body)
	resp, err := http.Post(base+"/v1/workers/enroll/github-actions", "application/json", bytes.NewReader(b))
	if err != nil { t.Fatal(err) }
	return resp
}

func TestGitHubOIDCEnrollmentAcceptsExactSelfHostedIdentity(t *testing.T) {
	_, ts := oidcFixture(); defer ts.Close()
	resp := postOIDC(t, ts.URL, map[string]any{"worker_id":"wrkr_jonas_lenovo","oidc_token":"signed"})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK { t.Fatalf("want 200 got %d", resp.StatusCode) }
	var out enrollmentResp
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil { t.Fatal(err) }
	if out.WorkerID != "wrkr_jonas_lenovo" || out.Token == "" { t.Fatalf("bad enrollment response: %+v", out) }
}

func TestGitHubOIDCEnrollmentRejectsWrongRepository(t *testing.T) {
	s, ts := oidcFixture(); defer ts.Close()
	s.GitHubOIDCVerifier = stubGitHubOIDCVerifier{claims: GitHubActionsOIDCClaims{
		Repository:"evil/fork",
		RepositoryID:"999",
		Ref:"refs/heads/bootstrap/lenovo-works-native",
		WorkflowRef:"Aftergraph/intelligence-systems-research/.github/workflows/bootstrap-lenovo-works-native.yml@refs/heads/bootstrap/lenovo-works-native",
		RunnerEnvironment:"self-hosted",
		EventName:"push",
	}}
	resp := postOIDC(t, ts.URL, map[string]any{"worker_id":"wrkr_jonas_lenovo","oidc_token":"signed"})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden { t.Fatalf("want 403 got %d", resp.StatusCode) }
}

func TestGitHubOIDCEnrollmentRejectsGitHubHostedRunner(t *testing.T) {
	s, ts := oidcFixture(); defer ts.Close()
	c := s.GitHubOIDCVerifier.(stubGitHubOIDCVerifier).claims
	c.RunnerEnvironment = "github-hosted"
	s.GitHubOIDCVerifier = stubGitHubOIDCVerifier{claims:c}
	resp := postOIDC(t, ts.URL, map[string]any{"worker_id":"wrkr_jonas_lenovo","oidc_token":"signed"})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden { t.Fatalf("want 403 got %d", resp.StatusCode) }
}

func TestGitHubOIDCEnrollmentDisabledFailsClosed(t *testing.T) {
	s := &Server{Auth:NewHMACIssuerWithKey([]byte("01234567890123456789012345678901")), AuthEnabled:true}
	ts := httptest.NewServer(s.Routes()); defer ts.Close()
	resp := postOIDC(t, ts.URL, map[string]any{"worker_id":"wrkr_jonas_lenovo","oidc_token":"signed"})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable { t.Fatalf("want 503 got %d", resp.StatusCode) }
}

func TestGitHubOIDCEnrollmentRejectsWrongImmutableRepositoryID(t *testing.T) {
	s, ts := oidcFixture(); defer ts.Close()
	c := s.GitHubOIDCVerifier.(stubGitHubOIDCVerifier).claims
	c.RepositoryID = "999"
	s.GitHubOIDCVerifier = stubGitHubOIDCVerifier{claims:c}
	resp := postOIDC(t, ts.URL, map[string]any{"worker_id":"wrkr_jonas_lenovo","oidc_token":"signed"})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden { t.Fatalf("want 403 got %d", resp.StatusCode) }
}

func TestGitHubOIDCEnrollmentRejectsNonPushEvent(t *testing.T) {
	s, ts := oidcFixture(); defer ts.Close()
	c := s.GitHubOIDCVerifier.(stubGitHubOIDCVerifier).claims
	c.EventName = "pull_request"
	s.GitHubOIDCVerifier = stubGitHubOIDCVerifier{claims:c}
	resp := postOIDC(t, ts.URL, map[string]any{"worker_id":"wrkr_jonas_lenovo","oidc_token":"signed"})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden { t.Fatalf("want 403 got %d", resp.StatusCode) }
}
