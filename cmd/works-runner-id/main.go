// Command works-runner-id mints, prints, and optionally registers a runner
// identity for the local works-worker.
//
// Usage:
//
//	works-runner-id                          # mint + print JSON identity to stdout
//	works-runner-id -tenant acme            # override tenant (default: "default")
//	works-runner-id -register <api-url>     # also POST to control plane
//	works-runner-id -id wrkr_existing123    # use an existing runner_id instead of minting
//	works-runner-id -os linux,darwin -arch amd64,arm64 \
//	                -cpu 4000 -mem 8192 -toolchains go,node -labels self-hosted,linux
//
// The JSON printed to stdout matches docs/standards/schemas/runner-identity.schema.json
// and can be piped directly to the API or stored on disk for restore-on-startup.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/JonasAbde/works-execution/services/runner"
)

func main() {
	var (
		tenant     = flag.String("tenant", envOr("WORKS_TENANT", "default"), "SPIFFE namespace (tenant)")
		existingID = flag.String("id", "", "use existing runner_id (else mint a fresh one)")
		osList     = flag.String("os", runtime.GOOS, "comma-separated OS capability list")
		archList   = flag.String("arch", runtime.GOARCH, "comma-separated arch capability list")
		cpuMilli   = flag.Int("cpu", 1000, "cpu_milli capability")
		memMiB     = flag.Int("mem", 1024, "memory_mib capability")
		gpu        = flag.Int("gpu", 0, "gpu capability")
		toolchains = flag.String("toolchains", "", "comma-separated toolchain capability list")
		labels     = flag.String("labels", "", "comma-separated label capability list")
		trust      = flag.String("trust", string(runner.TrustStandard), "trust_class: untrusted|standard|privileged")
		state      = flag.String("state", string(runner.StatePending), "lifecycle_state: pending|active|draining|retired")
		register   = flag.String("register", "", "if non-empty, POST identity to this API base URL")
		timeout    = flag.Duration("timeout", 10*time.Second, "registration HTTP timeout")
	)
	flag.Parse()

	id := *existingID
	if id == "" {
		id = runner.MintRunnerID()
	}

	caps := runner.Capabilities{
		OS:         splitCSV(*osList),
		Arch:       splitCSV(*archList),
		CPUMilli:   *cpuMilli,
		MemoryMiB:  *memMiB,
		GPU:        *gpu,
		Toolchains: splitCSV(*toolchains),
		Labels:     splitCSV(*labels),
	}

	ident, err := runner.BuildIdentity(id, *tenant, caps)
	if err != nil {
		log.Fatalf("build identity: %v", err)
	}
	ident.TrustClass = runner.TrustClass(*trust)
	ident.LifecycleState = runner.LifecycleState(*state)
	if err := ident.Validate(); err != nil {
		log.Fatalf("validate identity: %v", err)
	}

	out, err := json.MarshalIndent(ident, "", "  ")
	if err != nil {
		log.Fatalf("marshal: %v", err)
	}
	if _, err := os.Stdout.Write(append(out, '\n')); err != nil {
		log.Fatalf("write: %v", err)
	}

	if *register != "" {
		url := strings.TrimRight(*register, "/") + "/v1/runners/register"
		req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(out))
		if err != nil {
			log.Fatalf("build request: %v", err)
		}
		req.Header.Set("Content-Type", "application/json")
		client := &http.Client{Timeout: *timeout}
		resp, err := client.Do(req)
		if err != nil {
			log.Fatalf("register: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode/100 != 2 {
			log.Fatalf("register: API returned %s", resp.Status)
		}
		fmt.Fprintf(os.Stderr, "registered: %s\n", resp.Status)
	}
}

// splitCSV returns a non-nil empty slice for empty input so JSON marshals
// it as `[]` rather than `null`. Capabilities.OS/Arch are required fields
// in the schema, so callers must provide at least one entry; we don't
// silently coerce "linux" -> ["linux"] because that would mask typos.
func splitCSV(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return []string{}
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}