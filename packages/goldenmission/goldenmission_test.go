// TDD RED: Golden Mission runner acceptance (contract golden-mission/0.1).
//
// Vectors GOLDEN-001..003 are quoted verbatim from
// after-graph-governance docs/platform-conformance/v0.1/vectors.json.
// Branches success/refusal/revocation/crash-recovery/verifier-failure come
// from docs/golden-mission/0.1.json. This file is written FIRST against an
// API that does not exist yet: it must FAIL to compile until the GREEN step
// implements packages/goldenmission.
package goldenmission_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JonasAbde/works-execution/packages/goldenmission"
)

// hex32 repeats one hex digit 32 times, matching the canonical
// xxx_<32 lower-hex> identity shapes in platform-event-ref/0.1.
func hex32(c byte) string { return strings.Repeat(string([]byte{c}), 32) }

func ids77() goldenmission.Identity {
	return goldenmission.Identity{
		TenantID:           "ten_" + hex32('7'),
		PrincipalID:        "prn_" + hex32('7'),
		MissionID:          "mis_" + hex32('7'),
		AuthorityLeaseID:   "auth_" + hex32('7'),
		ExecutionContextID: "ctx_" + hex32('7'),
		WorkID:             "wrk_" + hex32('7'),
		TraceID:            "trc_" + hex32('7'),
		ActionID:           "act_" + hex32('7'),
		ActionDecisionID:   "pdr_" + hex32('7'),
	}
}

// golden001Stages quotes the GOLDEN-001 input stages verbatim: the full
// success chain with preserved canonical identity is accepted.
func golden001Stages() []goldenmission.Stage {
	c := ids77()
	return []goldenmission.Stage{
		{Name: "studio", IDs: goldenmission.Identity{
			TenantID: c.TenantID, PrincipalID: c.PrincipalID,
			MissionID: c.MissionID, TraceID: c.TraceID}},
		{Name: "aie", IDs: goldenmission.Identity{
			TenantID: c.TenantID, PrincipalID: c.PrincipalID,
			MissionID: c.MissionID, AuthorityLeaseID: c.AuthorityLeaseID,
			TraceID: c.TraceID}},
		{Name: "trust-gateway", IDs: goldenmission.Identity{
			TenantID: c.TenantID, PrincipalID: c.PrincipalID,
			MissionID: c.MissionID, AuthorityLeaseID: c.AuthorityLeaseID,
			TraceID: c.TraceID, ActionID: c.ActionID,
			ActionDecisionID: c.ActionDecisionID}},
		{Name: "runtime", IDs: goldenmission.Identity{
			TenantID: c.TenantID, PrincipalID: c.PrincipalID,
			MissionID: c.MissionID, AuthorityLeaseID: c.AuthorityLeaseID,
			TraceID: c.TraceID, ActionID: c.ActionID}},
		{Name: "works", IDs: goldenmission.Identity{
			TenantID: c.TenantID, PrincipalID: c.PrincipalID,
			MissionID: c.MissionID, AuthorityLeaseID: c.AuthorityLeaseID,
			ExecutionContextID: c.ExecutionContextID, WorkID: c.WorkID,
			TraceID: c.TraceID, ActionID: c.ActionID,
			ActionDecisionID: c.ActionDecisionID}},
		{Name: "verification", IDs: goldenmission.Identity{
			TenantID: c.TenantID, PrincipalID: c.PrincipalID,
			MissionID: c.MissionID, ExecutionContextID: c.ExecutionContextID,
			WorkID: c.WorkID, TraceID: c.TraceID, ActionID: c.ActionID,
			ActionDecisionID: c.ActionDecisionID}},
	}
}

