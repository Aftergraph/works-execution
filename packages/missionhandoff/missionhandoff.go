// Package missionhandoff compiles durable coordinator handoffs into WORKS Work objects.
package missionhandoff

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/JonasAbde/works-execution/packages/workgraph"
)

// Config is the operator-facing durable mission document.
type Config struct {
	Version         int                    `yaml:"version" json:"version"`
	MissionID       string                 `yaml:"mission_id" json:"mission_id"`
	Objective       string                 `yaml:"objective" json:"objective"`
	PurposeBindings []string               `yaml:"purpose_bindings" json:"purpose_bindings"`
	Budget          BudgetConfig           `yaml:"budget" json:"budget"`
	Verification    []VerificationConfig   `yaml:"verification" json:"verification"`
	Requirements    RequirementsConfig     `yaml:"requirements" json:"requirements"`
	Stages          map[string]StageConfig `yaml:"stages" json:"stages"`
}

type BudgetConfig struct {
	ComputeEUR float64 `yaml:"compute_eur" json:"compute_eur"`
	WallClockH float64 `yaml:"wall_clock_h" json:"wall_clock_h"`
}

type VerificationConfig struct {
	Criterion string `yaml:"criterion" json:"criterion"`
	Kind      string `yaml:"kind" json:"kind,omitempty"`
}

type RequirementsConfig struct {
	OS         string `yaml:"os" json:"os,omitempty"`
	Arch       string `yaml:"arch" json:"arch,omitempty"`
	Confidence string `yaml:"confidence" json:"confidence,omitempty"`
	Pool       string `yaml:"pool" json:"pool,omitempty"`
}

type StageConfig struct {
	Run         string            `yaml:"run" json:"run"`
	Reconcile   string            `yaml:"reconcile" json:"reconcile,omitempty"`
	Needs       []string          `yaml:"needs" json:"needs,omitempty"`
	Env         map[string]string `yaml:"env" json:"env,omitempty"`
	Permissions []string          `yaml:"permissions" json:"permissions,omitempty"`
	SideEffects []string          `yaml:"side_effects" json:"side_effects,omitempty"`
	TimeoutS    int               `yaml:"timeout_s" json:"timeout_s,omitempty"`
}

// Parse decodes one mission document. Unknown YAML fields are rejected so
// misspelled authority or recovery controls cannot silently disappear.
func Parse(raw []byte) (Config, error) {
	var cfg Config
	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("missionhandoff: decode: %w", err)
	}
	return cfg, nil
}

// Compile turns an operator mission into the existing durable Work contract.
func Compile(cfg Config) (*workgraph.Work, error) {
	if err := validateConfig(cfg); err != nil {
		return nil, err
	}

	fingerprint, err := specFingerprint(cfg)
	if err != nil {
		return nil, err
	}
	criteria := make([]workgraph.VerificationCriterion, 0, len(cfg.Verification))
	for _, v := range cfg.Verification {
		criteria = append(criteria, workgraph.VerificationCriterion{Criterion: v.Criterion, Kind: v.Kind})
	}

	nodes := make(map[string]workgraph.Node, len(cfg.Stages))
	names := make([]string, 0, len(cfg.Stages))
	for name := range cfg.Stages {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		s := cfg.Stages[name]
		run := s.Run
		if strings.TrimSpace(s.Reconcile) != "" {
			run = reconcileWrapper(s.Reconcile, s.Run)
		}
		nodes[name] = workgraph.Node{
			ID:          name,
			Run:         run,
			Needs:       append([]string(nil), s.Needs...),
			Env:         cloneEnv(s.Env),
			Permissions: append([]string(nil), s.Permissions...),
			SideEffects: append([]string(nil), s.SideEffects...),
			TimeoutS:    s.TimeoutS,
		}
	}

	w := &workgraph.Work{
		ID:             stableWorkID(cfg.MissionID),
		State:          workgraph.StateCreated,
		IdempotencyKey: cfg.MissionID,
		CorrelationID:  cfg.MissionID,
		Source:         workgraph.Source{Type: "cli", Revision: "durable-mission/1"},
		Objective: workgraph.Objective{
			Type:        "achieve_outcome",
			Description: cfg.Objective,
			Constraints: map[string]any{"mission_spec_sha256": fingerprint},
		},
		Graph: workgraph.Graph{Nodes: nodes},
		Requirements: workgraph.Requirements{
			OS: cfg.Requirements.OS, Arch: cfg.Requirements.Arch,
			Confidence: cfg.Requirements.Confidence, Pool: cfg.Requirements.Pool,
		},
		Mission: &workgraph.MissionContract{
			BudgetCeiling:   &workgraph.BudgetCeiling{ComputeEUR: cfg.Budget.ComputeEUR, WallClockH: cfg.Budget.WallClockH},
			Verification:    criteria,
			PurposeBindings: append([]string(nil), cfg.PurposeBindings...),
			KillSwitch:      "always",
		},
	}
	if err := w.Validate(); err != nil {
		return nil, fmt.Errorf("missionhandoff: work validation: %w", err)
	}
	if err := w.ValidateMissionWork(); err != nil {
		return nil, fmt.Errorf("missionhandoff: mission validation: %w", err)
	}
	return w, nil
}

