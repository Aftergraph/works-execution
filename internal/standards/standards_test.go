package standards_test

import (
	"strings"
	"testing"

	"github.com/JonasAbde/works-execution/internal/standards"
)

// sampleActionManifest is a minimal valid Action capability manifest for
// conformance testing of docs/standards/schemas/action-manifest.schema.json.
const sampleActionManifest = `{
  "action_id": "build_linux_amd64",
  "version": "1.0.0",
  "runtime": {"os": "linux", "arch": "amd64", "command": "make build"},
  "inputs": {
    "source": {"type": "file", "required": true, "path": "src/"},
    "version": {"type": "string", "required": true}
  },
  "outputs": {
    "binary": {"type": "file", "required": true, "path": "out/bin", "content_type": "application/octet-stream"}
  },
  "permissions": ["read", "execute"],
  "timeout_seconds": 600,
  "retries": {
    "max_attempts": 2,
    "backoff": "exponential",
    "retry_on": ["flaky_test", "infrastructure_failure"]
  },
  "cache": {"enabled": true, "key_inputs": ["source", "version"], "scope": "organization"},
  "side_effects": ["filesystem_write"]
}`

// sampleEvidenceBundle is a minimal valid evidence bundle per
// docs/standards/schemas/evidence-bundle.schema.json.
const sampleEvidenceBundle = `{
  "bundle_id": "evb_0123456789abcdef0123456789abcdef",
  "work_id": "wrk_0123456789abcdef0123456789abcdef",
  "created_at": "2026-08-31T01:00:00Z",
  "summary": {
    "result": "SUCCEEDED",
    "started_at": "2026-08-31T01:00:00Z",
    "finished_at": "2026-08-31T01:00:05Z",
    "duration_ms": 5000
  },
  "components": {
    "attempts": [
      {
        "id": "att_0123456789abcdef0123456789abcdef",
        "node_id": "build",
        "worker_id": "wrkr_local_1",
        "started_at": "2026-08-31T01:00:00Z",
        "finished_at": "2026-08-31T01:00:05Z",
        "exit_code": 0,
        "status": "succeeded",
        "command": "make build",
        "lease_id": "lse_0123456789abcdef0123456789abcdef"
      }
    ],
    "artifacts": [
      {
        "id": "art_0123456789abcdef0123456789abcdef",
        "digest": "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
        "size": 1234,
        "mime_type": "application/octet-stream",
        "node_id": "build",
        "path": "/tmp/artifacts/wrk_x/build.bin"
      }
    ],
    "evidence": [
      {
        "id": "evd_0123456789abcdef0123456789abcdef",
        "node_id": "build",
        "attempt_id": "att_0123456789abcdef0123456789abcdef",
        "type": "build",
        "result": "pass",
        "recorded_at": "2026-08-31T01:00:05Z",
        "signer": "wrkr_local_1"
      }
    ],
    "leases": [
      {
        "id": "lse_0123456789abcdef0123456789abcdef",
        "node_id": "build",
        "worker_id": "wrkr_local_1",
        "granted_at": "2026-08-31T01:00:00Z",
        "expires_at": "2026-08-31T01:00:30Z",
        "last_beat_at": "2026-08-31T01:00:15Z",
        "status": "RELEASED"
      }
    ]
  }
}`

// sampleRunnerIdentity is a minimal valid runner-identity record per
// docs/standards/schemas/runner-identity.schema.json.
const sampleRunnerIdentity = `{
  "runner_id": "wrkr_local_1",
  "spiffe_id": "spiffe://works-execution/ns/default/sa/wrkr_local_1",
  "trust_class": "standard",
  "capabilities": {
    "os": ["linux"],
    "arch": ["amd64"],
    "cpu_milli": 2000,
    "memory_mib": 4096,
    "gpu": 0,
    "toolchains": ["go1.23", "node20"],
    "labels": ["self-hosted", "linux", "x64"]
  },
  "lifecycle_state": "active",
  "enrolled_at": "2026-08-31T01:00:00Z",
  "last_heartbeat_at": "2026-08-31T01:05:00Z"
}`

// sampleFailureClassification is a minimal valid failure classification per
// docs/standards/schemas/failure-classification.schema.json.
const sampleFailureClassification = `{
  "class": "infrastructure_failure",
  "retryable": true,
  "max_retries": 3,
  "backoff": "exponential",
  "human_required": false,
  "autonomous_remediation": ["requeue_node", "rotate_worker"]
}`

// sampleWorkflowProvenance is a minimal valid SLSA-style provenance per
// docs/standards/schemas/workflow-provenance.schema.json.
const sampleWorkflowProvenance = `{
  "predicateType": "https://slsa.dev/provenance/v1",
  "subject": [
    {
      "name": "wrk_0123456789abcdef0123456789abcdef",
      "digest": {"sha256": "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}
    }
  ],
  "predicate": {
    "builder": {"id": "https://api.works-execution.dev", "version": "0.3.0"},
    "invocation": {
      "configSource": {"uri": "git+https://github.com/JonasAbde/works-execution", "entryPoint": "ci/local-runner/run-local-ci.sh"}
    },
    "materials": [
      {"uri": "git+https://github.com/JonasAbde/works-execution", "digest": {"sha256": "abc123"}}
    ],
    "metadata": {
      "buildStartedOn": "2026-08-31T01:00:00Z",
      "buildFinishedOn": "2026-08-31T01:00:05Z",
      "completeness": {"arguments": true, "environment": true, "materials": true},
      "reproducible": true
    }
  }
}`