// golden002 quotes GOLDEN-002 verbatim: principal drift across the
// consequential works seam is rejected.
func golden002() (goldenmission.Identity, []goldenmission.Stage) {
	c8 := func(prefix string) string { return prefix + hex32('8') }
	c := goldenmission.Identity{
		TenantID: c8("ten_"), PrincipalID: c8("prn_"),
		MissionID: c8("mis_"), AuthorityLeaseID: c8("auth_"),
		ExecutionContextID: c8("ctx_"), WorkID: c8("wrk_"),
		TraceID: c8("trc_"), ActionID: c8("act_"),
		ActionDecisionID: c8("pdr_"),
	}
	drifted := c
	drifted.PrincipalID = "prn_" + hex32('9')
	return c, []goldenmission.Stage{
		{Name: "trust-gateway", IDs: goldenmission.Identity{
			TenantID: c.TenantID, PrincipalID: c.PrincipalID,
			MissionID: c.MissionID, AuthorityLeaseID: c.AuthorityLeaseID,
			TraceID: c.TraceID, ActionID: c.ActionID,
			ActionDecisionID: c.ActionDecisionID}},
		{Name: "works", IDs: goldenmission.Identity{
			TenantID: c.TenantID, PrincipalID: drifted.PrincipalID,
			MissionID: c.MissionID, AuthorityLeaseID: c.AuthorityLeaseID,
			ExecutionContextID: c.ExecutionContextID, WorkID: c.WorkID,
			TraceID: c.TraceID, ActionID: c.ActionID,
			ActionDecisionID: c.ActionDecisionID}},
	}
}

// golden003 quotes GOLDEN-003 verbatim: a broken canonical action identity
// across the works seam, with no action-time decision, is rejected.
func golden003() (goldenmission.Identity, []goldenmission.Stage) {
	ce := func(prefix string) string { return prefix + hex32('e') }
	c := goldenmission.Identity{
		TenantID: ce("ten_"), PrincipalID: ce("prn_"),
		MissionID: ce("mis_"), AuthorityLeaseID: ce("auth_"),
		ExecutionContextID: ce("ctx_"), WorkID: ce("wrk_"),
		TraceID: ce("trc_"), ActionID: ce("act_"),
		ActionDecisionID: ce("pdr_"),
	}
	broken := c
	broken.ActionID = "act_" + hex32('f')
	broken.ActionDecisionID = ""
	return c, []goldenmission.Stage{
		{Name: "trust-gateway", IDs: goldenmission.Identity{
			TenantID: c.TenantID, PrincipalID: c.PrincipalID,
			MissionID: c.MissionID, AuthorityLeaseID: c.AuthorityLeaseID,
			TraceID: c.TraceID, ActionID: c.ActionID,
			ActionDecisionID: c.ActionDecisionID}},
		{Name: "works", IDs: goldenmission.Identity{
			TenantID: c.TenantID, PrincipalID: c.PrincipalID,
			MissionID: c.MissionID, AuthorityLeaseID: c.AuthorityLeaseID,
			ExecutionContextID: c.ExecutionContextID, WorkID: c.WorkID,
			TraceID: c.TraceID, ActionID: broken.ActionID}},
		{Name: "verification", IDs: goldenmission.Identity{
			TenantID: c.TenantID, PrincipalID: c.PrincipalID,
			MissionID: c.MissionID, ExecutionContextID: c.ExecutionContextID,
			WorkID: c.WorkID, TraceID: c.TraceID, ActionID: c.ActionID,
			ActionDecisionID: c.ActionDecisionID}},
	}
}

func newTestRunner(t *testing.T) (*goldenmission.Runner, *goldenmission.EffectLog) {
	t.Helper()
	log := goldenmission.NewEffectLog()
	r, err := goldenmission.NewRunner("prn_"+hex32('1'), log)
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	return r, log
}

func acceptVerification(c goldenmission.Identity) goldenmission.Verification {
	return goldenmission.Verification{
		VerifierPrincipal: "prn_" + hex32('d'),
		Verdict:           goldenmission.DecisionAccept,
		ActionID:          c.ActionID,
		ActionDecisionID:  c.ActionDecisionID,
	}
}

func TestGolden001SuccessChainAccepted(t *testing.T) {
	c := ids77()
	dec, reason := goldenmission.CheckChain(c, golden001Stages())
	if dec != goldenmission.DecisionAccept {
		t.Fatalf("GOLDEN-001 chain rejected: %s", reason)
	}
	if !strings.Contains(reason, "GOLDEN-001") {
		t.Fatalf("accept reason must cite GOLDEN-001, got %q", reason)
	}
}

