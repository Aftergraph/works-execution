package ci_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestRequiredWorkflowsRunOnMergeGroup(t *testing.T) {
	_, here, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot resolve test path")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(here), "..", ".."))
	for _, rel := range []string{
		".github/workflows/go-test.yml",
		".github/workflows/codeql.yml",
	} {
		data, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatalf("%s: %v", rel, err)
		}
		text := string(data)
		if !strings.Contains(text, "merge_group:") {
			t.Fatalf("%s must run on merge_group so GitHub merge queue tests the synthetic merge commit", rel)
		}
		if !strings.Contains(text, "types: [checks_requested]") {
			t.Fatalf("%s must bind merge_group to checks_requested", rel)
		}
	}
}
