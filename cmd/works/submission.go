package main

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"time"
)

const maxIdempotentSubmitAttempts = 3

type submitResult struct {
	StatusCode int
	Status     string
	Body       []byte
	Replay     bool
	Attempts   int
}

// submitWorkWithReconcile makes Work submission resilient to the ambiguous
// acknowledgement failure: the server may have committed the Work even when
// the controller loses the HTTP response.
//
// Without an idempotency key an ambiguous POST is never retried, because a
// retry could create a second Work. With a stable key, bounded retries are
// safe: the API reconciles the key back to the canonical accepted Work.
func submitWorkWithReconcile(client *http.Client, endpoint string, payload []byte, idempotencyKey string, auth *cliAuth) (submitResult, error) {
	if client == nil {
		client = http.DefaultClient
	}

	maxAttempts := 1
	if idempotencyKey != "" {
		maxAttempts = maxIdempotentSubmitAttempts
	} else if auth != nil && auth.canRenew() {
		// A definitive 401 is safe to retry after re-enrollment even without
		// an idempotency key because the auth middleware has not invoked the
		// mutating handler.
		maxAttempts = 2
	}

	var lastErr error
	renewedAuth := false
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(payload))
		if err != nil {
			return submitResult{}, fmt.Errorf("build submit request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		if auth != nil {
			if h := auth.authHeader(); h != "" {
				req.Header.Set("Authorization", h)
			}
		}
		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			if idempotencyKey != "" && attempt < maxAttempts {
				time.Sleep(submitRetryDelay(attempt))
				continue
			}
			if idempotencyKey == "" {
				return submitResult{}, fmt.Errorf("ambiguous submission not retried without --idempotency-key: %w", err)
			}
			return submitResult{}, fmt.Errorf("submit failed after %d idempotent attempts: %w", attempt, err)
		}

		raw, readErr := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if readErr != nil {
			lastErr = readErr
			if idempotencyKey != "" && attempt < maxAttempts {
				time.Sleep(submitRetryDelay(attempt))
				continue
			}
			if idempotencyKey == "" {
				return submitResult{}, fmt.Errorf("ambiguous response read not retried without --idempotency-key: %w", readErr)
			}
			return submitResult{}, fmt.Errorf("read submit response after %d attempts: %w", attempt, readErr)
		}

		result := submitResult{
			StatusCode: resp.StatusCode,
			Status:     resp.Status,
			Body:       raw,
			Replay:     resp.Header.Get("X-Works-Idempotent-Replay") == "true",
			Attempts:   attempt,
		}

		// WORKS returns 401 before invoking the mutating handler. If this CLI
		// was enrolled with a shared challenge, refresh exactly once and retry.
		// This specifically recovers the expected API-restart case where the
		// process-local HMAC issuer rotated and invalidated the old JWT.
		if resp.StatusCode == http.StatusUnauthorized && auth != nil && auth.canRenew() && !renewedAuth && attempt < maxAttempts {
			if err := auth.renew(); err != nil {
				return result, fmt.Errorf("renew auth after 401: %w", err)
			}
			renewedAuth = true
			continue
		}

		// A stable idempotency key makes transient server/gateway failures safe
		// to retry. The retry either creates the Work (if the first request did
		// not commit) or reconciles the already accepted canonical Work.
		if idempotencyKey != "" && retryableSubmitStatus(resp.StatusCode) && attempt < maxAttempts {
			lastErr = fmt.Errorf("transient submit status %s", resp.Status)
			time.Sleep(submitRetryDelay(attempt))
			continue
		}
		return result, nil
	}

	return submitResult{}, fmt.Errorf("submit exhausted retries: %w", lastErr)
}

func retryableSubmitStatus(code int) bool {
	return code == http.StatusTooManyRequests || code >= 500
}

func submitRetryDelay(attempt int) time.Duration {
	switch attempt {
	case 1:
		return 50 * time.Millisecond
	default:
		return 150 * time.Millisecond
	}
}
