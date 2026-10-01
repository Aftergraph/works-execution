package promotion

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

func promotionIdentity(req Request) (keyHash, fingerprint string, err error) {
	if err := req.Validate(); err != nil {
		return "", "", err
	}
	key := sha256.Sum256([]byte(req.IdempotencyKey))

	type canonical struct {
		Org              string `json:"org"`
		WorkID           string `json:"work_id"`
		WorkspaceID      string `json:"workspace_id"`
		CandidateSHA     string `json:"candidate_sha"`
		EvidenceBundleID string `json:"evidence_bundle_id"`
		DecisionRef      string `json:"decision_ref"`
		PolicyDecisionID string `json:"policy_decision_id,omitempty"`
		Target           Target `json:"target"`
	}
	raw, err := json.Marshal(canonical{
		Org:req.Org, WorkID:req.WorkID, WorkspaceID:req.Workspace.ID,
		CandidateSHA:req.Candidate.SHA, EvidenceBundleID:req.EvidenceBundleID,
		DecisionRef:req.DecisionRef, PolicyDecisionID:req.PolicyDecisionID,
		Target:req.Target,
	})
	if err != nil {
		return "", "", err
	}
	fp := sha256.Sum256(raw)
	return hex.EncodeToString(key[:12]), hex.EncodeToString(fp[:12]), nil
}
