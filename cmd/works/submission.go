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
func submitWorkWithReconcile(client *http.Client, endpoint string, payload []byte, idempotencyKey string) (submitResult, error) {
	if client == nil {
		client = http.DefaultClient
	}

	maxAttempts := 1
	if idempotencyKey != "" {
		maxAttempts = maxIdempotentSubmitAttempts
	}

	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		resp, err := client.Post(endpoint, "application/json", bytes.NewReader(payload))
		if err != nil {
			lastErr = err
			if attempt < maxAttempts {
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
			if attempt < maxAttempts {
				time.Sleep(submitRetryDelay(attempt))
				continue
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
