package store

// k-link-01 store tests: the v10 migration lands the link tables; device +
// mount rows survive a reopen (durable, not process-local); corrupt scopes
// fail closed on read.

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JonasAbde/works-execution/packages/link"
)

func openLinkStore(t *testing.T) (*SQLiteStore, string) {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "link.db")
	st, err := Open(p)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st, p
}

func TestLinkSchemaV10(t *testing.T) {
	st, _ := openLinkStore(t)
	var got int
	if err := st.db.QueryRow(`SELECT version FROM schema_version ORDER BY version DESC LIMIT 1`).Scan(&got); err != nil {
		t.Fatalf("schema_version read: %v", err)
	}
	if got != SchemaVersion {
		t.Fatalf("head schema = %d, want %d", got, SchemaVersion)
	}
	// Tables exist and enforce the frozen state enum.
	if _, err := st.db.Exec(`INSERT INTO link_devices (device_id, scopes_json, state, paired_at) VALUES ('dev_x','["T1_read"]','DISPLAY_CODE','x')`); err == nil {
		t.Fatal("link_devices accepted a non-PAIRED/REVOKED state — CHECK law missing")
	}
}

func TestLinkDeviceRoundtrip(t *testing.T) {
	st, p := openLinkStore(t)
	ctx := context.Background()
	ls := st.LinkDevices()
	now := mustNow(t)
	if err := ls.PutDevice(ctx, &link.Device{
		DeviceID: "dev_rt", Scopes: []string{link.ScopeT1Read, link.ScopeT2Action},
		State: link.StatePaired, PairedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	// Reopen: durability across process restarts is the point of v10.
	st2, err := Open(p)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer st2.Close()
	d, err := st2.LinkDevices().GetDevice(ctx, "dev_rt")
	if err != nil {
		t.Fatalf("get after reopen: %v", err)
	}
	if d.State != link.StatePaired || len(d.Scopes) != 2 || !d.RevokedAt.IsZero() {
		t.Fatalf("roundtrip drift: %+v", d)
	}
	// Unknown device fails closed with the sentinel.
	if _, err := st2.LinkDevices().GetDevice(ctx, "dev_ghost"); err != link.ErrUnknownDevice {
		t.Fatalf("unknown device: got %v, want ErrUnknownDevice", err)
	}
}

func TestLinkDeviceCorruptScopesFailClosed(t *testing.T) {
	st, _ := openLinkStore(t)
	ctx := context.Background()
	if _, err := st.db.Exec(`INSERT INTO link_devices (device_id, scopes_json, state, paired_at) VALUES ('dev_bad','{not json','PAIRED','2026-09-02T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.LinkDevices().GetDevice(ctx, "dev_bad"); err == nil {
		t.Fatal("corrupt scopes row read back as valid — fail-open")
	}
}

func TestLinkMountIdempotency(t *testing.T) {
	st, _ := openLinkStore(t)
	ctx := context.Background()
	ls := st.LinkDevices()
	rec := &link.MountRecord{
		ID: "mnt_test", DeviceID: "dev_m", WorkID: "wrk_m", PayloadHash: hash64(),
		Scope: link.ScopeT1Read, PurposeBinding: "wrk_m", CreatedAt: mustNow(t),
	}
	created, err := ls.InsertMount(ctx, rec)
	if err != nil || !created {
		t.Fatalf("first insert: created=%v err=%v", created, err)
	}
	again, err := ls.InsertMount(ctx, rec)
	if err != nil || again {
		t.Fatalf("replay must report not-created: created=%v err=%v", again, err)
	}
	var n int
	if err := st.db.QueryRow(`SELECT count(*) FROM link_mounts`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("replay created a second row (n=%d) — idempotency law broken", n)
	}
	got, err := ls.GetMount(ctx, "mnt_test")
	if err != nil || got.WorkID != "wrk_m" {
		t.Fatalf("get mount: %v %+v", err, got)
	}
	var _ *sql.DB = st.db // keep import honest for future raw-SQL assertions
}

func hash64() string { return "a" + strings.Repeat("b", 63) }

func mustNow(t *testing.T) time.Time {
	t.Helper()
	return time.Now().UTC().Truncate(time.Millisecond)
}
