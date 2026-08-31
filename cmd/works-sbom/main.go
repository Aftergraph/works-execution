// Command works-sbom emits SPDX 3.0.1 and CycloneDX 1.6 SBOMs for the
// current Go module.
//
// Usage:
//
//   works-sbom [--dir <module-dir>] [--out <output-dir>] [--name <sbom-name>]
//
// Defaults:
//
//   --dir  current working directory
//   --out  artifacts/sbom
//   --name <root module path>
//
// Exit codes:
//
//   0   success (both SBOMs written and self-verified)
//   2   invalid CLI flags
//   3   go list -m -json all failed
//   4   SBOM generation failed
//   5   self-verification failed
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/JonasAbde/works-execution/services/sbom"
)

func main() {
	dir := flag.String("dir", "", "Go module directory (default: cwd)")
	out := flag.String("out", "artifacts/sbom", "output directory for SBOM files")
	name := flag.String("name", "", "SBOM logical name (default: root module path)")
	skipVerify := flag.Bool("no-verify", false, "skip self-verification (debug only)")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: works-sbom [--dir DIR] [--out DIR] [--name NAME]\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	r, err := sbom.Emit(*dir, *name)
	if err != nil {
		fmt.Fprintf(os.Stderr, "works-sbom: emit failed: %v\n", err)
		os.Exit(3)
	}
	if !*skipVerify {
		if err := r.Verify(); err != nil {
			fmt.Fprintf(os.Stderr, "works-sbom: verify failed: %v\n", err)
			os.Exit(5)
		}
	}
	spdxPath, cdxPath, err := r.WriteToDir(*out)
	if err != nil {
		fmt.Fprintf(os.Stderr, "works-sbom: write failed: %v\n", err)
		os.Exit(4)
	}
	fmt.Printf("wrote %s (%d bytes)\n", spdxPath, len(r.SPDX))
	fmt.Printf("wrote %s (%d bytes)\n", cdxPath, len(r.CycloneDX))
	fmt.Printf("root=%s@%s deps=%d\n", r.Root.Path, r.Root.Version, len(r.Deps))
}