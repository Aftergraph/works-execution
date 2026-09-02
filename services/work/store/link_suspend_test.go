package store

// k-035 law tests: the revoke cascade suspends exactly the mounted active
// missions — nothing more, nothing less.
//   - QUEUED/RUNNING mission + mount  => SUSPENDED + handoff row + journal event
//   - CI work with mount              => untouched (device-independent)
//   - terminal (SUCCEEDED) mission    => untouched (skipped, not an error)
//   - two mounts of the same work     => one suspension (DISTINCT read)

import (
	"context"
	"testing"

	"github.com/JonasAbde/works-execution/packages/link"
	"github.com/JonasAbde/works-execution/packages/workgraph"
)

// seedMountedWork creates a Work and mounts it for the device via the real
// linkStore, so the cascade reads the same rows the link surface wrote.
func seedMountedWork(t *testing.T, st *SQLiteStore, deviceID, workID string, mission bool, state workgraph.State) {
	t.Helper()
	ctx := context.Background()
	w := &workgraph.Work{
		ID: workID, State: state,
		Source:    workgraph.Source{Type: "cli"},
		Objective: workgraph.Objective{Type: "verify_change"},
		Graph:     workgraph.Graph{Nodes: map[string]workgraph.Node{"a": {ID: "a", Run: "true"}}},
	}
	if mission {
		w.Mission = &workgraph.MissionContract{
			BudgetCeiling: &workgraph.BudgetCeiling{ComputeEUR: 50},
			Verification:  []workgraph.VerificationCriterion{{Criterion: "done"}},
			KillSwitch:    "always",
		}
	}
	if err := st.CreateWork(ctx, w); err != nil {
		t.Fatalf("create work %s: %v", workID, err)
	}
	ls := st.LinkDevices()
	rec := &link.MountRecord{
		ID: "mnt_" + deviceID + "_" + workID, DeviceID: deviceID, WorkID: workID,
		PayloadHash: hash64(), Scope: link.ScopeT1Read, PurposeBinding: workID,
		CreatedAt: mustNow(t),
	}
	if _, err := ls.InsertMount(ctx, rec); err != nil {
		t.Fatalf("insert mount %s: %v", rec.ID, err)
	}
}

func TestSuspendMissionsForDevice(t *testing.T) {
	cases := []struct {
		name        string
		workID      string
		mission     bool
		state       workgraph.State
		wantSuspend bool
	}{
		{"mission QUEUED mounts and suspends", "wrk_mq", true, workgraph.StateQueued, true},
		{"mission RUNNING mounts and suspends", "wrk_mr", true, workgraph.StateRunning, true},
		{"CI work never touched", "wrk_ci", false, workgraph.StateQueued, false},
		{"terminal SUCCEEDED mission untouched", "wrk_ms", true, workgraph.StateSucceeded, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st, _ := openLinkStore(t)
			ctx := context.Background()
			seedMountedWork(t, st, "dev_cascade", tc.workID, tc.mission, tc.state)

			suspended, err := st.SuspendMissionsForDevice(ctx, "dev_cascade")
			if err != nil {
				t.Fatalf("SuspendMissionsForDevice: %v", err)
			}
			if tc.wantSuspend {
				if len(suspended) != 1 || suspended[0] != tc.workID {
					t.Fatalf("suspended = %v, want [%s]", suspended, tc.workID)
				}
				// State is SUSPENDED.
				w, err := st.GetWork(ctx, tc.workID)
				if err != nil {
					t.Fatal(err)
				}
				if w.State != workgraph.StateSuspended {
					t.Fatalf("work state = %s, want SUSPENDED", w.State)
				}
				// Durable ADR-0010 handoff row exists.
				var handoffs int
				if err := st.db.QueryRow(`SELECT count(*) FROM work_handoffs WHERE work_id = ? AND to_state = 'SUSPENDED'`, tc.workID).Scan(&handoffs); err != nil {
					t.Fatal(err)
				}
				if handoffs != 1 {
					t.Fatalf("handoff rows = %d, want exactly 1 (a suspend without a handoff cannot happen)", handoffs)
				}
				var narrative string
				if err := st.db.QueryRow(`SELECT payload_json->>'$.narrative' FROM work_handoffs WHERE work_id = ?`, tc.workID).Scan(&narrative); err != nil {
					t.Fatal(err)
				}
				if narrative != "WORKS-Link device dev_cascade revoked — mission suspended for human review" {
					t.Fatalf("handoff narrative = %q", narrative)
				}
				// Journal event emitted (work.suspended via SuspendWorkEventful).
				var events int
				if err := st.db.QueryRow(`SELECT count(*) FROM work_events WHERE work_id = ? AND type = ?`, tc.workID, EventWorkSuspended).Scan(&events); err != nil {
					t.Fatal(err)
				}
				if events != 1 {
					t.Fatalf("work_events work.suspended rows = %d, want 1", events)
				}
			} else {
				// Skipped, not errors: empty list, work state unchanged.
				if len(suspended) != 0 {
					t.Fatalf("suspended = %v, want empty for %s", suspended, tc.state)
				}
				w, err := st.GetWork(ctx, tc.workID)
				if err != nil {
					t.Fatal(err)
				}
				if w.State != tc.state {
					t.Fatalf("work %s was touched: state = %s, want %s", tc.workID, w.State, tc.state)
				}
			}
		})
	}
}

func TestSuspendMissionsDedupesTwoMounts(t *testing.T) {
	st, _ := openLinkStore(t)
	ctx := context.Background()
	// Two DISTINCT mounts (different payload hashes) of the same work by the
	// same device: the cascade must suspend once, list it once.
	seedMountedWork(t, st, "dev_dup", "wrk_dup", true, workgraph.StateRunning)
	ls := st.LinkDevices()
	rec2 := &link.MountRecord{
		ID: "mnt_dev_dup_wrk_dup_2", DeviceID: "dev_dup", WorkID: "wrk_dup",
		PayloadHash: hash64() + "x", Scope: link.ScopeT2Action, PurposeBinding: "wrk_dup",
		CreatedAt: mustNow(t),
	}
	if _, err := ls.InsertMount(ctx, rec2); err != nil {
		t.Fatal(err)
	}
	suspended, err := st.SuspendMissionsForDevice(ctx, "dev_dup")
	if err != nil {
		t.Fatal(err)
	}
	if len(suspended) != 1 || suspended[0] != "wrk_dup" {
		t.Fatalf("suspended = %v, want exactly [wrk_dup] (dedupe law)", suspended)
	}
	var events int
	if err := st.db.QueryRow(`SELECT count(*) FROM work_events WHERE work_id = 'wrk_dup' AND type = ?`, EventWorkSuspended).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 1 {
		t.Fatalf("journal emitted %d suspend events for one suspension, want 1", events)
	}
}

func TestSuspendMissionsUnknownDeviceEmpty(t *testing.T) {
	st, _ := openLinkStore(t)
	suspended, err := st.SuspendMissionsForDevice(context.Background(), "dev_ghost")
	if err != nil {
		t.Fatalf("unknown device must be empty, not an error: %v", err)
	}
	if len(suspended) != 0 {
		t.Fatalf("suspended = %v, want empty", suspended)
	}
}
