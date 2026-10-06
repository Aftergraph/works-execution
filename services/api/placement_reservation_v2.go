package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/JonasAbde/works-execution/packages/workgraph"
	"github.com/JonasAbde/works-execution/services/runner"
	"github.com/JonasAbde/works-execution/services/work/store"
)

// placementReservationV2 is the narrow Runtime -> WORKS seam that closes the
// placement/lease gap without moving lease ownership into Runtime.
//
// Runtime supplies a placement-bound selected worker. WORKS revalidates that
// worker against its canonical runner/liveness/capability/policy truth and
// mints the WorkerLease + Attempt itself.
type placementReservationV2 struct {
	Schema           string                    `json:"schema"`
	NodeID           string                    `json:"node_id"`
	SelectedWorker   string                    `json:"selected_worker"`
	TTLSeconds       int                       `json:"ttl_seconds,omitempty"`
	PlacementBinding *dispatchPlacementBinding `json:"placement_binding"`
}

type placementReservationV2Response struct {
	Schema           string                    `json:"schema"`
	WorkID           string                    `json:"work_id"`
	WorkerID         string                    `json:"worker_id"`
	WorkerLeaseID    string                    `json:"worker_lease_id"`
	AttemptID        string                    `json:"attempt_id"`
	PlacementBinding *dispatchPlacementBinding `json:"placement_binding"`
}

func (s *Server) reservePlacementV2(w http.ResponseWriter, r *http.Request) {
	workID := strings.TrimSpace(r.PathValue("id"))
	if workID == "" {
		writeError(w, http.StatusBadRequest, "missing_id", "work id required")
		return
	}
	if !s.authorizeDispatchV2Platform(w, r) {
		return
	}

	var req placementReservationV2
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	req.NodeID = strings.TrimSpace(req.NodeID)
	req.SelectedWorker = strings.TrimSpace(req.SelectedWorker)
	if req.Schema != "placement.reservation/1.0" || req.NodeID == "" || req.SelectedWorker == "" || req.PlacementBinding == nil {
		writeError(w, http.StatusBadRequest, "placement_reservation_invalid", "schema, node_id, selected_worker and placement_binding are required")
		return
	}
	pb := req.PlacementBinding
	if pb.Schema != "runtime.placement-dispatch-binding/0.1" ||
		strings.TrimSpace(pb.MissionID) == "" ||
		strings.TrimSpace(pb.PlacementDecisionDigest) == "" ||
		strings.TrimSpace(pb.SnapshotDigest) == "" ||
		strings.TrimSpace(pb.PolicyVersion) == "" ||
		pb.SelectedNode != req.SelectedWorker {
		writeError(w, http.StatusConflict, "placement_binding_mismatch", "selected worker must exactly match the Runtime placement binding")
		return
	}
	if req.TTLSeconds <= 0 {
		req.TTLSeconds = 25
	}
	if req.TTLSeconds > 300 {
		writeError(w, http.StatusBadRequest, "placement_reservation_invalid", "ttl_seconds exceeds 300")
		return
	}

	work, err := s.Store.GetWork(r.Context(), workID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "work_not_found", workID)
			return
		}
		writeError(w, http.StatusInternalServerError, "get_work_failed", "failed to load work")
		return
	}

	// Platform reservation never trusts Runtime's view of availability.
	// WORKS re-checks the canonical runner registry and heartbeat at mutation time.
	if s.RunnerRegistry == nil {
		writeError(w, http.StatusConflict, "selected_worker_unavailable", "runner registry unavailable")
		return
	}
	identity, ok := s.RunnerRegistry.get(req.SelectedWorker)
	if !ok || !runnerHasFreshHeartbeat(identity, time.Now()) {
		writeError(w, http.StatusConflict, "selected_worker_unavailable", "selected worker is not active with a fresh heartbeat")
		return
	}
	if work.Requirements.Pool != "" && !s.runnerIsLivePoolMember(req.SelectedWorker, work.Requirements.Pool, time.Now()) {
		writeError(w, http.StatusConflict, "selected_worker_pool_mismatch", "selected worker is not a live member of the required pool")
		return
	}
	if eligible, reason := s.poolRunnerMeetsWorkRequirements(r.Context(), req.SelectedWorker, work, req.NodeID); !eligible {
		writeError(w, http.StatusConflict, "selected_worker_not_eligible", reason)
		return
	}

	// Preserve the existing lease-grant policy boundary. This is not worker
	// authentication; platform bridge auth already happened above. Policy still
	// sees the concrete worker and work before the lease mutation.
	if s.Policy != nil {
		evidence := work.Evidence
		if evidence == nil {
			evidence = []workgraph.Evidence{}
		}
		runnerView := RunnerView{
			RunnerID:       req.SelectedWorker,
			TrustClass:     runner.TrustUntrusted,
			LifecycleState: runner.StateActive,
		}
		if identity != nil {
			runnerView.TrustClass = identity.TrustClass
			runnerView.LifecycleState = identity.LifecycleState
		}
		input := DecisionInput{
			Request: RequestContext{
				Action:   "lease.grant",
				WorkID:   workID,
				NodeID:   req.NodeID,
				WorkerID: req.SelectedWorker,
			},
			Work: WorkView{ID: work.ID, Policy: work.Policy, State: work.State},
			Evidence: evidence,
			Runner:   runnerView,
		}
		if decision, perr := s.Policy.EvaluateOrError(r.Context(), input); perr != nil {
			writeError(w, http.StatusForbidden, formatDenyReason(firstReason(decision.DenyReasons)), "policy denied placement reservation")
			return
		}
	}

	lease, attempt, err := s.Store.GrantLease(
		r.Context(),
		workID,
		req.NodeID,
		req.SelectedWorker,
		time.Duration(req.TTLSeconds)*time.Second,
	)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrNotFound):
			writeError(w, http.StatusNotFound, "work_not_found", workID)
		case errors.Is(err, store.ErrLeaseConflict):
			writeError(w, http.StatusConflict, "lease_conflict", "node already leased")
		default:
			s.logf("placement reservation: %v", err)
			writeError(w, http.StatusInternalServerError, "placement_reservation_failed", "failed to reserve selected worker")
		}
		return
	}

	writeJSON(w, http.StatusCreated, placementReservationV2Response{
		Schema:           "placement.reservation/1.0",
		WorkID:           workID,
		WorkerID:         lease.WorkerID,
		WorkerLeaseID:    lease.ID,
		AttemptID:        attempt.ID,
		PlacementBinding: req.PlacementBinding,
	})
}
