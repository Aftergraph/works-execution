package workspace

import (
	"context"
	"encoding/json"
	"strings"
	"errors"
	"testing"
	"time"
)

func validSpec() Spec {
	return Spec{
		IdempotencyKey: "idem-1",
		WorkID: "wrk_0123456789abcdef0123456789abcdef",
		Org: "aftergraph",
		Name: "task-123",
		Baseline: SourceRef{
			Provider: "github",
			Repository: "Aftergraph/runtime",
			Ref: "refs/heads/main",
			SHA: "0123456789012345678901234567890123456789",
		},
		Mode: ModeWrite,
		TTL: time.Hour,
	}
}

func TestReferenceProviderConformance(t *testing.T) {
	ctx := context.Background()
	p := NewReferenceProvider("reference")
	spec := validSpec()

	ws, err := p.Create(ctx, spec)
	if err != nil { t.Fatal(err) }
	if err := ws.Validate(); err != nil { t.Fatalf("invalid workspace: %v", err) }
	if ws.CredentialRef == nil || ws.CredentialRef.String()[:9] != "secret://" {
		t.Fatalf("credential crossed boundary as non-ref: %#v", ws.CredentialRef)
	}

	again, err := p.Create(ctx, spec)
	if err != nil { t.Fatal(err) }
	if again.ID != ws.ID {
		t.Fatalf("idempotency broken: %s != %s", again.ID, ws.ID)
	}

	cand, err := p.Candidate(ctx, ws)
	if err != nil { t.Fatal(err) }
	if cand.WorkspaceID != ws.ID {
		t.Fatalf("candidate lost workspace provenance: %#v", cand)
	}

	foreign := ws
	foreign.Org = "other"
	if _, err := p.Candidate(ctx, foreign); !errors.Is(err, ErrForeignWorkspace) {
		t.Fatalf("foreign workspace accepted: %v", err)
	}

	if err := p.RevokeCredential(ctx, ws); err != nil { t.Fatal(err) }
	got, err := p.Get(ctx, ws)
	if err != nil { t.Fatal(err) }
	if got.CredentialRef == nil {
		t.Fatal("revocation removed inert credential ref from durable handle")
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("revocation made workspace structurally invalid: %v", err)
	}

	if err := p.Destroy(ctx, ws); err != nil { t.Fatal(err) }
	if _, err := p.Get(ctx, ws); !errors.Is(err, ErrNotFound) {
		t.Fatalf("destroy did not remove workspace: %v", err)
	}
}

func TestSpecFailsClosed(t *testing.T) {
	cases := []Spec{
		{},
		func() Spec { s:=validSpec(); s.WorkID=""; return s }(),
		func() Spec { s:=validSpec(); s.Mode="owner"; return s }(),
		func() Spec { s:=validSpec(); s.Baseline.Provider=""; return s }(),
		func() Spec { s:=validSpec(); s.Baseline.Ref=""; s.Baseline.SHA=""; return s }(),
	}
	for i, spec := range cases {
		if err := spec.Validate(); err == nil {
			t.Fatalf("case %d unexpectedly accepted", i)
		}
	}
}

func TestWorkspaceCredentialRefWireIsString(t *testing.T) {
	p := NewReferenceProvider("reference")
	ws, err := p.Create(context.Background(), validSpec())
	if err != nil { t.Fatal(err) }

	b, err := json.Marshal(ws)
	if err != nil { t.Fatal(err) }
	if strings.Contains(string(b), `"Value"`) {
		t.Fatalf("Go secret-ref implementation leaked into workspace wire: %s", b)
	}
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil { t.Fatal(err) }
	got, ok := doc["credential_ref"].(string)
	if !ok {
		t.Fatalf("credential_ref wire type=%T want string: %s", doc["credential_ref"], b)
	}
	if got != ws.CredentialRef.String() {
		t.Fatalf("credential_ref=%q want %q", got, ws.CredentialRef.String())
	}

	var round Workspace
	if err := json.Unmarshal(b, &round); err != nil { t.Fatal(err) }
	if round.CredentialRef == nil || round.CredentialRef.String() != got {
		t.Fatalf("roundtrip lost credential ref: %#v", round.CredentialRef)
	}
}

func TestWorkspaceValidateRejectsModeAndBaselineDrift(t *testing.T) {
	p := NewReferenceProvider("reference")
	ws, err := p.Create(context.Background(), validSpec())
	if err != nil { t.Fatal(err) }

	badMode := ws
	badMode.Mode = "owner"
	if err := badMode.Validate(); err == nil {
		t.Fatal("invalid workspace mode accepted")
	}

	badBaseline := ws
	badBaseline.Baseline.Provider = ""
	if err := badBaseline.Validate(); err == nil {
		t.Fatal("invalid workspace baseline accepted")
	}
}
