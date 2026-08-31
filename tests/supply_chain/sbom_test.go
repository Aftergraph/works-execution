// Package supply_chain_test contains supply-chain control tests for
// works-execution. This file exercises the SPDX 3.0.1 + CycloneDX 1.6
// SBOM generators in services/sbom/. The tests run against the real
// Go module graph of the works-execution repository, so they
// implicitly verify `go list -m -json all` succeeds, both emitters
// produce JSON that decodes back into Go structs, both documents
// carry the canonical spec discriminator fields, the two SBOMs agree
// on the dependency set (the "no dual-format divergence" invariant
// from docs/standards/mappings/supply-chain.md §6), and the files
// survive a second read from disk.
package supply_chain_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/JonasAbde/works-execution/services/sbom"
)

// repoRoot walks upward from the test cwd until it finds go.mod.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for i := 0; i < 8; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	if root, err := filepath.Abs("../../"); err == nil {
		return root
	}
	t.Fatal("could not locate works-venture repo root")
	return ""
}

func freshEmit(t *testing.T) *sbom.Result {
	t.Helper()
	r, err := sbom.Emit(repoRoot(t), "")
	if err != nil {
		t.Fatalf("sbom.Emit: %v", err)
	}
	if err := r.Verify(); err != nil {
		t.Fatalf("r.Verify: %v", err)
	}
	return r
}

// TestEmit_BothFormatsParseAndContainRoot asserts the minimum-viable
// SBOM contract: "the document names the thing we built."
func TestEmit_BothFormatsParseAndContainRoot(t *testing.T) {
	r := freshEmit(t)

	var spdx struct {
		Graph []struct {
			Type string `json:"type"`
			Name string `json:"name"`
		} `json:"@graph"`
	}
	if err := json.Unmarshal(r.SPDX, &spdx); err != nil {
		t.Fatalf("spdx not valid JSON: %v", err)
	}
	if !containsSoftware(spdx.Graph, r.Root.Path) {
		t.Errorf("SPDX has no software element for root %q", r.Root.Path)
	}

	var cdx struct {
		Components []struct {
			Name string `json:"name"`
		} `json:"components"`
	}
	if err := json.Unmarshal(r.CycloneDX, &cdx); err != nil {
		t.Fatalf("cyclonedx not valid JSON: %v", err)
	}
	if !containsComponent(cdx.Components, r.Root.Path) {
		t.Errorf("CycloneDX has no component for root %q", r.Root.Path)
	}
}

// TestEmit_SpecDiscriminators asserts the canonical version fields
// that downstream SPDX/CycloneDX validators use to pick the right
// schema.
func TestEmit_SpecDiscriminators(t *testing.T) {
	r := freshEmit(t)

	var spdx struct {
		Graph []struct {
			Type         string `json:"type"`
			CreationInfo *struct {
				SpecVersion string   `json:"specVersion"`
				DataLicense string   `json:"dataLicense"`
				Standard    string   `json:"standard"`
				CreatedBy   []string `json:"createdBy"`
			} `json:"creationInfo"`
		} `json:"@graph"`
	}
	if err := json.Unmarshal(r.SPDX, &spdx); err != nil {
		t.Fatalf("spdx unmarshal: %v", err)
	}
	var ci *struct {
		SpecVersion string   `json:"specVersion"`
		DataLicense string   `json:"dataLicense"`
		Standard    string   `json:"standard"`
		CreatedBy   []string `json:"createdBy"`
	}
	for _, g := range spdx.Graph {
		if g.Type == "creationInfo" && g.CreationInfo != nil {
			ci = g.CreationInfo
			break
		}
	}
	if ci == nil {
		t.Fatal("SPDX has no creationInfo element")
	}
	if !strings.HasPrefix(ci.SpecVersion, "SPDX-3") {
		t.Errorf("SPDX specVersion = %q, want SPDX-3.x", ci.SpecVersion)
	}
	if ci.DataLicense != "CC0-1.0" {
		t.Errorf("SPDX dataLicense = %q, want CC0-1.0", ci.DataLicense)
	}
	if ci.Standard != "ISO/IEC 5962:2021" {
		t.Errorf("SPDX standard = %q, want ISO/IEC 5962:2021 (per supply-chain mapping §5)", ci.Standard)
	}
	if len(ci.CreatedBy) == 0 {
		t.Error("SPDX creationInfo.createdBy is empty")
	}

	var cdx struct {
		BOMFormat    string `json:"bomFormat"`
		SpecVersion  string `json:"specVersion"`
		SerialNumber string `json:"serialNumber"`
	}
	if err := json.Unmarshal(r.CycloneDX, &cdx); err != nil {
		t.Fatalf("cyclonedx unmarshal: %v", err)
	}
	if cdx.BOMFormat != "CycloneDX" {
		t.Errorf("CycloneDX bomFormat = %q, want CycloneDX", cdx.BOMFormat)
	}
	if cdx.SpecVersion != "1.6" {
		t.Errorf("CycloneDX specVersion = %q, want 1.6", cdx.SpecVersion)
	}
	if !strings.HasPrefix(cdx.SerialNumber, "urn:uuid:") {
		t.Errorf("CycloneDX serialNumber = %q, want urn:uuid:...", cdx.SerialNumber)
	}
}

