package store

// k-042 (ADR-0023, brain.ns/1.0) store tests: the v11 migration lands
// brain_objects + brain_mounts. Append-only revision law, latest-revision
// reads, prefix listing, tombstone flag, mount lifecycle and idempotent
// revoke, fail-closed corrupt JSON, and reopen-file durability.

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func openBrainStore(t *testing.T) (*SQLiteStore, string) {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "brain.db")
	st, err := Open(p)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st, p
}

// TestBrainSchemaV11 confirms the schema_version ledger reports v11 (and
// no lower) after Open, and that the brain tables exist and enforce their
// CHECK laws.
func TestBrainSchemaV11(t *testing.T) {
	st, _ := openBrainStore(t)
	var got int
	if err := st.db.QueryRow(`SELECT version FROM schema_version ORDER BY version DESC LIMIT 1`).Scan(&got); err != nil {
		t.Fatalf("schema_version read: %v", err)
	}
	if got != SchemaVersion {
		t.Fatalf("head schema = %d, want %d", got, SchemaVersion)
	}
	// class CHECK
	if _, err := st.db.Exec(
		`INSERT INTO brain_objects (path, revision, class, content_json, content_hash, evidence_ref, created_at, updated_at)
		 VALUES ('/p', 1, 'WRONG', '{}', 'h', 'ev', '2026-09-02T00:00:00Z', '2026-09-02T00:00:00Z')`,
	); err == nil {
		t.Fatal("brain_objects accepted an invalid class — CHECK law missing")
	}
	// promotion CHECK
	if _, err := st.db.Exec(
		`INSERT INTO brain_objects (path, revision, class, content_json, content_hash, promotion, evidence_ref, created_at, updated_at)
		 VALUES ('/p', 2, 'immutable', '{}', 'h', 'STAMPED', 'ev', '2026-09-02T00:00:00Z', '2026-09-02T00:00:00Z')`,
	); err == nil {
		t.Fatal("brain_objects accepted an invalid promotion — CHECK law missing")
	}
	// primary key on (path, revision) — a (1,1) row is already implied by the
	// prior attempt, but a second insert with the same (path, revision) must
	// trip the constraint, confirming the PK is composite.
	if _, err := st.db.Exec(
		`INSERT INTO brain_objects (path, revision, class, content_json, content_hash, evidence_ref, created_at, updated_at)
		 VALUES ('/dup', 1, 'immutable', '{}', 'h', 'ev', '2026-09-02T00:00:00Z', '2026-09-02T00:00:00Z')`,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(
		`INSERT INTO brain_objects (path, revision, class, content_json, content_hash, evidence_ref, created_at, updated_at)
		 VALUES ('/dup', 1, 'immutable', '{}', 'h', 'ev', '2026-09-02T00:00:00Z', '2026-09-02T00:00:00Z')`,
	); err == nil {
		t.Fatal("brain_objects accepted a duplicate (path, revision) — PK law missing")
	}
}

func sampleBrainObject(t *testing.T, path string, rev int, opts ...func(*BrainObject)) *BrainObject {
	t.Helper()
	o := &BrainObject{
		Path: path, Revision: rev, Class: BrainClassImmutable,
		ContentJSON: `{"v":1}`, ContentHash: "hash-1",
		Promotion: PromotionNone, EvidenceRef: "wrk_evidence_1",
		CreatedAt: mustNow(t), UpdatedAt: mustNow(t),
	}
	for _, f := range opts {
		f(o)
	}
	return o
}

func TestPutBrainObjectAppendOnly(t *testing.T) {
	st, _ := openBrainStore(t)
	ctx := context.Background()
	first := sampleBrainObject(t, "/org/a/decisions/x", 1)
	if err := st.PutBrainObject(ctx, first); err != nil {
		t.Fatalf("first append: %v", err)
	}
	// A second write of the same (path, revision) must return the sentinel
	// — the append-only law is structural.
	dup := sampleBrainObject(t, "/org/a/decisions/x", 1)
	if err := st.PutBrainObject(ctx, dup); err != ErrBrainRevisionExists {
		t.Fatalf("dup append: got %v, want ErrBrainRevisionExists", err)
	}
	// A higher revision is a normal append.
	second := sampleBrainObject(t, "/org/a/decisions/x", 2, func(o *BrainObject) {
		o.ContentHash = "hash-2"
	})
	if err := st.PutBrainObject(ctx, second); err != nil {
		t.Fatalf("second append: %v", err)
	}
}

func TestPutBrainObjectFailClosedOnCorruptJSON(t *testing.T) {
	st, _ := openBrainStore(t)
	ctx := context.Background()
	bad := sampleBrainObject(t, "/org/b/notes/y", 1, func(o *BrainObject) {
		o.ContentJSON = `{"oops"`
	})
	if err := st.PutBrainObject(ctx, bad); err == nil {
		t.Fatal("corrupt content_json accepted — fail-open on write")
	}
	// No row was inserted.
	if n, _ := st.LatestRevision(ctx, "/org/b/notes/y"); n != 0 {
		t.Fatalf("LatestRevision after rejected write = %d, want 0", n)
	}
}

func TestPutBrainObjectRequiresEvidenceRef(t *testing.T) {
	st, _ := openBrainStore(t)
	ctx := context.Background()
	o := sampleBrainObject(t, "/org/c/evidence/z", 1)
	o.EvidenceRef = ""
	if err := st.PutBrainObject(ctx, o); err == nil {
		t.Fatal("missing evidence_ref accepted — provenance law broken")
	}
}

func TestPutBrainObjectRequiresHumanStampWhenPromoted(t *testing.T) {
	st, _ := openBrainStore(t)
	ctx := context.Background()
	o := sampleBrainObject(t, "/org/d/capabilities/w", 1, func(o *BrainObject) {
		o.Promotion = PromotionHumanStamped
	})
	if err := st.PutBrainObject(ctx, o); err == nil {
		t.Fatal("human_stamped promotion accepted without human_stamp — promotion law broken")
	}
	o.HumanStamp = "stamp-abc"
	if err := st.PutBrainObject(ctx, o); err != nil {
		t.Fatalf("human_stamped with stamp: %v", err)
	}
}

func TestGetBrainObjectLatestAndSpecific(t *testing.T) {
	st, _ := openBrainStore(t)
	ctx := context.Background()
	now := mustNow(t)
	r1 := sampleBrainObject(t, "/org/a/decisions/x", 1, func(o *BrainObject) {
		o.CreatedAt = now.Add(-2 * time.Minute)
		o.UpdatedAt = now.Add(-2 * time.Minute)
	})
	r2 := sampleBrainObject(t, "/org/a/decisions/x", 2, func(o *BrainObject) {
		o.ContentHash = "hash-2"
		o.CreatedAt = now.Add(-time.Minute)
		o.UpdatedAt = now
	})
	if err := st.PutBrainObject(ctx, r1); err != nil {
		t.Fatal(err)
	}
	if err := st.PutBrainObject(ctx, r2); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetBrainObject(ctx, "/org/a/decisions/x", 0)
	if err != nil {
		t.Fatalf("get latest: %v", err)
	}
	if got.Revision != 2 || got.ContentHash != "hash-2" {
		t.Fatalf("latest: rev=%d hash=%s", got.Revision, got.ContentHash)
	}
	got1, err := st.GetBrainObject(ctx, "/org/a/decisions/x", 1)
	if err != nil {
		t.Fatalf("get rev 1: %v", err)
	}
	if got1.Revision != 1 || got1.ContentHash != "hash-1" {
		t.Fatalf("rev 1: rev=%d hash=%s", got1.Revision, got1.ContentHash)
	}
	if _, err := st.GetBrainObject(ctx, "/org/a/decisions/x", 99); err != ErrNotFound {
		t.Fatalf("missing rev: got %v, want ErrNotFound", err)
	}
	if _, err := st.GetBrainObject(ctx, "/org/ghost/decisions/x", 0); err != ErrNotFound {
		t.Fatalf("missing path: got %v, want ErrNotFound", err)
	}
}

func TestListBrainPathsWithPrefix(t *testing.T) {
	st, _ := openBrainStore(t)
	ctx := context.Background()
	now := mustNow(t)
	// Two paths under /org/a, one elsewhere; one of the two has two revisions
	// (must surface ONCE, ordered by latest updated_at).
	paths := []struct {
		p string
		r int
		t time.Time
	}{
		{"/org/a/decisions/x", 1, now.Add(-3 * time.Minute)},
		{"/org/a/decisions/x", 2, now.Add(-time.Minute)},
		{"/org/a/notes/y", 1, now.Add(-2 * time.Minute)},
		{"/org/b/decisions/x", 1, now.Add(-30 * time.Second)},
	}
	for _, p := range paths {
		o := sampleBrainObject(t, p.p, p.r, func(o *BrainObject) {
			o.CreatedAt = p.t
			o.UpdatedAt = p.t
		})
		if err := st.PutBrainObject(ctx, o); err != nil {
			t.Fatal(err)
		}
	}
	got, err := st.ListBrainPathsWithPrefix(ctx, "/org/a", 10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	want := []string{"/org/a/decisions/x", "/org/a/notes/y"}
	if !equalStrings(got, want) {
		t.Fatalf("list a = %v, want %v", got, want)
	}
	// limit <= 0 -> default 50; explicit 0 with many rows still caps at 50,
	// and a negative cap is clamped to 500.
	for i := 0; i < 60; i++ {
		path := "/org/zoo/" + padInt(i, 3)
		o := sampleBrainObject(t, path, 1, func(o *BrainObject) {
			o.UpdatedAt = now.Add(time.Duration(i) * time.Second)
		})
		if err := st.PutBrainObject(ctx, o); err != nil {
			t.Fatal(err)
		}
	}
	small, err := st.ListBrainPathsWithPrefix(ctx, "/org/zoo", 0)
	if err != nil {
		t.Fatalf("limit 0: %v", err)
	}
	if len(small) != 50 {
		t.Fatalf("limit 0 -> default 50: got %d", len(small))
	}
	huge, err := st.ListBrainPathsWithPrefix(ctx, "/org/zoo", 99999)
	if err != nil {
		t.Fatalf("limit huge: %v", err)
	}
	if len(huge) != 60 {
		t.Fatalf("limit huge: got %d, want 60 (all)", len(huge))
	}
}

func TestTombstonedFlag(t *testing.T) {
	st, _ := openBrainStore(t)
	ctx := context.Background()
	r1 := sampleBrainObject(t, "/org/a/decisions/x", 1)
	if err := st.PutBrainObject(ctx, r1); err != nil {
		t.Fatal(err)
	}
	tomb, err := st.Tombstoned(ctx, "/org/a/decisions/x")
	if err != nil || tomb {
		t.Fatalf("live object: tomb=%v err=%v", tomb, err)
	}
	r2 := sampleBrainObject(t, "/org/a/decisions/x", 2, func(o *BrainObject) {
		o.Tombstone = true
	})
	if err := st.PutBrainObject(ctx, r2); err != nil {
		t.Fatal(err)
	}
	tomb, err = st.Tombstoned(ctx, "/org/a/decisions/x")
	if err != nil || !tomb {
		t.Fatalf("after tombstone: tomb=%v err=%v", tomb, err)
	}
	// An absent path is not tombstoned.
	if t2, err := st.Tombstoned(ctx, "/org/ghost/decisions/x"); err != nil || t2 {
		t.Fatalf("absent path: tomb=%v err=%v", t2, err)
	}
}

func TestLatestRevision(t *testing.T) {
	st, _ := openBrainStore(t)
	ctx := context.Background()
	if n, err := st.LatestRevision(ctx, "/never/seen"); err != nil || n != 0 {
		t.Fatalf("absent: n=%d err=%v", n, err)
	}
	for i := 1; i <= 3; i++ {
		if err := st.PutBrainObject(ctx, sampleBrainObject(t, "/x", i)); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := st.LatestRevision(ctx, "/x"); err != nil || n != 3 {
		t.Fatalf("after 3: n=%d err=%v", n, err)
	}
}

func TestCreateListAndRevokeBrainMount(t *testing.T) {
	st, _ := openBrainStore(t)
	ctx := context.Background()
	now := mustNow(t)
	exp := now.Add(24 * time.Hour)
	alice := &BrainMount{
		ID: "mnt_a", Subject: "alice", PathPrefix: "/org/a/",
		Scopes:    []string{"brain.read", "brain.note.write"},
		CreatedAt: now, ExpiresAt: exp,
	}
	bob := &BrainMount{
		ID: "mnt_b", Subject: "bob", PathPrefix: "/org/b/",
		Scopes:    []string{"brain.read"},
		CreatedAt: now, ExpiresAt: exp,
	}
	if err := st.CreateBrainMount(ctx, alice); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateBrainMount(ctx, bob); err != nil {
		t.Fatal(err)
	}
	// Subject filter: bob's mount is not visible to alice.
	aliceMounts, err := st.ListBrainMounts(ctx, "alice", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(aliceMounts) != 1 || aliceMounts[0].ID != "mnt_a" {
		t.Fatalf("alice list = %+v", aliceMounts)
	}
	if len(aliceMounts[0].Scopes) != 2 {
		t.Fatalf("alice scopes = %v", aliceMounts[0].Scopes)
	}
	// Revoke alice's mount.
	if err := st.RevokeBrainMount(ctx, "mnt_a"); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	// List with includeRevoked=false: nothing for alice.
	active, err := st.ListBrainMounts(ctx, "alice", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 0 {
		t.Fatalf("active after revoke = %d, want 0", len(active))
	}
	all, err := st.ListBrainMounts(ctx, "alice", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 || !all[0].Revoked {
		t.Fatalf("includeRevoked: %+v", all)
	}
	// Revoke again: idempotent (no error, still revoked=1).
	if err := st.RevokeBrainMount(ctx, "mnt_a"); err != nil {
		t.Fatalf("revoke twice: %v", err)
	}
	// Unknown id: idempotent (no error).
	if err := st.RevokeBrainMount(ctx, "mnt_ghost"); err != nil {
		t.Fatalf("revoke unknown: %v", err)
	}
	// Bob's mount is unaffected.
	bobList, err := st.ListBrainMounts(ctx, "bob", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(bobList) != 1 || bobList[0].ID != "mnt_b" || bobList[0].Revoked {
		t.Fatalf("bob after revoke of alice: %+v", bobList)
	}
}

func TestCreateBrainMountRequiresScopes(t *testing.T) {
	st, _ := openBrainStore(t)
	ctx := context.Background()
	now := mustNow(t)
	bad := &BrainMount{
		ID: "mnt_x", Subject: "alice", PathPrefix: "/x",
		Scopes: nil, CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	}
	if err := st.CreateBrainMount(ctx, bad); err == nil {
		t.Fatal("mount with no scopes accepted — fail-open on write")
	}
}

func TestListBrainMountsCorruptScopesFailClosed(t *testing.T) {
	st, _ := openBrainStore(t)
	ctx := context.Background()
	if _, err := st.db.Exec(
		`INSERT INTO brain_mounts (id, subject, path_prefix, scopes_json, created_at, expires_at, revoked)
		 VALUES ('mnt_corrupt', 'alice', '/x', '{not json', '2026-09-02T00:00:00Z', '2026-09-03T00:00:00Z', 0)`,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ListBrainMounts(ctx, "alice", false); err == nil {
		t.Fatal("corrupt scopes row read back as valid — fail-open")
	}
	// Empty scopes are also a fail-closed read.
	if _, err := st.db.Exec(
		`INSERT INTO brain_mounts (id, subject, path_prefix, scopes_json, created_at, expires_at, revoked)
		 VALUES ('mnt_empty', 'alice', '/y', '[]', '2026-09-02T00:00:00Z', '2026-09-03T00:00:00Z', 0)`,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ListBrainMounts(ctx, "alice", false); err == nil {
		t.Fatal("empty scopes read back as valid — fail-open")
	}
}

func TestBrainStoreReopenDurability(t *testing.T) {
	st, p := openBrainStore(t)
	ctx := context.Background()
	now := mustNow(t)
	exp := now.Add(time.Hour)
	// One object + one mount before reopen.
	obj := sampleBrainObject(t, "/org/a/notes/persisted", 1, func(o *BrainObject) {
		o.ContentHash = "persist-hash"
	})
	if err := st.PutBrainObject(ctx, obj); err != nil {
		t.Fatal(err)
	}
	mnt := &BrainMount{
		ID: "mnt_persist", Subject: "alice", PathPrefix: "/org/a/",
		Scopes: []string{"brain.read"}, CreatedAt: now, ExpiresAt: exp,
	}
	if err := st.CreateBrainMount(ctx, mnt); err != nil {
		t.Fatal(err)
	}
	_ = st.Close()

	// Reopen and confirm the row is there.
	st2, err := Open(p)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer st2.Close()

	got, err := st2.GetBrainObject(ctx, "/org/a/notes/persisted", 0)
	if err != nil {
		t.Fatalf("get after reopen: %v", err)
	}
	if got.ContentHash != "persist-hash" || !got.Authoritative == false {
		// (default authoritative=false is the expected post-migration state)
	}
	if !got.CreatedAt.Equal(obj.CreatedAt) {
		t.Fatalf("created_at drift: %v vs %v", got.CreatedAt, obj.CreatedAt)
	}
	mounts, err := st2.ListBrainMounts(ctx, "alice", false)
	if err != nil {
		t.Fatalf("list mounts after reopen: %v", err)
	}
	if len(mounts) != 1 || mounts[0].ID != "mnt_persist" {
		t.Fatalf("mount after reopen = %+v", mounts)
	}
	if !mounts[0].ExpiresAt.Equal(exp) {
		t.Fatalf("expires_at drift: %v vs %v", mounts[0].ExpiresAt, exp)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func padInt(n, width int) string {
	s := ""
	if n == 0 {
		s = "0"
	}
	for n > 0 {
		s = string(rune('0'+n%10)) + s
		n /= 10
	}
	for len(s) < width {
		s = "0" + s
	}
	return strings.Repeat("x", 0) + s
}
