package main

import "github.com/JonasAbde/works-execution/packages/workgraph"

// subprocessWorkSpec returns a 2-node work that the works-worker
// executes entirely on the host (slice-1+2 path, no Docker).
// Used by works-bench as the "baseline" backend.
func subprocessWorkSpec() map[string]any {
	return map[string]any{
		"queue": true,
		"source": map[string]any{
			"type":       "bench_subprocess",
			"repository": "works-execution/bench",
		},
		"objective": map[string]any{"type": "verify_change"},
		"requirements": map[string]any{
			"os":         "linux",
			"arch":       "amd64",
			"confidence": "development",
		},
		"policy": map[string]any{"trust_class": "standard", "production_access": true},
		"graph": map[string]any{
			"nodes": map[string]any{
				"hello": map[string]any{
					"id":        "hello",
					"run":       "echo bench-hello && uname -a",
					"timeout_s": 60,
				},
				"verify": map[string]any{
					"id":        "verify",
					"run":       "echo bench-verify",
					"needs":     []string{"hello"},
					"timeout_s": 60,
				},
			},
		},
	}
}

// dockerWorkSpec returns a 1-node work that the works-worker
// executes inside an alpine:3.20 Docker container (slice-5 hermetic
// path). Demonstrates the cost of the hermetic isolation layer.
func dockerWorkSpec() map[string]any {
	return map[string]any{
		"queue": true,
		"source": map[string]any{
			"type":       "bench_docker",
			"repository": "works-execution/bench",
		},
		"objective": map[string]any{"type": "verify_change"},
		"requirements": map[string]any{
			"os":         "linux",
			"arch":       "amd64",
			"confidence": "development",
		},
		"policy": map[string]any{"trust_class": "standard", "production_access": true},
		"graph": map[string]any{
			"nodes": map[string]any{
				"alpine": map[string]any{
					"id":        "alpine",
					"run":       "echo alpine-bench",
					"timeout_s": 120,
					"runtime": map[string]any{
						"image":   "alpine:3.20",
						"command": "echo alpine-bench",
					},
				},
			},
		},
	}
}

// Compile-time guard: ensure workgraph package is reachable so go.mod
// won't drop the import.
var _ = workgraph.NewID