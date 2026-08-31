package sbom

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// SPDX 3.0.1 JSON envelope. The shape follows the published SPDX
// 3.0.1 spec: a single document with `@context` for JSON-LD and
// `@graph` carrying typed elements (Software, Organization,
// Relationship, CreationInfo, Bundle) cross-referenced by SPDXID.
//
// We emit: one CreationInfo, one Organization (the project as
// supplier), one Software element per dep plus one for the root, one
// Relationship `describes` from the root to every dep, and one Bundle
// anchored at the document root.

// spdxElement is a generic SPDX 3.0.1 graph element. JSON-LD's type
// discriminator is the `type` field ("software", "relationship",
// "creationInfo", "organization", "bundle").
type spdxElement struct {
	Type             string        `json:"type"`
	SPDXID           string        `json:"spdxId"`
	CreationInfo     *spdxCreation `json:"creationInfo,omitempty"`
	Name             string        `json:"name,omitempty"`
	Description      string        `json:"description,omitempty"`
	VersionInfo      string        `json:"softwareVersion,omitempty"`
	DownloadLocation string        `json:"downloadLocation,omitempty"`
	PackageVersion   string        `json:"packageVersion,omitempty"`
	Email            string        `json:"email,omitempty"`
	From             string        `json:"from,omitempty"`
	To               []string      `json:"to,omitempty"`
	RelationshipType string        `json:"relationshipType,omitempty"`
	Comment          string        `json:"comment,omitempty"`
}

// spdxCreation is the embedded creationInfo element.
type spdxCreation struct {
	Type        string   `json:"type"`
	SPDXID      string   `json:"spdxId"`
	Created     []string `json:"created"`
	CreatedBy   []string `json:"createdBy"`
	SpecVersion string   `json:"specVersion"`
	DataLicense string   `json:"dataLicense"`
	Comment     string   `json:"comment,omitempty"`
	Standard    string   `json:"standard,omitempty"`
}

// spdxDoc is the top-level SPDX 3.0.1 document.
type spdxDoc struct {
	Context          string        `json:"@context"`
	Graph            []spdxElement `json:"@graph"`
	ID               string        `json:"@id"`
	Type             string        `json:"@type"`
	SPDXID           string        `json:"spdxId"`
	Name             string        `json:"name"`
	DocumentNamespace string       `json:"documentNamespace"`
}

// SPDXVersion is the spec version string we declare.
const SPDXVersion = "SPDX-3.0"

// SPDXDataLicense is CC0-1.0 (SPDX's public-domain dedication).
const SPDXDataLicense = "CC0-1.0"

// SPDXStandard bundles the ISO/IEC 5962:2021 citation per supply-chain.md §5.
const SPDXStandard = "ISO/IEC 5962:2021"

// SPDXToolID identifies the producer; matches the `enforcement_point` reference.
const SPDXToolID = "works-execution/services/sbom"

// EmitSPDX serialises the dependency graph as an SPDX 3.0.1 JSON
// document. root is the project module; deps is the full set of
// dependencies. Output is pretty-printed JSON for committing to
// artifacts/sbom/<name>.spdx.json.
func EmitSPDX(name, namespace string, root Module, deps []Module) ([]byte, error) {
	if root.Path == "" {
		return nil, fmt.Errorf("spdx: root module path is empty")
	}
	if name == "" {
		name = root.Path
	}
	if namespace == "" {
		// SPDX 3.0.1 requires an absolute URI namespace.
		namespace = "https://works-execution.dev/spdx/" + root.Path
	}

	creationSPDXID := "SPDXRef-CreationInfo"
	bundleSPDXID := "SPDXRef-Document-" + randHex(6)
	rootSPDXID := "SPDXRef-Package-" + sanitize(root.Path)
	supplierSPDXID := "SPDXRef-Organization-works-execution"

	now := time.Now().UTC().Format(time.RFC3339)
	graph := []spdxElement{
		newCreationInfo(creationSPDXID, now),
		newOrganization(supplierSPDXID),
	}

	rootVer := rootVersionOrUnspecified(root.Version)
	graph = append(graph, spdxElement{
		Type:             "software",
		SPDXID:           rootSPDXID,
		Name:             root.Path,
		Description:      "works-execution root package (" + rootVer + ")",
		VersionInfo:      rootVer,
		PackageVersion:   rootVer,
		DownloadLocation: "NOASSERTION",
	})

	for _, d := range deps {
		graph = append(graph, spdxElement{
			Type:             "software",
			SPDXID:           depSPDXID(d),
			Name:             d.Path,
			VersionInfo:      d.Version,
			PackageVersion:   d.Version,
			DownloadLocation: "NOASSERTION",
		})
	}

	// `describes` relationship from root to every dependency.
	rel := spdxElement{
		Type:             "relationship",
		SPDXID:           "SPDXRef-Relationship-" + randHex(4),
		From:             rootSPDXID,
		RelationshipType: "describes",
		To:               make([]string, 0, len(deps)),
	}
	for _, d := range deps {
		rel.To = append(rel.To, depSPDXID(d))
	}
	graph = append(graph, rel)

	// Anchor bundle referencing creationInfo, supplier, and root.
	graph = append(graph, spdxElement{
		Type:    "bundle",
		SPDXID:  bundleSPDXID,
		Comment: "works-execution SBOM bundle",
	})

	doc := spdxDoc{
		Context:          "https://spdx.org/spdx-3.0.1/rdf/schema",
		ID:               namespace + "#" + bundleSPDXID,
		Type:             "Bundle",
		SPDXID:           bundleSPDXID,
		Name:             name,
		DocumentNamespace: namespace,
		Graph:            graph,
	}

	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("spdx marshal: %w", err)
	}
	return out, nil
}

func newCreationInfo(id, createdAt string) spdxElement {
	return spdxElement{
		Type: "creationInfo",
		SPDXID: id,
		CreationInfo: &spdxCreation{
			Type:        "creationInfo",
			SPDXID:      id,
			Created:     []string{createdAt},
			CreatedBy:   []string{SPDXToolID},
			SpecVersion: SPDXVersion,
			DataLicense: SPDXDataLicense,
			Standard:    SPDXStandard,
			Comment:     "SBOM generated from `go list -m -json all`",
		},
	}
}

func newOrganization(id string) spdxElement {
	return spdxElement{
		Type:   "organization",
		SPDXID: id,
		Name:   "works-execution",
		Email:  "[email protected]",
	}
}

// depSPDXID returns the SPDXID for a dependency. SPDX 3.0.1 IDs are
// case-sensitive ASCII so we sanitise module paths.
func depSPDXID(m Module) string {
	return "SPDXRef-Package-" + sanitize(m.Path) + "-" + sanitize(m.Version)
}

// sanitize converts a Go module path/version into a string that's
// safe to embed in an SPDXID. SPDX 3.0.1 forbids whitespace and a few
// punctuation characters in SPDXIDs.
func sanitize(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	return b.String()
}

func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		// cryptographic entropy failure is unrecoverable here; fall
		// back to a timestamp-derived suffix so we still emit
		// distinct IDs in practice.
		return fmt.Sprintf("%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}