package publisher_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/JonasAbde/works-execution/services/publisher"
)

const lifecycleSHA = "8e5fac2a86f39b965539ec580b08ceb331830c49"
const otherLifecycleSHA = "efc88fc353a40000000000000000000000000000"

func lifecycleEvent(state publisher.LifecycleState, attempt int, observed time.Time) publisher.StatusEvent {
	return publisher.StatusEvent{
		WorkID: "wrk_truth_01",
		Repository: "Aftergraph/works-execution",
		SHA: lifecycleSHA,
		Attempt: attempt,
		State: state,
		DetailsURL: "https://works.example.test/v1/works/wrk_truth_01",
		ObservedAt: observed,
	}
}

func TestLifecycleEventValidate(t *testing.T) {
	base := lifecycleEvent(publisher.LifecycleQueued, 1, time.Unix(100, 0).UTC())
	cases := []struct {
		name string
		mutate func(*publisher.StatusEvent)
		want string
	}{
		{"valid", func(*publisher.StatusEvent) {}, ""},
		{"missing work", func(e *publisher.StatusEvent) { e.WorkID = "" }, "WorkID"},
		{"bad repo", func(e *publisher.StatusEvent) { e.Repository = "works" }, "Repository"},
		{"bad sha", func(e *publisher.StatusEvent) { e.SHA = "short" }, "SHA"},
		{"bad attempt", func(e *publisher.StatusEvent) { e.Attempt = 0 }, "Attempt"},
		{"bad state", func(e *publisher.StatusEvent) { e.State = "maybe" }, "State"},
		{"missing timestamp", func(e *publisher.StatusEvent) { e.ObservedAt = time.Time{} }, "ObservedAt"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := base
			tc.mutate(&e)
			err := e.Validate()
			if tc.want == "" {
				if err != nil { t.Fatalf("Validate: %v", err) }
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestGitHubStatusState(t *testing.T) {
	cases := map[publisher.LifecycleState]publisher.Conclusion{
		publisher.LifecycleQueued: publisher.ConclusionPending,
		publisher.LifecycleRunning: publisher.ConclusionPending,
		publisher.LifecycleSuccess: publisher.ConclusionSuccess,
		publisher.LifecycleFailure: publisher.ConclusionFailure,
		publisher.LifecycleCancelled: publisher.ConclusionFailure,
	}
	for state, want := range cases {
		if got := publisher.GitHubStatusState(state); got != want {
			t.Errorf("%s maps to %q, want %q", state, got, want)
		}
	}
	if got := publisher.GitHubStatusState("unknown"); got != "" {
		t.Fatalf("unknown maps to %q", got)
	}
}

func TestReconcileFirstQueued(t *testing.T) {
	decision, err := publisher.ReconcileStatus(nil, lifecycleEvent(publisher.LifecycleQueued, 1, time.Unix(100, 0).UTC()), lifecycleSHA)
	if err != nil || decision.Action != publisher.ReconcileApply {
		t.Fatalf("decision=%+v err=%v", decision, err)
	}
}

func TestReconcileLifecycleForwardTransitions(t *testing.T) {
	at := time.Unix(100, 0).UTC()
	states := []publisher.LifecycleState{publisher.LifecycleQueued, publisher.LifecycleRunning, publisher.LifecycleSuccess}
	var current *publisher.StatusEvent
	for _, state := range states {
		e := lifecycleEvent(state, 1, at)
		decision, err := publisher.ReconcileStatus(current, e, lifecycleSHA)
		if err != nil || decision.Action != publisher.ReconcileApply {
			t.Fatalf("state=%s decision=%+v err=%v", state, decision, err)
		}
		current = &e
		at = at.Add(time.Second)
	}
}

func TestReconcileFailureAndCancellationAreTerminal(t *testing.T) {
	for _, terminal := range []publisher.LifecycleState{publisher.LifecycleFailure, publisher.LifecycleCancelled} {
		queued := lifecycleEvent(publisher.LifecycleQueued, 1, time.Unix(100, 0).UTC())
		running := lifecycleEvent(publisher.LifecycleRunning, 1, time.Unix(101, 0).UTC())
		candidate := lifecycleEvent(terminal, 1, time.Unix(102, 0).UTC())
		if d, err := publisher.ReconcileStatus(&queued, running, lifecycleSHA); err != nil || d.Action != publisher.ReconcileApply { t.Fatalf("running: %+v %v", d, err) }
		if d, err := publisher.ReconcileStatus(&running, candidate, lifecycleSHA); err != nil || d.Action != publisher.ReconcileApply { t.Fatalf("%s: %+v %v", terminal, d, err) }
		later := lifecycleEvent(publisher.LifecycleRunning, 1, time.Unix(103, 0).UTC())
		if _, err := publisher.ReconcileStatus(&candidate, later, lifecycleSHA); err == nil { t.Fatalf("%s accepted backward transition", terminal) }
	}
}

func TestReconcileDuplicate(t *testing.T) {
	e := lifecycleEvent(publisher.LifecycleRunning, 1, time.Unix(101, 0).UTC())
	d, err := publisher.ReconcileStatus(&e, e, lifecycleSHA)
	if err != nil || d.Action != publisher.ReconcileDuplicate { t.Fatalf("decision=%+v err=%v", d, err) }
}

func TestReconcileOldSHAIsStale(t *testing.T) {
	e := lifecycleEvent(publisher.LifecycleRunning, 1, time.Unix(101, 0).UTC())
	e.SHA = otherLifecycleSHA
	d, err := publisher.ReconcileStatus(nil, e, lifecycleSHA)
	if err != nil || d.Action != publisher.ReconcileStale { t.Fatalf("decision=%+v err=%v", d, err) }
}

func TestReconcileOlderAttemptOrTimestampIsStale(t *testing.T) {
	current := lifecycleEvent(publisher.LifecycleRunning, 2, time.Unix(200, 0).UTC())
	olderAttempt := lifecycleEvent(publisher.LifecycleQueued, 1, time.Unix(201, 0).UTC())
	if d, err := publisher.ReconcileStatus(&current, olderAttempt, lifecycleSHA); err != nil || d.Action != publisher.ReconcileStale { t.Fatalf("attempt: %+v %v", d, err) }
	olderTime := lifecycleEvent(publisher.LifecycleRunning, 2, time.Unix(199, 0).UTC())
	if d, err := publisher.ReconcileStatus(&current, olderTime, lifecycleSHA); err != nil || d.Action != publisher.ReconcileStale { t.Fatalf("time: %+v %v", d, err) }
}

func TestReconcileRetryMustStartQueued(t *testing.T) {
	current := lifecycleEvent(publisher.LifecycleFailure, 1, time.Unix(200, 0).UTC())
	candidate := lifecycleEvent(publisher.LifecycleRunning, 2, time.Unix(201, 0).UTC())
	if _, err := publisher.ReconcileStatus(&current, candidate, lifecycleSHA); err == nil { t.Fatal("retry did not require queued") }
	candidate.State = publisher.LifecycleQueued
	if d, err := publisher.ReconcileStatus(&current, candidate, lifecycleSHA); err != nil || d.Action != publisher.ReconcileApply { t.Fatalf("retry: %+v %v", d, err) }
}

func TestReconcileCannotChangeIdentity(t *testing.T) {
	current := lifecycleEvent(publisher.LifecycleRunning, 1, time.Unix(100, 0).UTC())
	candidate := lifecycleEvent(publisher.LifecycleSuccess, 1, time.Unix(101, 0).UTC())
	candidate.WorkID = "wrk_other"
	if _, err := publisher.ReconcileStatus(&current, candidate, lifecycleSHA); err == nil { t.Fatal("identity change accepted") }
}

func TestPublishLifecycleMapsStateAndPreservesIdentity(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil { t.Fatal(err) }
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()
	p, err := publisher.NewStatusAPIPublisher("token")
	if err != nil { t.Fatal(err) }
	p.BaseURL = srv.URL
	e := lifecycleEvent(publisher.LifecycleCancelled, 3, time.Unix(100, 0).UTC())
	if err := p.PublishLifecycle(context.Background(), e); err != nil { t.Fatal(err) }
	if body["state"] != "failure" { t.Errorf("state=%v", body["state"]) }
	description, _ := body["description"].(string)
	if !strings.Contains(description, "wrk_truth_01") || !strings.Contains(description, "attempt=3") || !strings.Contains(description, "state=cancelled") {
		t.Errorf("description=%q", description)
	}
}
