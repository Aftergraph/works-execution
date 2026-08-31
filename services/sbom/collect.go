package sbom

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

// Module is a single row from `go list -m -json all` projected into
// the fields needed by SPDX and CycloneDX emitters.
//
// Path is the Go module path (e.g. "github.com/google/uuid"); Version
// is the semantic version or pseudo-version recorded in go.mod;
// Indirect is true for transitive deps brought in by something other
// than the main module; Hash is the module-cache content hash with
// the `h1:` algorithm prefix stripped (downstream SPDX/CycloneDX
// consumers expect a bare digest).
type Module struct {
	Path     string `json:"path"`
	Version  string `json:"version"`
	Indirect bool   `json:"indirect,omitempty"`
	Hash     string `json:"hash,omitempty"`
}

// collectModules invokes `go list -m -json all` in dir and decodes
// the resulting JSON stream into a Module list plus the main-module
// identity. The first object in the stream is the project root
// (`Main: true`); subsequent objects are direct + transitive deps.
//
// If dir is empty, cwd is used. The function performs no network
// I/O — `go list -m` reads the module cache and go.sum.
func collectModules(dir string) (deps []Module, root Module, err error) {
	cmd := exec.Command("go", "list", "-m", "-json", "all")
	if dir != "" {
		cmd.Dir = dir
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, Module{}, fmt.Errorf("go list -m -json all: %w (%s)", err, strings.TrimSpace(stderr.String()))
	}

	dec := json.NewDecoder(bytes.NewReader(stdout.Bytes()))
	rootSeen := false
	for dec.More() {
		var raw struct {
			Path     string `json:"Path"`
			Version  string `json:"Version"`
			Main     bool   `json:"Main"`
			Indirect bool   `json:"Indirect"`
			Hash     string `json:"Hash"`
		}
		if err := dec.Decode(&raw); err != nil {
			return nil, Module{}, fmt.Errorf("decode go list output: %w", err)
		}
		if raw.Path == "" {
			continue
		}
		m := Module{
			Path:     raw.Path,
			Version:  raw.Version,
			Indirect: raw.Indirect,
			Hash:     strings.TrimPrefix(raw.Hash, "h1:"),
		}
		if raw.Main && !rootSeen {
			root = m
			rootSeen = true
			continue
		}
		deps = append(deps, m)
	}
	if !rootSeen {
		return nil, Module{}, fmt.Errorf("no main module (Main: true) in go list output")
	}
	return deps, root, nil
}