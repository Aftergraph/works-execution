package api

// k-038: internal render probe — captures the exact HTML the journal
// section produces (including escaped hostile payloads) into the test
// log as regression evidence.

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/JonasAbde/works-execution/packages/workgraph"
	"github.com/JonasAbde/works-execution/services/work/store"
)

func TestWebUIJournalRenderedHTMLEvidence(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "probe.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()

	w := &workgraph.Work{
		ID:        "wrk_probe01",
		Objective: workgraph.Objective{Type: "verify_change"},
		Graph:     workgraph.Graph{Nodes: map[string]workgraph.Node{"a": {ID: "a", Run: "echo a"}}},
	}
	if err := s.CreateWorkEventful(ctx, w); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateStateEventful(ctx, w.ID, workgraph.StateQueued); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateStateEventful(ctx, w.ID, workgraph.StateRunning); err != nil {
		t.Fatal(err)
	}
	// Hostile row via the durable journal API (ids are server-minted, so
	// the payload is the injection surface).
	if _, err := s.AppendWorkEvent(ctx, store.WorkEvent{
		ID: "evt_hostile", WorkID: w.ID, Type: "work.state.changed",
		Data: []byte(`{"work_id":"wrk_probe01","from":"<script>alert(1)</script>","state":"<img src=x onerror=alert(2)>"}`),
	}); err != nil {
		t.Fatal(err)
	}

	srv := &Server{Store: s}
	t.Logf("rendered journal section:\n%s", srv.renderJournal(ctx, w.ID))
}
