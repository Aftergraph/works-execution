package economic

import (
	"errors"
	"sort"
	"strings"
)

type EvidenceSourceClass string

const (
	EvidenceRegistry       EvidenceSourceClass = "registry"
	EvidenceCustody        EvidenceSourceClass = "custody"
	EvidenceRepresentation EvidenceSourceClass = "representation"
)

type SourceObservationMeta struct {
	SourceClass    EvidenceSourceClass
	SourceID       string
	EvidenceHash   string
	ObservedAtUnix int64
	Generation     int64
}

type EvidenceCoherencePolicy struct {
	NowUnix        int64
	MaxAgeSeconds  int64
	MaxSkewSeconds int64
	MaxFutureSkewSeconds int64
}

type EvidenceCoherenceResult struct {
	Schema                  string
	State                   string
	Final                   bool
	ExternalEffects         int
	RefreshRequired         bool
	ObservedSources         []string
	OldestObservedAtUnix    int64
	NewestObservedAtUnix    int64
	Reasons                 []string
}

func validEvidenceHash(v string) bool {
	if len(v) != len("sha256:")+64 || !strings.HasPrefix(v, "sha256:") {
		return false
	}
	for _, c := range v[len("sha256:"):] {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

func EvaluateEvidenceCoherence(
	policy EvidenceCoherencePolicy,
	observations []SourceObservationMeta,
	previousGenerations map[string]int64,
) (EvidenceCoherenceResult, error) {
	if policy.NowUnix <= 0 || policy.MaxAgeSeconds <= 0 || policy.MaxSkewSeconds < 0 || policy.MaxFutureSkewSeconds < 0 {
		return EvidenceCoherenceResult{}, errors.New("economic evidence coherence policy invalid")
	}

	out := EvidenceCoherenceResult{
		Schema:          "aftergraph.economic-evidence-coherence/v1",
		State:           "TEMPORALLY_COHERENT",
		Final:           false,
		ExternalEffects: 0,
		RefreshRequired: false,
	}

	fail := func(reason string) {
		out.State = "EVIDENCE_REFRESH_REQUIRED"
		out.RefreshRequired = true
		out.Reasons = append(out.Reasons, reason)
	}

	required := map[EvidenceSourceClass]bool{
		EvidenceRegistry: false,
		EvidenceCustody: false,
		EvidenceRepresentation: false,
	}
	seenClass := map[EvidenceSourceClass]struct{}{}
	seenEvidence := map[string]string{}
	oldest := int64(0)
	newest := int64(0)

	for _, obs := range observations {
		if _, ok := required[obs.SourceClass]; !ok {
			fail(string(obs.SourceClass) + ":unsupported_source_class")
			continue
		}
		if _, dup := seenClass[obs.SourceClass]; dup {
			fail(string(obs.SourceClass) + ":duplicate_source_class")
			continue
		}
		seenClass[obs.SourceClass] = struct{}{}
		required[obs.SourceClass] = true

		if obs.SourceID == "" {
			fail(string(obs.SourceClass) + ":source_id_missing")
		}
		if !validEvidenceHash(obs.EvidenceHash) {
			fail(string(obs.SourceClass) + ":evidence_hash_invalid")
		} else if prior, exists := seenEvidence[obs.EvidenceHash]; exists {
			fail(string(obs.SourceClass) + ":evidence_reused_with_" + prior)
		} else {
			seenEvidence[obs.EvidenceHash] = string(obs.SourceClass)
		}

		if obs.Generation <= 0 {
			fail(string(obs.SourceClass) + ":generation_invalid")
		}
		if prior, ok := previousGenerations[obs.SourceID]; ok && obs.Generation <= prior {
			fail(string(obs.SourceClass) + ":generation_replayed_or_regressed")
		}

		if obs.ObservedAtUnix <= 0 {
			fail(string(obs.SourceClass) + ":observed_at_invalid")
		} else {
			if obs.ObservedAtUnix > policy.NowUnix+policy.MaxFutureSkewSeconds {
				fail(string(obs.SourceClass) + ":future_timestamp")
			}
			if policy.NowUnix-obs.ObservedAtUnix > policy.MaxAgeSeconds {
				fail(string(obs.SourceClass) + ":stale")
			}
			if oldest == 0 || obs.ObservedAtUnix < oldest {
				oldest = obs.ObservedAtUnix
			}
			if obs.ObservedAtUnix > newest {
				newest = obs.ObservedAtUnix
			}
		}

		out.ObservedSources = append(out.ObservedSources, string(obs.SourceClass)+":"+obs.SourceID)
	}

	for class, present := range required {
		if !present {
			fail(string(class) + ":missing")
		}
	}

	if oldest > 0 && newest > 0 && newest-oldest > policy.MaxSkewSeconds {
		fail("source_observation_skew_exceeded")
	}

	out.OldestObservedAtUnix = oldest
	out.NewestObservedAtUnix = newest
	sort.Strings(out.ObservedSources)
	sort.Strings(out.Reasons)

	// Freshness/coherence is evidence quality only, never settlement or legal finality.
	out.Final = false
	out.ExternalEffects = 0
	return out, nil
}
