package workspace

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JonasAbde/works-execution/packages/secrets"
)

func TestWorkspaceConformanceGitHubProvider(t *testing.T) {
	const baseline = "0123456789012345678901234567890123456789"

	var mu sync.Mutex
	branches := map[string]string{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer control-secret" {
			t.Fatalf("unexpected auth %q", r.Header.Get("Authorization"))
		}
		mu.Lock()
		defer mu.Unlock()

		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/git/matching-refs/heads/"):
			out := make([]any, 0, len(branches))
			for branch, sha := range branches {
				out = append(out, map[string]any{
					"ref": "refs/heads/" + branch,
					"object": map[string]any{"sha": sha, "type": "commit"},
				})
			}
			writeGitJSON(w, out)

		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/git/commits/"):
			got := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
			if got != baseline {
				t.Fatalf("unexpected baseline sha %q", got)
			}
			writeGitJSON(w, map[string]any{"sha": baseline})

		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/git/refs"):
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			ref, _ := body["ref"].(string)
			sha, _ := body["sha"].(string)
			branch := strings.TrimPrefix(ref, "refs/heads/")
			if _, exists := branches[branch]; exists {
				w.WriteHeader(http.StatusUnprocessableEntity)
				writeGitJSON(w, map[string]any{"message": "Reference already exists"})
				return
			}
			branches[branch] = sha
			writeGitJSON(w, map[string]any{
				"ref": ref,
				"object": map[string]any{"sha": sha, "type": "commit"},
			})

		case r.Method == http.MethodGet && r.URL.Path == "/repos/Aftergraph/runtime":
			writeGitJSON(w, map[string]any{
				"full_name": "Aftergraph/runtime",
				"clone_url": "https://github.com/Aftergraph/runtime.git",
				"default_branch": "main",
			})

		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/git/ref/heads/"):
			branch := strings.TrimPrefix(r.URL.Path, "/repos/Aftergraph/runtime/git/ref/heads/")
			branch, _ = url.PathUnescape(branch)
			sha, ok := branches[branch]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				writeGitJSON(w, map[string]any{"message": "Not Found"})
				return
			}
			writeGitJSON(w, map[string]any{
				"ref": "refs/heads/" + branch,
				"object": map[string]any{"sha": sha, "type": "commit"},
			})

		case r.Method == http.MethodDelete && strings.Contains(r.URL.Path, "/git/refs/heads/"):
			branch := strings.TrimPrefix(r.URL.Path, "/repos/Aftergraph/runtime/git/refs/heads/")
			branch, _ = url.PathUnescape(branch)
			delete(branches, branch)
			w.WriteHeader(http.StatusNoContent)

		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	}))
	defer ts.Close()

	p, err := NewGitHubWorkspaceProvider(
		GitHubWorkspaceConfig{
			ControlTokenRef: secrets.Must("secret://github/control"),
			APIBase:         ts.URL,
			TokenTTL:        time.Hour,
		},
		fakeResolver{value: "control-secret"},
		&fakeGitIssuer{},
		&fakeCredentialStore{},
		ts.Client(),
	)
	if err != nil {
		t.Fatal(err)
	}

	ConformanceSuite(t, p)
}

