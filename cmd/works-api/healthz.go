package main

import (
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"
)

// The image is distroless: no shell, no curl, no wget. A HEALTHCHECK therefore
// has to be answered by the binary itself, which is why the Dockerfile invokes
// `/works-api -healthz-only`.
//
// That flag was referenced by the Dockerfile and the governance compose file but
// never defined here, so flag.Parse rejected it with exit 2 and every container
// built from the image was marked unhealthy while serving perfectly well — it
// probes the real listener over HTTP rather than guessing at internal state.
var healthzOnly = flag.Bool(
	"healthz-only",
	false,
	"probe the running service's /healthz and exit 0 (healthy) or 1 (unhealthy); used by the container HEALTHCHECK",
)

// probeTimeout is deliberately short: a healthcheck that hangs is worse than one
// that fails, because Docker counts a timeout against the same retry budget.
const probeTimeout = 3 * time.Second

// healthzOnlyExit performs one GET against the listener this process would
// serve on, and maps the result to an exit status. listenAddr may be a wildcard
// such as 0.0.0.0:8080, which is not a dialable destination, so the host part
// is replaced with loopback.
func healthzOnlyExit(listenAddr string) int {
	host, port, err := net.SplitHostPort(listenAddr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "healthz: cannot parse -addr %q: %v\n", listenAddr, err)
		return 1
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}

	client := &http.Client{Timeout: probeTimeout}
	resp, err := client.Get(fmt.Sprintf("http://%s/healthz", net.JoinHostPort(host, port)))
	if err != nil {
		fmt.Fprintf(os.Stderr, "healthz: probe failed: %v\n", err)
		return 1
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "healthz: probe returned %d\n", resp.StatusCode)
		return 1
	}
	return 0
}
