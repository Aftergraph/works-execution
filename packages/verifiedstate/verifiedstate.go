// Package verifiedstate implements the verification lifecycle and typed
// dependency invalidation semantics for long-running WORKS execution.
//
// The package is deliberately independent of SQLite, HTTP, models, and workers.
// WORKS owns durable execution truth; this package defines the pure state law
// that persistence and API integrations can adopt without making an agent or
// controller authoritative.
//
// A consequential result is canonical only while it is VERIFIED. A checkpoint
// reconciliation may demote it to STALE or INVALIDATED; it must then pass
// through CANDIDATE and receive a fresh independent verification reference
// before it can become canonical again.
package verifiedstate

import (
	"errors"
	"fmt"
	"sort"
	"time"
)

// State is the verification state of one canonical subject.
type State string

const (
	StateUnknown     State = "UNKNOWN"
	StateCandidate   State = "CANDIDATE"
	StateVerified    State = "VERIFIED"
	StateStale       State = "STALE"
	StateInvalidated State = "INVALIDATED"
	StateRejected    State = "REJECTED"
)

// EdgeType describes why a downstream subject depends on an upstream subject.
//
// HARD invalidation edges: EFFECT, AUTHORITY, VERIFICATION.
// A change across one of these invalidates the downstream result.
//
// SOFT invalidation edges: DATA, RESOURCE, TEMPORAL, JOIN, EVENT.
// A change across one of these makes the downstream result stale and eligible
// for selective revalidation.
//
// Once INVALIDATED is reached on a path, invalidation dominates all downstream
// propagation even across a soft edge.
type EdgeType string

const (
	EdgeData         EdgeType = "DATA"
	EdgeEffect       EdgeType = "EFFECT"
	EdgeResource     EdgeType = "RESOURCE"
	EdgeAuthority    EdgeType = "AUTHORITY"
	EdgeTemporal     EdgeType = "TEMPORAL"
	EdgeVerification EdgeType = "VERIFICATION"
	EdgeJoin         EdgeType = "JOIN"
	EdgeEvent        EdgeType = "EVENT"
)

var (
	ErrUnknownNode         = errors.New("verifiedstate: unknown node")
	ErrDuplicateNode       = errors.New("verifiedstate: duplicate node")
	ErrInvalidState        = errors.New("verifiedstate: invalid state")
	ErrInvalidTransition   = errors.New("verifiedstate: invalid transition")
	ErrInvalidEdge         = errors.New("verifiedstate: invalid edge")
	ErrInvalidVerification = errors.New("verifiedstate: invalid verification reference")
	ErrNotCanonical        = errors.New("verifiedstate: subject is not canonical")
	ErrInvalidSnapshot     = errors.New("verifiedstate: invalid snapshot")
)

// VerificationRef points at verification truth already accepted by the owner
// boundary (for example a Sentinel verdict persisted by WORKS). It is a
// reference, not a verifier implementation; independence and signature checks
// remain the responsibility of the existing verification boundary.
type VerificationRef struct {
	VerifierID  string    `json:"verifier_id"`
	EvidenceRef string    `json:"evidence_ref"`
	SubjectRef  string    `json:"subject_ref"`
	VerifiedAt  time.Time `json:"verified_at"`
}

// Node is one canonical subject tracked by the verification lifecycle.
//
// Fingerprint is the fingerprint of the subject that was last verified. It is
// intentionally not overwritten by Reconcile: observed world state does not
// become canonical merely because it was observed.
type Node struct {
	ID           string           `json:"id"`
	State        State            `json:"state"`
	Fingerprint  string           `json:"fingerprint,omitempty"`
	Verification *VerificationRef `json:"verification,omitempty"`
}

// Edge is one typed dependency from From -> To.
type Edge struct {
	From string   `json:"from"`
	To   string   `json:"to"`
	Type EdgeType `json:"type"`
}

// Snapshot is a complete observation set for the subjects being reconciled.
// A missing key means the previously observed subject is now absent.
type Snapshot map[string]string

// Impact records the new verification state caused by reconciliation.
type Impact struct {
	NodeID string `json:"node_id"`
	State  State  `json:"state"`
}

// ReconcileResult is deterministic and suitable for evidence/audit recording.
type ReconcileResult struct {
	Changed  []string `json:"changed"`
	Missing  []string `json:"missing"`
	Added    []string `json:"added"`
	Impacted []Impact `json:"impacted"`
}

// Graph owns only the pure in-memory law. Durable ownership stays in WORKS.
type Graph struct {
	nodes    map[string]*Node
	outgoing map[string][]Edge
}

