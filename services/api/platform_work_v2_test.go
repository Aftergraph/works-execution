package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JonasAbde/works-execution/packages/workgraph"
	"github.com/JonasAbde/works-execution/services/work/store"
)

func TestPlatformWorkV2CreatesCanonicalCreatedWorkWithoutWorkerJWT(t *testing.T) {
	const token = "works-platform-api-create-test-0123456789abcdef"
	const bridge = "works-platform-bridge-create-test-0123456789abcdef"
	t.Setenv("WORKS_PLATFORM_BRIDGE_SECRET", bridge)

	st, err := store.Open(filepath.Join(t.TempDir(), "platform-create.db"))
	if err != nil { t.Fatal(err) }
	t.Cleanup(func() { _ = st.Close() })

	srv := &Server{Store: st, PlatformAPIToken: []byte(token)}
	body := `{
		"source":{"type":"api","actor":"aftergraph-runtime"},
		"objective":{"type":"custom","description":"Lume governed computer session"},
		"graph":{"nodes":{"computer":{"id":"computer","run":"runtime:computer"}}},
		"requirements":{"os":"linux","arch":"amd64","pool":"vds"},
		"policy":{"trust_class":"standard"},
		"idempotency_key":"lume-computer-test-1"
	}`
	req := httptest.NewRequest(http.MethodPost, "/v2/works", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Works-Platform-Bridge", bridge)
	rec := httptest.NewRecorder()

	srv.createPlatformWorkV2(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var work workgraph.Work
	if err := json.Unmarshal(rec.Body.Bytes(), &work); err != nil { t.Fatal(err) }
	if work.ID == "" || work.State != workgraph.StateCreated {
		t.Fatalf("work=%+v", work)
	}
	got, err := st.GetWork(req.Context(), work.ID)
	if err != nil { t.Fatal(err) }
	if got.State != workgraph.StateCreated {
		t.Fatalf("persisted state=%s want CREATED", got.State)
	}
}

func TestPlatformWorkV2RejectsMissingBridgeBeforeMutation(t *testing.T) {
	const token = "works-platform-api-create-test-0123456789abcdef"
	t.Setenv("WORKS_PLATFORM_BRIDGE_SECRET", "works-platform-bridge-create-test-0123456789abcdef")
	st, err := store.Open(filepath.Join(t.TempDir(), "platform-create-denied.db"))
	if err != nil { t.Fatal(err) }
	t.Cleanup(func() { _ = st.Close() })

	srv := &Server{Store: st, PlatformAPIToken: []byte(token)}
	req := httptest.NewRequest(http.MethodPost, "/v2/works", strings.NewReader(`{
		"source":{"type":"api"},
		"objective":{"type":"custom"},
		"graph":{"nodes":{"computer":{"id":"computer","run":"runtime:computer"}}}
	}`))
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	srv.createPlatformWorkV2(rec, req)
	if rec.Code != http.StatusUnauthorized && rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	list, err := st.ListWorks(req.Context(), 10)
	if err != nil { t.Fatal(err) }
	if len(list) != 0 { t.Fatalf("denied create persisted work: %+v", list) }
}
