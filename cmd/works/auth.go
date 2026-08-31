package main

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// cliAuth resolves and carries the Bearer token the CLI attaches to
// every control-plane call. Since PR #1, POST/GET /v1/works require a
// valid enrollment JWT (services/api/auth.go). The CLI enrolls through
// the same Zero-Secret flow as workers (k-impl-003): it POSTs the
// operator-provisioned shared secret to /v1/workers/enroll and receives
// a short-lived HS256 JWT.
//
// Resolution order:
//  1. explicit token (--token flag or WORKS_TOKEN env) — for operators
//     who mint a token once and reuse it,
//  2. enrollment (--enroll-secret flag or WORKS_ENROLL_SECRET env),
//  3. no token — dev-mode servers (AuthEnabled=false) accept;
//     production servers 401 and the CLI prints a fix-it hint.
//
// The token lives only in process memory for the CLI's lifetime.
type cliAuth struct {
	api   string
	token string
	// Renewal support: when both are set, postJSON/getJSON transparently
	// re-enroll on 401 (same renewal loop as the worker Client).
	workerID     string
	enrollSecret string
}

// newCLIAuth resolves the bearer token per the resolution order above.
func newCLIAuth(api, tokenFlag, enrollSecret string) (*cliAuth, error) {
	a := &cliAuth{api: api}

	// 1. Explicit token.
	if tokenFlag != "" {
		a.token = tokenFlag
		return a, nil
	}
	if t := os.Getenv("WORKS_TOKEN"); t != "" {
		a.token = t
		return a, nil
	}

	// 2. Enroll with the shared secret.
	if enrollSecret == "" {
		enrollSecret = os.Getenv("WORKS_ENROLL_SECRET")
	}
	if enrollSecret == "" {
		// 3. No credentials — allowed; callers handle 401s with a hint.
		return a, nil
	}

	tok, err := enrollCLI(api, enrollSecret)
	if err != nil {
		return nil, err
	}
	a.token = tok
	a.workerID = workerIDFromToken(tok)
	a.enrollSecret = enrollSecret
	return a, nil
}

// workerIDFromToken extracts the worker_id claim from an enrollment
// JWT without verifying the signature (the server verifies; we only
// need our own id back for re-enrollment).
func workerIDFromToken(jwtRaw string) string {
	parts := strings.Split(jwtRaw, ".")
	if len(parts) != 3 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var claims struct {
		WorkerID string `json:"worker_id"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return ""
	}
	return claims.WorkerID
}

// enrollCLI POSTs /v1/workers/enroll with a CLI-scoped worker id and
// returns the raw JWT. worker_id must match ^[A-Za-z0-9_.-]{1,128}$.
func enrollCLI(api, secret string) (string, error) {
	suffix := make([]byte, 6)
	_, _ = rand.Read(suffix)
	body := map[string]any{
		"worker_id":   "cli_" + hex.EncodeToString(suffix),
		"challenge":   secret,
		"scope":       "worker",
		"ttl_seconds": 3600,
	}
	buf, _ := json.Marshal(body)
	c := &http.Client{Timeout: 10 * time.Second}
	resp, err := c.Post(api+"/v1/workers/enroll", "application/json", bytes.NewReader(buf))
	if err != nil {
		return "", fmt.Errorf("enroll: POST %s/v1/workers/enroll: %w", api, err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("enroll: status=%d body=%s", resp.StatusCode, truncateForMsg(string(respBody)))
	}
	var er struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(respBody, &er); err != nil || er.Token == "" {
		return "", fmt.Errorf("enroll: decode response: %v", err)
	}
	return er.Token, nil
}

// authHeader returns the Authorization header value, empty when
// tokenless (dev mode).
func (a *cliAuth) authHeader() string {
	if a == nil || a.token == "" {
		return ""
	}
	return "Bearer " + a.token
}

// hint401 is the fix-it message printed when the control plane
// rejects an unauthenticated call.
const hint401 = "control plane requires a Bearer token on this endpoint.\n" +
	"  Fix: set WORKS_ENROLL_SECRET (same value as the server's) or pass\n" +
	"  --enroll-secret <secret>; or mint once and reuse: WORKS_TOKEN=<jwt>"

// postJSON POSTs body as JSON to api+path and decodes the response
// into out (when out != nil). 401 responses get the fix-it hint.
func (a *cliAuth) postJSON(path string, body any, out any) (*http.Response, error) {
	buf, _ := json.Marshal(body)
	req, err := http.NewRequest(http.MethodPost, a.api+path, bytes.NewReader(buf))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if h := a.authHeader(); h != "" {
		req.Header.Set("Authorization", h)
	}
	c := &http.Client{Timeout: 15 * time.Second}
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		msg := fmt.Sprintf("%s: status=%d body=%s", path, resp.StatusCode, truncateForMsg(string(b)))
		if resp.StatusCode == http.StatusUnauthorized {
			msg += "\n  " + hint401
		}
		return nil, fmt.Errorf("%s", msg)
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return nil, fmt.Errorf("%s: decode response: %w", path, err)
		}
	}
	return resp, nil
}

// getJSON GETs api+path and decodes into out (when out != nil).
func (a *cliAuth) getJSON(path string, out any) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodGet, a.api+path, nil)
	if err != nil {
		return nil, err
	}
	if h := a.authHeader(); h != "" {
		req.Header.Set("Authorization", h)
	}
	c := &http.Client{Timeout: 15 * time.Second}
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		msg := fmt.Sprintf("%s: status=%d body=%s", path, resp.StatusCode, truncateForMsg(string(b)))
		if resp.StatusCode == http.StatusUnauthorized {
			msg += "\n  " + hint401
		}
		return nil, fmt.Errorf("%s", msg)
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return nil, fmt.Errorf("%s: decode response: %w", path, err)
		}
	}
	return resp, nil
}

func truncateForMsg(s string) string {
	if len(s) <= 256 {
		return s
	}
	return s[:256] + "..."
}