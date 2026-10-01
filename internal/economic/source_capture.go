package economic

import "errors"

type RuntimeEconomicSourceCursorV1 struct {
	Kind  string
	Value string
}

type RuntimeEconomicRightCaptureV1 struct {
	Schema          string
	SourceClass     EvidenceSourceClass
	SourceID        string
	Generation      int64
	Cursor          RuntimeEconomicSourceCursorV1
	ObservedAtUnix  int64
	EvidenceHash    string
	RecordDigest    string
	CaptureHash     string
	SourceTransport string
	Final           bool
	ExternalEffects int
}

func SourceObservationFromRuntimeCapture(c RuntimeEconomicRightCaptureV1) (SourceObservationMeta, error) {
	if c.Schema != "aftergraph.economic-right-capture/v1" {
		return SourceObservationMeta{}, errors.New("economic source capture schema mismatch")
	}
	switch c.SourceClass {
	case EvidenceRegistry, EvidenceCustody, EvidenceRepresentation:
	default:
		return SourceObservationMeta{}, errors.New("economic source capture class invalid")
	}
	if c.SourceID == "" {
		return SourceObservationMeta{}, errors.New("economic source capture id required")
	}
	if c.Generation <= 0 {
		return SourceObservationMeta{}, errors.New("economic source capture generation invalid")
	}
	if c.ObservedAtUnix <= 0 {
		return SourceObservationMeta{}, errors.New("economic source capture observation time invalid")
	}
	if !validEvidenceHash(c.EvidenceHash) || !validEvidenceHash(c.RecordDigest) || !validEvidenceHash(c.CaptureHash) {
		return SourceObservationMeta{}, errors.New("economic source capture hash invalid")
	}
	switch c.Cursor.Kind {
	case "version", "sequence", "block", "offset", "etag":
	default:
		return SourceObservationMeta{}, errors.New("economic source capture cursor kind invalid")
	}
	if c.Cursor.Value == "" {
		return SourceObservationMeta{}, errors.New("economic source capture cursor value required")
	}
	if c.SourceTransport != "READ_ONLY" {
		return SourceObservationMeta{}, errors.New("economic source capture transport must be read-only")
	}
	if c.Final {
		return SourceObservationMeta{}, errors.New("economic source capture finality forbidden")
	}
	if c.ExternalEffects != 0 {
		return SourceObservationMeta{}, errors.New("economic source capture external effects forbidden")
	}

	return SourceObservationMeta{
		SourceClass:    c.SourceClass,
		SourceID:       c.SourceID,
		EvidenceHash:   c.EvidenceHash,
		ObservedAtUnix: c.ObservedAtUnix,
		Generation:     c.Generation,
	}, nil
}