// NewGraph validates and constructs a typed verification dependency graph.
// Cycles are permitted because long-running EVENT/TEMPORAL relationships may
// be cyclic; propagation is monotone and therefore terminates.
func NewGraph(nodes []Node, edges []Edge) (*Graph, error) {
	g := &Graph{
		nodes:    make(map[string]*Node, len(nodes)),
		outgoing: make(map[string][]Edge),
	}
	for _, n := range nodes {
		if n.ID == "" {
			return nil, fmt.Errorf("%w: empty node id", ErrUnknownNode)
		}
		if !validState(n.State) {
			return nil, fmt.Errorf("%w: %s", ErrInvalidState, n.State)
		}
		if _, exists := g.nodes[n.ID]; exists {
			return nil, fmt.Errorf("%w: %s", ErrDuplicateNode, n.ID)
		}
		cp := n
		if n.Verification != nil {
			v := *n.Verification
			cp.Verification = &v
		}
		if cp.State == StateVerified {
			if cp.Fingerprint == "" || !validVerification(cp.Verification) {
				return nil, fmt.Errorf("%w: verified node %s requires fingerprint and verification", ErrInvalidVerification, cp.ID)
			}
		}
		g.nodes[n.ID] = &cp
	}
	for _, e := range edges {
		if e.From == "" || e.To == "" || e.From == e.To || !validEdgeType(e.Type) {
			return nil, fmt.Errorf("%w: %#v", ErrInvalidEdge, e)
		}
		if _, ok := g.nodes[e.From]; !ok {
			return nil, fmt.Errorf("%w: edge source %s", ErrUnknownNode, e.From)
		}
		if _, ok := g.nodes[e.To]; !ok {
			return nil, fmt.Errorf("%w: edge target %s", ErrUnknownNode, e.To)
		}
		g.outgoing[e.From] = append(g.outgoing[e.From], e)
	}
	for id := range g.outgoing {
		sort.Slice(g.outgoing[id], func(i, j int) bool {
			a, b := g.outgoing[id][i], g.outgoing[id][j]
			if a.To == b.To {
				return a.Type < b.Type
			}
			return a.To < b.To
		})
	}
	return g, nil
}

// Node returns a defensive copy of one subject.
func (g *Graph) Node(id string) (Node, bool) {
	n, ok := g.nodes[id]
	if !ok {
		return Node{}, false
	}
	cp := *n
	if n.Verification != nil {
		v := *n.Verification
		cp.Verification = &v
	}
	return cp, true
}

// BeginCandidate starts a fresh verification attempt. VERIFIED cannot be
// overwritten directly: it must first become STALE or INVALIDATED through an
// explicit freshness/invalidation event.
func (g *Graph) BeginCandidate(id string) error {
	n, err := g.mustNode(id)
	if err != nil {
		return err
	}
	switch n.State {
	case StateUnknown, StateStale, StateInvalidated, StateRejected:
		n.State = StateCandidate
		n.Verification = nil
		return nil
	case StateCandidate:
		return nil
	default:
		return fmt.Errorf("%w: %s -> %s", ErrInvalidTransition, n.State, StateCandidate)
	}
}

// Verify is the only path to VERIFIED. A proof reference and exact subject
// fingerprint are mandatory.
func (g *Graph) Verify(id, fingerprint string, proof VerificationRef) error {
	n, err := g.mustNode(id)
	if err != nil {
		return err
	}
	if n.State != StateCandidate {
		return fmt.Errorf("%w: %s -> %s", ErrInvalidTransition, n.State, StateVerified)
	}
	if fingerprint == "" || !validVerification(&proof) {
		return ErrInvalidVerification
	}
	n.State = StateVerified
	n.Fingerprint = fingerprint
	cp := proof
	cp.VerifiedAt = proof.VerifiedAt.UTC()
	n.Verification = &cp
	return nil
}

// Reject records an independent negative verdict for a candidate.
func (g *Graph) Reject(id string) error {
	n, err := g.mustNode(id)
	if err != nil {
		return err
	}
	if n.State != StateCandidate {
		return fmt.Errorf("%w: %s -> %s", ErrInvalidTransition, n.State, StateRejected)
	}
	n.State = StateRejected
	return nil
}

// CanCanonicalize enforces the central law: every requested subject must be
// VERIFIED and carry the fingerprint + verification reference that justified
// that state.
func (g *Graph) CanCanonicalize(ids ...string) error {
	if len(ids) == 0 {
		return fmt.Errorf("%w: no subjects requested", ErrNotCanonical)
	}
	for _, id := range ids {
		n, err := g.mustNode(id)
		if err != nil {
			return err
		}
		if n.State != StateVerified || n.Fingerprint == "" || !validVerification(n.Verification) {
			return fmt.Errorf("%w: %s is %s", ErrNotCanonical, id, n.State)
		}
	}
	return nil
}