func TestGolden002PrincipalDriftRejected(t *testing.T) {
	c, stages := golden002()
	dec, reason := goldenmission.CheckChain(c, stages)
	if dec != goldenmission.DecisionReject {
		t.Fatalf("GOLDEN-002 drift accepted, want reject")
	}
	if !strings.Contains(reason, "GOLDEN-002") {
		t.Fatalf("reject reason must cite GOLDEN-002, got %q", reason)
	}
}

func TestGolden003BrokenActionIdentityRejected(t *testing.T) {
	c, stages := golden003()
	dec, reason := goldenmission.CheckChain(c, stages)
	if dec != goldenmission.DecisionReject {
		t.Fatalf("GOLDEN-003 broken action accepted, want reject")
	}
	if !strings.Contains(reason, "GOLDEN-003") {
		t.Fatalf("reject reason must cite GOLDEN-003, got %q", reason)
	}
}

func TestSuccessBranchAcceptsFullChainWithIndependentVerification(t *testing.T) {
	r, log := newTestRunner(t)
	c := ids77()
	m := goldenmission.Mission{
		Branch:    goldenmission.BranchSuccess,
		Canonical: c,
		Stages:    golden001Stages(),
	}
	res, err := r.RunWithVerification(m, acceptVerification(c))
	if err != nil {
		t.Fatalf("RunWithVerification: %v", err)
	}
	if res.Decision != goldenmission.DecisionAccept {
		t.Fatalf("success branch not accepted: %s", res.Reason)
	}
	if got := log.Count(c.WorkID + "|" + c.ActionID); got != 1 {
		t.Fatalf("consequential effect must apply exactly once, got %d", got)
	}
	if res.Pin == "" {
		t.Fatal("result must carry a transcript pin")
	}
}

func TestSuccessBranchWithoutVerificationFailsClosed(t *testing.T) {
	r, log := newTestRunner(t)
	c := ids77()
	m := goldenmission.Mission{
		Branch:    goldenmission.BranchSuccess,
		Canonical: c,
		Stages:    golden001Stages(),
	}
	res, err := r.Run(m)
	if err == nil && res.Decision == goldenmission.DecisionAccept {
		t.Fatal("unverified success accepted — runner must require independent verification")
	}
	if res.Decision == goldenmission.DecisionAccept {
		t.Fatalf("unverified success accepted: %s", res.Reason)
	}
	if got := log.Count(c.WorkID + "|" + c.ActionID); got != 0 {
		t.Fatalf("no effect may apply without verification, got %d", got)
	}
}

func TestRunnerNeverSelfVerifies(t *testing.T) {
	r, _ := newTestRunner(t)
	c := ids77()
	m := goldenmission.Mission{
		Branch:    goldenmission.BranchSuccess,
		Canonical: c,
		Stages:    golden001Stages(),
	}
	self := acceptVerification(c)
	self.VerifierPrincipal = "prn_" + hex32('1') // == runner principal
	res, err := r.RunWithVerification(m, self)
	if err == nil {
		t.Fatal("self-verification must return ErrSelfVerification")
	}
	if res.Decision == goldenmission.DecisionAccept {
		t.Fatal("self-verified run accepted — runner must never verify its own output")
	}
}

func TestMissionPrincipalCannotVerifyItself(t *testing.T) {
	r, _ := newTestRunner(t)
	c := ids77()
	m := goldenmission.Mission{
		Branch:    goldenmission.BranchSuccess,
		Canonical: c,
		Stages:    golden001Stages(),
	}
	self := acceptVerification(c)
	self.VerifierPrincipal = c.PrincipalID
	res, err := r.RunWithVerification(m, self)
	if err == nil {
		t.Fatal("mission self-verification must fail")
	}
	if res.Decision == goldenmission.DecisionAccept {
		t.Fatal("mission self-verified run accepted")
	}
}

