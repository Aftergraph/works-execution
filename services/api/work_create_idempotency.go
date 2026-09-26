package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"sort"

	"github.com/JonasAbde/works-execution/internal/manifest"
	"github.com/JonasAbde/works-execution/packages/workgraph"
	"github.com/JonasAbde/works-execution/services/work/store"
)

type idempotencyWorkGetter interface {
	GetWorkByIdempotencyKey(ctx context.Context, key string) (*workgraph.Work, error)
}

func (s *Server) lookupIdempotentWork(ctx context.Context, key string) (*workgraph.Work, error) {
	if key == "" {
		return nil, nil
	}
	getter, ok := s.Store.(idempotencyWorkGetter)
	if !ok {
		return nil, nil
	}
	w, err := getter.GetWorkByIdempotencyKey(ctx, key)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	return w, err
}

// creationIntent is the immutable semantic payload used for idempotency
// comparison. Controller-generated IDs, timestamps, state and runtime outputs
// are deliberately excluded.
type creationIntent struct {
	Source       workgraph.Source
	Objective    workgraph.Objective
	Graph        workgraph.Graph
	Requirements workgraph.Requirements
	Policy       workgraph.Policy
	Mission      *workgraph.MissionContract
}

type admissionDefaultsSnapshot struct {
	TimeoutSeconds     int      `json:"timeout_seconds"`
	RetryMaxAttempts   int      `json:"retry_max_attempts"`
	Backoff            string   `json:"backoff"`
	CacheScope         string   `json:"cache_scope"`
	DefaultPermissions []string `json:"default_permissions"`
}

func currentAdmissionDefaults() admissionDefaultsSnapshot {
	return admissionDefaultsSnapshot{
		TimeoutSeconds:     manifest.DefaultTimeoutSeconds,
		RetryMaxAttempts:   manifest.DefaultRetryMaxAttempts,
		Backoff:            manifest.DefaultBackoff,
		CacheScope:         manifest.DefaultCacheScope,
		DefaultPermissions: []string{"read"},
	}
}

func encodeAdmissionDefaults(d admissionDefaultsSnapshot) string {
	raw, _ := json.Marshal(d)
	return string(raw)
}

func decodeAdmissionDefaults(raw string) (admissionDefaultsSnapshot, bool) {
	if raw == "" {
		return admissionDefaultsSnapshot{}, false
	}
	var d admissionDefaultsSnapshot
	if err := json.Unmarshal([]byte(raw), &d); err != nil {
		return admissionDefaultsSnapshot{}, false
	}
	if d.TimeoutSeconds <= 0 || d.RetryMaxAttempts <= 0 ||
		d.Backoff == "" || d.CacheScope == "" || len(d.DefaultPermissions) == 0 {
		return admissionDefaultsSnapshot{}, false
	}
	return d, true
}

