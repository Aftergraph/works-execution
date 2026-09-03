package standards_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/JonasAbde/works-execution/internal/standards"
)

func tempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "audit-test")
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func writeMapping(t *testing.T, repoRoot, relPath string) {
	t.Helper()
	full := filepath.Join(repoRoot, relPath)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte("# mapping\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func makeDocs(rows ...standards.StandardRow) standards.Docs {
	return standards.Docs{
		SchemaVersion: "1.0.0",
		GeneratedAt:   "2026-08-31",
		Standards:     rows,
	}
}

func row(id, status, evidence string) standards.StandardRow {
	return standards.StandardRow{
		StandardID: id,
		Status:     status,
		Evidence:   evidence,
	}
}

func countKind(findings []standards.Finding, kind string) int {
	n := 0
	for _, f := range findings {
		if f.Kind == kind {
			n++
		}
	}
	return n
}

func TestAudit_MissingMappingFile(t *testing.T) {
	tests := []struct {
		name  string
		row   standards.StandardRow
		want  int
		setup func(dir string)
	}{
		{
			name: "evidence path that does not exist",
			row:  row("std-missing", "IMPLEMENTED", "docs/standards/mappings/ghost.md"),
			want: 1,
		},
		{
			name: "evidence is PLANNED not a path",
			row:  row("std-ok", "PLANNED", "PLANNED"),
			want: 0,
		},
		{
			name: "evidence is empty",
			row:  row("std-ok", "IMPLEMENTED", ""),
			want: 0,
		},
		{
			name: "evidence points to a schema not a mapping",
			row:  row("std-ok", "IMPLEMENTED", "docs/standards/schemas/action-manifest.schema.json"),
			want: 0,
		},
		{
			name: "existing mapping file is not reported",
			row:  row("std-ok", "IMPLEMENTED", "docs/standards/mappings/security.md"),
			want: 0,
			setup: func(dir string) {
				writeMapping(t, dir, "docs/standards/mappings/security.md")
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := tempDir(t)
			defer os.RemoveAll(dir)

			if tt.setup != nil {
				tt.setup(dir)
			}

			docs := makeDocs(tt.row)
			now := time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC)
			findings, err := standards.Audit(docs, dir, now)
			if err != nil {
				t.Fatalf("Audit: %v", err)
			}
			got := countKind(findings, "missing-mapping-file")
			if got != tt.want {
				t.Errorf("missing-mapping-file findings: got %d, want %d", got, tt.want)
			}
			for _, f := range findings {
				if f.Kind == "missing-mapping-file" && f.Severity != "High" {
					t.Errorf("severity = %q, want High", f.Severity)
				}
			}
		})
	}
}

func TestAudit_OrphanMapping(t *testing.T) {
	t.Run("unreferenced mapping file is orphan", func(t *testing.T) {
		dir := tempDir(t)
		defer os.RemoveAll(dir)

		writeMapping(t, dir, "docs/standards/mappings/ai.md")
		writeMapping(t, dir, "docs/standards/mappings/ci.md")
		writeMapping(t, dir, "docs/standards/mappings/security.md")

		docs := makeDocs(
			row("std-1", "IMPLEMENTED", "docs/standards/mappings/security.md"),
		)
		now := time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC)
		findings, err := standards.Audit(docs, dir, now)
		if err != nil {
			t.Fatalf("Audit: %v", err)
		}
		if got := countKind(findings, "orphan-mapping"); got != 2 {
			t.Errorf("orphan-mapping findings: got %d, want 2", got)
		}
		for _, f := range findings {
			if f.Kind == "orphan-mapping" && f.Severity != "High" {
				t.Errorf("severity = %q, want High", f.Severity)
			}
		}
	})

	t.Run("all referenced files are not orphans", func(t *testing.T) {
		dir := tempDir(t)
		defer os.RemoveAll(dir)

		writeMapping(t, dir, "docs/standards/mappings/security.md")
		writeMapping(t, dir, "docs/standards/mappings/ssd.md")

		docs := makeDocs(
			row("std-1", "IMPLEMENTED", "docs/standards/mappings/security.md"),
			row("std-2", "PARTIAL", "docs/standards/mappings/ssd.md"),
		)
		now := time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC)
		findings, err := standards.Audit(docs, dir, now)
		if err != nil {
			t.Fatalf("Audit: %v", err)
		}
		if got := countKind(findings, "orphan-mapping"); got != 0 {
			t.Errorf("orphan-mapping findings: got %d, want 0", got)
		}
	})
}

