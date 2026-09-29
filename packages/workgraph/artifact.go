package workgraph

// MaxArtifactBytes is the maximum size of one artifact transferred through
// the WORKS worker completion API. The limit is part of the wire contract so
// workers can fail the node before sending an unbounded request.
const MaxArtifactBytes = 32 << 20