// Snapshot returns the canonical fingerprints for the selected subjects.
// With no ids it snapshots all graph nodes. Nodes without a fingerprint fail
// closed because a resume checkpoint that cannot identify its prior subject
// cannot safely be reconciled.
func (g *Graph) Snapshot(ids ...string) (Snapshot, error) {
	if len(ids) == 0 {
		ids = make([]string, 0, len(g.nodes))
		for id := range g.nodes {
			ids = append(ids, id)
		}
		sort.Strings(ids)
	}
	out := make(Snapshot, len(ids))
	for _, id := range ids {
		n, err := g.mustNode(id)
		if err != nil {
			return nil, err
		}
		if n.Fingerprint == "" {
			return nil, fmt.Errorf("%w: %s has no fingerprint", ErrInvalidSnapshot, id)
		}
		out[id] = n.Fingerprint
	}
	return out, nil
}

// Reconcile compares the durable checkpoint observation with a fresh world
// observation, then computes the minimal typed dependency impact.
//
// Changed/missing roots become STALE. Propagation through EFFECT, AUTHORITY,
// or VERIFICATION edges becomes INVALIDATED. Soft edges propagate STALE.
// INVALIDATED dominates every downstream edge. Added subjects are reported but
// do not invalidate existing proof unless a declared dependency connects them.
//
// Reconcile mutates only verification state; it never replaces fingerprints or
// verification references with unverified observations.
func (g *Graph) Reconcile(checkpoint, current Snapshot) (ReconcileResult, error) {
	if checkpoint == nil || current == nil {
		return ReconcileResult{}, ErrInvalidSnapshot
	}
	if err := g.validateSnapshot(checkpoint); err != nil {
		return ReconcileResult{}, err
	}
	if err := g.validateSnapshot(current); err != nil {
		return ReconcileResult{}, err
	}

	result := ReconcileResult{}
	for id, oldFP := range checkpoint {
		newFP, ok := current[id]
		switch {
		case !ok:
			result.Missing = append(result.Missing, id)
		case oldFP != newFP:
			result.Changed = append(result.Changed, id)
		}
	}
	for id := range current {
		if _, ok := checkpoint[id]; !ok {
			result.Added = append(result.Added, id)
		}
	}
	sort.Strings(result.Changed)
	sort.Strings(result.Missing)
	sort.Strings(result.Added)

	// severity: 1=STALE, 2=INVALIDATED. Keeping only the strongest known
	// severity makes propagation a monotone fixed-point and guarantees
	// termination even when EVENT/TEMPORAL edges form cycles.
	severity := map[string]int{}
	queue := make([]string, 0, len(result.Changed)+len(result.Missing))
	for _, id := range append(append([]string{}, result.Changed...), result.Missing...) {
		if severity[id] < 1 {
			severity[id] = 1
			queue = append(queue, id)
		}
	}

	for len(queue) > 0 {
		from := queue[0]
		queue = queue[1:]
		base := severity[from]
		for _, edge := range g.outgoing[from] {
			next := base
			if hardInvalidation(edge.Type) || base == 2 {
				next = 2
			} else {
				next = 1
			}
			if severity[edge.To] >= next {
				continue
			}
			severity[edge.To] = next
			queue = append(queue, edge.To)
		}
	}

	ids := make([]string, 0, len(severity))
	for id := range severity {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		state := StateStale
		if severity[id] == 2 {
			state = StateInvalidated
		}
		g.nodes[id].State = state
		result.Impacted = append(result.Impacted, Impact{NodeID: id, State: state})
	}
	return result, nil
}

func (g *Graph) validateSnapshot(s Snapshot) error {
	for id, fp := range s {
		if id == "" || fp == "" {
			return fmt.Errorf("%w: empty id/fingerprint", ErrInvalidSnapshot)
		}
		if _, ok := g.nodes[id]; !ok {
			return fmt.Errorf("%w: snapshot subject %s", ErrUnknownNode, id)
		}
	}
	return nil
}

func (g *Graph) mustNode(id string) (*Node, error) {
	n, ok := g.nodes[id]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnknownNode, id)
	}
	return n, nil
}

func validState(s State) bool {
	switch s {
	case StateUnknown, StateCandidate, StateVerified, StateStale, StateInvalidated, StateRejected:
		return true
	default:
		return false
	}
}

func validEdgeType(t EdgeType) bool {
	switch t {
	case EdgeData, EdgeEffect, EdgeResource, EdgeAuthority, EdgeTemporal, EdgeVerification, EdgeJoin, EdgeEvent:
		return true
	default:
		return false
	}
}

func hardInvalidation(t EdgeType) bool {
	switch t {
	case EdgeEffect, EdgeAuthority, EdgeVerification:
		return true
	default:
		return false
	}
}

func validVerification(v *VerificationRef) bool {
	return v != nil &&
		v.VerifierID != "" &&
		v.EvidenceRef != "" &&
		v.SubjectRef != "" &&
		!v.VerifiedAt.IsZero()
}
