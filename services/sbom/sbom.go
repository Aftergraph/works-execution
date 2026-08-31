package sbom

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Result is the bundle returned by Emit: both SBOM documents plus
// the discovered module graph, so callers (tests, CI, the Make
// target) can introspect what produced each file.
type Result struct {
	Root      Module
	Deps      []Module
	SPDX      []byte
	CycloneDX []byte
	SPDXName  string
}

// Emit runs the full SBOM pipeline for a Go module rooted at dir:
//  1. `go list -m -json all` -> Module list
//  2. EmitSPDX    -> SPDX 3.0.1 JSON
//  3. EmitCycloneDX -> CycloneDX 1.6 JSON
// If dir is empty, cwd is used. name defaults to the root module path.
func Emit(dir, name string) (*Result, error) {
	deps, root, err := collectModules(dir)
	if err != nil {
		return nil, err
	}
	if name == "" {
		name = root.Path
	}
	spdxBytes, err := EmitSPDX(name, "", root, deps)
	if err != nil {
		return nil, fmt.Errorf("emit spdx: %w", err)
	}
	cdxBytes, err := EmitCycloneDX(root, deps)
	if err != nil {
		return nil, fmt.Errorf("emit cyclonedx: %w", err)
	}
	return &Result{
		Root:      root,
		Deps:      deps,
		SPDX:      spdxBytes,
		CycloneDX: cdxBytes,
		SPDXName:  name,
	}, nil
}

// WriteToDir writes the SPDX and CycloneDX documents to dir using
// canonical file names (<base>.spdx.json and <base>.cdx.json). dir
// is created (mkdir -p) if it does not exist. Returns the two paths.
func (r *Result) WriteToDir(dir string) (spdxPath, cdxPath string, err error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", "", fmt.Errorf("mkdir %s: %w", dir, err)
	}
	base := sanitize(r.SPDXName)
	if base == "" {
		base = "sbom"
	}
	spdxPath = filepath.Join(dir, base+".spdx.json")
	cdxPath = filepath.Join(dir, base+".cdx.json")
	if err := os.WriteFile(spdxPath, r.SPDX, 0o644); err != nil {
		return "", "", fmt.Errorf("write spdx: %w", err)
	}
	if err := os.WriteFile(cdxPath, r.CycloneDX, 0o644); err != nil {
		return "", "", fmt.Errorf("write cyclonedx: %w", err)
	}
	return spdxPath, cdxPath, nil
}

// Verify parses both emitted documents and returns an error if
// either is not valid JSON or is missing the canonical discriminator
// fields. Full schema validation lives in
// tests/supply_chain/sbom_test.go.
func (r *Result) Verify() error {
	if _, err := parseSPDX(r.SPDX); err != nil {
		return fmt.Errorf("spdx: %w", err)
	}
	if _, err := parseCycloneDX(r.CycloneDX); err != nil {
		return fmt.Errorf("cyclonedx: %w", err)
	}
	return nil
}

// parseSPDX decodes an SPDX document and asserts the required
// discriminator fields are present.
func parseSPDX(b []byte) (any, error) {
	var doc struct {
		Graph []struct {
			Type       string `json:"type"`
			SPDXID     string `json:"spdxId"`
			CreationInfo *struct {
				SpecVersion string `json:"specVersion"`
				DataLicense string `json:"dataLicense"`
			} `json:"creationInfo,omitempty"`
		} `json:"@graph"`
		SPDXID string `json:"spdxId"`
	}
	if err := jsonDecode(b, &doc); err != nil {
		return nil, fmt.Errorf("not valid JSON: %w", err)
	}
	if len(doc.Graph) == 0 {
		return nil, fmt.Errorf("missing @graph")
	}
	hasCreation := false
	for _, g := range doc.Graph {
		if g.Type == "creationInfo" && g.CreationInfo != nil {
			hasCreation = true
			if !strings.HasPrefix(g.CreationInfo.SpecVersion, "SPDX-3") {
				return nil, fmt.Errorf("specVersion = %q (want SPDX-3.x)", g.CreationInfo.SpecVersion)
			}
		}
	}
	if !hasCreation {
		return nil, fmt.Errorf("no creationInfo element in @graph")
	}
	return doc, nil
}

// parseCycloneDX decodes a CycloneDX document and asserts the
// discriminator fields are present.
func parseCycloneDX(b []byte) (any, error) {
	var doc struct {
		BOMFormat   string `json:"bomFormat"`
		SpecVersion string `json:"specVersion"`
		Components  []struct {
			Type    string `json:"type"`
			BOMRef  string `json:"bom-ref"`
			PURL    string `json:"purl"`
		} `json:"components"`
		SerialNumber string `json:"serialNumber"`
	}
	if err := jsonDecode(b, &doc); err != nil {
		return nil, fmt.Errorf("not valid JSON: %w", err)
	}
	if doc.BOMFormat != "CycloneDX" {
		return nil, fmt.Errorf("bomFormat = %q (want CycloneDX)", doc.BOMFormat)
	}
	if doc.SpecVersion != "1.6" {
		return nil, fmt.Errorf("specVersion = %q (want 1.6)", doc.SpecVersion)
	}
	if !strings.HasPrefix(doc.SerialNumber, "urn:uuid:") {
		return nil, fmt.Errorf("serialNumber = %q (want urn:uuid:...)", doc.SerialNumber)
	}
	return doc, nil
}