func refuseFixture() (goldenmission.Identity, []goldenmission.Stage) {
	mk := func(prefix string) string { return prefix + hex32('a') }
	c := goldenmission.Identity{
		TenantID: mk("ten_"), PrincipalID: mk("prn_"),
		MissionID: mk("mis_"), AuthorityLeaseID: mk("auth_"),
		ExecutionContextID: mk("ctx_"), WorkID: mk("wrk_"),
		TraceID: mk("trc_"), ActionID: mk("act_"),
		ActionDecisionID: mk("pdr_"),
	}
	return c, []goldenmission.Stage{
		{Name: "studio", IDs: goldenmission.Identity{
			TenantID: c.TenantID, PrincipalID: c.PrincipalID,
			MissionID: c.MissionID, TraceID: c.TraceID}},
		{Name: "aie", IDs: goldenmission.Identity{
			TenantID: c.TenantID, PrincipalID: c.PrincipalID,
			MissionID: c.MissionID, AuthorityLeaseID: c.AuthorityLeaseID,
			TraceID: c.TraceID}},
		{Name: "trust-gateway", IDs: goldenmission.Identity{
			TenantID: c.TenantID, PrincipalID: c.PrincipalID,
			MissionID: c.MissionID, AuthorityLeaseID: c.AuthorityLeaseID,
			TraceID: c.TraceID, ActionID: c.ActionID,
			ActionDecisionID: c.ActionDecisionID}},
	}
}

func TestRefusalBranchRecordsRefusalWithoutEffect(t *testing.T) {
	r, log := newTestRunner(t)
	c, stages := refuseFixture()
	m := goldenmission.Mission{
		Branch:    goldenmission.BranchRefusal,
		Canonical: c,
		Stages:    stages,
		RefusedBy: "trust-gateway",
	}
	res, err := r.Run(m)
	if err != nil {
		t.Fatalf("Run refusal: %v", err)
	}
	if res.Decision != goldenmission.DecisionRefuse {
		t.Fatalf("refusal branch decided %q, want refuse: %s", res.Decision, res.Reason)
	}
	if got := log.Count(c.WorkID + "|" + c.ActionID); got != 0 {
		t.Fatalf("refused mission must apply no effect, got %d", got)
	}
}

func TestRefusalBranchWithWorksEffectFailsClosed(t *testing.T) {
	r, _ := newTestRunner(t)
	c, stages := refuseFixture()
	stages = append(stages, goldenmission.Stage{
		Name: "works",
		IDs:  c,
	})
	m := goldenmission.Mission{
		Branch:    goldenmission.BranchRefusal,
		Canonical: c,
		Stages:    stages,
		RefusedBy: "trust-gateway",
	}
	res, err := r.Run(m)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Decision != goldenmission.DecisionReject {
		t.Fatalf("refusal with works effect decided %q, want reject", res.Decision)
	}
}

func TestRevocationBranchHaltsBeforeConsequentialEffect(t *testing.T) {
	r, log := newTestRunner(t)
	mk := func(prefix string) string { return prefix + hex32('b') }
	c := goldenmission.Identity{
		TenantID: mk("ten_"), PrincipalID: mk("prn_"),
		MissionID: mk("mis_"), AuthorityLeaseID: mk("auth_"),
		ExecutionContextID: mk("ctx_"), WorkID: mk("wrk_"),
		TraceID: mk("trc_"), ActionID: mk("act_"),
		ActionDecisionID: mk("pdr_"),
	}
	m := goldenmission.Mission{
		Branch:    goldenmission.BranchRevocation,
		Canonical: c,
		Stages: []goldenmission.Stage{
			{Name: "trust-gateway", IDs: goldenmission.Identity{
				TenantID: c.TenantID, PrincipalID: c.PrincipalID,
				MissionID: c.MissionID, AuthorityLeaseID: c.AuthorityLeaseID,
				TraceID: c.TraceID, ActionID: c.ActionID,
				ActionDecisionID: c.ActionDecisionID}},
			{Name: "runtime", IDs: goldenmission.Identity{
				TenantID: c.TenantID, PrincipalID: c.PrincipalID,
				MissionID: c.MissionID, AuthorityLeaseID: c.AuthorityLeaseID,
				TraceID: c.TraceID, ActionID: c.ActionID}},
		},
		RevokedAfter: "runtime",
	}
	res, err := r.Run(m)
	if err != nil {
		t.Fatalf("Run revocation: %v", err)
	}
	if res.Decision != goldenmission.DecisionRevoked {
		t.Fatalf("revocation branch decided %q, want revoked: %s", res.Decision, res.Reason)
	}
	if got := log.Count(c.WorkID + "|" + c.ActionID); got != 0 {
		t.Fatalf("halted mission must apply no effect, got %d", got)
	}
}