func TestListSchemas(t *testing.T) {
	got := standards.ListSchemas()
	if len(got) == 0 {
		t.Fatal("ListSchemas returned empty")
	}
	for _, want := range []string{
		"action-manifest.schema.json",
		"evidence-bundle.schema.json",
		"failure-classification.schema.json",
		"runner-identity.schema.json",
		"workflow-provenance.schema.json",
	} {
		found := false
		for _, g := range got {
			if g == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("missing schema %q in %v", want, got)
		}
	}
}

func TestLoad(t *testing.T) {
	all, err := standards.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(all) < 5 {
		t.Errorf("expected >=5 schemas loaded, got %d", len(all))
	}
}

func TestValidate_ActionManifest_OK(t *testing.T) {
	if err := standards.ValidateBytes("action-manifest.schema.json", []byte(sampleActionManifest)); err != nil {
		t.Fatalf("valid manifest rejected: %v", err)
	}
}

func TestValidate_ActionManifest_MissingRequired(t *testing.T) {
	// Remove "action_id" — required field.
	bad := strings.Replace(sampleActionManifest, `"action_id": "build_linux_amd64",`, ``, 1)
	err := standards.ValidateBytes("action-manifest.schema.json", []byte(bad))
	if err == nil {
		t.Fatal("expected validation error for missing action_id")
	}
}

func TestValidate_ActionManifest_BadPermissions(t *testing.T) {
	// 'privileged' is in the enum but the test ensures the enum is honored.
	bad := strings.Replace(sampleActionManifest, `"read", "execute"`, `"privileged"`, 1)
	if err := standards.ValidateBytes("action-manifest.schema.json", []byte(bad)); err != nil {
		t.Fatalf("privileged should be valid: %v", err)
	}
	// But 'sudo' is NOT in the enum.
	bad = strings.Replace(sampleActionManifest, `"read", "execute"`, `"sudo"`, 1)
	if err := standards.ValidateBytes("action-manifest.schema.json", []byte(bad)); err == nil {
		t.Fatal("expected validation error for invalid permission 'sudo'")
	}
}

func TestValidate_ActionManifest_BadRetryClass(t *testing.T) {
	// 'random_class' is not in the enum.
	bad := strings.Replace(sampleActionManifest, `"flaky_test"`, `"random_class"`, 1)
	if err := standards.ValidateBytes("action-manifest.schema.json", []byte(bad)); err == nil {
		t.Fatal("expected validation error for invalid retry class")
	}
}

func TestValidate_EvidenceBundle_OK(t *testing.T) {
	if err := standards.ValidateBytes("evidence-bundle.schema.json", []byte(sampleEvidenceBundle)); err != nil {
		t.Fatalf("valid bundle rejected: %v", err)
	}
}

func TestValidate_EvidenceBundle_BadDigest(t *testing.T) {
	bad := strings.Replace(sampleEvidenceBundle, `"sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"`, `"sha256:tooshort"`, 1)
	if err := standards.ValidateBytes("evidence-bundle.schema.json", []byte(bad)); err == nil {
		t.Fatal("expected validation error for malformed digest")
	}
}

func TestValidate_RunnerIdentity_OK(t *testing.T) {
	if err := standards.ValidateBytes("runner-identity.schema.json", []byte(sampleRunnerIdentity)); err != nil {
		t.Fatalf("valid runner identity rejected: %v", err)
	}
}

func TestValidate_RunnerIdentity_BadSPIFFE(t *testing.T) {
	bad := strings.Replace(sampleRunnerIdentity, `spiffe://works-execution/ns/default/sa/wrkr_local_1`, `not-a-spiffe-id`, 1)
	if err := standards.ValidateBytes("runner-identity.schema.json", []byte(bad)); err == nil {
		t.Fatal("expected validation error for invalid SPIFFE ID")
	}
}

func TestValidate_FailureClassification_OK(t *testing.T) {
	if err := standards.ValidateBytes("failure-classification.schema.json", []byte(sampleFailureClassification)); err != nil {
		t.Fatalf("valid classification rejected: %v", err)
	}
}

func TestValidate_FailureClassification_BadClass(t *testing.T) {
	bad := strings.Replace(sampleFailureClassification, `"infrastructure_failure"`, `"unknown_class"`, 1)
	if err := standards.ValidateBytes("failure-classification.schema.json", []byte(bad)); err == nil {
		t.Fatal("expected validation error for unknown class")
	}
}

func TestValidate_WorkflowProvenance_OK(t *testing.T) {
	if err := standards.ValidateBytes("workflow-provenance.schema.json", []byte(sampleWorkflowProvenance)); err != nil {
		t.Fatalf("valid provenance rejected: %v", err)
	}
}

func TestValidate_UnknownSchema(t *testing.T) {
	err := standards.ValidateBytes("does-not-exist.schema.json", []byte(`{}`))
	if err == nil {
		t.Fatal("expected error for unknown schema")
	}
}