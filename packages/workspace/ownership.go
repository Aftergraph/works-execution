package workspace

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// workspaceHandleID is the durable, provider-neutral ownership seal for a
// workspace handle. It binds tenant/work provenance to the provider resource
// coordinates without storing a server-side ownership registry.
func workspaceHandleID(providerID, org, workID, name, defaultBranch string) string {
	parts := []string{
		strings.TrimSpace(providerID),
		strings.TrimSpace(org),
		strings.TrimSpace(workID),
		strings.TrimSpace(name),
		strings.TrimSpace(defaultBranch),
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return "wsp_" + hex.EncodeToString(sum[:16])
}

func workspaceHandleOwnedBy(ws Workspace, providerID string) bool {
	if ws.ID == "" || ws.ProviderID != providerID {
		return false
	}
	return ws.ID == workspaceHandleID(providerID, ws.Org, ws.WorkID, ws.Name, ws.DefaultBranch)
}