func TestCrashRecoveryResumeAppliesEffectOnce(t *testing.T) {
	log := goldenmission.NewEffectLog()
	runnerPrincipal := "prn_" + hex32('1')
	r1, err := goldenmission.NewRunner(runnerPrincipal, log)
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	mk := func(prefix string) string { return prefix + hex32('c') }
	c := goldenmission.Identity{
		TenantID: mk("ten_"), PrincipalID: mk("prn_"),
		MissionID: mk("mis_"), AuthorityLeaseID: mk("auth_"),
		ExecutionContextID: mk("ctx_"), WorkID: mk("wrk_"),
		TraceID: mk("trc_"), ActionID: mk("act_"),
		ActionDecisionID: mk("pdr_"),
	}
	full := []goldenmission.Stage{
		{Name: "trust-gateway", IDs: goldenmission.Identity{
			TenantID: c.TenantID, PrincipalID: c.PrincipalID,
			MissionID: c.MissionID, AuthorityLeaseID: c.AuthorityLeaseID,
			TraceID: c.TraceID, ActionID: c.ActionID,
			ActionDecisionID: c.ActionDecisionID}},
		{Name: "runtime", IDs: goldenmission.Identity{
			TenantID: c.TenantID, PrincipalID: c.PrincipalID,
			MissionID: c.MissionID, AuthorityLeaseID: c.AuthorityLeaseID,
			TraceID: c.TraceID, ActionID: c.ActionID}},
		{Name: "works", IDs: goldenmission.Identity{
			TenantID: c.TenantID, PrincipalID: c.PrincipalID,
			MissionID: c.MissionID, AuthorityLeaseID: c.AuthorityLeaseID,
			ExecutionContextID: c.ExecutionContextID, WorkID: c.WorkID,
			TraceID: c.TraceID, ActionID: c.ActionID,
			ActionDecisionID: c.ActionDecisionID}},
		{Name: "verification", IDs: goldenmission.Identity{
			TenantID: c.TenantID, PrincipalID: c.PrincipalID,
			MissionID: c.MissionID, ExecutionContextID: c.ExecutionContextID,
			WorkID: c.WorkID, TraceID: c.TraceID, ActionID: c.ActionID,
			ActionDecisionID: c.ActionDecisionID}},
	}
	pre := goldenmission.Mission{
		Branch:    goldenmission.BranchCrashRecovery,
		Canonical: c,
		Stages:    full,
	}
	journal, err := r1.RunPrefix(pre, "works")
	if err != nil {
		t.Fatalf("RunPrefix: %v", err)
	}
	// Crash: r1 is dropped. Resume from the durable journal + effect log.
	r2, err := goldenmission.Recover(runnerPrincipal, log, journal)
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	res, err := r2.RunWithVerification(pre, acceptVerification(c))
	if err != nil {
		t.Fatalf("resumed RunWithVerification: %v", err)
	}
	if res.Decision != goldenmission.DecisionRecovered {
		t.Fatalf("resumed run decided %q, want recovered: %s", res.Decision, res.Reason)
	}
	if got := log.Count(c.WorkID + "|" + c.ActionID); got != 1 {
		t.Fatalf("crash resume must not double-apply the effect, got %d", got)
	}
}

func TestCrashRecoveryWithoutJournalFailsClosed(t *testing.T) {
	r, _ := newTestRunner(t)
	mk := func(prefix string) string { return prefix + hex32('c') }
	c := goldenmission.Identity{
		TenantID: mk("ten_"), PrincipalID: mk("prn_"),
		MissionID: mk("mis_"), AuthorityLeaseID: mk("auth_"),
		ExecutionContextID: mk("ctx_"), WorkID: mk("wrk_"),
		TraceID: mk("trc_"), ActionID: mk("act_"),
		ActionDecisionID: mk("pdr_"),
	}
	m := goldenmission.Mission{
		Branch:    goldenmission.BranchCrashRecovery,
		Canonical: c,
		Stages: []goldenmission.Stage{
			{Name: "runtime", IDs: goldenmission.Identity{
				TenantID: c.TenantID, PrincipalID: c.PrincipalID,
				MissionID: c.MissionID, AuthorityLeaseID: c.AuthorityLeaseID,
				TraceID: c.TraceID, ActionID: c.ActionID}},
		},
	}
	res, err := r.RunWithVerification(m, acceptVerification(c))
	if err != nil {
		t.Fatalf("RunWithVerification: %v", err)
	}
	if res.Decision == goldenmission.DecisionRecovered ||
		res.Decision == goldenmission.DecisionAccept {
		t.Fatalf("crash-recovery without journal recovery accepted: %s", res.Reason)
	}
}

