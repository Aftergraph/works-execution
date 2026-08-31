package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"
)

const testSecret = "shhh-this-is-the-webhook-secret"

// TestVerifySignature_HappyPath: a correctly-signed body verifies.
func TestVerifySignature_HappyPath(t *testing.T) {
	body := []byte(`{"hello":"world"}`)
	// Compute the expected signature inline so the test does not
	// depend on a generator function (intentional — we want the
	// test to fail if VerifySignature is using a different algo).
	mac := hmacSHA256(testSecret, body)
	sig := "sha256=" + mac
	if err := VerifySignature(testSecret, sig, body); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
}

// TestVerifySignature_MissingHeader: no header → ErrMissingSignature.
func TestVerifySignature_MissingHeader(t *testing.T) {
	err := VerifySignature(testSecret, "", []byte("body"))
	if !errors.Is(err, ErrMissingSignature) {
		t.Fatalf("expected ErrMissingSignature, got %v", err)
	}
}

// TestVerifySignature_WrongSecret: signature with the wrong secret
// fails. This is the attack the HMAC is supposed to prevent.
func TestVerifySignature_WrongSecret(t *testing.T) {
	body := []byte(`{"hello":"world"}`)
	sig := "sha256=" + hmacSHA256("wrong-secret", body)
	err := VerifySignature(testSecret, sig, body)
	if !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("expected ErrInvalidSignature, got %v", err)
	}
}

// TestVerifySignature_TamperedBody: signature for one body, but the
// body was modified after signing. Must fail.
func TestVerifySignature_TamperedBody(t *testing.T) {
	sig := "sha256=" + hmacSHA256(testSecret, []byte(`{"a":1}`))
	err := VerifySignature(testSecret, sig, []byte(`{"a":2}`))
	if !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("expected ErrInvalidSignature, got %v", err)
	}
}

// TestVerifySignature_BadPrefix: missing "sha256=" prefix fails.
func TestVerifySignature_BadPrefix(t *testing.T) {
	body := []byte(`{"a":1}`)
	sig := "md5=" + hmacSHA256(testSecret, body)
	err := VerifySignature(testSecret, sig, body)
	if !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("expected ErrInvalidSignature, got %v", err)
	}
}

// TestVerifySignature_BadHex: non-hex characters in the signature
// decode → error.
func TestVerifySignature_BadHex(t *testing.T) {
	err := VerifySignature(testSecret, "sha256=zzznotvalid", []byte("body"))
	if !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("expected ErrInvalidSignature, got %v", err)
	}
}

