package reliability_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JonasAbde/works-execution/internal/worker"
	"github.com/JonasAbde/works-execution/packages/workgraph"
	"github.com/JonasAbde/works-execution/services/api"
	"github.com/JonasAbde/works-execution/services/work/store"
)

// TestControllerLossAfterDurableAcceptDoesNotStopWork proves the continuity
// invariant needed by disposable AI/UI controllers:
//
//	controller lifetime != accepted Work lifetime
//
// The submitting controller disappears immediately after the API durably
// accepts the Work. Only then is a worker started. A fresh controller later
// reconnects and must observe the SAME Work reach SUCCEEDED exactly once.
func TestControllerLossAfterDurableAcceptDoesNotStopWork(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	dbDir := t.TempDir()
	dbPath := filepath.Join(dbDir, "controller-loss.db")
	artDir := filepath.Join(dbDir, "artifacts")
	if err := os.MkdirAll(artDir, 0o755); err != nil {
		t.Fatal(err)
	}

	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	apiSrv := &api.Server{Store: st, ArtifactsDir: artDir}
	ts := httptest.NewServer(apiSrv.Routes())
	defer ts.Close()

	rctx, rcancel := context.WithCancel(ctx)
	defer rcancel()
	go func() {
		_ = api.RunLeaseReaper(rctx, st, api.ReaperConfig{Interval: 250 * time.Millisecond})
	}()

	// Controller A submits and then disappears. No worker exists yet, so the
	// accepted Work cannot have completed before the disconnect.
	transportA := &http.Transport{DisableKeepAlives: true}
	controllerA := &http.Client{Transport: transportA, Timeout: 5 * time.Second}
	body := `{
		"queue": true,
		"source": {"type": "controller-loss-proof", "repository": "aftergraph/reliability"},
		"objective": {"type": "verify_change"},
		"graph": {
			"nodes": {
				"run": {"id": "run", "run": "echo controller-loss-proof-ok"}
			}
		},
		"requirements": {"os": "linux"},
		"policy": {}
	}`

	resp, err := controllerA.Post(ts.URL+"/v1/works", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("controller A submit: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		raw, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		t.Fatalf("controller A submit: status=%d body=%s", resp.StatusCode, string(raw))
	}
	var accepted workgraph.Work
	if err := json.NewDecoder(resp.Body).Decode(&accepted); err != nil {
		_ = resp.Body.Close()
		t.Fatalf("decode accepted Work: %v", err)
	}
	_ = resp.Body.Close()
	transportA.CloseIdleConnections()

	if accepted.ID == "" {
		t.Fatal("durable accept returned empty work id")
	}
	if accepted.State != workgraph.StateQueued {
		t.Fatalf("expected accepted Work to be QUEUED, got %s", accepted.State)
	}

	// The execution worker comes online only after the submitting controller
	// is gone. If controller lifetime owned mission lifetime, this Work could
	// never advance.
	w := &worker.Worker{
		ID:             "wrkr_controller_loss",
		Client:         &worker.Client{BaseURL: ts.URL, HTTP: http.DefaultClient},
		ArtifactsDir:   artDir,
		Logger:         testLogger{t: t},
		PollEvery:      100 * time.Millisecond,
		LeaseTTL:       3 * time.Second,
		HeartbeatEvery: time.Second,
	}
	wctx, wcancel := context.WithCancel(ctx)
	defer wcancel()
	go func() { _ = w.Run(wctx) }()

	// Controller B is a fresh client/session. It reconciles from canonical
	// Work state; it does not resubmit or reconstruct from conversational
	// memory.
	controllerB := &http.Client{Timeout: 5 * time.Second}
	deadline := time.Now().Add(10 * time.Second)
	var final workgraph.Work
	for time.Now().Before(deadline) {
		r, err := controllerB.Get(ts.URL + "/v1/works/" + accepted.ID)
		if err != nil {
			time.Sleep(100 * time.Millisecond)
			continue
		}
		err = json.NewDecoder(r.Body).Decode(&final)
		_ = r.Body.Close()
		if err != nil {
			t.Fatalf("controller B decode: %v", err)
		}
		if final.State.IsTerminal() {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	if final.ID != accepted.ID {
		t.Fatalf("reconnect observed different Work: accepted=%s final=%s", accepted.ID, final.ID)
	}
	if final.State != workgraph.StateSucceeded {
		t.Fatalf("accepted Work did not survive controller loss: state=%s attempts=%d", final.State, len(final.Attempts))
	}
	if len(final.Attempts) != 1 {
		t.Fatalf("controller reconnect must not duplicate execution: attempts=%d", len(final.Attempts))
	}

	logs, err := controllerB.Get(ts.URL + "/v1/works/" + accepted.ID + "/nodes/run/logs")
	if err != nil {
		t.Fatalf("read logs after reconnect: %v", err)
	}
	rawLogs, _ := io.ReadAll(logs.Body)
	_ = logs.Body.Close()
	if logs.StatusCode != http.StatusOK {
		t.Fatalf("logs status=%d body=%s", logs.StatusCode, string(rawLogs))
	}
	if !strings.Contains(string(rawLogs), "controller-loss-proof-ok") {
		t.Fatalf("effect evidence missing after reconnect: %q", string(rawLogs))
	}
}

type testLogger struct{ t *testing.T }

func (l testLogger) Printf(format string, args ...any) { l.t.Logf(format, args...) }
