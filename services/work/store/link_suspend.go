package store

// k-035 — revoke cascade law (ADR-0020/0026): when a WORKS-Link device is
// revoked, every MISSION Work the device had mounted and that is still in an
// active state must be atomically suspended to SUSPENDED with a durable
// ADR-0010 handoff. This file is the kernel-side sink the link Service calls
// as its Cascade (best-effort: the revoke stands even if this fails).
//
// Laws encoded here:
//   - missions only: a Work without the mission contract (CI work) is NEVER
//     touched — device revocation cannot reach kernel-independent CI works
//     (SuspendWork would refuse the transition anyway; we filter first).
//   - active states only: CREATED, PLANNING, QUEUED, RUNNING, VERIFYING,
//     BLOCKED. Terminal (SUCCEEDED/FAILED/CANCELLED) and already-suspended
//     works are skipped, never errors.
//   - RUNNING/VERIFYING missions suspend through the canonical
//     SuspendWorkEventful path (the frozen state machine has those edges).
//     The other active states (CREATED, PLANNING, QUEUED, BLOCKED) have no
//     frozen edge into SUSPENDED, so the cascade writes them through
//     cascadeSuspendWork below: the SAME atomic kernel transaction (state +
//     handoff + journal event, one commit) under the ADR-0020 rule that a
//     device-local revoke always wins. The handoff IS the evidence there, too.

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/JonasAbde/works-execution/packages/workgraph"
)

// suspendableStates is the active-state set the cascade law suspends.
// Everything else (terminal, SUSPENDED, WAITING_HUMAN, BUDGET_EXHAUSTED) is
// already stopped or past caring — skipped, not errors.
var suspendableStates = map[workgraph.State]bool{
	workgraph.StateCreated:   true,
	workgraph.StatePlanning:  true,
	workgraph.StateQueued:    true,
	workgraph.StateRunning:   true,
	workgraph.StateVerifying: true,
	workgraph.StateBlocked:   true,
}

// revokeCascadeHandoff builds the frozen ADR-0010 checkpoint for a
// revoke-cascade suspend. Narrative/DecisionLog/Warnings are law-fixed.
func revokeCascadeHandoff(deviceID string) *workgraph.Handoff {
	return &workgraph.Handoff{
		StateSnapshot: map[string]any{
			"device_id": deviceID,
			"reason":    "device_revoked",
		},
		Narrative:   fmt.Sprintf("WORKS-Link device %s revoked — mission suspended for human review", deviceID),
		DecisionLog: []string{"revoke-cascade k-035"},
		// Priority queue left empty on purpose: the human review owns the
		// next steps, not the automaton.
		Warnings:      []string{"device-local revoke always wins (ADR-0020)"},
		PayloadSchema: workgraph.HandoffVersion,
	}
}

// SuspendMissionsForDevice suspends every active mission Work the device has
// mounted (link_mounts, distinct) and returns their IDs in mount order.
// Best-effort per work: one failing Work does not strand the rest — its
// error is collected and returned joined AFTER the remaining works were
// given their chance. A store failure on the mount list itself aborts.
func (s *SQLiteStore) SuspendMissionsForDevice(ctx context.Context, deviceID string) ([]string, error) {
	workIDs, err := (&linkStore{db: s.db}).ListMountWorkIDs(ctx, deviceID)
	if err != nil {
		return nil, fmt.Errorf("revoke cascade: list mounts for %s: %w", deviceID, err)
	}
	suspended := []string{}
	var failures []string
	for _, id := range workIDs {
		w, err := s.GetWork(ctx, id)
		if err != nil {
			if err == ErrNotFound {
				continue // mount for a deleted work: nothing to suspend
			}
			failures = append(failures, fmt.Sprintf("load %s: %v", id, err))
			continue
		}
		// Law filter: missions only, active only. CI works and already
		// stopped works are skipped — device revocation never touches them.
		if !w.IsMission() || !suspendableStates[w.State] {
			continue
		}
		h := revokeCascadeHandoff(deviceID)
		// Frozen forward edges RUNNING/VERIFYING -> SUSPENDED exist, so the
		// canonical journal-owned suspend path handles them. Every other
		// active state needs the kernel-authorized cascade transaction.
		var serr error
		if w.State == workgraph.StateRunning || w.State == workgraph.StateVerifying {
			_, serr = s.SuspendWorkEventful(ctx, id, workgraph.StateSuspended, h)
		} else {
			serr = s.cascadeSuspendWork(ctx, id, h)
		}
		if serr != nil {
			failures = append(failures, fmt.Sprintf("suspend %s: %v", id, serr))
			continue
		}
		suspended = append(suspended, id)
	}
	if len(failures) > 0 {
		return suspended, fmt.Errorf("revoke cascade: %s", strings.Join(failures, "; "))
	}
	return suspended, nil
}

