package dispatch

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v5"
)

func acceptanceSchemaBytes(t *testing.T) []byte {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate test file")
	}
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "..", "contracts", "schemas", "dispatch.acceptance.schema.json"))
	if err != nil {
		t.Fatalf("read acceptance schema: %v", err)
	}
	return raw
}

func compileAcceptanceSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource(
		"contract:dispatch.acceptance/1.0",
		strings.NewReader(string(acceptanceSchemaBytes(t))),
	); err != nil {
		t.Fatalf("compile acceptance schema: %v", err)
	}
	return compiler.MustCompile("contract:dispatch.acceptance/1.0")
}

// The frozen contract must accept the exact record shape the Acceptor
// produces and reject malformed dispatches at the schema boundary.
func TestAcceptanceSchema_RoundTrip(t *testing.T) {
	schema := compileAcceptanceSchema(t)
	record := map[string]any{
		"mission_id":           "mission/golden-001",
		"authority_ref":        "a/value",
		"authority_epoch":      7,
		"runtime_dispatch_id":  "rdisp/1",
		"works_execution_id":   "wexec/idem/1",
		"attempt_id":           "attempt/1",
		"effect_id":            "effect/1",
		"idempotency_key":      "idem/1",
		"budget_ref":           "budget/1",
		"budget_ceiling":       100,
		"checkpoint_id":        "checkpoint/1",
		"evidence_root":        "evidence/1",
		"verification_subject": "subject/1",
		"causal_id":            "causal/1",
		"outcome":              "ACCEPTED",
		"verified":             false,
	}
	if err := schema.Validate(record); err != nil {
		t.Fatalf("valid acceptance record rejected: %v", err)
	}

	missingKey := map[string]any{}
	for k, v := range record {
		if k != "idempotency_key" {
			missingKey[k] = v
		}
	}
	if err := schema.Validate(missingKey); err == nil {
		t.Fatal("record without idempotency_key must be rejected")
	}

	negativeEpoch := map[string]any{}
	for k, v := range record {
		negativeEpoch[k] = v
	}
	negativeEpoch["authority_epoch"] = -1
	if err := schema.Validate(negativeEpoch); err == nil {
		t.Fatal("record with negative authority_epoch must be rejected")
	}
}
