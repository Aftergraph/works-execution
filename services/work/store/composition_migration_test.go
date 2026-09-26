package store

import "testing"

func TestComposedMigrationExecutionContextThenDispatchAcceptance(t *testing.T) {
	st, _ := openBrainStore(t)
	var got int
	if err := st.db.QueryRow(`SELECT version FROM schema_version ORDER BY version DESC LIMIT 1`).Scan(&got); err != nil {
		t.Fatalf("schema_version read: %v", err)
	}
	if got != 14 || SchemaVersion != 14 {
		t.Fatalf("composed schema must end at v14, got ledger=%d const=%d", got, SchemaVersion)
	}
	for _, table := range []string{"work_execution_contexts", "dispatch_acceptances"} {
		var name string
		if err := st.db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&name); err != nil {
			t.Fatalf("required composed table %s missing: %v", table, err)
		}
	}
	for _, col := range []string{"creation_intent_hash", "admission_defaults_json", "queue_requested"} {
		var count int
		if err := st.db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('works') WHERE name=?`, col).Scan(&count); err != nil {
			t.Fatalf("inspect works.%s: %v", col, err)
		}
		if count != 1 {
			t.Fatalf("required v14 column works.%s missing", col)
		}
	}
}
