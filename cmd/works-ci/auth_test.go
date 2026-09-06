package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"
)

func TestNewAuthForUsesRegistryWorkerID(t *testing.T) {
	var workerID string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		workerID, _ = body["worker_id"].(string)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"token":"test-token"}`))
	}))
	defer server.Close()

	if _, err := newAuthFor(server.URL, "challenge"); err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^wrkr_[a-z0-9_-]{1,64}$`).MatchString(workerID) {
		t.Fatalf("worker_id %q does not match registry contract", workerID)
	}
}
