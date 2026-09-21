package contracts

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v5"
)

func compileDispatchAcceptanceV2Proposal(t *testing.T) *jsonschema.Schema {
	t.Helper()
	path := filepath.Join(schemaDir, "..", "proposals", "dispatch.acceptance.v2.schema.json")
	sch, err := jsonschema.Compile(path)
	if err != nil {
		t.Fatalf("dispatch.acceptance/2.0 proposal failed to compile: %v", err)
	}
	return sch
}

func v2Request() map[string]any {
	return map[string]any{
		"schema": "dispatch.acceptance/2.0",
		"kind": "request",
		"organization_id": "org_11111111111111111111111111111111",
		"tenant_id": "ten_22222222222222222222222222222222",
		"principal_id": "prn_33333333333333333333333333333333",
		"mission_id": "mis_p2",
		"authority_lease_id": "auth_44444444444444444444444444444444",
		"worker_lease_id": "lse_55555555555555555555555555555555",
		"admission_decision_id": "pdr_66666666666666666666666666666666",
		"runtime_dispatch_id": "rdisp/p2/1",
		"attempt_id": "att_p2_1",
		"effect_id": "eff_git_push_1",
		"idempotency_key": "idem_p2_1",
		"budget_ref": "budget_p2_1",
		"budget_ceiling": 100,
		"checkpoint_id": "checkpoint_p2_1",
		"evidence_root": "evidence_p2_1",
		"verification_subject": "git:Aftergraph/STEWARD-by-Aftergraph@aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"causal_id": "cause_p2_1",
	}
}

func v2Acceptance() map[string]any {
	out := v2Request()
	out["kind"] = "acceptance"
	out["work_id"] = "wrk_77777777777777777777777777777777"
	out["works_execution_id"] = "wexec/idem_p2_1"
	out["execution_context_id"] = "ctx_88888888888888888888888888888888"
	out["trace_id"] = "trc_99999999999999999999999999999999"
	out["outcome"] = "ACCEPTED"
	out["verified"] = false
	return out
}

func TestDispatchAcceptanceV2Proposal_RequestBindsCanonicalV21Identity(t *testing.T) {
	sch := compileDispatchAcceptanceV2Proposal(t)
	if err := sch.Validate(v2Request()); err != nil {
		t.Fatalf("canonical request rejected: %v", err)
	}
}

func TestDispatchAcceptanceV2Proposal_RequestCannotMintWorksCorrelation(t *testing.T) {
	sch := compileDispatchAcceptanceV2Proposal(t)
	for _, field := range []string{"execution_context_id", "trace_id", "works_execution_id", "work_id"} {
		req := v2Request()
		req[field] = "ctx_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		if err := sch.Validate(req); err == nil {
			t.Fatalf("request was allowed to supply WORKS-owned field %q", field)
		}
	}
}

func TestDispatchAcceptanceV2Proposal_HasNoAuthorityEpochSemantic(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(schemaDir, "..", "proposals", "dispatch.acceptance.v2.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	defs := doc["definitions"].(map[string]any)
	req := defs["request"].(map[string]any)
	props := req["properties"].(map[string]any)
	if _, ok := props["authority_epoch"]; ok {
		t.Fatal("2.0 request must not silently reinterpret authority_epoch")
	}
	if _, ok := props["authority_ref"]; ok {
		t.Fatal("2.0 request must bind the canonical authority_lease_id instead of generic authority_ref")
	}
}

func TestDispatchAcceptanceV2Proposal_AcceptanceRequiresMaterializedContextIdentity(t *testing.T) {
	sch := compileDispatchAcceptanceV2Proposal(t)
	ok := v2Acceptance()
	if err := sch.Validate(ok); err != nil {
		t.Fatalf("canonical acceptance rejected: %v", err)
	}
	for _, field := range []string{"work_id", "execution_context_id", "trace_id"} {
		bad := v2Acceptance()
		delete(bad, field)
		if err := sch.Validate(bad); err == nil {
			t.Fatalf("acceptance without %s was accepted", field)
		}
	}
}

func TestDispatchAcceptanceV2Proposal_VerifiedStillRequiresIndependentVerdict(t *testing.T) {
	sch := compileDispatchAcceptanceV2Proposal(t)
	bad := v2Acceptance()
	bad["outcome"] = "SUCCEEDED"
	bad["verified"] = true
	if err := sch.Validate(bad); err == nil {
		t.Fatal("verified=true without verifier/evidence verdict was accepted")
	}

	good := v2Acceptance()
	good["outcome"] = "SUCCEEDED"
	good["verified"] = true
	good["verifier_id"] = "sentinel/prn_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	good["verdict"] = map[string]any{
		"result": "ACCEPT",
		"subject": good["verification_subject"],
		"evidence_ref": "evidence/sentinel/p2",
		"recorded_at": "2026-09-21T06:30:00Z",
	}
	if err := sch.Validate(good); err != nil {
		t.Fatalf("independently verified acceptance rejected: %v", err)
	}
}
