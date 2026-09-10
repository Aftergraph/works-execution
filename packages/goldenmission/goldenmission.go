// Package goldenmission is the durable Golden Mission runner for
// Aftergraph/works-execution (contract golden-mission/0.1, acceptance
// GOLDEN-001..003).
//
// It executes the registered branches (success, refusal, revocation,
// crash-recovery, verifier-failure) as durable execution: canonical
// identity is preserved across every consequential seam, any break fails
// closed, and the runner never verifies its own output — completion of an
// effect-bearing branch requires a verdict from an independent verifier
// (continuum/sentinel), presented with a principal distinct from both the
// runner and the mission.
//
// Identity shapes follow platform-event-ref/0.1 correlation ID patterns.
// Possession of an identity or a pin confers no authority.
//
// This slice delivers the standalone runner package and its owner tests.
// Wiring consumers (API/CLI surfaces) is a later integration slice.
package goldenmission

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// Branch names a registered Golden Mission branch
// (governance docs/golden-mission/0.1.json).
type Branch string

// Registered branches.
const (
	BranchSuccess         Branch = "success"
	BranchRefusal         Branch = "refusal"
	BranchRevocation      Branch = "revocation"
	BranchCrashRecovery   Branch = "crash-recovery"
	BranchVerifierFailure Branch = "verifier-failure"
)

// allBranches is the fixed skeleton order. Map iteration is random in Go;
// pins must be deterministic, so every enumeration uses this slice.
var allBranches = []Branch{
	BranchSuccess,
	BranchRefusal,
	BranchRevocation,
	BranchCrashRecovery,
	BranchVerifierFailure,
}

func validBranch(b Branch) bool {
	for _, known := range allBranches {
		if b == known {
			return true
		}
	}
	return false
}

// Identity is the canonical mission identity. Field shapes follow
// platform-event-ref/0.1 correlation patterns.
type Identity struct {
	TenantID           string `json:"tenant_id"`
	PrincipalID        string `json:"principal_id"`
	MissionID          string `json:"mission_id"`
	AuthorityLeaseID   string `json:"authority_lease_id"`
	ExecutionContextID string `json:"execution_context_id"`
	WorkID             string `json:"work_id"`
	TraceID            string `json:"trace_id"`
	ActionID           string `json:"action_id"`
	ActionDecisionID   string `json:"action_decision_id"`
}

// Stage is one observed seam record. Name is the stage label; IDs carries
// the stage-observed identity fields. Absent fields are "" and are not
// checked — every PRESENT field must equal the canonical value.
type Stage struct {
	Name string   `json:"name"`
	IDs  Identity `json:"ids"`
}

// Mission is a durable mission execution request.
type Mission struct {
	Branch       Branch   `json:"branch"`
	Canonical    Identity `json:"canonical"`
	Stages       []Stage  `json:"stages"`
	RefusedBy    string   `json:"refused_by,omitempty"`
	RevokedAfter string   `json:"revoked_after,omitempty"`
}

// Decision is a runner verdict.
type Decision string

// Runner decisions.
const (
	DecisionAccept    Decision = "accept"
	DecisionReject    Decision = "reject"
	DecisionRefuse    Decision = "refuse"
	DecisionRevoked   Decision = "revoked"
	DecisionRecovered Decision = "recovered"
)

// Verification is an independent verifier's verdict over one canonical
// action. It must arrive from outside the runner: see ErrSelfVerification.
type Verification struct {
	VerifierPrincipal string   `json:"verifier_principal"`
	Verdict           Decision `json:"verdict"`
	ActionID          string   `json:"action_id"`
	ActionDecisionID  string   `json:"action_decision_id"`
}

// Result is the runner's verdict plus the transcript pin binding it.
type Result struct {
	Decision Decision `json:"decision"`
	Reason   string   `json:"reason"`
	Pin      string   `json:"pin"`
}

// Sentinel errors. Policy outcomes are returned as reject Results;
// malformed inputs are returned as errors. Both fail closed.
var (
	ErrUnknownBranch      = errors.New("goldenmission: unknown branch")
	ErrBadIdentity        = errors.New("goldenmission: invalid canonical identity")
	ErrSelfVerification   = errors.New("goldenmission: runner never self-verifies")
	ErrBadVerification    = errors.New("goldenmission: malformed verification")
	ErrBadJournal         = errors.New("goldenmission: invalid recovery journal")
	ErrBadRunnerPrincipal = errors.New("goldenmission: invalid runner principal")
	ErrPrefixExecuted     = errors.New("goldenmission: prefix already executed")
)

