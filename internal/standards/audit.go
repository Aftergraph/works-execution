package standards

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// StandardRow represents one row in the standards registry.
type StandardRow struct {
	StandardID          string   `json:"standard_id"`
	StandardName        string   `json:"standard_name"`
	Version             string   `json:"version"`
	Domain              string   `json:"domain"`
	Requirement         string   `json:"requirement"`
	ControlID           string   `json:"control_id"`
	Status              string   `json:"status"`
	Owner               string   `json:"owner"`
	Implementation      string   `json:"implementation"`
	EnforcementPoint    string   `json:"enforcement_point"`
	Test                string   `json:"test"`
	Evidence            string   `json:"evidence"`
	Exceptions          []string `json:"exceptions"`
	BlockedReason       string   `json:"blocked_reason,omitempty"`
	UnblockCheck        string   `json:"unblock_check,omitempty"`
	NotApplicableReason string   `json:"not_applicable_reason,omitempty"`
}

// Summary holds the summary section of the registry.
type Summary struct {
	Total    int            `json:"total"`
	ByStatus map[string]int `json:"by_status"`
	ByDomain map[string]int `json:"by_domain"`
}

// Docs is the parsed form of docs/standards/registry.json.
type Docs struct {
	SchemaVersion string        `json:"schema_version"`
	GeneratedAt   string        `json:"generated_at"`
	Venture       string        `json:"venture,omitempty"`
	Domains       []string      `json:"domains"`
	Standards     []StandardRow `json:"standards"`
	Summary       Summary       `json:"summary"`
}

// Finding records one drift observation between the registry and repo reality.
type Finding struct {
	Kind     string
	Subject  string
	Detail   string
	Severity string
}

// mappingPathPrefix is the directory where per-standard mapping documents live.
const mappingPathPrefix = "docs/standards/mappings/"

// mappingFileExtensions lists valid suffixes for a mapping file claim.
var mappingFileExtensions = []string{".md"}

// isMappingPath returns true when s looks like a mapping file path
// under docs/standards/mappings/.
func isMappingPath(s string) bool {
	if !strings.HasPrefix(s, mappingPathPrefix) {
		return false
	}
	for _, ext := range mappingFileExtensions {
		if strings.HasSuffix(s, ext) {
			return true
		}
	}
	return false
}

// Audit cross-checks registry claims against repo reality and returns
// every drift finding. A nil/empty finding slice means the registry is
// in sync with the filesystem.
func Audit(registry Docs, repoRoot string, now time.Time) ([]Finding, error) {
	var findings []Finding

	findings = append(findings, checkMissingMappingFile(registry, repoRoot)...)
	findings = append(findings, checkOrphanMapping(registry, repoRoot)...)
	findings = append(findings, checkEmptyStatus(registry)...)
	findings = append(findings, checkDuplicateID(registry)...)
	findings = append(findings, checkStaleGeneratedAt(registry, now)...)

	sort.Slice(findings, func(i, j int) bool {
		if findings[i].Kind != findings[j].Kind {
			return findings[i].Kind < findings[j].Kind
		}
		return findings[i].Subject < findings[j].Subject
	})

	return findings, nil
}

// checkMissingMappingFile reports every row whose evidence field claims a
// mapping file that does not exist on disk.
func checkMissingMappingFile(registry Docs, repoRoot string) []Finding {
	var findings []Finding
	for _, row := range registry.Standards {
		if !isMappingPath(row.Evidence) {
			continue
		}
		full := filepath.Join(repoRoot, row.Evidence)
		if _, err := os.Stat(full); os.IsNotExist(err) {
			findings = append(findings, Finding{
				Kind:     "missing-mapping-file",
				Subject:  row.StandardID,
				Detail:   fmt.Sprintf("mapping file %s does not exist on disk", row.Evidence),
				Severity: "High",
			})
		}
	}
	return findings
}

// checkOrphanMapping reports every file under docs/standards/mappings/ that
// no registry row references via its evidence field.
func checkOrphanMapping(registry Docs, repoRoot string) []Finding {
	pattern := filepath.Join(repoRoot, mappingPathPrefix+"*.md")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil
	}

	referenced := map[string]bool{}
	for _, row := range registry.Standards {
		if isMappingPath(row.Evidence) {
			referenced[row.Evidence] = true
		}
	}

	var findings []Finding
	for _, path := range matches {
		rel, err := filepath.Rel(repoRoot, path)
		if err != nil {
			continue
		}
		// Normalise to forward slashes for comparison with registry values.
		rel = filepath.ToSlash(rel)
		if !referenced[rel] {
			findings = append(findings, Finding{
				Kind:     "orphan-mapping",
				Subject:  rel,
				Detail:   "mapping file exists but is not referenced by any registry row",
				Severity: "High",
			})
		}
	}
	return findings
}

// checkEmptyStatus reports rows whose status is empty or whitespace-only.
func checkEmptyStatus(registry Docs) []Finding {
	var findings []Finding
	for _, row := range registry.Standards {
		if strings.TrimSpace(row.Status) == "" {
			findings = append(findings, Finding{
				Kind:     "empty-status",
				Subject:  row.StandardID,
				Detail:   "status is empty or whitespace",
				Severity: "Medium",
			})
		}
	}
	return findings
}

// checkDuplicateID reports every standard_id that appears more than once.
// If the internal/standards validator already catches duplicates, this
// check is skipped (the validator would need to expose that capability).
func checkDuplicateID(registry Docs) []Finding {
	seen := map[string]int{} // id -> first row index
	var findings []Finding
	for i, row := range registry.Standards {
		if row.StandardID == "" {
			continue
		}
		if first, exists := seen[row.StandardID]; exists {
			findings = append(findings, Finding{
				Kind:     "duplicate-id",
				Subject:  row.StandardID,
				Detail:   fmt.Sprintf("duplicate of row at index %d", first),
				Severity: "Medium",
			})
		} else {
			seen[row.StandardID] = i
		}
	}
	return findings
}

// checkStaleGeneratedAt reports when the registry header generated_at is
// older than 30 days relative to the provided now.
func checkStaleGeneratedAt(registry Docs, now time.Time) []Finding {
	if registry.GeneratedAt == "" {
		return nil
	}
	t, err := time.Parse("2006-01-02", registry.GeneratedAt)
	if err != nil {
		return nil
	}
	if now.Sub(t) > 30*24*time.Hour {
		return []Finding{{
			Kind:     "stale-generated-at",
			Subject:  registry.SchemaVersion,
			Detail:   fmt.Sprintf("generated_at %s is older than 30 days (now: %s)", registry.GeneratedAt, now.Format("2006-01-02")),
			Severity: "Medium",
		}}
	}
	return nil
}
