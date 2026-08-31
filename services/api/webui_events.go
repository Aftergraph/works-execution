// Package api — SSE live event stream for the execution view (RFC-0007).
//
// GET /v1/ui/events streams server-sent events so the UI updates
// without polling or full reloads:
//
//	event: work
//	data: {"id":"wrk_...","state":"SUCCEEDED","repo":"...","type":"github_push"}
//
//	event: runner
//	data: {"id":"wrkr_...","pool":"avc-core","stale":false,...}
//
//	event: ping
//	data: {"t":1693500000}
//
// Delivery model: every `tick` (default 2s) the handler snapshots the
// work list and the runner pool, compares against the previous
// snapshot, and emits only changed records (diff). A `ping` is sent
// when nothing changed so proxies keep the connection open. Clients
// reconnect automatically via EventSource.
//
// The stream is read-only and inherits the same auth model as the
// rest of /v1/ui (requireBearer unless WebUIConfig.Public).
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/JonasAbde/works-execution/packages/workgraph"
)

// contextWithTimeout is a tiny indirection so the SSE handler can cap
// snapshot latency without shadowing r.Context().
func contextWithTimeout(r *http.Request, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), d)
}

// sseTick is the snapshot/diff cadence. 2s feels live without
// hammering SQLite; the reaper and lease heartbeats operate on
// similar timescales.
const sseTick = 2 * time.Second

// workEvent is the wire shape for one work row.
type workEvent struct {
	ID        string `json:"id"`
	State     string `json:"state"`
	Type      string `json:"type,omitempty"`
	Repo      string `json:"repo,omitempty"`
	SHA       string `json:"sha,omitempty"`
	UpdatedAt string `json:"updated_at"`
}

// runnerEvent is the wire shape for one runner row.
type runnerEvent struct {
	ID        string `json:"id"`
	Pool      string `json:"pool,omitempty"`
	Trust     string `json:"trust,omitempty"`
	Lifecycle string `json:"lifecycle,omitempty"`
	Stale     bool   `json:"stale"`
	Heartbeat string `json:"heartbeat,omitempty"`
	OSArch    string `json:"os_arch,omitempty"`
}

// sseSnapshot is the full-world state used for diffing.
type sseSnapshot struct {
	works   map[string]workEvent
	runners map[string]runnerEvent
}

// sseWrite emits one SSE frame. Returns a write error so the handler
// can stop streaming when the client disconnects.
func sseWrite(w http.ResponseWriter, flusher http.Flusher, event, data string) error {
	if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data); err != nil {
		return err
	}
	flusher.Flush()
	return nil
}

// uiEvents is the SSE endpoint: GET /v1/ui/events.
func (s *Server) uiEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", r.Method)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "sse_unsupported", "response writer cannot flush")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // nginx/cloudflare: pass through

	// Cloudflare proxies may buffer; a first flush opens the pipe.
	fmt.Fprint(w, ": stream open\n\n")
	flusher.Flush()

	ctx := r.Context()
	var prev sseSnapshot
	snap := s.takeSnapshot
	first := true

	t := time.NewTicker(sseTick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			cur := snap(r)
			changed := false

			// Works diff: new or updated rows.
			for id, we := range cur.works {
				if first {
					// First tick emits the full world so a fresh
					// client paints immediately.
					b, _ := json.Marshal(we)
					if sseWrite(w, flusher, "work", string(b)) != nil {
						return
					}
					changed = true
					continue
				}
				if old, ok := prev.works[id]; !ok || old != we {
					b, _ := json.Marshal(we)
					if sseWrite(w, flusher, "work", string(b)) != nil {
						return
					}
					changed = true
				}
			}
			// Works diff: removals.
			if !first {
				for id := range prev.works {
					if _, ok := cur.works[id]; !ok {
						b, _ := json.Marshal(map[string]string{"id": id, "state": "DELETED"})
						if sseWrite(w, flusher, "work", string(b)) != nil {
							return
						}
						changed = true
					}
				}
			}

			// Runners diff.
			for id, re := range cur.runners {
				if first {
					b, _ := json.Marshal(re)
					if sseWrite(w, flusher, "runner", string(b)) != nil {
						return
					}
					changed = true
					continue
				}
				if old, ok := prev.runners[id]; !ok || old != re {
					b, _ := json.Marshal(re)
					if sseWrite(w, flusher, "runner", string(b)) != nil {
						return
					}
					changed = true
				}
			}
			if !first {
				for id := range prev.runners {
					if _, ok := cur.runners[id]; !ok {
						b, _ := json.Marshal(map[string]string{"id": id, "state": "DELETED"})
						if sseWrite(w, flusher, "runner", string(b)) != nil {
							return
						}
						changed = true
					}
				}
			}

			if !changed || first {
				b, _ := json.Marshal(map[string]int64{"t": time.Now().Unix()})
				_ = sseWrite(w, flusher, "ping", string(b))
			}

			prev = cur
			first = false
		}
	}
}

// takeSnapshot builds the diff source from the store + registry.
func (s *Server) takeSnapshot(r *http.Request) sseSnapshot {
	snap := sseSnapshot{
		works:   map[string]workEvent{},
		runners: map[string]runnerEvent{},
	}
	ctx, cancel := contextWithTimeout(r, 1500*time.Millisecond)
	defer cancel()

	if list, err := s.Store.ListWorks(ctx, 50); err == nil {
		for _, wk := range list {
			if wk == nil {
				continue
			}
			snap.works[wk.ID] = workEvent{
				ID:        wk.ID,
				State:     string(wk.State),
				Type:      wk.Source.Type,
				Repo:      wk.Source.Repository,
				SHA:       shortSHA(wk.Source.SHA),
				UpdatedAt: wk.UpdatedAt.UTC().Format(time.RFC3339),
			}
		}
	}
	cutoff := time.Now().Add(-3 * defaultHeartbeatInterval)
	if s.RunnerRegistry != nil {
		for _, id := range s.RunnerRegistry.List() {
			if id == nil {
				continue
			}
			re := runnerEvent{
				ID:        id.RunnerID,
				Trust:     string(id.TrustClass),
				Lifecycle: string(id.LifecycleState),
			}
			for _, l := range id.Capabilities.Labels {
				if len(l) > 5 && l[:5] == "pool:" {
					re.Pool = l[5:]
					break
				}
			}
			if len(id.Capabilities.OS) > 0 {
				re.OSArch = id.Capabilities.OS[0]
			}
			if len(id.Capabilities.Arch) > 0 {
				re.OSArch += "/" + id.Capabilities.Arch[0]
			}
			if id.LastHeartbeatAt != nil {
				re.Heartbeat = time.Since(*id.LastHeartbeatAt).Round(time.Second).String()
				re.Stale = id.LastHeartbeatAt.Before(cutoff)
			} else {
				re.Stale = true
			}
			snap.runners[id.RunnerID] = re
		}
	}
	return snap
}

// shortSHA returns the first 8 chars of a commit SHA, "" when absent.
func shortSHA(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}

// stateBadgeClass maps a work state to its CSS badge class (shared
// by the server templates and the client-side work row renderer).
func stateBadgeClass(s workgraph.State) string {
	switch s {
	case workgraph.StateSucceeded:
		return "ok"
	case workgraph.StateFailed:
		return "fail"
	case workgraph.StateRunning, workgraph.StateVerifying:
		return "run"
	case workgraph.StateQueued, workgraph.StateCreated, workgraph.StatePlanning:
		return "queued"
	default:
		return "other"
	}
}
