package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

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
	gotWork, gotLease, err := recoverFixture(ctx, st, prior, db, time.Now().UTC())
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
	if _, _, err := recoverFixture(ctx, st, prior, db, time.Now().UTC()); err == nil {
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
	if _, _, err := recoverFixture(ctx, st, prior, db, time.Now().UTC()); err == nil {
		t.Fatal("expected terminal lease rejection")
	}
}

var _ = workgraph.LeaseActive