func validateConfig(cfg Config) error {
	if cfg.Version != 1 {
		return errors.New("missionhandoff: version must be 1")
	}
	if strings.TrimSpace(cfg.MissionID) == "" {
		return errors.New("missionhandoff: mission_id is required")
	}
	if len(cfg.MissionID) > 256 {
		return errors.New("missionhandoff: mission_id exceeds 256 bytes")
	}
	if strings.TrimSpace(cfg.Objective) == "" {
		return errors.New("missionhandoff: objective is required")
	}
	if len(cfg.PurposeBindings) == 0 {
		return errors.New("missionhandoff: purpose_bindings must not be empty")
	}
	if cfg.Budget.ComputeEUR <= 0 && cfg.Budget.WallClockH <= 0 {
		return errors.New("missionhandoff: budget must set compute_eur or wall_clock_h")
	}
	if cfg.Budget.ComputeEUR < 0 || cfg.Budget.WallClockH < 0 {
		return errors.New("missionhandoff: budget values must be non-negative")
	}
	if len(cfg.Verification) == 0 {
		return errors.New("missionhandoff: verification must not be empty")
	}
	for i, v := range cfg.Verification {
		if strings.TrimSpace(v.Criterion) == "" {
			return fmt.Errorf("missionhandoff: verification[%d].criterion is required", i)
		}
		if v.Kind != "" && v.Kind != "deterministic" && v.Kind != "human_review" {
			return fmt.Errorf("missionhandoff: verification[%d].kind %q unsupported", i, v.Kind)
		}
	}
	if len(cfg.Stages) == 0 {
		return errors.New("missionhandoff: stages must not be empty")
	}
	for name, s := range cfg.Stages {
		if strings.TrimSpace(name) == "" || strings.TrimSpace(s.Run) == "" {
			return fmt.Errorf("missionhandoff: stage %q requires run", name)
		}
		if len(s.SideEffects) > 0 && strings.TrimSpace(s.Reconcile) == "" {
			return fmt.Errorf("missionhandoff: consequential stage %q requires read-only reconcile", name)
		}
		for key, value := range s.Env {
			if secretLikeKey(key) && value != "" && !strings.HasPrefix(value, "secret://") {
				return fmt.Errorf("missionhandoff: stage %q env %q must use secret:// reference", name, key)
			}
		}
	}
	if err := validateAcyclic(cfg.Stages); err != nil {
		return err
	}
	return nil
}

func validateAcyclic(stages map[string]StageConfig) error {
	const (
		unseen = iota
		visiting
		done
	)
	state := make(map[string]int, len(stages))
	var visit func(string) error
	visit = func(name string) error {
		switch state[name] {
		case visiting:
			return fmt.Errorf("missionhandoff: dependency cycle includes stage %q", name)
		case done:
			return nil
		}
		stage, ok := stages[name]
		if !ok {
			return fmt.Errorf("missionhandoff: stage dependency %q is not declared", name)
		}
		state[name] = visiting
		for _, dep := range stage.Needs {
			if _, ok := stages[dep]; !ok {
				return fmt.Errorf("missionhandoff: stage %q needs undeclared stage %q", name, dep)
			}
			if err := visit(dep); err != nil {
				return err
			}
		}
		state[name] = done
		return nil
	}
	for name := range stages {
		if err := visit(name); err != nil {
			return err
		}
	}
	return nil
}

func stableWorkID(missionID string) string {
	sum := sha256.Sum256([]byte(missionID))
	return "wrk_" + hex.EncodeToString(sum[:16])
}

func specFingerprint(cfg Config) (string, error) {
	raw, err := json.Marshal(cfg)
	if err != nil {
		return "", fmt.Errorf("missionhandoff: fingerprint: %w", err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func cloneEnv(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func secretLikeKey(k string) bool {
	u := strings.ToUpper(strings.TrimSpace(k))
	for _, exact := range []string{"TOKEN", "SECRET", "PASSWORD", "API_KEY", "PRIVATE_KEY", "CREDENTIAL"} {
		if u == exact || strings.HasSuffix(u, "_"+exact) {
			return true
		}
	}
	return false
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}

// reconcileWrapper implements observe-before-mutate recovery semantics.
// Exit 0 means state already exists and mutation is skipped; exit 1 means
// proven absent and run may execute; every other code is indeterminate and
// fails closed before the consequential command is replayed.
func reconcileWrapper(reconcile, run string) string {
	return strings.Join([]string{
		"set +e",
		"sh -c " + shellQuote(reconcile),
		"rc=$?",
		"set -e",
		"case \"$rc\" in",
		"  0) printf '%s\\n' 'missionhandoff: reconciled; mutation skipped'; exit 0 ;;",
		"  1) exec sh -c " + shellQuote(run) + " ;;",
		"  *) printf '%s\\n' \"missionhandoff: reconcile indeterminate rc=$rc\" >&2; exit 70 ;;",
		"esac",
	}, "\n")
}
