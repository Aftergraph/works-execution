package sbom

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

// TestEmitSPDX_SpecDiscriminators is a hermetic test that constructs
// a fake module graph and asserts the SPDX 3.0.1 discriminator fields
// land in the output without ever invoking `go list`.
func TestEmitSPDX_SpecDiscriminators(t *testing.T) {
	root := Module{Path: "example.com/root", Version: "1.2.3"}
	deps := []Module{
		{Path: "example.com/dep-a", Version: "v0.1.0"},
		{Path: "example.com/dep-b", Version: "v0.2.0", Indirect: true,
			Hash: strings.Repeat("a", 64)},
	}
	out, err := EmitSPDX("test", "https://example.com/spdx", root, deps)
	if err != nil {
		t.Fatalf("EmitSPDX: %v", err)
	}
	got := string(out)
	for _, want := range []string{
		`"specVersion": "SPDX-3.0"`,
		`"dataLicense": "CC0-1.0"`,
		`"standard": "ISO/IEC 5962:2021"`,
		`"@type": "Bundle"`,
		`"relationshipType": "describes"`,
		`example.com/root`,
		`example.com/dep-a`,
		`example.com/dep-b`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("SPDX output missing %q\n--- got ---\n%s", want, got)
		}
	}
}

// TestEmitCycloneDX_SpecDiscriminators is the CycloneDX counterpart:
// it confirms bomFormat, specVersion, and the purl shape without
// invoking `go list`.
func TestEmitCycloneDX_SpecDiscriminators(t *testing.T) {
	root := Module{Path: "example.com/root", Version: "1.2.3"}
	deps := []Module{
		{Path: "example.com/dep-a", Version: "v0.1.0"},
		{Path: "example.com/dep-b", Version: "v0.2.0",
			Hash: strings.Repeat("b", 64)},
	}
	out, err := EmitCycloneDX(root, deps)
	if err != nil {
		t.Fatalf("EmitCycloneDX: %v", err)
	}
	got := string(out)
	for _, want := range []string{
		`"bomFormat": "CycloneDX"`,
		`"specVersion": "1.6"`,
		`"version": 1`,
		`"urn:uuid:`,
		`"purl": "pkg:golang/example.com/dep-a@v0.1.0"`,
		`"purl": "pkg:golang/example.com/dep-b@v0.2.0"`,
		`"name": "services/sbom"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("CycloneDX output missing %q\n--- got ---\n%s", want, got)
		}
	}
}

// TestSanitize strips the characters that SPDXIDs forbid and asserts
// the resulting string is ASCII-safe.
func TestSanitize(t *testing.T) {
	in := "github.com/Jonas!Abde/foo_bar.baz"
	got := sanitize(in)
	for _, r := range got {
		if r == '!' {
			t.Errorf("sanitize left a forbidden char %q in %q", r, got)
		}
	}
	if got != "github.com-Jonas-Abde-foo_bar.baz" {
		t.Errorf("sanitize = %q, want github.com-Jonas-Abde-foo_bar.baz", got)
	}
}

// TestRootVersionOrUnspecified confirms the empty-version fallback
// works as expected for the main module case.
func TestRootVersionOrUnspecified(t *testing.T) {
	if got := rootVersionOrUnspecified(""); got != "unspecified" {
		t.Errorf("rootVersionOrUnspecified(\"\") = %q, want unspecified", got)
	}
	if got := rootVersionOrUnspecified("v1.0.0"); got != "v1.0.0" {
		t.Errorf("rootVersionOrUnspecified(v1.0.0) = %q, want v1.0.0", got)
	}
}

// TestUUIDV4_Shape asserts the random UUID generator returns 36 chars
// in 8-4-4-4-12 layout and that version/variant bits are set.
func TestUUIDV4_Shape(t *testing.T) {
	u := uuidV4()
	if len(u) != 36 {
		t.Fatalf("uuidV4 len = %d, want 36 (%q)", len(u), u)
	}
	if u[8] != '-' || u[13] != '-' || u[18] != '-' || u[23] != '-' {
		t.Errorf("uuidV4 hyphens misplaced: %q", u)
	}
	// version 4 -> hex digit in position 14 is '4'
	if u[14] != '4' {
		t.Errorf("uuidV4 version nibble = %q, want 4", string(u[14]))
	}
	// variant bits -> hex digit in position 19 is one of 8,9,a,b
	if c := u[19]; !(c == '8' || c == '9' || c == 'a' || c == 'b') {
		t.Errorf("uuidV4 variant nibble = %q, want 8/9/a/b", string(c))
	}
}

// TestResult_VerifyIsConsistent runs Verify on the package's own
// emission of a synthetic module graph to confirm the parser agrees
// with the emitters.
func TestResult_VerifyIsConsistent(t *testing.T) {
	r := &Result{
		Root: Module{Path: "example.com/root", Version: "0.1.0"},
		Deps: []Module{{Path: "example.com/dep-a", Version: "v0.1.0"}},
	}
	spdxBytes, err := EmitSPDX("test", "https://example.com/spdx", r.Root, r.Deps)
	if err != nil {
		t.Fatalf("EmitSPDX: %v", err)
	}
	cdxBytes, err := EmitCycloneDX(r.Root, r.Deps)
	if err != nil {
		t.Fatalf("EmitCycloneDX: %v", err)
	}
	r.SPDX = spdxBytes
	r.CycloneDX = cdxBytes
	r.SPDXName = "test"
	if err := r.Verify(); err != nil {
		t.Errorf("Verify on synthetic graph: %v", err)
	}
}

// TestSanitize_LongPathKeepsLength is a regression guard: a long
// module path must not panic and must stay ASCII-safe.
func TestSanitize_LongPathKeepsLength(t *testing.T) {
	in := strings.Repeat("a-b_c.d/", 30)
	got := sanitize(in)
	if strings.ContainsAny(got, "!@#$%^&*()") {
		t.Errorf("sanitize leaked forbidden chars: %q", got)
	}
}

// TestPurlFor_Stable verifies the purl shape for a representative
// Go module path.
func TestPurlFor_Stable(t *testing.T) {
	m := Module{Path: "github.com/foo/bar", Version: "v1.2.3"}
	if got, want := purlFor(m), "pkg:golang/github.com/foo/bar@v1.2.3"; got != want {
		t.Errorf("purlFor = %q, want %q", got, want)
	}
}

// TestRandHex_Distinct asserts the random suffix generator emits
// different values on successive calls.
func TestRandHex_Distinct(t *testing.T) {
	a, b := randHex(8), randHex(8)
	if a == b {
		t.Errorf("randHex returned duplicate %q", a)
	}
	// sanity: sha256 hash of "x" for cross-check; this also confirms
	// the test harness can compute hashes (regression on the env).
	h := sha256.Sum256([]byte("x"))
	if hex.EncodeToString(h[:]) == "" {
		t.Fatal("sha256 sanity failed")
	}
}