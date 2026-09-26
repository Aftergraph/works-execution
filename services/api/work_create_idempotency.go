package api

import (
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

// reconcileReplayQueue closes the crash seam between durable creation and the
// convenience queue transition. A replay may complete only the missing
// CREATED -> QUEUED transition; later states are never moved backwards.
func (s *Server) reconcileReplayQueue(ctx context.Context, existing *workgraph.Work, queue bool) (*workgraph.Work, error) {
	if existing == nil || !queue || existing.State != workgraph.StateCreated {
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
