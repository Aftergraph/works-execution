package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"
)

var workspaceGitSHA = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)

// ConformanceSuite is executable evidence that a Workspace Provider honors
// workspace/1.0. Interface compliance alone is not sufficient.
func ConformanceSuite(t *testing.T, p Provider) {
	t.Helper()
	ctx := context.Background()
	base := Spec{
		IdempotencyKey: "conf-1",
		WorkID:         "wrk_0123456789abcdef0123456789abcdef",
		Org:            "aftergraph",
		Name:           "conformance",
		Baseline: SourceRef{
			Provider:   "github",
			Repository: "Aftergraph/runtime",
			Ref:        "refs/heads/main",
			SHA:        "0123456789012345678901234567890123456789",
		},
		Mode: ModeWrite,
		TTL:  time.Hour,
	}

	t.Run("create/valid-handle", func(t *testing.T) {
		ws, err := p.Create(ctx, base)
		if err != nil {
			t.Fatal(err)
		}
		if err := ws.Validate(); err != nil {
			t.Fatalf("invalid workspace handle: %v", err)
		}
		b, err := json.Marshal(ws)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), `"Value"`) {
			t.Fatalf("secret-ref implementation shape leaked: %s", b)
		}
		if !strings.Contains(string(b), `"credential_ref":"secret://`) {
			t.Fatalf("credential ref is not an inert string: %s", b)
		}
	})

	t.Run("create/replay-same-spec-idempotent", func(t *testing.T) {
		first, err := p.Create(ctx, base)
		if err != nil {
			t.Fatal(err)
		}
		again, err := p.Create(ctx, base)
		if err != nil {
			t.Fatal(err)
		}
		if first.ID != again.ID {
			t.Fatalf("same idempotency key + same spec produced two workspaces: %s != %s", first.ID, again.ID)
		}
	})

	ws, err := p.Create(ctx, Spec{
		IdempotencyKey: "conf-2",
		WorkID:         base.WorkID,
		Org:            base.Org,
		Name:           "lifecycle",
		Baseline:       base.Baseline,
		Mode:           ModeWrite,
		TTL:            time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}

	t.Run("get/cross-tenant-fails-closed", func(t *testing.T) {
		foreign := ws
		foreign.Org = "other"
		if _, err := p.Get(ctx, foreign); !errors.Is(err, ErrForeignWorkspace) {
			t.Fatalf("got %v, want ErrForeignWorkspace", err)
		}
	})

	t.Run("candidate/immutable-sha-and-provenance", func(t *testing.T) {
		c, err := p.Candidate(ctx, ws)
		if err != nil {
			t.Fatal(err)
		}
		if c.WorkspaceID != ws.ID {
			t.Fatalf("candidate lost workspace provenance: %#v", c)
		}
		if !workspaceGitSHA.MatchString(c.SHA) {
			t.Fatalf("candidate sha is not immutable git shape: %q", c.SHA)
		}
	})

	t.Run("candidate/cross-tenant-fails-closed", func(t *testing.T) {
		foreign := ws
		foreign.WorkID = "wrk_evil"
		if _, err := p.Candidate(ctx, foreign); !errors.Is(err, ErrForeignWorkspace) {
			t.Fatalf("got %v, want ErrForeignWorkspace", err)
		}
	})

	t.Run("revoke/handle-remains-structurally-valid", func(t *testing.T) {
		if err := p.RevokeCredential(ctx, ws); err != nil {
			t.Fatal(err)
		}
		got, err := p.Get(ctx, ws)
		if err != nil {
			t.Fatal(err)
		}
		if err := got.Validate(); err != nil {
			t.Fatalf("revoked workspace handle is invalid: %v", err)
		}
	})

	t.Run("destroy/makes-handle-stale", func(t *testing.T) {
		if err := p.Destroy(ctx, ws); err != nil {
			t.Fatal(err)
		}
		if _, err := p.Get(ctx, ws); !errors.Is(err, ErrNotFound) {
			t.Fatalf("get after destroy: %v", err)
		}
		if _, err := p.Candidate(ctx, ws); !errors.Is(err, ErrNotFound) {
			t.Fatalf("candidate after destroy: %v", err)
		}
	})
}

func TestWorkspaceConformanceReferenceProvider(t *testing.T) {
	ConformanceSuite(t, NewReferenceProvider("reference"))
}
