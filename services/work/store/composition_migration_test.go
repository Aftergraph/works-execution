package store

import "testing"

func TestComposedMigrationExecutionContextThenDispatchAcceptance(t *testing.T) {
	st, _ := openBrainStore(t)
	var got int
	if err := st.db.QueryRow(`SELECT version FROM schema_version ORDER BY version DESC LIMIT 1`).Scan(&got); err != nil {
		t.Fatalf("schema_version read: %v", err)
	}
	if got != 15 || SchemaVersion != 15 {
		t.Fatalf("composed schema must end at v15, got ledger=%d const=%d", got, SchemaVersion)
	}
	for _, table := range []string{"work_execution_contexts", "dispatch_acceptances", "circuit_runs", "circuit_verdicts"} {
		var name string
		if err := st.db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&name); err != nil {
			t.Fatalf("required composed table %s missing: %v", table, err)
		}
	}
}
