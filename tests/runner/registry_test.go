// Package runner_test exercises the Runner Identity mint/validate/build
// layer and the API endpoints that consume it.
//
// Tests cover (k-impl-002 acceptance):
//   - MintRunnerID shape + uniqueness
//   - ValidateSPIFFE positive / negative cases (including trust domain)
//   - BuildSPIFFE / BuildIdentity happy path
//   - Identity.Validate rejects bad trust_class / lifecycle / capabilities
//   - Schema cross-validation against runner-identity.schema.json
//   - POST /v1/runners/register 201 round-trip
//   - GET  /v1/runners/{id} 200/404
package runner_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/JonasAbde/works-execution/internal/standards"
	"github.com/JonasAbde/works-execution/services/api"
	"github.com/JonasAbde/works-execution/services/runner"
)

func sampleCaps() runner.Capabilities {
	return runner.Capabilities{
		OS:         []string{"linux"},
		Arch:       []string{"amd64"},
		CPUMilli:   2000,
		MemoryMiB:  4096,
		Toolchains: []string{"go", "node"},
		Labels:     []string{"self-hosted"},
	}
}

func TestMintRunnerID_ShapeAndUniqueness(t *testing.T) {
	a := runner.MintRunnerID()
	b := runner.MintRunnerID()
	if a == b {
		t.Fatalf("two consecutive mints collided: %s", a)
	}
	for _, id := range []string{a, b} {
		if !strings.HasPrefix(id, "wrkr_") {
			t.Errorf("missing wrkr_ prefix: %s", id)
		}
		if len(id) > 5+64 { // "wrkr_" + 64 chars max per schema
			t.Errorf("id too long: %s", id)
		}
	}
}

func TestValidateSPIFFE(t *testing.T) {
	cases := []struct {
		name  string
		id    string
		valid bool
	}{
		{"happy", "spiffe://works-execution/ns/acme/sa/wrkr_abc123", true},
		{"wrong_domain", "spiffe://example.com/ns/acme/sa/wrkr_abc", false},
		{"missing_path", "spiffe://works-execution", false},
		{"missing_sa", "spiffe://works-execution/ns/acme", false},
		{"uppercase", "spiffe://works-execution/ns/Acme/sa/wrkr_abc", false},
		{"empty", "", false},
		{"extra_path", "spiffe://works-execution/ns/acme/sa/wrkr_abc/extra", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := runner.ValidateSPIFFE(c.id)
			if c.valid && err != nil {
				t.Errorf("expected valid, got error: %v", err)
			}
			if !c.valid && err == nil {
				t.Errorf("expected invalid, got nil for %q", c.id)
			}
		})
	}
}

func TestBuildSPIFFE_AndIdentity(t *testing.T) {
	spiffe := runner.BuildSPIFFE("acme", "wrkr_deadbeef")
	if err := runner.ValidateSPIFFE(spiffe); err != nil {
		t.Fatalf("BuildSPIFFE produced invalid id: %v", err)
	}
	if !strings.Contains(spiffe, "/ns/acme/sa/wrkr_deadbeef") {
		t.Errorf("BuildSPIFFE shape wrong: %s", spiffe)
	}

	id, err := runner.BuildIdentity("wrkr_cafe0123", "acme", sampleCaps())
	if err != nil {
		t.Fatalf("BuildIdentity: %v", err)
	}
	if id.SpiffeID == "" {
		t.Error("SpiffeID not set")
	}
	if id.LifecycleState != runner.StatePending {
		t.Errorf("default lifecycle: got %s, want pending", id.LifecycleState)
	}
	if id.EnrolledAt.IsZero() {
		t.Error("EnrolledAt not stamped")
	}
}

func TestBuildIdentity_RejectsBadInputs(t *testing.T) {
	cases := []struct {
		name   string
		id     string
		tenant string
		caps   runner.Capabilities
	}{
		{"bad_runner_id", "not_wrkr_prefix", "acme", sampleCaps()},
		{"empty_tenant", "wrkr_abc", "", sampleCaps()},
		{"no_os", "wrkr_abc", "acme", runner.Capabilities{Arch: []string{"amd64"}}},
		{"no_arch", "wrkr_abc", "acme", runner.Capabilities{OS: []string{"linux"}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := runner.BuildIdentity(c.id, c.tenant, c.caps); err == nil {
				t.Errorf("expected error for case %s", c.name)
			}
		})
	}
}

func TestIdentityValidate_FullCoverage(t *testing.T) {
	id, err := runner.BuildIdentity(runner.MintRunnerID(), "acme", sampleCaps())
	if err != nil {
		t.Fatal(err)
	}
	if err := id.Validate(); err != nil {
		t.Fatalf("fresh identity should validate: %v", err)
	}
	// Bad trust_class.
	id.TrustClass = "ghost"
	if err := id.Validate(); err == nil {
		t.Error("expected error for bad trust_class")
	}
	id.TrustClass = runner.TrustPrivileged
	// Bad lifecycle_state.
	id.LifecycleState = "exploding"
	if err := id.Validate(); err == nil {
		t.Error("expected error for bad lifecycle_state")
	}
	id.LifecycleState = runner.StateActive
	// Bad SPIFFE.
	id.SpiffeID = "spiffe://other.example/ns/acme/sa/wrkr_abc"
	if err := id.Validate(); err == nil {
		t.Error("expected error for wrong-domain spiffe")
	}
	id.SpiffeID = ""
	// Missing enrolled_at.
	id.EnrolledAt = time.Time{}
	if err := id.Validate(); err == nil {
		t.Error("expected error for missing enrolled_at")
	}
}