// Canonical stage order. A mission chain must be a strictly increasing
// subsequence of this order.
var stageOrder = map[string]int{
	"studio":        0,
	"aie":           1,
	"trust-gateway": 2,
	"runtime":       3,
	"works":         4,
	"verification":  5,
}

// fullChain is the exact stage sequence the success branch requires.
var fullChain = []string{"studio", "aie", "trust-gateway", "runtime", "works", "verification"}

// consequential marks seams whose identity break is a provenance failure
// (GOLDEN-002 class). Studio/aie breaks still fail closed, generically.
var consequential = map[string]bool{
	"trust-gateway": true,
	"runtime":       true,
	"works":         true,
	"verification":  true,
}

var idPatterns = map[string]*regexp.Regexp{
	"tenant_id":            regexp.MustCompile(`^ten_[a-f0-9]{32}$`),
	"principal_id":         regexp.MustCompile(`^prn_[a-f0-9]{32}$`),
	"authority_lease_id":   regexp.MustCompile(`^auth_[a-f0-9]{32}$`),
	"execution_context_id": regexp.MustCompile(`^ctx_[a-f0-9]{32}$`),
	"work_id":              regexp.MustCompile(`^wrk_[a-f0-9]{32}$`),
	"trace_id":             regexp.MustCompile(`^trc_[a-f0-9]{32}$`),
	"action_id":            regexp.MustCompile(`^act_[a-f0-9]{32}$`),
	"action_decision_id":   regexp.MustCompile(`^pdr_[a-f0-9]{32}$`),
}

func checkPattern(name, value string) error {
	rx := idPatterns[name]
	if value == "" || !rx.MatchString(value) {
		return fmt.Errorf("%w: invalid %s", ErrBadIdentity, name)
	}
	return nil
}

func validIdentity(c Identity) error {
	for _, f := range []struct {
		name  string
		value string
	}{
		{"tenant_id", c.TenantID},
		{"principal_id", c.PrincipalID},
		{"authority_lease_id", c.AuthorityLeaseID},
		{"execution_context_id", c.ExecutionContextID},
		{"work_id", c.WorkID},
		{"trace_id", c.TraceID},
		{"action_id", c.ActionID},
		{"action_decision_id", c.ActionDecisionID},
	} {
		if err := checkPattern(f.name, f.value); err != nil {
			return err
		}
	}
	if c.MissionID == "" || len(c.MissionID) > 256 {
		return fmt.Errorf("%w: invalid mission_id", ErrBadIdentity)
	}
	return nil
}

// CheckChain enforces the pure identity law over a stage chain: every
// present stage field must equal the canonical value. It reports accept
// only when the whole chain preserves canonical identity.
//
//   - GOLDEN-001: full preservation is accepted.
//   - GOLDEN-002: principal drift across a consequential seam is rejected.
//   - GOLDEN-003: a broken canonical action identity across the works
//     seam, with no action-time decision, is rejected.
//
// Any other break (unknown stage, disorder, malformed canonical, any other
// mismatch) is rejected fail-closed. Chain acceptance is identity
// preservation only — branch completeness (required stages, independent
// verification) is enforced by Runner.
func CheckChain(canonical Identity, stages []Stage) (Decision, string) {
	if err := validIdentity(canonical); err != nil {
		return DecisionReject, fmt.Sprintf("FAIL-CLOSED: %v", err)
	}
	if len(stages) == 0 {
		return DecisionReject, "FAIL-CLOSED: empty stage chain"
	}
	prev := -1
	for i, s := range stages {
		idx, ok := stageOrder[s.Name]
		if !ok {
			return DecisionReject, fmt.Sprintf("FAIL-CLOSED: unknown stage %q at position %d", s.Name, i)
		}
		if idx <= prev {
			return DecisionReject, fmt.Sprintf("FAIL-CLOSED: stage %q out of canonical order at position %d", s.Name, i)
		}
		prev = idx
		fields := []struct {
			name     string
			observed string
			want     string
		}{
			{"tenant_id", s.IDs.TenantID, canonical.TenantID},
			{"principal_id", s.IDs.PrincipalID, canonical.PrincipalID},
			{"mission_id", s.IDs.MissionID, canonical.MissionID},
			{"authority_lease_id", s.IDs.AuthorityLeaseID, canonical.AuthorityLeaseID},
			{"execution_context_id", s.IDs.ExecutionContextID, canonical.ExecutionContextID},
			{"work_id", s.IDs.WorkID, canonical.WorkID},
			{"trace_id", s.IDs.TraceID, canonical.TraceID},
			{"action_id", s.IDs.ActionID, canonical.ActionID},
			{"action_decision_id", s.IDs.ActionDecisionID, canonical.ActionDecisionID},
		}
		for _, f := range fields {
			if f.observed == "" || f.observed == f.want {
				continue
			}
			if f.name == "principal_id" && consequential[s.Name] {
				return DecisionReject, fmt.Sprintf("GOLDEN-002: principal drift across consequential %s seam", s.Name)
			}
			if s.Name == "works" && f.name == "action_id" && s.IDs.ActionDecisionID == "" {
				return DecisionReject, "GOLDEN-003: broken canonical action identity across works seam with no action-time decision"
			}
			return DecisionReject, fmt.Sprintf("FAIL-CLOSED: %s mismatch at %s seam", f.name, s.Name)
		}
	}
	return DecisionAccept, fmt.Sprintf("GOLDEN-001: canonical identity preserved across %d stages", len(stages))
}

