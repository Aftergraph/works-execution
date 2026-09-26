package api

import (
	"testing"

	"github.com/JonasAbde/works-execution/packages/workgraph"
)

func TestReplayIntentUsesPersistedAdmissionDefaultsSnapshot(t *testing.T) {
	oldDefaults := admissionDefaultsSnapshot{
		TimeoutSeconds:     600,
		RetryMaxAttempts:   2,
		Backoff:            "exponential",
		CacheScope:         "organization",
		DefaultPermissions: []string{"read"},
	}
	futureDefaults := admissionDefaultsSnapshot{
		TimeoutSeconds:     900,
		RetryMaxAttempts:   4,
		Backoff:            "linear",
		CacheScope:         "global",
		DefaultPermissions: []string{"read"},
	}

	acceptedRequest := &workgraph.Work{
		Source:       workgraph.Source{Type: "controller", Repository: "Aftergraph/reliability"},
		Objective:    workgraph.Objective{Type: "verify_change"},
		Requirements: workgraph.Requirements{OS: "linux"},
		Graph: workgraph.Graph{Nodes: map[string]workgraph.Node{
			"run": {ID: "run", Run: "echo proof"},
		}},
	}

	existing := cloneWorkCreationRequest(*acceptedRequest)
	existing.CreationIntentHash = creationIntentHashWithDefaults(acceptedRequest, oldDefaults)
	existing.AdmissionDefaultsJSON = encodeAdmissionDefaults(oldDefaults)
	queue := true
	existing.QueueRequested = &queue

	// A later controller may spell out the defaults that were implicit when
	// the Work was accepted. This must still reconcile against the persisted
	// acceptance snapshot, even if today's defaults have since changed.
	replay := cloneWorkCreationRequest(*acceptedRequest)
	n := replay.Graph.Nodes["run"]
	n.TimeoutS = oldDefaults.TimeoutSeconds
	n.Permissions = []string{"read"}
	n.Retries = &workgraph.RetrySpec{
		MaxAttempts: oldDefaults.RetryMaxAttempts,
		Backoff:     oldDefaults.Backoff,
	}
	n.CacheSpec = &workgraph.CacheSpec{
		Enabled: false,
		Scope:   oldDefaults.CacheScope,
	}
	replay.Graph.Nodes["run"] = n

	if !replayMatchesDurableIntent(&existing, &replay, true) {
		t.Fatal("replay did not use persisted admission defaults from acceptance")
	}
	if creationIntentHashWithDefaults(&replay, futureDefaults) == existing.CreationIntentHash {
		t.Fatal("test invalid: future defaults unexpectedly produce the historical intent hash")
	}
}
