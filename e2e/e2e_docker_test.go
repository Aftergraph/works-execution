//go:build e2e_docker

// e2e_docker_test.go: end-to-end smoke test that exercises the slice-5
// docker worker backend. Submits a work with one node that declares
// Node.Runtime.Image = "alpine:3.20", waits for SUCCEEDED, and asserts
// the artifact was produced with the alpine content (no host-machine
// contamination in stdout).
//
// Run with: go test -tags=e2e_docker -count=1 -timeout 120s ./e2e/
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestE2E_DockerWorker runs the full API + worker stack against a real
// Docker daemon. The test boots the API on a free port, starts a worker
// that polls it, submits a work that has Node.Runtime.Image = "alpine:3.20",
// and asserts SUCCEEDED with the expected alpine output.
func TestE2E_DockerWorker(t *testing.T) {
	if os.Getenv("SKIP_E2E_DOCKER") == "1" {
		t.Skip("SKIP_E2E_DOCKER=1")
	}
	// Confirm docker is available.
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skipf("docker CLI not on PATH: %v", err)
	}
	// Confirm docker daemon is alive.
	pcmd := exec.Command("docker", "info")
	if err := pcmd.Run(); err != nil {
		t.Skipf("docker daemon unreachable: %v", err)
	}

	// Pick a free port.
	port := freePort(t)
	apiURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	enrollSecret := "e2e-docker-test-secret"
	artDir := t.TempDir()
	dbPath := filepath.Join(t.TempDir(), "e2e-docker.db")
	workerID := "wrkr_e2e_docker"

	// Boot the API using the prebuilt binary so we don't pay the
	// `go run` compile cost on every test invocation.
	apiCmd := exec.Command(
		filepath.Join(getRepoRoot(t), "bin", "works-api"),
		"-addr", fmt.Sprintf("127.0.0.1:%d", port),
		"-db", dbPath,
		"-enroll-secret", enrollSecret,
		"-policy", filepath.Join(getRepoRoot(t), "policies", "lease_grant.rego"),
	)
	apiCmd.Env = append(os.Environ(), "WORKS_ARTIFACTS="+artDir)
	if !fileExists(filepath.Join(getRepoRoot(t), "bin", "works-api")) {
		t.Logf("DEBUG: repo root=%s api binary missing at %s/bin/works-api", getRepoRoot(t), getRepoRoot(t))
		t.Fatalf("works-api binary missing — run `make build` first")
	}
	apiLog, _ := apiCmd.StderrPipe()
	go func() {
		buf := make([]byte, 1024)
		for {
			n, err := apiLog.Read(buf)
			if n > 0 {
				t.Logf("[api] %s", strings.TrimSpace(string(buf[:n])))
			}
			if err != nil {
				return
			}
		}
	}()
	if err := apiCmd.Start(); err != nil {
		t.Fatalf("start api: %v", err)
	}
	defer func() {
		_ = apiCmd.Process.Kill()
		_ = apiCmd.Wait()
	}()

	// Wait for the API to become healthy.
	hc := &http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(20 * time.Second)
	for {
		resp, err := hc.Get(apiURL + "/healthz")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == 200 {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("api never became healthy: last err=%v", err)
		}
		time.Sleep(200 * time.Millisecond)
	}

	// Boot the worker using the prebuilt binary.
	workerCmd := exec.Command(
		filepath.Join(getRepoRoot(t), "bin", "works-worker"),
		"-api", apiURL,
		"-id", workerID,
		"-artifacts", artDir,
		"-poll", "200ms",
		"-lease-ttl", "60s",
		"-heartbeat", "10s",
	)
	workerCmd.Env = append(os.Environ(), "WORKS_ENROLL_SECRET="+enrollSecret)
	_ = workerCmd.Start()
	defer func() {
		_ = workerCmd.Process.Kill()
		_ = workerCmd.Wait()
	}()

	// Submit a work with one node that has Node.Runtime.Image = alpine:3.20.
	body := map[string]any{
		"queue": true,
		"source": map[string]any{
			"type":       "e2e_docker",
			"repository": "works-execution/e2e",
		},
		"objective": map[string]any{"type": "verify_change"},
		"graph": map[string]any{
			"nodes": map[string]any{
				"alpine": map[string]any{
					"id":      "alpine",
					"run":     "echo alpine-e2e-ready && uname -a",
					"timeout_s": 120,
					"runtime": map[string]any{
						"image": "alpine:3.20",
					},
				},
			},
		},
	}
	bodyBytes, _ := json.Marshal(body)
	resp, err := hc.Post(apiURL+"/v1/works", "application/json", bytes.NewReader(bodyBytes))
	if err != nil {
		t.Fatalf("POST /v1/works: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		out, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		t.Fatalf("POST /v1/works: status=%d body=%s", resp.StatusCode, string(out))
	}
	var w struct{ ID string }
	_ = json.NewDecoder(resp.Body).Decode(&w)
	_ = resp.Body.Close()

	// Poll until SUCCEEDED.
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	var state string
	for {
		select {
		case <-ctx.Done():
			t.Fatalf("work did not reach SUCCEEDED within 90s; last state=%s", state)
		case <-time.After(500 * time.Millisecond):
		}
		r, err := hc.Get(apiURL + "/v1/works/" + w.ID)
		if err != nil {
			continue
		}
		var cur struct{ State string }
		_ = json.NewDecoder(r.Body).Decode(&cur)
		_ = r.Body.Close()
		state = cur.State
		if state == "SUCCEEDED" {
			break
		}
		if state == "FAILED" {
			t.Fatalf("work ended in FAILED state")
		}
	}

	// Inspect the artifact. The alpine node should have produced
	// exactly the alpine echo, and the `uname -a` output should be the
	// alpine *userland* — not contain artifacts from the host
	// environment that the slice-1+2 host path would carry (PATH
	// inheritance, HOME, user database, etc.).
	logPath := filepath.Join(artDir, w.ID, "alpine.log")
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read artifact: %v (path=%s)", err, logPath)
	}
	log := string(data)
	if !strings.Contains(log, "alpine-e2e-ready") {
		t.Errorf("log missing alpine marker: %q", log)
	}
	// alpine:3.20 is built on the musl libc and BusyBox. Its
	// `uname -a` line is short and never includes GNU/Linux
	// `Ubuntu` build strings. Critically, alpine's `id` and `pwd`
	// output is 0:0 and /, not /home/<hostuser>/... So we assert
	// the artifact is small AND doesn't contain a `/home/` path
	// (which the host subprocess would have polluted via $HOME).
	if len(data) > 4096 {
		t.Errorf("artifact suspiciously large (%d bytes); docker may not have been used", len(data))
	}
	if strings.Contains(log, "/home/") {
		t.Errorf("artifact contains /home/ — host env leaked into docker run: %q", log)
	}
	// alpine:3.20 is built on the musl libc. When the container
	// runs `uname -a`, the kernel version is the host kernel
	// (Docker always shares the host kernel), but the
	// "operating system" field is the GNU userland name. Alpine
	// reports "Linux" (no GNU suffix) while glibc userland
	// reports "GNU/Linux". This is a clean signal that the
	// container ran alpine and not the host.
	if !strings.Contains(log, " x86_64 Linux\n") {
		t.Errorf("artifact missing alpine-style uname trailer (no GNU suffix): %q", log)
	}
	if strings.Contains(log, "GNU/Linux") {
		t.Errorf("artifact contains GNU/Linux trailer — host userland leaked into docker run: %q", log)
	}
	t.Logf("OK: artifact path=%s size=%d", logPath, len(data))
}

// freePort finds a free TCP port for the API to listen on. Returns
// the port number and a successful t.
func freePort(t *testing.T) int {
	t.Helper()
	ln, err := exec.Command("sh", "-c", "python3 -c 'import socket; s=socket.socket(); s.bind((\"127.0.0.1\",0)); print(s.getsockname()[1]); s.close()'").Output()
	if err != nil {
		t.Fatalf("free port lookup failed: %v", err)
	}
	portStr := strings.TrimSpace(string(ln))
	var port int
	if _, err := fmt.Sscanf(portStr, "%d", &port); err != nil {
		t.Fatalf("parse free port: %v", err)
	}
	return port
}

// getRepoRoot returns the absolute path to the works-venture module
// root. Used to locate the prebuilt bin/ directory regardless of the
// test's working directory.
func getRepoRoot(t *testing.T) string {
	t.Helper()
	// Walk up from cwd until we find go.mod.
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	dir := wd
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
	t.Fatalf("could not find go.mod from %s", wd)
	return ""
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}