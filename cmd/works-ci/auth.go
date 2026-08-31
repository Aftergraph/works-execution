package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

// apiAuth is works-ci's minimal auth client. It mirrors cmd/works'
// cliAuth (Zero-Secret enrollment) but is duplicated here rather than
// imported so the CI binary stays dependency-light and its failure
// modes are obvious. Renewal on 401 is not needed: a CI run is
// short-lived and mints a fresh token per invocation.
type apiAuth struct {
	api   string
	token string
}

// newAuthFor enrolls with the shared secret and returns an authed
// client. worker_id must match ^[A-Za-z0-9_.-]{1,128}$.
func newAuthFor(api, enrollSecret string) (*apiAuth, error) {
	suffix := make([]byte, 6)
	_, _ = rand.Read(suffix)
	body := map[string]any{
		"worker_id":   "works-ci-" + hex.EncodeToString(suffix),
		"challenge":   enrollSecret,
		"scope":       "worker",
		"ttl_seconds": 3600,
	}
	buf, _ := json.Marshal(body)
	c := &http.Client{Timeout: 10 * time.Second}
	resp, err := c.Post(api+"/v1/workers/enroll", "application/json", bytes.NewReader(buf))
	if err != nil {
		return nil, fmt.Errorf("enroll: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("enroll: status=%d body=%s", resp.StatusCode, string(b))
	}
	var er struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&er); err != nil || er.Token == "" {
		return nil, fmt.Errorf("enroll: decode response: %v", err)
	}
	return &apiAuth{api: api, token: er.Token}, nil
}

// postJSON POSTs and decodes; 401s carry a fix-it hint.
func (a *apiAuth) postJSON(path string, body any, out any) (*http.Response, error) {
	buf, _ := json.Marshal(body)
	req, err := http.NewRequest(http.MethodPost, a.api+path, bytes.NewReader(buf))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+a.token)
	c := &http.Client{Timeout: 15 * time.Second}
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("%s: status=%d body=%s", path, resp.StatusCode, truncate(string(b)))
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return nil, fmt.Errorf("%s: decode: %w", path, err)
		}
	}
	return resp, nil
}

// getJSON GETs and decodes.
func (a *apiAuth) getJSON(path string, out any) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodGet, a.api+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+a.token)
	c := &http.Client{Timeout: 15 * time.Second}
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("%s: status=%d body=%s", path, resp.StatusCode, truncate(string(b)))
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return nil, fmt.Errorf("%s: decode: %w", path, err)
		}
	}
	return resp, nil
}

func truncate(s string) string {
	if len(s) <= 256 {
		return s
	}
	return s[:256] + "..."
}

var _ = os.Getenv // silence unused-import churn during edits