func TestWorkspaceConformanceCloudflareArtifactsProvider(t *testing.T) {
	type repoState struct {
		ID            string
		Name          string
		DefaultBranch string
		Remote        string
		SHA           string
	}

	const baseline = "0123456789012345678901234567890123456789"
	var mu sync.Mutex
	repos := map[string]repoState{}
	tokenSeq := 0

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer control-secret" {
			t.Fatalf("unexpected auth %q", r.Header.Get("Authorization"))
		}
		mu.Lock()
		defer mu.Unlock()

		base := "/accounts/acct/artifacts/namespaces/default"
		switch {
		case r.Method == http.MethodGet && r.URL.Path == base+"/repos":
			search := r.URL.Query().Get("search")
			items := []any{}
			for _, repo := range repos {
				if search == "" || strings.Contains(repo.Name, search) {
					items = append(items, map[string]any{
						"id": repo.ID,
						"name": repo.Name,
						"default_branch": repo.DefaultBranch,
						"remote": repo.Remote,
					})
				}
			}
			writeJSON(w, map[string]any{
				"success": true,
				"result": items,
				"result_info": map[string]any{"cursor": ""},
			})

		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/import"):
			name := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, base+"/repos/"), "/import")
			name, _ = url.PathUnescape(name)
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if _, exists := repos[name]; exists {
				w.WriteHeader(http.StatusConflict)
				writeJSON(w, map[string]any{"success": false, "errors": []any{map[string]any{"code": 409, "message": "exists"}}})
				return
			}
			repo := repoState{
				ID:            "repo_" + name,
				Name:          name,
				DefaultBranch: "main",
				Remote:        "https://acct.artifacts.cloudflare.net/git/default/" + name + ".git",
				SHA:           baseline,
			}
			repos[name] = repo
			writeJSON(w, map[string]any{
				"success": true,
				"result": map[string]any{
					"id": repo.ID,
					"name": repo.Name,
					"default_branch": repo.DefaultBranch,
					"remote": repo.Remote,
					"token": "initial-token-never-persist",
				},
			})

		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/commit/"):
			namePart := strings.TrimPrefix(r.URL.Path, base+"/repos/")
			idx := strings.Index(namePart, "/commit/")
			if idx < 0 {
				t.Fatalf("bad commit path %s", r.URL.Path)
			}
			name, _ := url.PathUnescape(namePart[:idx])
			sha := namePart[idx+len("/commit/"):]
			repo, ok := repos[name]
			if !ok || repo.SHA != sha {
				w.WriteHeader(http.StatusNotFound)
				writeJSON(w, map[string]any{"success": false})
				return
			}
			writeJSON(w, map[string]any{"success": true, "result": map[string]any{"hash": sha}})

		case r.Method == http.MethodPost && r.URL.Path == base+"/tokens":
			tokenSeq++
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			repo, _ := body["repo"].(string)
			if _, ok := repos[repo]; !ok {
				t.Fatalf("token minted for missing repo %q", repo)
			}
			writeJSON(w, map[string]any{
				"success": true,
				"result": map[string]any{
					"id": fmt.Sprintf("tok_%d", tokenSeq),
					"plaintext": fmt.Sprintf("repo-token-%d", tokenSeq),
					"scope": body["scope"],
					"expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
				},
			})

		case r.Method == http.MethodDelete && strings.Contains(r.URL.Path, base+"/tokens/"):
			writeJSON(w, map[string]any{"success": true, "result": map[string]any{"ok": true}})

		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, base+"/repos/") && strings.HasSuffix(r.URL.Path, "/log"):
			namePart := strings.TrimPrefix(r.URL.Path, base+"/repos/")
			name := strings.TrimSuffix(namePart, "/log")
			name, _ = url.PathUnescape(name)
			repo, ok := repos[name]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				writeJSON(w, map[string]any{"success": false})
				return
			}
			writeJSON(w, map[string]any{
				"success": true,
				"result": map[string]any{
					"commits": []any{map[string]any{"hash": repo.SHA}},
				},
			})

		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, base+"/repos/"):
			name := strings.TrimPrefix(r.URL.Path, base+"/repos/")
			name, _ = url.PathUnescape(name)
			repo, ok := repos[name]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				writeJSON(w, map[string]any{"success": false})
				return
			}
			writeJSON(w, map[string]any{
				"success": true,
				"result": map[string]any{
					"id": repo.ID,
					"name": repo.Name,
					"default_branch": repo.DefaultBranch,
					"remote": repo.Remote,
				},
			})

		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, base+"/repos/"):
			name := strings.TrimPrefix(r.URL.Path, base+"/repos/")
			name, _ = url.PathUnescape(name)
			delete(repos, name)
			writeJSON(w, map[string]any{"success": true, "result": map[string]any{"ok": true}})

		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	}))
	defer ts.Close()

	p, err := NewCloudflareArtifactsProvider(
		CloudflareArtifactsConfig{
			AccountID:       "acct",
			Namespace:       "default",
			ControlTokenRef: secrets.Must("secret://cloudflare/control"),
			TokenTTL:        time.Hour,
			ReadyTimeout:    time.Second,
			BaseURL:         ts.URL,
		},
		fakeResolver{value: "control-secret"},
		&fakeCredentialStore{},
		ts.Client(),
	)
	if err != nil {
		t.Fatal(err)
	}

	ConformanceSuite(t, p)
}