// cascadeSuspendWork is the kernel-authorized hard stop for active states the
// frozen forward table cannot legally route into SUSPENDED (CREATED,
// PLANNING, QUEUED, BLOCKED — ADR-0020: a device-local revoke always wins,
// and the frozen edge list is about normal operational flow, not emergency
// device revocation). It performs the SAME atomic transaction as SuspendWork
// — state change + ADR-0010 handoff in one commit — then journals
// work.suspended (same shape SuspendWorkEventful emits). Idempotent: a
// replay of the identical handoff persists nothing new.
func (s *SQLiteStore) cascadeSuspendWork(ctx context.Context, id string, h *workgraph.Handoff) error {
	if err := workgraph.ValidateHandoff(h); err != nil {
		return fmt.Errorf("invalid handoff: %w", err)
	}
	payload := mustJSON(h)
	hash := handoffHash(payload)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var currentState, missionRaw string
	if err := tx.QueryRowContext(ctx, `SELECT state, COALESCE(mission_json,'') FROM works WHERE id = ?`, id).
		Scan(&currentState, &missionRaw); err != nil {
		if err == sql.ErrNoRows {
			return ErrNotFound
		}
		return err
	}
	// Re-validate under the write lock: missions only, active only. A work
	// that moved since the read is skipped lawfully, not forced.
	w := &workgraph.Work{ID: id, State: workgraph.State(currentState)}
	if missionRaw != "" {
		var m workgraph.MissionContract
		if err := json.Unmarshal([]byte(missionRaw), &m); err != nil {
			return fmt.Errorf("decode mission: %w", err)
		}
		w.Mission = &m
	}
	if !w.IsMission() || !suspendableStates[w.State] {
		return nil // raced into a non-suspendable state: skip, not an error
	}
	// The full mission contract must still hold (freeze law, same check
	// SuspendWork runs before it will touch a Work).
	if err := w.ValidateMissionWork(); err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `UPDATE works SET state = ?, updated_at = ? WHERE id = ?`,
		string(workgraph.StateSuspended), now, id); err != nil {
		return err
	}
	// Idempotent checkpoint: same (work, to_state, payload hash) ⇒ no-op.
	var existingID string
	err = tx.QueryRowContext(ctx,
		`SELECT id FROM work_handoffs WHERE work_id = ? AND to_state = ? AND payload_hash = ?`,
		id, string(workgraph.StateSuspended), hash).Scan(&existingID)
	if err == nil {
		// identical checkpoint already persisted — idempotent success
	} else if err == sql.ErrNoRows {
		if _, err := tx.ExecContext(ctx, `
            INSERT INTO work_handoffs (id, work_id, to_state, payload_hash, payload_json, created_at)
            VALUES (?, ?, ?, ?, ?, ?)`,
			workgraph.NewID("handoff"), id, string(workgraph.StateSuspended), hash, payload, now); err != nil {
			return fmt.Errorf("insert handoff: %w", err)
		}
	} else {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	// Journal event after the durable suspend (same law as the other
	// journal-owned wrappers: never an event for a rolled-back transition).
	return s.journalWorkEvent(ctx, journalEvent{
		ID:     workgraph.NewID("evt"),
		WorkID: id,
		Type:   EventWorkSuspended,
		Data: map[string]any{
			"work_id": id,
			"state":   string(workgraph.StateSuspended),
		},
	})
}
