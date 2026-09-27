package verifiedstate

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
)

// HandoffStateKey is the reserved handoff.state_snapshot key carrying a
// verification-aware resume checkpoint. It lives inside the frozen
// handoff.schema/1.0 payload, so adopting this primitive does not mutate the
// handoff wire contract.
const HandoffStateKey = "aftergraph_verified_state"

// CheckpointSchema versions the value stored at HandoffStateKey.
const CheckpointSchema = "verified-state-checkpoint/0.1"

var ErrInvalidCheckpoint = errors.New("verifiedstate: invalid checkpoint")

// Checkpoint is the verification-aware portion of a durable handoff.
//
// Nodes + Edges capture the exact verification dependency graph at suspension.
// Snapshot contains the subset of canonical subjects that must be re-observed
// before resume. Every graph node must be VERIFIED at checkpoint creation;
// Snapshot fingerprints must equal their corresponding canonical node
// fingerprints. This makes a checkpoint a record of verified truth, not a bag
// of agent assertions.
type Checkpoint struct {
	Schema   string   `json:"schema"`
	Nodes    []Node   `json:"nodes"`
	Edges    []Edge   `json:"edges,omitempty"`
	Snapshot Snapshot `json:"snapshot"`
}

// NewCheckpoint constructs and validates a verification-aware handoff
// checkpoint. snapshot may monitor a subset of graph nodes; this is useful for
// dependencies whose world state can change while other verified outcomes are
// immutable artifacts.
func NewCheckpoint(nodes []Node, edges []Edge, snapshot Snapshot) (Checkpoint, error) {
	cp := Checkpoint{
		Schema:   CheckpointSchema,
		Nodes:    cloneNodes(nodes),
		Edges:    append([]Edge(nil), edges...),
		Snapshot: cloneSnapshot(snapshot),
	}
	if err := cp.Validate(); err != nil {
		return Checkpoint{}, err
	}
	return cp, nil
}

// Validate proves that the checkpoint contains a canonical verified graph and
// that every monitored fingerprint is exactly the fingerprint that was
// verified. It fails closed on partial or contradictory checkpoint state.
func (c Checkpoint) Validate() error {
	if c.Schema != CheckpointSchema {
		return fmt.Errorf("%w: schema %q, want %q", ErrInvalidCheckpoint, c.Schema, CheckpointSchema)
	}
	if len(c.Nodes) == 0 {
		return fmt.Errorf("%w: nodes are required", ErrInvalidCheckpoint)
	}
	if len(c.Snapshot) == 0 {
		return fmt.Errorf("%w: snapshot is required", ErrInvalidCheckpoint)
	}

	g, err := NewGraph(c.Nodes, c.Edges)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidCheckpoint, err)
	}

	ids := make([]string, 0, len(c.Nodes))
	for _, n := range c.Nodes {
		ids = append(ids, n.ID)
	}
	sort.Strings(ids)
	if err := g.CanCanonicalize(ids...); err != nil {
		return fmt.Errorf("%w: graph is not fully verified: %v", ErrInvalidCheckpoint, err)
	}

	if err := g.validateSnapshot(c.Snapshot); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidCheckpoint, err)
	}
	for id, fingerprint := range c.Snapshot {
		n, ok := g.Node(id)
		if !ok || n.Fingerprint != fingerprint {
			return fmt.Errorf("%w: snapshot fingerprint for %s does not match canonical verified fingerprint", ErrInvalidCheckpoint, id)
		}
	}
	return nil
}

// Reconcile compares this durable checkpoint with a fresh observation. The
// returned impact is deterministic. No live observation becomes canonical.
func (c Checkpoint) Reconcile(current Snapshot) (ReconcileResult, error) {
	if err := c.Validate(); err != nil {
		return ReconcileResult{}, err
	}
	g, err := NewGraph(c.Nodes, c.Edges)
	if err != nil {
		return ReconcileResult{}, fmt.Errorf("%w: %v", ErrInvalidCheckpoint, err)
	}
	result, err := g.Reconcile(c.Snapshot, current)
	if err != nil {
		return ReconcileResult{}, err
	}
	return result, nil
}

// AttachToStateSnapshot installs a validated checkpoint under the reserved key
// in a handoff state_snapshot. The value is normalized through JSON so callers
// observe the same generic shape before and after durable handoff round-trips.
func AttachToStateSnapshot(state map[string]any, checkpoint Checkpoint) error {
	if state == nil {
		return fmt.Errorf("%w: handoff state_snapshot is nil", ErrInvalidCheckpoint)
	}
	if err := checkpoint.Validate(); err != nil {
		return err
	}
	raw, err := json.Marshal(checkpoint)
	if err != nil {
		return fmt.Errorf("%w: encode: %v", ErrInvalidCheckpoint, err)
	}
	var normalized any
	if err := json.Unmarshal(raw, &normalized); err != nil {
		return fmt.Errorf("%w: normalize: %v", ErrInvalidCheckpoint, err)
	}
	state[HandoffStateKey] = normalized
	return nil
}

// CheckpointFromStateSnapshot returns (zero,false,nil) for a legacy handoff
// with no verification-aware checkpoint. Once the reserved key exists, any
// malformed or unsupported value fails closed.
func CheckpointFromStateSnapshot(state map[string]any) (Checkpoint, bool, error) {
	if state == nil {
		return Checkpoint{}, false, nil
	}
	value, ok := state[HandoffStateKey]
	if !ok {
		return Checkpoint{}, false, nil
	}

	switch typed := value.(type) {
	case Checkpoint:
		cp := cloneCheckpoint(typed)
		if err := cp.Validate(); err != nil {
			return Checkpoint{}, true, err
		}
		return cp, true, nil
	case *Checkpoint:
		if typed == nil {
			return Checkpoint{}, true, fmt.Errorf("%w: nil checkpoint", ErrInvalidCheckpoint)
		}
		cp := cloneCheckpoint(*typed)
		if err := cp.Validate(); err != nil {
			return Checkpoint{}, true, err
		}
		return cp, true, nil
	}

	raw, err := json.Marshal(value)
	if err != nil {
		return Checkpoint{}, true, fmt.Errorf("%w: encode embedded checkpoint: %v", ErrInvalidCheckpoint, err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var cp Checkpoint
	if err := dec.Decode(&cp); err != nil {
		return Checkpoint{}, true, fmt.Errorf("%w: decode embedded checkpoint: %v", ErrInvalidCheckpoint, err)
	}
	if err := cp.Validate(); err != nil {
		return Checkpoint{}, true, err
	}
	return cp, true, nil
}

func cloneCheckpoint(in Checkpoint) Checkpoint {
	return Checkpoint{
		Schema:   in.Schema,
		Nodes:    cloneNodes(in.Nodes),
		Edges:    append([]Edge(nil), in.Edges...),
		Snapshot: cloneSnapshot(in.Snapshot),
	}
}

func cloneNodes(in []Node) []Node {
	out := make([]Node, 0, len(in))
	for _, n := range in {
		cp := n
		if n.Verification != nil {
			v := *n.Verification
			cp.Verification = &v
		}
		out = append(out, cp)
	}
	return out
}

func cloneSnapshot(in Snapshot) Snapshot {
	if in == nil {
		return nil
	}
	out := make(Snapshot, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