// TestParseGitHubDelivery_Push: a real push event parses into the
// expected Delivery.
func TestParseGitHubDelivery_Push(t *testing.T) {
	body := []byte(`{
		"ref": "refs/heads/main",
		"before": "0000000000000000000000000000000000000000",
		"after":  "abc1234567890def1234567890abcdef12345678",
		"repository": {
			"full_name": "JonasAbde/works-execution",
			"html_url":  "https://github.com/JonasAbde/works-execution",
			"clone_url": "https://github.com/JonasAbde/works-execution.git"
		}
	}`)
	d, err := ParseGitHubDelivery("push", "delivery-001", body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d.Ref != "refs/heads/main" {
		t.Errorf("ref: got %q", d.Ref)
	}
	if d.SHA != "abc1234567890def1234567890abcdef12345678" {
		t.Errorf("sha: got %q", d.SHA)
	}
	if d.RepoFullName != "JonasAbde/works-execution" {
		t.Errorf("repo: got %q", d.RepoFullName)
	}
	if !d.ShouldCreateWork() {
		t.Error("expected ShouldCreateWork()=true for push to main")
	}
}

// TestParseGitHubDelivery_PullRequest_Opened: PR opened event parses
// and ShouldCreateWork()=true.
func TestParseGitHubDelivery_PullRequest_Opened(t *testing.T) {
	body := []byte(`{
		"action": "opened",
		"number": 42,
		"pull_request": {
			"head": {
				"sha": "deadbeefcafebabedeadbeefcafebabedeadbeef",
				"ref": "feature/m1-pilot",
				"repo": {
					"full_name": "JonasAbde/works-execution",
					"clone_url": "https://github.com/JonasAbde/works-execution.git"
				}
			},
			"base": {"ref": "main"}
		},
		"repository": {
			"full_name": "JonasAbde/works-execution",
			"html_url":  "https://github.com/JonasAbde/works-execution"
		}
	}`)
	d, err := ParseGitHubDelivery("pull_request", "delivery-002", body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d.PRAction != "opened" {
		t.Errorf("action: got %q", d.PRAction)
	}
	if d.PRNumber != 42 {
		t.Errorf("number: got %d", d.PRNumber)
	}
	if d.PRHead != "feature/m1-pilot" {
		t.Errorf("head: got %q", d.PRHead)
	}
	if d.PRBase != "main" {
		t.Errorf("base: got %q", d.PRBase)
	}
	if !d.ShouldCreateWork() {
		t.Error("expected ShouldCreateWork()=true for opened PR")
	}
}

// TestParseGitHubDelivery_PullRequest_Closed: a "closed" PR event
// must NOT create a work — we don't build closed PRs.
func TestParseGitHubDelivery_PullRequest_Closed(t *testing.T) {
	body := []byte(`{
		"action": "closed",
		"number": 42,
		"pull_request": {
			"head": {"sha": "deadbeef12345678deadbeef12345678deadbeef", "ref": "x"},
			"base": {"ref": "main"}
		},
		"repository": {"full_name": "x/y", "html_url": "http://x"}
	}`)
	d, err := ParseGitHubDelivery("pull_request", "delivery-003", body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d.ShouldCreateWork() {
		t.Error("expected ShouldCreateWork()=false for closed PR")
	}
}

// TestParseGitHubDelivery_Ping: a ping event is recognized but
// doesn't create a work.
func TestParseGitHubDelivery_Ping(t *testing.T) {
	d, err := ParseGitHubDelivery("ping", "delivery-004", []byte(`{"zen":"Hello"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d.ShouldCreateWork() {
		t.Error("ping should not create a work")
	}
}

// TestParseGitHubDelivery_UnknownEvent: an unknown event returns
// ErrUnsupportedEvent.
func TestParseGitHubDelivery_UnknownEvent(t *testing.T) {
	_, err := ParseGitHubDelivery("starred", "delivery-005", []byte(`{}`))
	if !errors.Is(err, ErrUnsupportedEvent) {
		t.Fatalf("expected ErrUnsupportedEvent, got %v", err)
	}
}

// TestParseGitHubDelivery_MissingDeliveryID: empty delivery ID is
// rejected up front.
func TestParseGitHubDelivery_MissingDeliveryID(t *testing.T) {
	_, err := ParseGitHubDelivery("push", "", []byte(`{}`))
	if !errors.Is(err, ErrMissingDeliveryID) {
		t.Fatalf("expected ErrMissingDeliveryID, got %v", err)
	}
}

// TestParseGitHubDelivery_BadJSON: malformed push payload returns a
// decode error wrapping the original.
func TestParseGitHubDelivery_BadJSON(t *testing.T) {
	_, err := ParseGitHubDelivery("push", "delivery-006", []byte(`{not json`))
	if err == nil || strings.Contains(err.Error(), "decode push payload") == false {
		t.Fatalf("expected decode error, got %v", err)
	}
}

// TestDelivery_ReceivedAt: ReceivedAt is set to "now" at parse time.
func TestDelivery_ReceivedAt(t *testing.T) {
	before := time.Now().UTC().Add(-time.Second)
	d, err := ParseGitHubDelivery("ping", "delivery-007", []byte(`{}`))
	after := time.Now().UTC().Add(time.Second)
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if d.ReceivedAt.Before(before) || d.ReceivedAt.After(after) {
		t.Errorf("ReceivedAt %v outside [%v, %v]", d.ReceivedAt, before, after)
	}
}

// hmacSHA256 is a tiny inline helper so the test file is
// self-contained and the reader can see exactly what the expected
// signature should be.
func hmacSHA256(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}