// TestEmit_AllModulesPresentInBoth asserts the dual-emission
// invariant from supply-chain.md §6: SPDX and CycloneDX must agree
// on the dependency set.
func TestEmit_AllModulesPresentInBoth(t *testing.T) {
	r := freshEmit(t)

	want := make([]string, 0, len(r.Deps))
	for _, d := range r.Deps {
		want = append(want, d.Path)
	}
	sort.Strings(want)
	wantSet := make(map[string]bool, len(want))
	for _, w := range want {
		wantSet[w] = true
	}

	var spdx struct {
		Graph []struct {
			Type string `json:"type"`
			Name string `json:"name"`
		} `json:"@graph"`
	}
	if err := json.Unmarshal(r.SPDX, &spdx); err != nil {
		t.Fatalf("spdx unmarshal: %v", err)
	}
	seenSPDX := map[string]bool{}
	for _, g := range spdx.Graph {
		if g.Type == "software" && wantSet[g.Name] {
			seenSPDX[g.Name] = true
		}
	}

	var cdx struct {
		Components []struct {
			Name string `json:"name"`
		} `json:"components"`
	}
	if err := json.Unmarshal(r.CycloneDX, &cdx); err != nil {
		t.Fatalf("cyclonedx unmarshal: %v", err)
	}
	seenCDX := map[string]bool{}
	for _, c := range cdx.Components {
		if wantSet[c.Name] {
			seenCDX[c.Name] = true
		}
	}

	for _, w := range want {
		if !seenSPDX[w] {
			t.Errorf("SPDX missing software element for dep %q", w)
		}
		if !seenCDX[w] {
			t.Errorf("CycloneDX missing component for dep %q", w)
		}
	}
}

// TestEmit_PurlAndSPDXIDForKnownDep asserts modernc.org/sqlite — the
// only direct dependency the project declares — round-trips into
// both a valid purl and a non-empty SPDXID. Guards module-path
// sanitization in spdx.go and the purl constructor in cyclonedx.go.
func TestEmit_PurlAndSPDXIDForKnownDep(t *testing.T) {
	r := freshEmit(t)

	wantPath := "modernc.org/sqlite"
	var version string
	for _, d := range r.Deps {
		if d.Path == wantPath {
			version = d.Version
			break
		}
	}
	if version == "" {
		t.Skipf("%s not in dep graph; skipping", wantPath)
	}

	expectedPurl := "pkg:golang/" + wantPath + "@" + version

	var cdx struct {
		Components []struct {
			Name string `json:"name"`
			PURL string `json:"purl"`
		} `json:"components"`
	}
	if err := json.Unmarshal(r.CycloneDX, &cdx); err != nil {
		t.Fatalf("cyclonedx unmarshal: %v", err)
	}
	found := false
	for _, c := range cdx.Components {
		if c.Name == wantPath {
			if c.PURL != expectedPurl {
				t.Errorf("CycloneDX purl = %q, want %q", c.PURL, expectedPurl)
			}
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("CycloneDX has no component for %s", wantPath)
	}

	var spdx struct {
		Graph []struct {
			Type   string `json:"type"`
			Name   string `json:"name"`
			SPDXID string `json:"spdxId"`
		} `json:"@graph"`
	}
	if err := json.Unmarshal(r.SPDX, &spdx); err != nil {
		t.Fatalf("spdx unmarshal: %v", err)
	}
	for _, g := range spdx.Graph {
		if g.Type == "software" && g.Name == wantPath {
			if g.SPDXID == "" || !strings.HasPrefix(g.SPDXID, "SPDXRef-Package-") {
				t.Errorf("SPDX spdxId for %s = %q, want SPDXRef-Package- prefix", wantPath, g.SPDXID)
			}
			return
		}
	}
	t.Errorf("SPDX has no software element for %s", wantPath)
}

// TestEmit_WriteToDirRoundtrip writes both SBOMs to a temp directory
// and re-reads them to confirm they survive disk persistence — the
// make-sbom contract.
func TestEmit_WriteToDirRoundtrip(t *testing.T) {
	r := freshEmit(t)

	dir := t.TempDir()
	spdxPath, cdxPath, err := r.WriteToDir(dir)
	if err != nil {
		t.Fatalf("WriteToDir: %v", err)
	}
	for _, p := range []string{spdxPath, cdxPath} {
		body, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("read %s: %v", p, err)
		}
		if len(body) == 0 {
			t.Errorf("%s is empty", p)
			continue
		}
		var probe map[string]any
		if err := json.Unmarshal(body, &probe); err != nil {
			t.Errorf("%s not valid JSON on disk: %v", p, err)
		}
	}
}

// containsSoftware reports whether any element in graph is a
// software element named want.
func containsSoftware(graph []struct {
	Type string `json:"type"`
	Name string `json:"name"`
}, want string) bool {
	for _, g := range graph {
		if g.Type == "software" && g.Name == want {
			return true
		}
	}
	return false
}

// containsComponent reports whether any component in comps is named want.
func containsComponent(comps []struct {
	Name string `json:"name"`
}, want string) bool {
	for _, c := range comps {
		if c.Name == want {
			return true
		}
	}
	return false
}