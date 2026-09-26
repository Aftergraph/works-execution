package api

import (
	"crypto/sha256"
	"encoding/hex"
	"bytes"
	"context"
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

// canonicalCreationIntent produces a stable semantic encoding of creation
// intent. It neutralizes admission defaults that were persisted by historical
// WORKS versions so an accepted Work can be recovered before current admission
// policy is re-run.
func canonicalCreationIntent(w *workgraph.Work) []byte {
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

	for id, n := range in.Graph.Nodes {
		sort.Strings(n.Needs)
		sort.Strings(n.Permissions)
		sort.Strings(n.SideEffects)
		sort.Strings(n.Evidence.Types)
		if n.Retries != nil {
			// Admission fills the nested backoff default even when the caller
			// supplied max_attempts explicitly. Normalize that default before
			// comparing persisted accepted intent with a raw replay.
			if n.Retries.Backoff == "" {
				n.Retries.Backoff = manifest.DefaultBackoff
			}
			sort.Strings(n.Retries.RetryOn)
		}
		if n.CacheSpec != nil {
			// Admission likewise materializes the default cache scope on a
			// caller-supplied cache_spec. Apply it here so pre-admission replay
			// compares semantically, not by representation accident.
			if n.CacheSpec.Scope == "" {
				n.CacheSpec.Scope = manifest.DefaultCacheScope
			}
			sort.Strings(n.CacheSpec.KeyInputs)
		}

		// Slice-4 historical admission defaults are semantic omission.
		if n.TimeoutS == manifest.DefaultTimeoutSeconds {
			n.TimeoutS = 0
		}
		if len(n.Permissions) == 1 && n.Permissions[0] == "read" {
			n.Permissions = nil
		}
		if n.Retries != nil &&
			n.Retries.MaxAttempts == manifest.DefaultRetryMaxAttempts &&
			n.Retries.Backoff == manifest.DefaultBackoff &&
			len(n.Retries.RetryOn) == 0 {
			n.Retries = nil
		}
		if n.CacheSpec != nil &&
			!n.CacheSpec.Enabled &&
			n.CacheSpec.Scope == manifest.DefaultCacheScope &&
			len(n.CacheSpec.KeyInputs) == 0 {
			n.CacheSpec = nil
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

func sameWorkCreationIdentity(a, b *workgraph.Work) bool {
	return bytes.Equal(canonicalCreationIntent(a), canonicalCreationIntent(b))
}

func rawCreationIntentHash(w *workgraph.Work) string {
	if w == nil {
		return ""
	}
	// Normalize representation-only differences without applying admission
	// defaults. The hash captures exactly the semantic caller intent that was
	// accepted, independent of later policy/default changes.
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
	for id, n := range in.Graph.Nodes {
		sort.Strings(n.Needs)
		sort.Strings(n.Permissions)
		sort.Strings(n.SideEffects)
		sort.Strings(n.Evidence.Types)
		if n.Retries != nil {
			sort.Strings(n.Retries.RetryOn)
		}
		if n.CacheSpec != nil {
			sort.Strings(n.CacheSpec.KeyInputs)
		}
		if len(n.Needs) == 0 { n.Needs = nil }
		if len(n.Permissions) == 0 { n.Permissions = nil }
		if len(n.SideEffects) == 0 { n.SideEffects = nil }
		if len(n.Evidence.Types) == 0 { n.Evidence.Types = nil }
		if len(n.Env) == 0 { n.Env = nil }
		in.Graph.Nodes[id] = n
	}
	if len(in.Policy.SecretsScope) == 0 {
		in.Policy.SecretsScope = nil
	} else {
		sort.Strings(in.Policy.SecretsScope)
	}
	encoded, _ := json.Marshal(in)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func replayMatchesDurableIntent(existing, request *workgraph.Work, queue bool) bool {
	if existing == nil || request == nil {
		return false
	}
	if existing.CreationIntentHash != "" && existing.QueueRequested != nil {
		return existing.CreationIntentHash == rawCreationIntentHash(request) &&
			*existing.QueueRequested == queue
	}
	// Legacy v13 rows have no persisted pre-admission intent metadata. They
	// may be read/reconciled conservatively, but queue repair is never inferred
	// for them because the original queue decision is unknowable.
	return sameWorkCreationIdentity(existing, request)
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
