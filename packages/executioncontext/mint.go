package executioncontext

import "github.com/JonasAbde/works-execution/packages/workgraph"

// MintCorrelationIDs generates the two WORKS-owned correlation identities that
// a durable dispatch acceptance binds to a Runtime execution: the
// execution-context id (ctx_) and the trace id (trc_). Per execution-context/1.0
// ownership, clients cannot choose either field, so minting lives in the
// identity-owner package and reuses the single crypto/rand generator
// (workgraph.NewID) that CreateExecutionContext already trusts. The returned
// IDs satisfy the contract patterns ^ctx_[a-f0-9]{32}$ and ^trc_[a-f0-9]{32}$.
func MintCorrelationIDs() (executionContextID, traceID string) {
	return workgraph.NewID("ctx"), workgraph.NewID("trc")
}
