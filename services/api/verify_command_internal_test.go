package api

import (
	"testing"

	"github.com/JonasAbde/works-execution/services/webhook"
)

func TestVerifyCommand_AllowsOnlyKnownRepositories(t *testing.T) {
	if got := verifyCommand(webhook.Delivery{RepoFullName: "JonasAbde/Renos-Control"}); got != "npm ci && npm --prefix backend/operations ci && npm run verify" {
		t.Fatalf("RenOS command: got %q", got)
	}
	if got := verifyCommand(webhook.Delivery{RepoFullName: "JonasAbde/works-execution"}); got != "go vet ./... && go test ./..." {
		t.Fatalf("Works command: got %q", got)
	}
	if got := verifyCommand(webhook.Delivery{RepoFullName: "attacker/example"}); got != "exit 78" {
		t.Fatalf("unknown command: got %q", got)
	}
}
