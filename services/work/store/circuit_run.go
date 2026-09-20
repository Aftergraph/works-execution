package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/JonasAbde/works-execution/packages/circuitrun"
	"github.com/JonasAbde/works-execution/packages/workgraph"
)

var ErrCircuitRunConflict = errors.New("circuit run binding conflict")
var ErrCircuitRunWorkNotMission = errors.New("circuit run requires a mission work")
var ErrCircuitRunSubjectMismatch = errors.New("circuit run exact subject mismatch")

func (s *SQLiteStore) CreateCircuitRun(ctx context.Context, in circuitrun.Input) (*circuitrun.Run, error) {
	if err := in.Validate(); err != nil {
		return nil, fmt.Errorf("circuit run input: %w", err)
	}
	canonical, digest, err := circuitrun.CanonicalizeSpec(in.CircuitSpec)
	if err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var missionJSON string
	if err := tx.QueryRowContext(ctx, `SELECT mission_json FROM works WHERE id = ?`, in.WorkID).Scan(&missionJSON); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if missionJSON == "" {
		return nil, ErrCircuitRunWorkNotMission
	}
	var mission workgraph.MissionContract
	if err := json.Unmarshal([]byte(missionJSON), &mission); err != nil || mission.BudgetCeiling == nil {
		return nil, ErrCircuitRunWorkNotMission
	}

	existing, err := getCircuitRunByWorkIDTx(ctx, tx, in.WorkID)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		if existing.CircuitID == in.CircuitID && existing.CircuitSpecSHA256 == digest && existing.MissionID == in.MissionID {
			if err := tx.Commit(); err != nil {
				return nil, err
			}
			return existing, nil
		}
		return nil, ErrCircuitRunConflict
	}

	run := &circuitrun.Run{
		ID: workgraph.NewID("crun"), CircuitID: in.CircuitID,
		CircuitSpec: canonical, CircuitSpecSHA256: digest,
		WorkID: in.WorkID, MissionID: in.MissionID, CreatedAt: time.Now().UTC(),
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO circuit_runs (
        id, circuit_id, circuit_spec_sha256, circuit_spec_json, work_id, mission_id, created_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		run.ID, run.CircuitID, run.CircuitSpecSHA256, string(run.CircuitSpec), run.WorkID, run.MissionID,
		run.CreatedAt.Format(time.RFC3339Nano))
	if err != nil {
		if isSQLiteConstraint(err) {
			return nil, ErrCircuitRunConflict
		}
		return nil, fmt.Errorf("insert circuit run: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return run, nil
}

func (s *SQLiteStore) GetCircuitRun(ctx context.Context, id string) (*circuitrun.Run, error) {
	return scanCircuitRun(s.db.QueryRowContext(ctx, `SELECT id, circuit_id, circuit_spec_sha256, circuit_spec_json,
        work_id, mission_id, created_at FROM circuit_runs WHERE id = ?`, id))
}

func (s *SQLiteStore) GetCircuitRunByWorkID(ctx context.Context, workID string) (*circuitrun.Run, error) {
	return scanCircuitRun(s.db.QueryRowContext(ctx, `SELECT id, circuit_id, circuit_spec_sha256, circuit_spec_json,
        work_id, mission_id, created_at FROM circuit_runs WHERE work_id = ?`, workID))
}

type rowScanner interface{ Scan(dest ...any) error }

func scanCircuitRun(row rowScanner) (*circuitrun.Run, error) {
	var run circuitrun.Run
	var specJSON, createdAt string
	if err := row.Scan(&run.ID, &run.CircuitID, &run.CircuitSpecSHA256, &specJSON,
		&run.WorkID, &run.MissionID, &createdAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	canonical, digest, err := circuitrun.CanonicalizeSpec(json.RawMessage(specJSON))
	if err != nil || digest != run.CircuitSpecSHA256 {
		return nil, ErrCircuitRunSubjectMismatch
	}
	probe := circuitrun.Input{CircuitID: run.CircuitID, CircuitSpec: canonical, WorkID: run.WorkID, MissionID: run.MissionID}
	if err := probe.Validate(); err != nil {
		return nil, ErrCircuitRunSubjectMismatch
	}
	run.CircuitSpec = canonical
	parsed, err := time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return nil, fmt.Errorf("parse circuit run created_at: %w", err)
	}
	run.CreatedAt = parsed
	return &run, nil
}

func getCircuitRunByWorkIDTx(ctx context.Context, tx *sql.Tx, workID string) (*circuitrun.Run, error) {
	run, err := scanCircuitRun(tx.QueryRowContext(ctx, `SELECT id, circuit_id, circuit_spec_sha256, circuit_spec_json,
        work_id, mission_id, created_at FROM circuit_runs WHERE work_id = ?`, workID))
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	return run, err
}
