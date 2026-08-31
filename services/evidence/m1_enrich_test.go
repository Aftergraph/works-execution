package evidence

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestBundle_SourceSerialization: source round-trips through JSON.
func TestBundle_SourceSerialization(t *testing.T) {
	b := &Bundle{
		BundleID: "evb_0000000000000000000000000000abcd",
		WorkID:   "wrk_0000000000000000000000000000abcd",
		Source: &Source{
			Provider:   "github",
			Repository: "JonasAbde/works-execution",
			Ref:        "refs/heads/main",
			SHA:        "0123456789abcdef0123456789abcdef01234567",
			CloneURL:   "https://github.com/JonasAbde/works-execution.git",
			HTMLURL:    "https://github.com/JonasAbde/works-execution",
		},
		Environment: &Environment{
			GoVersion: "go version go1.23.4 linux/amd64",
			Runtime:   "host",
			Platform:  "linux/amd64",
		},
	}
	data, err := json.Marshal(b)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back map[string]any
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	src, ok := back["source"].(map[string]any)
	if !ok {
		t.Fatal("source missing from JSON")
	}
	if src["repository"] != "JonasAbde/works-execution" {
		t.Errorf("source.repository: %v", src["repository"])
	}
	if src["sha"] != "0123456789abcdef0123456789abcdef01234567" {
		t.Errorf("source.sha: %v", src["sha"])
	}
	env, ok := back["environment"].(map[string]any)
	if !ok {
		t.Fatal("environment missing from JSON")
	}
	if env["runtime"] != "host" {
		t.Errorf("environment.runtime: %v", env["runtime"])
	}
	if env["go_version"] != "go version go1.23.4 linux/amd64" {
		t.Errorf("environment.go_version: %v", env["go_version"])
	}
}

// TestBundle_NilSourceOmitted: when Source is nil, the field is
// not emitted in JSON (omitempty).
func TestBundle_NilSourceOmitted(t *testing.T) {
	b := &Bundle{WorkID: "wrk_0000000000000000000000000000abcd"}
	data, _ := json.Marshal(b)
	s := string(data)
	if strings.Contains(s, `"source"`) {
		t.Errorf("source should be omitted when nil: %s", s)
	}
	if strings.Contains(s, `"environment"`) {
		t.Errorf("environment should be omitted when nil: %s", s)
	}
}
