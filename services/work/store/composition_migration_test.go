package store

import "testing"

func TestComposedMigrationExecutionContextDispatchAcceptanceAndAuthorityBinding(t *testing.T) {
	st, _ := openBrainStore(t)
	var got int
	if err := st.db.QueryRow(`SELECT version FROM schema_version ORDER BY version DESC LIMIT 1`).Scan(&got); err != nil {
		t.Fatalf("schema_version read: %v", err)
	}
	if got != 14 || SchemaVersion != 14 {
		t.Fatalf("composed schema must end at v14, got ledger=%d const=%d", got, SchemaVersion)
	}
	for _, table := range []string{"work_execution_contexts", "dispatch_acceptances", "dispatch_authority_bindings"} {
		var name string
		if err := st.db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&name); err != nil {
			t.Fatalf("required composed table %s missing: %v", table, err)
		}
	}
}