// EffectLog is the durable record of applied consequential effects, keyed
// by work|action. Resume paths consult it so a crash retry never applies
// an effect twice. It is the only effect memory the runner trusts.
type EffectLog struct {
	mu      sync.Mutex
	applied map[string]int
}

// NewEffectLog returns an empty effect log.
func NewEffectLog() *EffectLog { return &EffectLog{applied: map[string]int{}} }

// Apply records one application of key. It reports whether this call
// applied the effect (true) or the effect was already applied (false).
func (l *EffectLog) Apply(key string) bool {
	if l == nil {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.applied[key]++
	return l.applied[key] == 1
}

// Count reports how many times key was applied.
func (l *EffectLog) Count(key string) int {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.applied[key]
}

// JournalEntry is one durable runner record. Seq starts at 1 and is
// contiguous; ActionKey binds every entry to a single canonical action and
// Canonical binds the journal to the exact mission identity that executed
// the prefix — a journal from a divergent mission (same work/action, new
// mission or principal) must never complete another mission's resume.
type JournalEntry struct {
	Seq       int      `json:"seq"`
	Kind      string   `json:"kind"` // "mission-started" or "stage"
	Stage     string   `json:"stage,omitempty"`
	ActionKey string   `json:"action_key"`
	Canonical Identity `json:"canonical"`
}

// Runner executes Golden Mission branches durably.
type Runner struct {
	principal string
	log       *EffectLog
	journal   []JournalEntry
	recovered bool
}

// NewRunner returns a runner identified by a well-formed runner principal.
// A nil log is replaced with a fresh in-memory log.
func NewRunner(principal string, log *EffectLog) (*Runner, error) {
	if !idPatterns["principal_id"].MatchString(principal) {
		return nil, fmt.Errorf("%w: %q", ErrBadRunnerPrincipal, principal)
	}
	if log == nil {
		log = NewEffectLog()
	}
	return &Runner{principal: principal, log: log}, nil
}

// Recover rebuilds a runner from a durable journal snapshot plus the
// shared effect log after a crash. The journal is validated fail-closed:
// only a well-formed snapshot resumes, and the resumed runner completes
// crash-recovery missions only.
func Recover(principal string, log *EffectLog, entries []JournalEntry) (*Runner, error) {
	if !idPatterns["principal_id"].MatchString(principal) {
		return nil, fmt.Errorf("%w: %q", ErrBadRunnerPrincipal, principal)
	}
	if err := validJournal(entries); err != nil {
		return nil, err
	}
	if log == nil {
		log = NewEffectLog()
	}
	cp := append([]JournalEntry(nil), entries...)
	return &Runner{principal: principal, log: log, journal: cp, recovered: true}, nil
}

func validJournal(entries []JournalEntry) error {
	if len(entries) < 2 {
		return fmt.Errorf("%w: journal too short", ErrBadJournal)
	}
	key := entries[0].ActionKey
	if key == "" {
		return fmt.Errorf("%w: journal not bound to an action", ErrBadJournal)
	}
	if err := validIdentity(entries[0].Canonical); err != nil {
		return fmt.Errorf("%w: journal bound to invalid identity", ErrBadJournal)
	}
	if entries[0].Kind != "mission-started" || entries[0].Seq != 1 {
		return fmt.Errorf("%w: journal must open with mission-started seq 1", ErrBadJournal)
	}
	prev := -1
	for i, e := range entries[1:] {
		if e.Seq != i+2 {
			return fmt.Errorf("%w: non-contiguous seq at position %d", ErrBadJournal, i+1)
		}
		if e.Kind != "stage" {
			return fmt.Errorf("%w: unexpected kind %q", ErrBadJournal, e.Kind)
		}
		if e.ActionKey != key {
			return fmt.Errorf("%w: journal spans actions", ErrBadJournal)
		}
		if e.Canonical != entries[0].Canonical {
			return fmt.Errorf("%w: journal spans missions", ErrBadJournal)
		}
		idx, ok := stageOrder[e.Stage]
		if !ok {
			return fmt.Errorf("%w: unknown stage %q", ErrBadJournal, e.Stage)
		}
		if idx <= prev {
			return fmt.Errorf("%w: stages out of order", ErrBadJournal)
		}
		prev = idx
	}
	return nil
}

// Journal returns a copy of the runner's durable journal.
func (r *Runner) Journal() []JournalEntry {
	return append([]JournalEntry(nil), r.journal...)
}

func effectKey(c Identity) string { return c.WorkID + "|" + c.ActionID }

func stageNames(stages []Stage) []string {
	names := make([]string, len(stages))
	for i, s := range stages {
		names[i] = s.Name
	}
	return names
}

func hasStage(stages []Stage, name string) bool {
	for _, s := range stages {
		if s.Name == name {
			return true
		}
	}
	return false
}

func pinResult(m Mission, res Result) Result {
	res.Pin = PinFor(m, res.Decision, res.Reason)
	return res
}

// Run executes the non-verified branches: refusal and revocation.
// Effect-bearing branches (success, crash-recovery, verifier-failure)
// are rejected here: they require RunWithVerification with an independent
// verdict. No effect is ever applied on a rejected or refused path.
func (r *Runner) Run(m Mission) (Result, error) {
	if !validBranch(m.Branch) {
		return Result{}, fmt.Errorf("%w: %q", ErrUnknownBranch, m.Branch)
	}
	if dec, reason := CheckChain(m.Canonical, m.Stages); dec == DecisionReject {
		return pinResult(m, Result{Decision: DecisionReject, Reason: reason}), nil
	}
	var res Result
	switch m.Branch {
	case BranchRefusal:
		res = r.runRefusal(m)
	case BranchRevocation:
		res = r.runRevocation(m)
	default:
		res = Result{Decision: DecisionReject,
			Reason: fmt.Sprintf("FAIL-CLOSED: branch %q requires independent verification; unverified execution refused", m.Branch)}
	}
	return pinResult(m, res), nil
}

func (r *Runner) runRefusal(m Mission) Result {
	if m.RefusedBy != "trust-gateway" {
		return Result{Decision: DecisionReject,
			Reason: "FAIL-CLOSED: refusal requires a trust-gateway admission decision"}
	}
	if !hasStage(m.Stages, "trust-gateway") {
		return Result{Decision: DecisionReject,
			Reason: "FAIL-CLOSED: refusal without a trust-gateway stage"}
	}
	if hasStage(m.Stages, "works") || hasStage(m.Stages, "verification") {
		return Result{Decision: DecisionReject,
			Reason: "FAIL-CLOSED: consequential stage on a refused mission"}
	}
	return Result{Decision: DecisionRefuse,
		Reason: "REFUSAL: admission refused by trust-gateway; no effect applied"}
}

func (r *Runner) runRevocation(m Mission) Result {
	point, ok := stageOrder[m.RevokedAfter]
	if m.RevokedAfter == "" || !ok {
		return Result{Decision: DecisionReject,
			Reason: "FAIL-CLOSED: revocation requires a known revocation point"}
	}
	if !hasStage(m.Stages, "trust-gateway") {
		return Result{Decision: DecisionReject,
			Reason: "FAIL-CLOSED: revocation without a trust-gateway stage"}
	}
	for _, s := range m.Stages {
		if stageOrder[s.Name] > point {
			return Result{Decision: DecisionReject,
				Reason: fmt.Sprintf("FAIL-CLOSED: post-revocation %s stage refused", s.Name)}
		}
	}
	if hasStage(m.Stages, "works") {
		r.log.Apply(effectKey(m.Canonical))
	}
	return Result{Decision: DecisionRevoked,
		Reason: fmt.Sprintf("REVOCATION: authority revoked after %s; mission halted", m.RevokedAfter)}
}

// RunPrefix executes a crash-recovery mission up to (and including) the
// named stage, journals every step durably, applies the works effect when
// reached, and returns the journal snapshot for crash resume. It refuses
// to run twice: a retry must Recover, never re-execute the prefix.
func (r *Runner) RunPrefix(m Mission, upto string) ([]JournalEntry, error) {
	if m.Branch != BranchCrashRecovery {
		return nil, fmt.Errorf("%w: prefix execution is crash-recovery only", ErrUnknownBranch)
	}
	if len(r.journal) != 0 {
		return nil, ErrPrefixExecuted
	}
	at := -1
	for i, s := range m.Stages {
		if s.Name == upto {
			at = i
			break
		}
	}
	if at < 0 {
		return nil, fmt.Errorf("goldenmission: prefix stage %q not in mission", upto)
	}
	if dec, reason := CheckChain(m.Canonical, m.Stages[:at+1]); dec == DecisionReject {
		return nil, fmt.Errorf("goldenmission: prefix rejected: %s", reason)
	}
	key := effectKey(m.Canonical)
	r.journal = append(r.journal, JournalEntry{Seq: 1, Kind: "mission-started", ActionKey: key, Canonical: m.Canonical})
	for i, s := range m.Stages[:at+1] {
		r.journal = append(r.journal, JournalEntry{Seq: i + 2, Kind: "stage", Stage: s.Name, ActionKey: key, Canonical: m.Canonical})
	}
	if hasStage(m.Stages[:at+1], "works") {
		r.log.Apply(key)
	}
	return r.Journal(), nil
}

func validVerificationInput(v Verification) error {
	if !idPatterns["principal_id"].MatchString(v.VerifierPrincipal) {
		return fmt.Errorf("%w: invalid verifier principal", ErrBadVerification)
	}
	if v.Verdict != DecisionAccept && v.Verdict != DecisionReject {
		return fmt.Errorf("%w: verdict must be accept or reject", ErrBadVerification)
	}
	if !idPatterns["action_id"].MatchString(v.ActionID) {
		return fmt.Errorf("%w: invalid action id", ErrBadVerification)
	}
	if !idPatterns["action_decision_id"].MatchString(v.ActionDecisionID) {
		return fmt.Errorf("%w: invalid action decision id", ErrBadVerification)
	}
	return nil
}

// RunWithVerification completes the effect-bearing branches with an
// external, independent verdict. The verifier must be distinct from both
// the runner and the mission principal: any self-verification attempt
// fails closed with ErrSelfVerification and a reject Result.
//
//   - success: exact full chain + accepting verifier -> accept.
//   - crash-recovery: journal-resumed runner + accepting verifier ->
//     recovered, with the pre-crash effect applied exactly once.
//   - verifier-failure: rejecting verifier (or a GOLDEN-003 chain) ->
//     reject, no effect.
//   - refusal/revocation: delegated to Run; verification is not applicable.
func (r *Runner) RunWithVerification(m Mission, v Verification) (Result, error) {
	if !validBranch(m.Branch) {
		return Result{}, fmt.Errorf("%w: %q", ErrUnknownBranch, m.Branch)
	}
	if err := validVerificationInput(v); err != nil {
		return Result{}, err
	}
	if v.VerifierPrincipal == r.principal || v.VerifierPrincipal == m.Canonical.PrincipalID {
		res := pinResult(m, Result{Decision: DecisionReject,
			Reason: "RUNNER-NEVER-SELF-VERIFIES: verifier must be independent of runner and mission"})
		return res, ErrSelfVerification
	}
	if v.ActionID != m.Canonical.ActionID || v.ActionDecisionID != m.Canonical.ActionDecisionID {
		return pinResult(m, Result{Decision: DecisionReject,
			Reason: "FAIL-CLOSED: verification binds a different action/decision"}), nil
	}
	if dec, reason := CheckChain(m.Canonical, m.Stages); dec == DecisionReject {
		return pinResult(m, Result{Decision: DecisionReject, Reason: reason}), nil
	}
	switch m.Branch {
	case BranchRefusal, BranchRevocation:
		return r.Run(m)
	case BranchSuccess:
		return pinResult(m, r.runSuccess(m, v)), nil
	case BranchCrashRecovery:
		return pinResult(m, r.runCrashRecovery(m, v)), nil
	case BranchVerifierFailure:
		return pinResult(m, r.runVerifierFailure(m, v)), nil
	}
	return pinResult(m, Result{Decision: DecisionReject, Reason: "FAIL-CLOSED: unreachable"}), nil
}

func (r *Runner) runSuccess(m Mission, v Verification) Result {
	if names := stageNames(m.Stages); !equalStrings(names, fullChain) {
		return Result{Decision: DecisionReject,
			Reason: "FAIL-CLOSED: success requires the exact studio..verification chain"}
	}
	if v.Verdict == DecisionReject {
		return Result{Decision: DecisionReject,
			Reason: "VERIFIER-FAILURE: independent verifier rejected the success chain"}
	}
	r.log.Apply(effectKey(m.Canonical))
	return Result{Decision: DecisionAccept,
		Reason: fmt.Sprintf("GOLDEN-001: success chain accepted; independently verified by %s", v.VerifierPrincipal)}
}

func (r *Runner) runCrashRecovery(m Mission, v Verification) Result {
	if !r.recovered {
		return Result{Decision: DecisionReject,
			Reason: "FAIL-CLOSED: crash-recovery requires journal recovery before completion"}
	}
	if r.journal[0].Canonical != m.Canonical {
		return Result{Decision: DecisionReject,
			Reason: "FAIL-CLOSED: journal bound to a different mission identity"}
	}
	journalStages := make([]string, 0, len(r.journal))
	for _, e := range r.journal {
		if e.Kind == "stage" {
			journalStages = append(journalStages, e.Stage)
		}
	}
	names := stageNames(m.Stages)
	if len(journalStages) == 0 || len(journalStages) > len(names) ||
		!equalStrings(names[:len(journalStages)], journalStages) {
		return Result{Decision: DecisionReject,
			Reason: "FAIL-CLOSED: journal diverges from the resumed mission"}
	}
	if !hasStage(m.Stages, "works") || !hasStage(m.Stages, "verification") {
		return Result{Decision: DecisionReject,
			Reason: "FAIL-CLOSED: crash-recovery requires works and verification stages"}
	}
	if v.Verdict == DecisionReject {
		return Result{Decision: DecisionReject,
			Reason: "VERIFIER-FAILURE: independent verifier rejected the resumed chain"}
	}
	// The journal proves which prefix steps already executed pre-crash.
	// Re-applying the works effect on resume would double-apply: when the
	// journal already covers works, the pre-crash application stands and
	// the resume only completes the remaining stages.
	preCrashApplied := false
	for _, s := range journalStages {
		if s == "works" {
			preCrashApplied = true
			break
		}
	}
	if !preCrashApplied {
		r.log.Apply(effectKey(m.Canonical))
	}
	return Result{Decision: DecisionRecovered,
		Reason: fmt.Sprintf("GOLDEN-001 (recovered): chain accepted after journal resume at seq %d; effect applied exactly once", len(r.journal))}
}

func (r *Runner) runVerifierFailure(m Mission, v Verification) Result {
	if v.Verdict == DecisionAccept {
		return Result{Decision: DecisionReject,
			Reason: "FAIL-CLOSED: verifier-failure branch requires a rejecting verifier"}
	}
	return Result{Decision: DecisionReject,
		Reason: "VERIFIER-FAILURE: independent verifier rejected; failed closed"}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// transcript is the canonical, deterministic record bound by a pin. It
// carries no timestamps, no randomness, and no head SHA: pins are stable
// for a given code + mission input on any checkout.
type transcript struct {
	Schema    string   `json:"schema"`
	Branch    string   `json:"branch"`
	Canonical Identity `json:"canonical"`
	Stages    []Stage  `json:"stages"`
	Decision  string   `json:"decision"`
	Reason    string   `json:"reason"`
}

// Transcript renders the canonical JSON bound by PinFor.
func Transcript(m Mission, decision Decision, reason string) string {
	stages := append([]Stage(nil), m.Stages...)
	raw, err := jsonMarshal(transcript{
		Schema:    "golden-mission-transcript/0.1",
		Branch:    string(m.Branch),
		Canonical: m.Canonical,
		Stages:    stages,
		Decision:  string(decision),
		Reason:    reason,
	})
	if err != nil {
		return ""
	}
	return raw
}

// PinFor binds (mission, decision, reason) to a sha256 hex digest. Any
// tampering with identity, stages, or outcome changes the pin.
func PinFor(m Mission, decision Decision, reason string) string {
	sum := sha256.Sum256([]byte(Transcript(m, decision, reason)))
	return hex.EncodeToString(sum[:])
}

func jsonMarshal(v any) (string, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return "", err
	}
	return strings.TrimSuffix(buf.String(), "\n"), nil
}

// pinsFile is the stored skeleton-run pins shape.
type pinsFile struct {
	Schema string            `json:"schema"`
	Head   string            `json:"head"`
	Pins   map[string]string `json:"pins"`
}

// WritePinsFile runs every branch skeleton and stores the resulting pins
// plus the exact generating HEAD. The file is machine-generated: hand
// edits are detected by the owner tests, which recompute every pin from
// the skeletons and compare.
func WritePinsFile(path string) error {
	computed, err := ComputePins()
	if err != nil {
		return err
	}
	head, err := exec.Command("git", "rev-parse", "HEAD").Output()
	if err != nil {
		return fmt.Errorf("goldenmission: generating HEAD unavailable: %w", err)
	}
	sha := strings.TrimSpace(string(head))
	if len(sha) != 40 || strings.Trim(sha, "0123456789abcdef") != "" {
		return fmt.Errorf("goldenmission: generating HEAD %q is not a 40-hex SHA", sha)
	}
	pins := make(map[string]string, len(computed))
	for _, b := range allBranches {
		pins[string(b)] = computed[b]
	}
	raw, err := json.MarshalIndent(pinsFile{
		Schema: "golden-mission-pins/0.1",
		Head:   sha,
		Pins:   pins,
	}, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if dir := dirOf(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return os.WriteFile(path, raw, 0o644)
}

func dirOf(path string) string {
	if i := strings.LastIndex(path, "/"); i >= 0 {
		return path[:i]
	}
	return ""
}

// skeletonIDs builds a deterministic canonical identity from one hex digit.
func skeletonIDs(c byte) Identity {
	h := strings.Repeat(string([]byte{c}), 32)
	return Identity{
		TenantID: "ten_" + h, PrincipalID: "prn_" + h,
		MissionID: "mis_" + h, AuthorityLeaseID: "auth_" + h,
		ExecutionContextID: "ctx_" + h, WorkID: "wrk_" + h,
		TraceID: "trc_" + h, ActionID: "act_" + h,
		ActionDecisionID: "pdr_" + h,
	}
}

func skeletonRunner() (*Runner, *EffectLog) {
	log := NewEffectLog()
	r, err := NewRunner("prn_"+strings.Repeat("1", 32), log)
	if err != nil {
		panic(err)
	}
	return r, log
}

func skeletonVerifier(c Identity, verdict Decision) Verification {
	return Verification{
		VerifierPrincipal: "prn_" + strings.Repeat("d", 32),
		Verdict:           verdict,
		ActionID:          c.ActionID,
		ActionDecisionID:  c.ActionDecisionID,
	}
}

func skeletonFullChain(c Identity) []Stage {
	return []Stage{
		{Name: "studio", IDs: Identity{
			TenantID: c.TenantID, PrincipalID: c.PrincipalID,
			MissionID: c.MissionID, TraceID: c.TraceID}},
		{Name: "aie", IDs: Identity{
			TenantID: c.TenantID, PrincipalID: c.PrincipalID,
			MissionID: c.MissionID, AuthorityLeaseID: c.AuthorityLeaseID,
			TraceID: c.TraceID}},
		{Name: "trust-gateway", IDs: Identity{
			TenantID: c.TenantID, PrincipalID: c.PrincipalID,
			MissionID: c.MissionID, AuthorityLeaseID: c.AuthorityLeaseID,
			TraceID: c.TraceID, ActionID: c.ActionID,
			ActionDecisionID: c.ActionDecisionID}},
		{Name: "runtime", IDs: Identity{
			TenantID: c.TenantID, PrincipalID: c.PrincipalID,
			MissionID: c.MissionID, AuthorityLeaseID: c.AuthorityLeaseID,
			TraceID: c.TraceID, ActionID: c.ActionID}},
		{Name: "works", IDs: Identity{
			TenantID: c.TenantID, PrincipalID: c.PrincipalID,
			MissionID: c.MissionID, AuthorityLeaseID: c.AuthorityLeaseID,
			ExecutionContextID: c.ExecutionContextID, WorkID: c.WorkID,
			TraceID: c.TraceID, ActionID: c.ActionID,
			ActionDecisionID: c.ActionDecisionID}},
		{Name: "verification", IDs: Identity{
			TenantID: c.TenantID, PrincipalID: c.PrincipalID,
			MissionID: c.MissionID, ExecutionContextID: c.ExecutionContextID,
			WorkID: c.WorkID, TraceID: c.TraceID, ActionID: c.ActionID,
			ActionDecisionID: c.ActionDecisionID}},
	}
}

// RunSkeleton executes the deterministic skeleton mission for a branch and
// returns the mission plus the runner's verdict. Skeleton runs — never
// hand-authorship — are the source of the exact-head SHA pins.
func RunSkeleton(b Branch) (Mission, Result, error) {
	switch b {
	case BranchSuccess:
		c := skeletonIDs('7')
		m := Mission{Branch: b, Canonical: c, Stages: skeletonFullChain(c)}
		r, _ := skeletonRunner()
		res, err := r.RunWithVerification(m, skeletonVerifier(c, DecisionAccept))
		return m, res, err
	case BranchRefusal:
		c := skeletonIDs('a')
		m := Mission{Branch: b, Canonical: c, Stages: skeletonFullChain(c)[:3], RefusedBy: "trust-gateway"}
		r, _ := skeletonRunner()
		res, err := r.Run(m)
		return m, res, err
	case BranchRevocation:
		c := skeletonIDs('b')
		full := skeletonFullChain(c)
		m := Mission{Branch: b, Canonical: c, Stages: full[2:4], RevokedAfter: "runtime"}
		r, _ := skeletonRunner()
		res, err := r.Run(m)
		return m, res, err
	case BranchCrashRecovery:
		c := skeletonIDs('c')
		full := skeletonFullChain(c)
		stages := append([]Stage(nil), full[2:]...)
		m := Mission{Branch: b, Canonical: c, Stages: stages}
		r1, _ := skeletonRunner()
		log := r1.log
		journal, err := r1.RunPrefix(m, "works")
		if err != nil {
			return m, Result{}, err
		}
		r2, err := Recover(r1.principal, log, journal)
		if err != nil {
			return m, Result{}, err
		}
		res, err := r2.RunWithVerification(m, skeletonVerifier(c, DecisionAccept))
		return m, res, err
	case BranchVerifierFailure:
		c := skeletonIDs('e')
		full := skeletonFullChain(c)
		broken := full[4]
		broken.IDs.ActionID = "act_" + strings.Repeat("f", 32)
		broken.IDs.ActionDecisionID = ""
		m := Mission{Branch: b, Canonical: c, Stages: []Stage{full[2], broken, full[5]}}
		r, _ := skeletonRunner()
		res, err := r.RunWithVerification(m, skeletonVerifier(c, DecisionReject))
		return m, res, err
	}
	return Mission{}, Result{}, fmt.Errorf("%w: %q", ErrUnknownBranch, b)
}

// ComputePins runs every branch skeleton and binds each verdict to its
// transcript pin. The map covers all registered branches.
func ComputePins() (map[Branch]string, error) {
	pins := make(map[Branch]string, len(allBranches))
	for _, b := range allBranches {
		m, res, err := RunSkeleton(b)
		if err != nil {
			return nil, fmt.Errorf("goldenmission: skeleton %q: %w", b, err)
		}
		if res.Pin == "" {
			return nil, fmt.Errorf("goldenmission: skeleton %q produced no pin", b)
		}
		pins[b] = PinFor(m, res.Decision, res.Reason)
		if pins[b] != res.Pin {
			return nil, fmt.Errorf("goldenmission: skeleton %q pin not transcript-bound", b)
		}
	}
	return pins, nil
}

// SortedBranches returns the deterministic branch enumeration.
func SortedBranches() []Branch {
	return append([]Branch(nil), allBranches...)
}

// VerifyPins recomputes every skeleton pin and reports mismatches against
// stored. It returns a sorted human-readable diff, empty when all match.
func VerifyPins(stored map[string]string) []string {
	computed, err := ComputePins()
	if err != nil {
		return []string{fmt.Sprintf("compute: %v", err)}
	}
	var diff []string
	keys := make([]string, 0, len(allBranches))
	for _, b := range allBranches {
		keys = append(keys, string(b))
	}
	sort.Strings(keys)
	for _, k := range keys {
		if stored[k] != computed[Branch(k)] {
			diff = append(diff, fmt.Sprintf("branch %q: stored %q != skeleton %q", k, stored[k], computed[Branch(k)]))
		}
	}
	return diff
}