// canonicalCreationIntentWithDefaults produces a stable semantic encoding of
// the caller's creation intent using the exact admission defaults that were in
// force when the Work was accepted. Persisting that snapshot avoids rebuilding
// historical intent from mutable future constants.
func canonicalCreationIntentWithDefaults(w *workgraph.Work, defaults admissionDefaultsSnapshot) []byte {
	if w == nil {
		return nil
	}

	raw, _ := json.Marshal(creationIntent{
		Source:       w.Source,
		Objective:    w.Objective,
		Graph:        w.Graph,
		Requirements: w.Requirements,
		Policy:       w.Policy,
		Mission:      w.Mission,
	})
	var in creationIntent
	_ = json.Unmarshal(raw, &in)

	defaultPermissions := append([]string(nil), defaults.DefaultPermissions...)
	sort.Strings(defaultPermissions)

	for id, n := range in.Graph.Nodes {
		sort.Strings(n.Needs)
		sort.Strings(n.Permissions)
		sort.Strings(n.SideEffects)
		sort.Strings(n.Evidence.Types)

		if n.TimeoutS == defaults.TimeoutSeconds {
			n.TimeoutS = 0
		}
		if equalStrings(n.Permissions, defaultPermissions) {
			n.Permissions = nil
		}

		if n.Retries != nil {
			if n.Retries.Backoff == "" {
				n.Retries.Backoff = defaults.Backoff
			}
			sort.Strings(n.Retries.RetryOn)
			if n.Retries.MaxAttempts == defaults.RetryMaxAttempts &&
				n.Retries.Backoff == defaults.Backoff &&
				len(n.Retries.RetryOn) == 0 {
				n.Retries = nil
			}
		}
		if n.CacheSpec != nil {
			if n.CacheSpec.Scope == "" {
				n.CacheSpec.Scope = defaults.CacheScope
			}
			sort.Strings(n.CacheSpec.KeyInputs)
			if !n.CacheSpec.Enabled &&
				n.CacheSpec.Scope == defaults.CacheScope &&
				len(n.CacheSpec.KeyInputs) == 0 {
				n.CacheSpec = nil
			}
		}

		if len(n.Needs) == 0 {
			n.Needs = nil
		}
		if len(n.Permissions) == 0 {
			n.Permissions = nil
		}
		if len(n.SideEffects) == 0 {
			n.SideEffects = nil
		}
		if len(n.Evidence.Types) == 0 {
			n.Evidence.Types = nil
		}
		if len(n.Env) == 0 {
			n.Env = nil
		}
		in.Graph.Nodes[id] = n
	}

	if len(in.Policy.SecretsScope) == 0 {
		in.Policy.SecretsScope = nil
	} else {
		sort.Strings(in.Policy.SecretsScope)
	}
	if in.Mission != nil &&
		in.Mission.BudgetCeiling == nil &&
		len(in.Mission.Verification) == 0 &&
		len(in.Mission.PurposeBindings) == 0 &&
		in.Mission.KillSwitch == "" {
		in.Mission = nil
	}

	out, _ := json.Marshal(in)
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func canonicalCreationIntent(w *workgraph.Work) []byte {
	return canonicalCreationIntentWithDefaults(w, currentAdmissionDefaults())
}

func sameWorkCreationIdentity(a, b *workgraph.Work) bool {
	return bytes.Equal(canonicalCreationIntent(a), canonicalCreationIntent(b))
}

func creationIntentHashWithDefaults(w *workgraph.Work, defaults admissionDefaultsSnapshot) string {
	encoded := canonicalCreationIntentWithDefaults(w, defaults)
	if encoded == nil {
		return ""
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func replayMatchesDurableIntent(existing, request *workgraph.Work, queue bool) bool {
	if existing == nil || request == nil {
		return false
	}

	hasDurableMetadata := existing.CreationIntentHash != "" ||
		existing.AdmissionDefaultsJSON != "" ||
		existing.QueueRequested != nil
	if hasDurableMetadata {
		if existing.CreationIntentHash == "" ||
			existing.AdmissionDefaultsJSON == "" ||
			existing.QueueRequested == nil {
			return false
		}
		defaults, ok := decodeAdmissionDefaults(existing.AdmissionDefaultsJSON)
		if !ok {
			return false
		}
		return existing.CreationIntentHash == creationIntentHashWithDefaults(request, defaults) &&
			*existing.QueueRequested == queue
	}

	// Legacy rows predate durable creation metadata. Their original omission
	// choices cannot be reconstructed safely; compare conservatively under the
	// current representation and never infer queue repair for them.
	return sameWorkCreationIdentity(existing, request)
}

func replayMatchesAcceptedHash(existing *workgraph.Work, requestHash string, queue bool) bool {
	if existing == nil || existing.CreationIntentHash == "" ||
		existing.AdmissionDefaultsJSON == "" || existing.QueueRequested == nil {
		return false
	}
	return existing.CreationIntentHash == requestHash && *existing.QueueRequested == queue
}


// reconcileReplayQueue closes the crash seam between durable creation and the
// convenience queue transition. A replay may complete only the missing
// CREATED -> QUEUED transition; later states are never moved backwards.
func (s *Server) reconcileReplayQueue(ctx context.Context, existing *workgraph.Work) (*workgraph.Work, error) {
	// Queue repair is permitted only when v14 durable metadata proves that
	// the ORIGINAL accepted submission requested queue=true. Never infer this
	// from a later replay.
	if existing == nil || existing.QueueRequested == nil || !*existing.QueueRequested || existing.State != workgraph.StateCreated {
		return existing, nil
	}
	updated, err := s.Store.UpdateState(ctx, existing.ID, workgraph.StateQueued)
	if err == nil {
		return updated, nil
	}

	latest, getErr := s.Store.GetWork(ctx, existing.ID)
	if getErr == nil && latest.State != workgraph.StateCreated {
		return latest, nil
	}
	return nil, err
}

func writeIdempotentReplay(w http.ResponseWriter, existing *workgraph.Work) {
	w.Header().Set("X-Works-Idempotent-Replay", "true")
	writeJSON(w, http.StatusOK, existing)
}
