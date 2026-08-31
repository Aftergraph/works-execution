package sbom

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// CycloneDX 1.6 BOM shape. See https://cyclonedx.org/docs/1.6/json/
// for the authoritative schema. We emit: bomFormat "CycloneDX",
// specVersion "1.6", a random serialNumber, metadata (timestamp,
// tools, root component), components[] (every Go module + transitive
// dep with purl + hashes), and dependencies[] (root dependsOn every
// dep). The optional vulnerabilities/compositions fields are omitted —
// they belong to a separate vuln-scan / provenance step (Sigstore §7
// in docs/standards/mappings/supply-chain.md).

// cdxComponent is a single CycloneDX component entry.
type cdxComponent struct {
	Type      string    `json:"type"`
	BOMRef    string    `json:"bom-ref"`
	Publisher string    `json:"publisher,omitempty"`
	Name      string    `json:"name"`
	Version   string    `json:"version,omitempty"`
	PURL      string    `json:"purl,omitempty"`
	Hashes    []cdxHash `json:"hashes,omitempty"`
}

// cdxHash is a single hash entry inside a component.
type cdxHash struct {
	Alg     string `json:"alg"`
	Content string `json:"content"`
}

// cdxMetadata holds the BOM-level metadata block.
type cdxMetadata struct {
	Timestamp string         `json:"timestamp"`
	Tools     []cdxTool      `json:"tools"`
	Component *cdxComponent  `json:"component,omitempty"`
}

// cdxTool is the tool that produced the BOM (CycloneDX wants vendor + name + version).
type cdxTool struct {
	Vendor  string `json:"vendor"`
	Name    string `json:"name"`
	Version string `json:"version"`
}

// cdxDependency is a `dependsOn` list from one BOM-ref to others.
type cdxDependency struct {
	Ref       string   `json:"ref"`
	DependsOn []string `json:"dependsOn,omitempty"`
}

// cdxBOM is the top-level CycloneDX 1.6 document.
type cdxBOM struct {
	BOMFormat    string          `json:"bomFormat"`
	SpecVersion  string          `json:"specVersion"`
	SerialNumber string          `json:"serialNumber"`
	Version      int             `json:"version"`
	Metadata     cdxMetadata     `json:"metadata"`
	Components   []cdxComponent  `json:"components"`
	Dependencies []cdxDependency `json:"dependencies"`
}

// CDXSpecVersion is the CycloneDX spec version we declare.
const CDXSpecVersion = "1.6"

// CDXFormat is the bomFormat discriminator.
const CDXFormat = "CycloneDX"

// CDXToolVendor / CDXToolName / CDXToolVersion identify the producer.
const (
	CDXToolVendor  = "works-execution"
	CDXToolName    = "services/sbom"
	CDXToolVersion = "0.1.0"
)

// EmitCycloneDX serialises the dependency graph as a CycloneDX 1.6
// JSON document. root identifies the project; deps is the full set
// of dependencies. The returned bytes are pretty-printed JSON.
func EmitCycloneDX(root Module, deps []Module) ([]byte, error) {
	if root.Path == "" {
		return nil, fmt.Errorf("cyclonedx: root module path is empty")
	}

	rootRef := "pkg:golang/" + root.Path + "@" + rootVersionOrUnspecified(root.Version)
	rootComponent := cdxComponent{
		Type:      "application",
		BOMRef:    rootRef,
		Publisher: CDXToolVendor,
		Name:      root.Path,
		Version:   rootVersionOrUnspecified(root.Version),
		PURL:      rootRef,
	}

	comps := make([]cdxComponent, 0, len(deps)+1)
	comps = append(comps, rootComponent)
	for _, d := range deps {
		c := cdxComponent{
			Type:    "library",
			BOMRef:  purlFor(d),
			Name:    d.Path,
			Version: d.Version,
			PURL:    purlFor(d),
		}
		if d.Hash != "" {
			c.Hashes = []cdxHash{{Alg: "SHA-256", Content: d.Hash}}
		}
		comps = append(comps, c)
	}

	deps0 := make([]string, 0, len(deps))
	for _, d := range deps {
		deps0 = append(deps0, purlFor(d))
	}
	depList := []cdxDependency{
		{Ref: rootRef, DependsOn: deps0},
	}

	bom := cdxBOM{
		BOMFormat:    CDXFormat,
		SpecVersion:  CDXSpecVersion,
		SerialNumber: "urn:uuid:" + uuidV4(),
		Version:      1,
		Metadata: cdxMetadata{
			Timestamp: time.Now().UTC().Format(time.RFC3339),
			Tools: []cdxTool{{
				Vendor:  CDXToolVendor,
				Name:    CDXToolName,
				Version: CDXToolVersion,
			}},
			Component: &rootComponent,
		},
		Components:   comps,
		Dependencies: depList,
	}

	out, err := json.MarshalIndent(bom, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("cyclonedx marshal: %w", err)
	}
	return out, nil
}

// purlFor builds a Package URL for a Go module per the purl-spec:
// pkg:golang/<module-path>@<version> . The GoVersion qualifier is
// optional and omitted.
func purlFor(m Module) string {
	return "pkg:golang/" + m.Path + "@" + m.Version
}

// rootVersionOrUnspecified returns the root module's version or
// "unspecified" if empty. `go list -m -json` reports an empty Version
// for the main module; purl/SPDX/CycloneDX all require non-empty.
func rootVersionOrUnspecified(v string) string {
	if v == "" {
		return "unspecified"
	}
	return v
}

// uuidV4 returns a random UUIDv4 string. We avoid pulling in
// github.com/google/uuid just for this (RFC 4122 §4.4).
func uuidV4() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// entropy failure: fall back to a timestamp-derived UUID so
		// serialNumber stays unique enough for CI purposes.
		ts := time.Now().UnixNano()
		for i := 0; i < 16; i++ {
			b[i] = byte(ts >> (8 * i))
		}
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	hexStr := hex.EncodeToString(b[:])
	return strings.Join([]string{
		hexStr[0:8], hexStr[8:12], hexStr[12:16], hexStr[16:20], hexStr[20:32],
	}, "-")
}