func TestRecoveredRunnerRefusesDivergentMission(t *testing.T) {
	log := goldenmission.NewEffectLog()
	principal := "prn_" + hex32('1')
	r1, err := goldenmission.NewRunner(principal, log)
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	c := ids77()
	m := goldenmission.Mission{
		Branch:    goldenmission.BranchCrashRecovery,
		Canonical: c,
		Stages:    golden001Stages()[2:],
	}
	journal, err := r1.RunPrefix(m, "works")
	if err != nil {
		t.Fatalf("RunPrefix: %v", err)
	}
	r2, err := goldenmission.Recover(principal, log, journal)
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	// Same work/action, but a divergent mission identity must not resume
	// on another mission's journal.
	other := c
	other.MissionID = "mis_" + hex32('0')
	stages := golden001Stages()[2:]
	for i := range stages {
		stages[i].IDs.MissionID = other.MissionID
	}
	m2 := goldenmission.Mission{
		Branch:    goldenmission.BranchCrashRecovery,
		Canonical: other,
		Stages:    stages,
	}
	res, err := r2.RunWithVerification(m2, acceptVerification(other))
	if err != nil {
		t.Fatalf("RunWithVerification: %v", err)
	}
	if res.Decision == goldenmission.DecisionRecovered || res.Decision == goldenmission.DecisionAccept {
		t.Fatalf("divergent mission completed on another journal: %s", res.Reason)
	}
}

func TestVerifierFailureBranchFailsClosed(t *testing.T) {
	r, log := newTestRunner(t)
	c, stages := golden003()
	m := goldenmission.Mission{
		Branch:    goldenmission.BranchVerifierFailure,
		Canonical: c,
		Stages:    stages,
	}
	v := acceptVerification(c)
	v.Verdict = goldenmission.DecisionReject
	res, err := r.RunWithVerification(m, v)
	if err != nil {
		t.Fatalf("RunWithVerification: %v", err)
	}
	if res.Decision != goldenmission.DecisionReject {
		t.Fatalf("verifier-failure branch decided %q, want reject", res.Decision)
	}
	if got := log.Count(c.WorkID + "|" + c.ActionID); got != 0 {
		t.Fatalf("failed verification must apply no effect, got %d", got)
	}
}

func TestEmptyCanonicalFailsClosed(t *testing.T) {
	dec, _ := goldenmission.CheckChain(goldenmission.Identity{}, golden001Stages())
	if dec != goldenmission.DecisionReject {
		t.Fatal("empty canonical identity accepted")
	}
}

func TestMalformedIDsFailClosed(t *testing.T) {
	c := ids77()
	cases := map[string]func(*goldenmission.Identity){
		"tenant":    func(i *goldenmission.Identity) { i.TenantID = "ten_SHORT" },
		"principal": func(i *goldenmission.Identity) { i.PrincipalID = "prn_ZZZZ" },
		"lease":     func(i *goldenmission.Identity) { i.AuthorityLeaseID = "" },
		"trace":     func(i *goldenmission.Identity) { i.TraceID = "trc_" + hex32('g') },
		"action":    func(i *goldenmission.Identity) { i.ActionID = "act_" + hex32('7')[:31] },
		"mission":   func(i *goldenmission.Identity) { i.MissionID = "" },
	}
	for name, mutate := range cases {
		bad := c
		mutate(&bad)
		if dec, _ := goldenmission.CheckChain(bad, golden001Stages()); dec != goldenmission.DecisionReject {
			t.Fatalf("malformed %s accepted", name)
		}
	}
}

