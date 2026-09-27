package verifiedstate

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

func proof(subject string) VerificationRef {
	return VerificationRef{
		VerifierID:  "sentinel:test",
		EvidenceRef: "dvr_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		SubjectRef:  subject,
		VerifiedAt:  time.Date(2026, 9, 27, 8, 0, 0, 0, time.UTC),
	}
}

func verifiedNode(id, fp string) Node {
	p := proof("git:Aftergraph/example@" + "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	return Node{
		ID:           id,
		State:        StateVerified,
		Fingerprint:  fp,
		Verification: &p,
	}
}

func TestCanonicalizationRequiresFreshIndependentProof(t *testing.T) {
	g, err := NewGraph([]Node{{ID: "build", State: StateUnknown}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := g.CanCanonicalize("build"); !errors.Is(err, ErrNotCanonical) {
		t.Fatalf("unknown node canonicalization error = %v, want ErrNotCanonical", err)
	}
	if err := g.BeginCandidate("build"); err != nil {
		t.Fatal(err)
	}
	if err := g.CanCanonicalize("build"); !errors.Is(err, ErrNotCanonical) {
		t.Fatalf("candidate canonicalization error = %v, want ErrNotCanonical", err)
	}
	if err := g.Verify("build", "sha256:artifact-v1", proof("git:Aftergraph/example@bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")); err != nil {
		t.Fatal(err)
	}
	if err := g.CanCanonicalize("build"); err != nil {
		t.Fatalf("verified node should canonicalize: %v", err)
	}

	checkpoint, err := g.Snapshot("build")
	if err != nil {
		t.Fatal(err)
	}
	_, err = g.Reconcile(checkpoint, Snapshot{"build": "sha256:artifact-v2"})
	if err != nil {
		t.Fatal(err)
	}
	if err := g.CanCanonicalize("build"); !errors.Is(err, ErrNotCanonical) {
		t.Fatalf("stale proof canonicalization error = %v, want ErrNotCanonical", err)
	}
	if err := g.BeginCandidate("build"); err != nil {
		t.Fatal(err)
	}
	if err := g.Verify("build", "sha256:artifact-v2", proof("git:Aftergraph/example@cccccccccccccccccccccccccccccccccccccccc")); err != nil {
		t.Fatal(err)
	}
	if err := g.CanCanonicalize("build"); err != nil {
		t.Fatalf("reverified node should canonicalize: %v", err)
	}
}

func TestTypedInvalidationPropagationIsSelective(t *testing.T) {
	nodes := []Node{
		verifiedNode("source", "v1"),
		verifiedNode("compile", "v1"),
		verifiedNode("security", "v1"),
		verifiedNode("deploy", "v1"),
		verifiedNode("cache", "v1"),
		verifiedNode("unrelated", "v1"),
	}
	edges := []Edge{
		{From: "source", To: "compile", Type: EdgeData},
		{From: "compile", To: "security", Type: EdgeVerification},
		{From: "security", To: "deploy", Type: EdgeData},
		{From: "source", To: "cache", Type: EdgeResource},
	}
	g, err := NewGraph(nodes, edges)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint, err := g.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	current := Snapshot{
		"source":    "v2",
		"compile":   "v1",
		"security":  "v1",
		"deploy":    "v1",
		"cache":     "v1",
		"unrelated": "v1",
	}
	got, err := g.Reconcile(checkpoint, current)
	if err != nil {
		t.Fatal(err)
	}
	want := ReconcileResult{
		Changed: []string{"source"},
		Impacted: []Impact{
			{NodeID: "cache", State: StateStale},
			{NodeID: "compile", State: StateStale},
			{NodeID: "deploy", State: StateInvalidated},
			{NodeID: "security", State: StateInvalidated},
			{NodeID: "source", State: StateStale},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("reconcile = %#v, want %#v", got, want)
	}
	if n, _ := g.Node("unrelated"); n.State != StateVerified {
		t.Fatalf("unrelated state = %s, want VERIFIED", n.State)
	}
	if err := g.CanCanonicalize("source", "compile", "security", "deploy", "cache"); !errors.Is(err, ErrNotCanonical) {
		t.Fatalf("affected set must not remain canonical: %v", err)
	}
	if err := g.CanCanonicalize("unrelated"); err != nil {
		t.Fatalf("unrelated verified subject should remain canonical: %v", err)
	}
}

func TestAuthorityChangeHardInvalidatesDependentOutcome(t *testing.T) {
	nodes := []Node{
		verifiedNode("authority", "lease:7"),
		verifiedNode("effect", "effect:1"),
		verifiedNode("outcome", "outcome:1"),
	}
	edges := []Edge{
		{From: "authority", To: "effect", Type: EdgeAuthority},
		{From: "effect", To: "outcome", Type: EdgeData},
	}
	g, err := NewGraph(nodes, edges)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint, _ := g.Snapshot()
	current := Snapshot{
		"authority": "lease:8",
		"effect":    "effect:1",
		"outcome":   "outcome:1",
	}
	if _, err := g.Reconcile(checkpoint, current); err != nil {
		t.Fatal(err)
	}
	if n, _ := g.Node("authority"); n.State != StateStale {
		t.Fatalf("authority root = %s, want STALE", n.State)
	}
	if n, _ := g.Node("effect"); n.State != StateInvalidated {
		t.Fatalf("effect = %s, want INVALIDATED", n.State)
	}
	if n, _ := g.Node("outcome"); n.State != StateInvalidated {
		t.Fatalf("invalidated severity must dominate downstream soft edge; got %s", n.State)
	}
}

func TestResumeReconciliationReportsMissingAndAdded(t *testing.T) {
	nodes := []Node{
		verifiedNode("repo-head", "git:a"),
		verifiedNode("build", "build:a"),
		verifiedNode("new-observation", "obs:1"),
	}
	edges := []Edge{
		{From: "repo-head", To: "build", Type: EdgeData},
	}
	g, err := NewGraph(nodes, edges)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint := Snapshot{
		"repo-head": "git:a",
		"build":     "build:a",
	}
	current := Snapshot{
		"build":           "build:a",
		"new-observation": "obs:1",
	}
	got, err := g.Reconcile(checkpoint, current)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Missing, []string{"repo-head"}) {
		t.Fatalf("missing = %v", got.Missing)
	}
	if !reflect.DeepEqual(got.Added, []string{"new-observation"}) {
		t.Fatalf("added = %v", got.Added)
	}
	if n, _ := g.Node("repo-head"); n.State != StateStale {
		t.Fatalf("missing root = %s, want STALE", n.State)
	}
	if n, _ := g.Node("build"); n.State != StateStale {
		t.Fatalf("data dependent = %s, want STALE", n.State)
	}
	if n, _ := g.Node("new-observation"); n.State != StateVerified {
		t.Fatalf("added observation alone must not invalidate existing graph state; got %s", n.State)
	}
}

func TestCyclicTemporalGraphTerminates(t *testing.T) {
	nodes := []Node{
		verifiedNode("watch", "tick:1"),
		verifiedNode("wake", "wake:1"),
	}
	edges := []Edge{
		{From: "watch", To: "wake", Type: EdgeEvent},
		{From: "wake", To: "watch", Type: EdgeTemporal},
	}
	g, err := NewGraph(nodes, edges)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint, _ := g.Snapshot()
	current := Snapshot{"watch": "tick:2", "wake": "wake:1"}
	got, err := g.Reconcile(checkpoint, current)
	if err != nil {
		t.Fatal(err)
	}
	want := []Impact{
		{NodeID: "wake", State: StateStale},
		{NodeID: "watch", State: StateStale},
	}
	if !reflect.DeepEqual(got.Impacted, want) {
		t.Fatalf("impacted = %#v, want %#v", got.Impacted, want)
	}
}

func TestVerifiedNodeCannotBeSilentlyOverwritten(t *testing.T) {
	g, err := NewGraph([]Node{verifiedNode("release", "sha256:v1")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := g.BeginCandidate("release"); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("BeginCandidate on VERIFIED = %v, want ErrInvalidTransition", err)
	}
}

func TestInvalidGraphAndSnapshotFailClosed(t *testing.T) {
	if _, err := NewGraph([]Node{{ID: "a", State: State("MAYBE")}}, nil); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("invalid state error = %v", err)
	}
	if _, err := NewGraph(
		[]Node{{ID: "a", State: StateUnknown}},
		[]Edge{{From: "a", To: "missing", Type: EdgeData}},
	); !errors.Is(err, ErrUnknownNode) {
		t.Fatalf("unknown edge target error = %v", err)
	}

	g, err := NewGraph([]Node{verifiedNode("a", "v1")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.Reconcile(Snapshot{"a": "v1"}, Snapshot{"foreign": "v1"}); !errors.Is(err, ErrUnknownNode) {
		t.Fatalf("foreign observation error = %v, want ErrUnknownNode", err)
	}
}
