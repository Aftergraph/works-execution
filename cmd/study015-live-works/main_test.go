package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/JonasAbde/works-execution/packages/executioncontext"
	"github.com/JonasAbde/works-execution/packages/workgraph"
	"github.com/JonasAbde/works-execution/services/work/store"
)

func TestRecoverFixtureReusesDurableWorkAndLease(t *testing.T) {
	db := filepath.Join(t.TempDir(), "works.db")
	st, err := store.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	ctx := context.Background()
	w, lease, err := createFixture(ctx, st)
	if err != nil {
		t.Fatal(err)
	}
	prior := fixtureReceipt{
		Schema:        "study015.live-works-fixture/1.1",
		WorkID:        w.ID,
		WorkerLeaseID: lease.ID,
		WorkerID:      lease.WorkerID,
		DBPath:        db,
	}
	gotWork, gotLease, err := recoverFixture(ctx, st, prior, db, time.Now().UTC(), false)
	if err != nil {
		t.Fatal(err)
	}
	if gotWork.ID != w.ID || gotLease.ID != lease.ID || gotLease.WorkerID != lease.WorkerID {
		t.Fatalf("recovery rebound identity: work=%s lease=%s worker=%s", gotWork.ID, gotLease.ID, gotLease.WorkerID)
	}
}

func TestRecoverFixtureRejectsWrongDatabaseBinding(t *testing.T) {
	db := filepath.Join(t.TempDir(), "works.db")
	st, err := store.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	ctx := context.Background()
	w, lease, err := createFixture(ctx, st)
	if err != nil {
		t.Fatal(err)
	}
	prior := fixtureReceipt{
		WorkID: w.ID, WorkerLeaseID: lease.ID, WorkerID: lease.WorkerID,
		DBPath: filepath.Join(t.TempDir(), "other.db"),
	}
	if _, _, err := recoverFixture(ctx, st, prior, db, time.Now().UTC(), false); err == nil {
		t.Fatal("expected DB binding mismatch")
	}
}

func TestReadFixtureRequiresDurableIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fixture.json")
	raw, _ := json.Marshal(fixtureReceipt{Schema: "study015.live-works-fixture/1.1"})
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readFixture(path); err == nil {
		t.Fatal("expected incomplete fixture rejection")
	}
}

func TestRecoverFixtureRejectsNonActiveLease(t *testing.T) {
	db := filepath.Join(t.TempDir(), "works.db")
	st, err := store.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	ctx := context.Background()
	w, lease, err := createFixture(ctx, st)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.RevokeLease(ctx, lease.ID, "study015-test"); err != nil {
		t.Fatal(err)
	}
	prior := fixtureReceipt{
		WorkID: w.ID, WorkerLeaseID: lease.ID, WorkerID: lease.WorkerID, DBPath: db,
	}
	if _, _, err := recoverFixture(ctx, st, prior, db, time.Now().UTC(), false); err == nil {
		t.Fatal("expected terminal lease rejection")
	}
}

var _ = workgraph.LeaseActive


func TestValidateResumePathSamePathPasses(t *testing.T) {
	if err := validateResumePath("/tmp/a/works.db", "/tmp/a/works.db", false); err != nil {
		t.Fatalf("same path rejected: %v", err)
	}
}

func TestValidateResumePathMismatchFailsClosedByDefault(t *testing.T) {
	if err := validateResumePath("/host-a/works.db", "/host-b/works.db", false); err == nil {
		t.Fatal("mismatched path accepted without explicit relocation")
	}
}

func TestValidateResumePathExplicitRelocationAllowsPathChange(t *testing.T) {
	if err := validateResumePath("/host-a/works.db", "/host-b/works.db", true); err != nil {
		t.Fatalf("explicit relocation rejected: %v", err)
	}
}

func TestRecoverFixtureAllowsRelocatedDatabaseOnlyWhenIdentityExists(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "works.db")
	st, err := store.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	w, lease, err := createFixture(ctx, st)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	relocatedDir := t.TempDir()
	relocated := filepath.Join(relocatedDir, "works.db")
	raw, err := os.ReadFile(db)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(relocated, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	st2, err := store.Open(relocated)
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()

	prior := fixtureReceipt{
		WorkID: w.ID, WorkerLeaseID: lease.ID, WorkerID: lease.WorkerID, DBPath: db,
	}
	gotWork, gotLease, err := recoverFixture(ctx, st2, prior, relocated, time.Now().UTC(), true)
	if err != nil {
		t.Fatal(err)
	}
	if gotWork.ID != w.ID || gotLease.ID != lease.ID || gotLease.WorkerID != lease.WorkerID {
		t.Fatalf("relocated recovery rebound identity: work=%s lease=%s worker=%s", gotWork.ID, gotLease.ID, gotLease.WorkerID)
	}
}


func TestStudy015CorrelationReadReturnsDurablePDR(t *testing.T) {
	db := filepath.Join(t.TempDir(), "works.db")
	st, err := store.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	ctx := context.Background()
	w, lease, err := createFixture(ctx, st)
	if err != nil {
		t.Fatal(err)
	}
	platformCtx, err := st.CreateExecutionContext(ctx, executioncontext.Context{
		Schema:              "execution-context/1.0",
		OrganizationID:      "org_11111111111111111111111111111111",
		TenantID:            "ten_22222222222222222222222222222222",
		PrincipalID:         "prn_33333333333333333333333333333333",
		MissionID:           "mis_study015_test",
		AuthorityLeaseID:    "auth_44444444444444444444444444444444",
		WorkID:              w.ID,
		WorkerLeaseID:       lease.ID,
		AdmissionDecisionID: "pdr_55555555555555555555555555555555",
		TraceID:             "trc_66666666666666666666666666666666",
	})
	if err != nil {
		t.Fatal(err)
	}
	const pdr = "pdr_88888888888888888888888888888888"
	if _, _, err := st.RecordExecutionPolicyCorrelation(ctx, store.ExecutionPolicyCorrelation{
		WorkID:             w.ID,
		ExecutionContextID: platformCtx.ID,
		ExecutionPDRID:     pdr,
		BindingDigest:      "study015-test-binding",
	}); err != nil {
		t.Fatal(err)
	}

	const secret = "bridge-secret-00000000000000000000000000000000"
	req := httptest.NewRequest(
		http.MethodGet,
		"/study015/execution-policy-correlation?execution_context_id="+platformCtx.ID,
		nil,
	)
	req.Header.Set("X-Works-Platform-Bridge", secret)
	rec := httptest.NewRecorder()
	study015Handler(st, secret, http.NotFoundHandler()).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["work_id"] != w.ID || body["execution_context_id"] != platformCtx.ID || body["execution_pdr_id"] != pdr {
		t.Fatalf("wrong durable correlation: %#v", body)
	}
}

func TestStudy015CorrelationReadRequiresBridgeSecret(t *testing.T) {
	db := filepath.Join(t.TempDir(), "works.db")
	st, err := store.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	const secret = "bridge-secret-00000000000000000000000000000000"
	req := httptest.NewRequest(
		http.MethodGet,
		"/study015/execution-policy-correlation?execution_context_id=ctx_11111111111111111111111111111111",
		nil,
	)
	req.Header.Set("X-Works-Platform-Bridge", "wrong")
	rec := httptest.NewRecorder()
	study015Handler(st, secret, http.NotFoundHandler()).ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}
