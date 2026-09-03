package api

import (
	"testing"

	"github.com/JonasAbde/works-execution/packages/abi"
	"github.com/JonasAbde/works-execution/services/runner"
)

// TestRunnerRegistryABIMethods exercises the registry RAB leg against the
// production constructor (newRunnerRegistry) directly: fail-closed store,
// copy-out semantics, and deterministic listABI — the same mutex/lock
// discipline as get/put.
func TestRunnerRegistryABIMethods(t *testing.T) {
	reg := newRunnerRegistry()
	reg.put(&runner.Identity{RunnerID: "wrkr_in_1"})
	reg.put(&runner.Identity{RunnerID: "wrkr_in_2"})

	// getABI before any post: miss.
	if _, ok := reg.getABI("wrkr_in_1"); ok {
		t.Fatal("getABI before putABI must miss")
	}

	// Fail-closed: an illegal RAB (control without token) is never stored.
	bad := &abi.RAB{Abi: abi.AbiVersion, Caps: []string{"control"}}
	if err := reg.putABI("wrkr_in_1", bad); err == nil {
		t.Fatal("putABI must reject control-without-token")
	}
	if _, ok := reg.getABI("wrkr_in_1"); ok {
		t.Fatal("illegal RAB must not be stored")
	}
	// Wrong ABI version likewise.
	if err := reg.putABI("wrkr_in_1", &abi.RAB{Abi: "rab/9.9"}); err == nil {
		t.Fatal("putABI must reject unknown abi version")
	}
	// Nil advertisement is a programming error, not a cap set.
	if err := reg.putABI("wrkr_in_1", nil); err == nil {
		t.Fatal("putABI must reject nil")
	}

	// Legal advertisement round-trips; getABI hands out a copy.
	tr := true
	good := &abi.RAB{Abi: abi.AbiVersion, Caps: []string{"observe", "control"}, ControlTokenRequired: &tr}
	if err := reg.putABI("wrkr_in_1", good); err != nil {
		t.Fatalf("putABI legal: %v", err)
	}
	got, ok := reg.getABI("wrkr_in_1")
	if !ok {
		t.Fatal("getABI after putABI: miss")
	}
	if !got.Has("control") || !got.RequiresControlToken() {
		t.Errorf("stored RAB lost control law state: %+v", got)
	}
	got.Caps[0] = "record" // mutate the copy
	again, _ := reg.getABI("wrkr_in_1")
	if again.Caps[0] != "observe" {
		t.Errorf("getABI must copy out: caller mutation leaked (%v)", again.Caps)
	}

	// Overwrite semantics: second put replaces the first.
	if err := reg.putABI("wrkr_in_1", &abi.RAB{Abi: abi.AbiVersion, Caps: []string{"screenshot"}}); err != nil {
		t.Fatalf("putABI overwrite: %v", err)
	}
	over, _ := reg.getABI("wrkr_in_1")
	if len(over.Caps) != 1 || over.Caps[0] != "screenshot" {
		t.Errorf("overwrite failed: %v", over.Caps)
	}

	// listABI: only runners with advertisements, sorted deterministically.
	if err := reg.putABI("wrkr_in_2", &abi.RAB{Abi: abi.AbiVersion, Caps: []string{"record"}}); err != nil {
		t.Fatalf("putABI wrkr_in_2: %v", err)
	}
	all := reg.listABI()
	if len(all) != 2 {
		t.Fatalf("listABI: got %d records, want 2", len(all))
	}
	if all[0].RunnerID != "wrkr_in_1" || all[1].RunnerID != "wrkr_in_2" {
		t.Errorf("listABI not sorted: %s, %s", all[0].RunnerID, all[1].RunnerID)
	}
	if all[0].RegisteredAt.IsZero() {
		t.Error("listABI record missing server-stamped RegisteredAt")
	}
}