func TestUnknownBranchRejected(t *testing.T) {
	r, _ := newTestRunner(t)
	m := goldenmission.Mission{
		Branch:    "side-quest",
		Canonical: ids77(),
		Stages:    golden001Stages(),
	}
	if _, err := r.Run(m); err == nil {
		t.Fatal("unknown branch must error fail-closed")
	}
}

func TestUnknownStageRejected(t *testing.T) {
	stages := golden001Stages()
	stages[2].Name = "ministry-of-truth"
	if dec, _ := goldenmission.CheckChain(ids77(), stages); dec != goldenmission.DecisionReject {
		t.Fatal("unknown stage accepted")
	}
}

func TestOutOfOrderStagesRejected(t *testing.T) {
	stages := golden001Stages()
	stages[2], stages[4] = stages[4], stages[2] // works before trust-gateway
	if dec, _ := goldenmission.CheckChain(ids77(), stages); dec != goldenmission.DecisionReject {
		t.Fatal("out-of-order stages accepted")
	}
}

func TestTamperedPinDetected(t *testing.T) {
	r, _ := newTestRunner(t)
	c := ids77()
	m := goldenmission.Mission{
		Branch:    goldenmission.BranchSuccess,
		Canonical: c,
		Stages:    golden001Stages(),
	}
	res, err := r.RunWithVerification(m, acceptVerification(c))
	if err != nil {
		t.Fatalf("RunWithVerification: %v", err)
	}
	recomputed := goldenmission.PinFor(m, res.Decision, res.Reason)
	if recomputed != res.Pin {
		t.Fatal("pin must be a deterministic function of the transcript")
	}
	mutated := m
	mutated.Canonical.PrincipalID = "prn_" + hex32('9')
	if goldenmission.PinFor(mutated, res.Decision, res.Reason) == res.Pin {
		t.Fatal("tampered identity collides with the recorded pin")
	}
}

func TestSkeletonPinsMatchStoredFile(t *testing.T) {
	if os.Getenv("GOLDEN_UPDATE_PINS") == "1" {
		t.Skip("pin regeneration mode")
	}
	path := filepath.Join("testdata", "golden-pins.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("stored pins missing (generate with GOLDEN_UPDATE_PINS=1): %v", err)
	}
	var stored struct {
		Schema string            `json:"schema"`
		Head   string            `json:"head"`
		Pins   map[string]string `json:"pins"`
	}
	if err := json.Unmarshal(raw, &stored); err != nil {
		t.Fatalf("stored pins unparsable: %v", err)
	}
	if stored.Schema != "golden-mission-pins/0.1" {
		t.Fatalf("pins schema %q", stored.Schema)
	}
	if len(stored.Head) != 40 || strings.Trim(stored.Head, "0123456789abcdef") != "" {
		t.Fatalf("pins head must be a 40-hex exact-head SHA, got %q", stored.Head)
	}
	computed, err := goldenmission.ComputePins()
	if err != nil {
		t.Fatalf("ComputePins: %v", err)
	}
	for _, b := range []goldenmission.Branch{
		goldenmission.BranchSuccess, goldenmission.BranchRefusal,
		goldenmission.BranchRevocation, goldenmission.BranchCrashRecovery,
		goldenmission.BranchVerifierFailure,
	} {
		got, ok := computed[b]
		if !ok || got == "" {
			t.Fatalf("no skeleton pin for branch %q", b)
		}
		if stored.Pins[string(b)] != got {
			t.Fatalf("stored pin for %q does not match skeleton run (hand-edited?)", b)
		}
	}
}

func TestPinRegenerationIsDocumented(t *testing.T) {
	t.Log("regenerate with: GOLDEN_UPDATE_PINS=1 go test ./packages/goldenmission/ -run TestWritePins -count=1")
}

func TestWritePins(t *testing.T) {
	if os.Getenv("GOLDEN_UPDATE_PINS") != "1" {
		t.Skip("set GOLDEN_UPDATE_PINS=1 to regenerate the stored pins")
	}
	if err := goldenmission.WritePinsFile(filepath.Join("testdata", "golden-pins.json")); err != nil {
		t.Fatalf("WritePinsFile: %v", err)
	}
}
