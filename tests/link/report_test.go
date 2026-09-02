package linkconformance

import "testing"

// k-036 report hook: logs the total number of schema assertions (positive +
// negative) this suite made, and fails if it ran without a single one.
// Read with: go test ./tests/link/ -run TestReportValidatedCases -v
func TestReportValidatedCases(t *testing.T) {
	n := validatedCases.Load()
	if n == 0 {
		t.Fatal("suite made zero schema validations — harness wired wrong")
	}
	t.Logf("k-036 conformance: %d schema-validated fixtures (positive+negative)", n)
}
