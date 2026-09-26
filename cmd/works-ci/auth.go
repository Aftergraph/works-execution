package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/JonasAbde/works-execution/packages/protocol"
)

const (
	maxCISubmitAttempts = 3
	ciSubmitTimeout      = 15 * time.Second
)

// apiAuth is works-ci's minimal auth client. It retains the enrollment
// challenge so an API restart can rotate the token without turning a durable
// CI submission into a manual recovery event.
type apiAuth struct {
	api          string
	token        string
	enrollSecret string
	client       *http.Client
}

func (a *apiAuth) httpClient(timeout time.Duration) *http.Client {
	if a != nil && a.client != nil {
		clone := *a.client
		if clone.Timeout <= 0 {
			clone.Timeout = timeout
		}
		return &clone
	}
	return &http.Client{Timeout: timeout}
}

func newAuthFor(api, enrollSecret string) (*apiAuth, error) {
	tok, err := enrollToken(api, enrollSecret)
	if err != nil {
		return nil, err
	}
	return &apiAuth{api: api, token: tok, enrollSecret: enrollSecret}, nil
}

func enrollToken(api, enrollSecret string) (string, error) {
	suffix := make([]byte, 6)
	_, _ = rand.Read(suffix)
	body := map[string]any{
		"worker_id":   "wrkr_works_ci_" + hex.EncodeToString(suffix),
		"challenge":   enrollSecret,
		"scope":       "worker",
		"ttl_seconds": 3600,
	}
	buf, _ := json.Marshal(body)
	c := &http.Client{Timeout: 10 * time.Second}
	resp, err := c.Post(api+"/v1/workers/enroll", "application/json", bytes.NewReader(buf))
	if err != nil {
		return "", fmt.Errorf("enroll: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("enroll: status=%d body=%s", resp.StatusCode, string(b))
	}
	var er struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&er); err != nil || er.Token == "" {
		return "", fmt.Errorf("enroll: decode response: %v", err)
	}
	return er.Token, nil
}

func (a *apiAuth) renew() error {
	if a == nil || a.enrollSecret == "" {
		return fmt.Errorf("auth renewal unavailable")
	}
	tok, err := enrollToken(a.api, a.enrollSecret)
	if err != nil {
		return err
	}
	a.token = tok
	return nil
}

type workSubmitResult struct {
	StatusCode int
	Replay     bool
	Attempts   int
}

// postWorkResilient submits a Work through the same bounded recovery law as the
// developer CLI. A stable idempotency key is mandatory because transport/read
// ambiguity can occur after the server committed the Work.
func (a *apiAuth) postWorkResilient(path string, body any, idempotencyKey string, out any) (workSubmitResult, error) {
	if a == nil {
		return workSubmitResult{}, fmt.Errorf("nil auth client")
	}
	if idempotencyKey == "" {
		return workSubmitResult{}, fmt.Errorf("idempotency key required for resilient work submission")
	}
	buf, err := json.Marshal(body)
	if err != nil {
		return workSubmitResult{}, fmt.Errorf("marshal work: %w", err)
	}

	maxHTTPAttempts := maxCISubmitAttempts + 1 // one extra slot for pre-mutation 401 renewal
	submissionAttempts := 0
	renewedAuth := false
	recoveryCause := ""
	var lastErr error

	for httpAttempt := 1; httpAttempt <= maxHTTPAttempts; httpAttempt++ {
		submissionAttempts++
		req, err := http.NewRequest(http.MethodPost, a.api+path, bytes.NewReader(buf))
		if err != nil {
			return workSubmitResult{}, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+a.token)
		if recoveryCause != "" {
			req.Header.Set(protocol.RecoveryCauseHeader, recoveryCause)
		}

		resp, err := a.httpClient(ciSubmitTimeout).Do(req)
		if err != nil {
			lastErr = err
			if recoveryCause == "" {
				recoveryCause = protocol.RecoveryCauseAmbiguousTransport
			}
			if submissionAttempts < maxCISubmitAttempts {
				time.Sleep(ciRetryDelay(submissionAttempts))
				continue
			}
			return workSubmitResult{}, fmt.Errorf("work submit failed after %d idempotent attempts: %w", submissionAttempts, err)
		}

		raw, readErr := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if readErr != nil {
			lastErr = readErr
			if recoveryCause == "" {
				recoveryCause = protocol.RecoveryCauseAmbiguousResponseRead
			}
			if submissionAttempts < maxCISubmitAttempts {
				time.Sleep(ciRetryDelay(submissionAttempts))
				continue
			}
			return workSubmitResult{}, fmt.Errorf("work submit response read failed after %d attempts: %w", submissionAttempts, readErr)
		}

		result := workSubmitResult{
			StatusCode: resp.StatusCode,
			Replay:     resp.Header.Get("X-Works-Idempotent-Replay") == "true",
			Attempts:   httpAttempt,
		}

		if resp.StatusCode == http.StatusUnauthorized && !renewedAuth {
			if recoveryCause == "" {
				recoveryCause = protocol.RecoveryCauseAuthRenewal
			}
			if err := a.renew(); err != nil {
				return result, fmt.Errorf("renew auth after 401: %w", err)
			}
			renewedAuth = true
			submissionAttempts-- // definitive 401 happened before mutation
			continue
		}

		if (resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500) &&
			submissionAttempts < maxCISubmitAttempts {
			if recoveryCause == "" {
				recoveryCause = protocol.RecoveryCauseTransientStatus
			}
			lastErr = fmt.Errorf("transient submit status %d", resp.StatusCode)
			time.Sleep(ciRetryDelay(submissionAttempts))
			continue
		}

		if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
			return result, fmt.Errorf("%s: status=%d body=%s", path, resp.StatusCode, truncate(string(raw)))
		}
		if out != nil {
			if err := json.Unmarshal(raw, out); err != nil {
				return result, fmt.Errorf("%s: decode: %w", path, err)
			}
		}
		return result, nil
	}
	return workSubmitResult{}, fmt.Errorf("work submit exhausted retries: %w", lastErr)
}

func ciRetryDelay(attempt int) time.Duration {
	if attempt <= 1 {
		return 50 * time.Millisecond
	}
	return 150 * time.Millisecond
}

// postJSON POSTs and decodes; used for non-submission API calls.
func (a *apiAuth) postJSON(path string, body any, out any) (*http.Response, error) {
	buf, _ := json.Marshal(body)
	req, err := http.NewRequest(http.MethodPost, a.api+path, bytes.NewReader(buf))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+a.token)
	resp, err := a.httpClient(15 * time.Second).Do(req)
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

func (a *apiAuth) getJSON(path string, out any) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodGet, a.api+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+a.token)
	resp, err := a.httpClient(15 * time.Second).Do(req)
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