func TestAudit_EmptyStatus(t *testing.T) {
	tests := []struct {
		name string
		row  standards.StandardRow
		want int
	}{
		{
			name: "empty status string",
			row:  row("std-1", "", "docs/standards/mappings/security.md"),
			want: 1,
		},
		{
			name: "whitespace-only status",
			row:  row("std-2", "   ", "docs/standards/mappings/security.md"),
			want: 1,
		},
		{
			name: "valid status",
			row:  row("std-3", "IMPLEMENTED", "docs/standards/mappings/security.md"),
			want: 0,
		},
		{
			name: "PARTIAL status",
			row:  row("std-4", "PARTIAL", "docs/standards/mappings/security.md"),
			want: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			docs := makeDocs(tt.row)
			now := time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC)
			findings, err := standards.Audit(docs, ".", now)
			if err != nil {
				t.Fatalf("Audit: %v", err)
			}
			if got := countKind(findings, "empty-status"); got != tt.want {
				t.Errorf("empty-status findings: got %d, want %d", got, tt.want)
			}
		})
	}
}

func TestAudit_DuplicateID(t *testing.T) {
	t.Run("duplicate ids are detected", func(t *testing.T) {
		docs := makeDocs(
			row("dup-id", "IMPLEMENTED", ""),
			row("dup-id", "PLANNED", ""),
			row("unique-id", "PARTIAL", ""),
		)
		now := time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC)
		findings, err := standards.Audit(docs, ".", now)
		if err != nil {
			t.Fatalf("Audit: %v", err)
		}
		if got := countKind(findings, "duplicate-id"); got != 1 {
			t.Errorf("duplicate-id findings: got %d, want 1", got)
		}
	})

	t.Run("no duplicates", func(t *testing.T) {
		docs := makeDocs(
			row("id-1", "IMPLEMENTED", ""),
			row("id-2", "PARTIAL", ""),
			row("id-3", "PLANNED", ""),
		)
		now := time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC)
		findings, err := standards.Audit(docs, ".", now)
		if err != nil {
			t.Fatalf("Audit: %v", err)
		}
		if got := countKind(findings, "duplicate-id"); got != 0 {
			t.Errorf("duplicate-id findings: got %d, want 0", got)
		}
	})
}

func TestAudit_StaleGeneratedAt(t *testing.T) {
	t.Run("generated_at older than 30 days is stale", func(t *testing.T) {
		docs := standards.Docs{
			SchemaVersion: "1.0.0",
			GeneratedAt:   "2026-01-01",
		}
		now := time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC)
		findings, err := standards.Audit(docs, ".", now)
		if err != nil {
			t.Fatalf("Audit: %v", err)
		}
		if got := countKind(findings, "stale-generated-at"); got != 1 {
			t.Errorf("stale-generated-at findings: got %d, want 1", got)
		}
		for _, f := range findings {
			if f.Kind == "stale-generated-at" && f.Severity != "Medium" {
				t.Errorf("severity = %q, want Medium", f.Severity)
			}
		}
	})

	t.Run("generated_at within 30 days is not stale", func(t *testing.T) {
		docs := standards.Docs{
			SchemaVersion: "1.0.0",
			GeneratedAt:   "2026-08-31",
		}
		now := time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC)
		findings, err := standards.Audit(docs, ".", now)
		if err != nil {
			t.Fatalf("Audit: %v", err)
		}
		if got := countKind(findings, "stale-generated-at"); got != 0 {
			t.Errorf("stale-generated-at findings: got %d, want 0", got)
		}
	})

	t.Run("empty generated_at is skipped", func(t *testing.T) {
		docs := standards.Docs{
			SchemaVersion: "1.0.0",
			GeneratedAt:   "",
		}
		now := time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC)
		findings, err := standards.Audit(docs, ".", now)
		if err != nil {
			t.Fatalf("Audit: %v", err)
		}
		if got := countKind(findings, "stale-generated-at"); got != 0 {
			t.Errorf("stale-generated-at findings: got %d, want 0", got)
		}
	})
}

func TestAudit_RealRepoNoPanic(t *testing.T) {
	// Integration-ish check: point at the real repo and assert no panic
	// and a non-nil findings slice. Do NOT assert counts — they change
	// as content lands.
	findings, err := standards.Audit(
		standards.Docs{SchemaVersion: "1.0.0"},
		"/tmp/wt-s049",
		time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC),
	)
	if err != nil {
		t.Fatalf("Audit on real repo returned error: %v", err)
	}
	if findings == nil {
		t.Fatal("Audit returned nil findings slice")
	}
	_ = findings
}
