package publisher_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/JonasAbde/works-execution/services/publisher"
)

func okResult() publisher.Result {
	return publisher.Result{
		Repository:  "JonasAbde/works-execution",
		SHA:         "abcdef0123456789abcdef0123456789abcdef01",
		Conclusion:  publisher.ConclusionSuccess,
		Description: "works-execution/wrk_0123456789abcdef",
		DetailsURL:  "https://works.example.com/v1/works/wrk_0123456789abcdef",
		Output:      "go test ./... -count=1  PASS",
	}
}

func TestResult_Validate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*publisher.Result)
		wantErr string
	}{
		{"valid ok", func(*publisher.Result) {}, ""},
		{"missing repo", func(r *publisher.Result) { r.Repository = "" }, "Repository"},
		{"missing sha", func(r *publisher.Result) { r.SHA = "" }, "SHA"},
		{"short sha", func(r *publisher.Result) { r.SHA = "abc" }, "40 hex"},
		{"bad conclusion", func(r *publisher.Result) { r.Conclusion = "yellow" }, "invalid conclusion"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := okResult()
			tt.mutate(&r)
			err := r.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("want nil, got %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("want error containing %q, got %v", tt.wantErr, err)
			}
		})
	}
}

func TestStatusAPIPublisher_HappyPath(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if !strings.HasSuffix(r.URL.Path, "/repos/JonasAbde/works-execution/statuses/abcdef0123456789abcdef0123456789abcdef01") {
			t.Errorf("path = %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("auth = %q, want Bearer test-token", got)
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["state"] != "success" {
			t.Errorf("state = %v, want success", body["state"])
		}
		if body["context"] != "works-execution" {
			t.Errorf("context = %v, want works-execution", body["context"])
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":1}`))
	}))
	defer t.Cleanup(srv.Close)

	p, err := publisher.NewStatusAPIPublisher("test-token")
	if err != nil {
		t.Fatal(err)
	}
	p.BaseURL = srv.URL

	if err := p.Publish(context.Background(), okResult()); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if calls != 1 {
		t.Errorf("calls = %d, want 1", calls)
	}
	if p.Kind() != "status" {
		t.Errorf("Kind = %q, want status", p.Kind())
	}
}

func TestStatusAPIPublisher_Non2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"bad token"}`))
	}))
	defer t.Cleanup(srv.Close)

	p, _ := publisher.NewStatusAPIPublisher("test-token")
	p.BaseURL = srv.URL
	err := p.Publish(context.Background(), okResult())
	if err == nil {
		t.Fatal("want error on 401, got nil")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("error %q missing 401", err.Error())
	}
}

func TestStatusAPIPublisher_EmptyToken(t *testing.T) {
	if _, err := publisher.NewStatusAPIPublisher(""); err == nil {
		t.Fatal("want error on empty token")
	}
}

func TestCheckRunPublisher_HappyPath(t *testing.T) {
	var gotAuth string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":42}`))
	}))
	defer t.Cleanup(srv.Close)

	called := 0
	tokFn := func(_ context.Context, _ string) (string, error) {
		called++
		return "install-token", nil
	}
	p, err := publisher.NewCheckRunPublisher(12345, tokFn)
	if err != nil {
		t.Fatal(err)
	}
	p.BaseURL = srv.URL

	if err := p.Publish(context.Background(), okResult()); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if called != 1 {
		t.Errorf("InstallationToken called %d times, want 1", called)
	}
	if gotAuth != "Bearer install-token" {
		t.Errorf("auth = %q, want Bearer install-token", gotAuth)
	}
	if gotBody["name"] != "works-execution" {
		t.Errorf("name = %v", gotBody["name"])
	}
	if gotBody["head_sha"] != okResult().SHA {
		t.Errorf("head_sha = %v", gotBody["head_sha"])
	}
	if gotBody["conclusion"] != "success" {
		t.Errorf("conclusion = %v", gotBody["conclusion"])
	}
	if gotBody["external_id"] != okResult().Description {
		t.Errorf("external_id = %v, want %v", gotBody["external_id"], okResult().Description)
	}
	if p.Kind() != "check-run" {
		t.Errorf("Kind = %q, want check-run", p.Kind())
	}
}

func TestCheckRunPublisher_NilTokenFn(t *testing.T) {
	if _, err := publisher.NewCheckRunPublisher(12345, nil); err == nil {
		t.Fatal("want error on nil InstallationToken")
	}
}

func TestCheckRunPublisher_TokenFnError(t *testing.T) {
	tokFn := func(_ context.Context, _ string) (string, error) {
		return "", context.DeadlineExceeded
	}
	p, _ := publisher.NewCheckRunPublisher(12345, tokFn)
	err := p.Publish(context.Background(), okResult())
	if err == nil {
		t.Fatal("want error from token fn")
	}
	if !strings.Contains(err.Error(), "installation token") {
		t.Errorf("error %q missing token context", err.Error())
	}
}

func TestNoopPublisher_Records(t *testing.T) {
	n := newNoopPublisher()
	if err := n.Publish(context.Background(), okResult()); err != nil {
		t.Fatal(err)
	}
	if len(n.Recorded) != 1 {
		t.Errorf("Recorded len = %d, want 1", len(n.Recorded))
	}
	// Validation is delegated, so a bad Result should propagate.
	err := n.Publish(context.Background(), publisher.Result{})
	if err == nil {
		t.Fatal("want validation error")
	}
}

// newNoopPublisher is a tiny local accessor to avoid exporting the
// type into the package (NoopPublisher is a test-only convenience).
func newNoopPublisher() *publisher.NoopPublisher {
	return publisher.NewNoopPublisher("noop")
}