func TestIdentity_MatchesEmbeddedSchema(t *testing.T) {
	id, err := runner.BuildIdentity(runner.MintRunnerID(), "acme", sampleCaps())
	if err != nil {
		t.Fatal(err)
	}
	id.LifecycleState = runner.StateActive
	b, err := json.Marshal(id)
	if err != nil {
		t.Fatal(err)
	}
	if err := standards.ValidateBytes("runner-identity.schema.json", b); err != nil {
		t.Fatalf("schema validation failed for valid identity: %v\npayload=%s", err, b)
	}
}

// --- API endpoint tests ---

func newRunnerServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := &api.Server{}
	ts := httptest.NewServer(srv.Routes())
	t.Cleanup(ts.Close)
	return ts
}

func postIdentity(t *testing.T, url string, body any) (*http.Response, []byte) {
	t.Helper()
	b, _ := json.Marshal(body)
	resp, err := http.Post(url, "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	buf := new(bytes.Buffer)
	_, _ = buf.ReadFrom(resp.Body)
	return resp, buf.Bytes()
}

func TestRegisterRunner_201(t *testing.T) {
	ts := newRunnerServer(t)

	// Caller omits runner_id; server must mint one and return 201.
	body := map[string]any{
		"spiffe_id": "spiffe://works-execution/ns/acme/sa/wrkr_seed0001",
		"trust_class": string(runner.TrustStandard),
		"capabilities": map[string]any{
			"os":    []string{"linux"},
			"arch":  []string{"amd64"},
			"cpu_milli":  1000,
			"memory_mib": 512,
		},
		"lifecycle_state": "active",
	}
	resp, raw := postIdentity(t, ts.URL+"/v1/runners/register", body)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status: got %d, want 201; body=%s", resp.StatusCode, raw)
	}
	var got runner.Identity
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !strings.HasPrefix(got.RunnerID, "wrkr_") {
		t.Errorf("server did not mint runner_id: %q", got.RunnerID)
	}
	if got.SpiffeID == "" {
		t.Error("spiffe_id missing in response")
	}
	if got.EnrolledAt.IsZero() {
		t.Error("enrolled_at missing in response")
	}
}

func TestRegisterRunner_Idempotent(t *testing.T) {
	ts := newRunnerServer(t)
	fixedID := runner.MintRunnerID()
	body := map[string]any{
		"runner_id": fixedID,
		"trust_class": "standard",
		"capabilities": map[string]any{
			"os":   []string{"linux"},
			"arch": []string{"amd64"},
		},
		"lifecycle_state": "active",
	}
	resp1, raw1 := postIdentity(t, ts.URL+"/v1/runners/register", body)
	if resp1.StatusCode != http.StatusCreated {
		t.Fatalf("first register: got %d body=%s", resp1.StatusCode, raw1)
	}
	resp2, raw2 := postIdentity(t, ts.URL+"/v1/runners/register", body)
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("re-register: got %d body=%s", resp2.StatusCode, raw2)
	}
	var first, second runner.Identity
	_ = json.Unmarshal(raw1, &first)
	_ = json.Unmarshal(raw2, &second)
	if first.RunnerID != second.RunnerID || first.SpiffeID != second.SpiffeID {
		t.Errorf("re-register changed identity: first=%+v second=%+v", first, second)
	}
}

func TestRegisterRunner_BadSPIFFE_400(t *testing.T) {
	ts := newRunnerServer(t)
	body := map[string]any{
		"spiffe_id": "spiffe://other.example/ns/acme/sa/wrkr_abc",
		"trust_class": "standard",
		"capabilities": map[string]any{
			"os":   []string{"linux"},
			"arch": []string{"amd64"},
		},
		"lifecycle_state": "active",
	}
	resp, raw := postIdentity(t, ts.URL+"/v1/runners/register", body)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status: got %d want 400; body=%s", resp.StatusCode, raw)
	}
	if !bytes.Contains(raw, []byte("validation_failed")) {
		t.Errorf("expected validation_failed code, got: %s", raw)
	}
}

func TestRegisterRunner_BadJSON_400(t *testing.T) {
	ts := newRunnerServer(t)
	resp, err := http.Post(ts.URL+"/v1/runners/register", "application/json",
		strings.NewReader("not-json"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status: got %d want 400", resp.StatusCode)
	}
}

func TestGetRunner_RoundTrip(t *testing.T) {
	ts := newRunnerServer(t)
	body := map[string]any{
		"runner_id": "wrkr_gettest01",
		"trust_class": "standard",
		"capabilities": map[string]any{
			"os":   []string{"darwin"},
			"arch": []string{"arm64"},
		},
		"lifecycle_state": "active",
	}
	resp, _ := postIdentity(t, ts.URL+"/v1/runners/register", body)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("register failed: %d", resp.StatusCode)
	}
	resp, err := http.Get(ts.URL + "/v1/runners/wrkr_gettest01")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET status: got %d", resp.StatusCode)
	}
	var got runner.Identity
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.RunnerID != "wrkr_gettest01" {
		t.Errorf("runner_id round-trip mismatch: %q", got.RunnerID)
	}
}

func TestGetRunner_404(t *testing.T) {
	ts := newRunnerServer(t)
	resp, err := http.Get(ts.URL + "/v1/runners/wrkr_nope")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status: got %d want 404", resp.StatusCode)
	}
}