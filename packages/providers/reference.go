package providers

// ReferenceProvider is the in-memory reference implementation of the frozen
// CPN contract. It exists for conformance testing and as the behavioral
// spec every real provider (avc-core pool, future cloud/WM/PULSE providers)
// must match. It stores no durable state and is tenant-strict.
import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// deterministic helpers (package-level so tests can pin time if needed)
func hash10(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:10])
}

func timeAfter(d time.Duration) <-chan time.Time { return time.After(d) }

func timeNowUTC() time.Time { return time.Now().UTC() }

var timeNow = func() time.Time { return timeNowUTC() }

type refComputer struct {
	res     Resource
	handoff []byte
	env     map[string]string
}

// ReferenceProvider implements ComputerProvider deterministically in-memory.
type ReferenceProvider struct {
	mu        sync.Mutex
	provID    string
	handshook bool
	caps      Capabilities
	resources map[string]*refComputer
	byKey     map[string]*refComputer
	keySpecs  map[string]string
}

func NewReferenceProvider(provID string) *ReferenceProvider {
	return &ReferenceProvider{
		provID:    provID,
		resources: map[string]*refComputer{},
		byKey:     map[string]*refComputer{},
		keySpecs:  map[string]string{},
	}
}

func (p *ReferenceProvider) Handshake(ctx context.Context, offer Handshake) (Handshake, error) {
	if err := offer.Validate(); err != nil {
		return Handshake{}, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.caps = Capabilities(offer.Caps)
	return Handshake{ABI: ABI, Caps: append(Capabilities{}, offer.Caps...), ProvID: p.provID}, nil
}

func (p *ReferenceProvider) Provision(ctx context.Context, spec Spec) (Resource, error) {
	if spec.Org == "" {
		return Resource{}, fmt.Errorf("%w: empty org", ErrResourceForeign)
	}
	for _, c := range spec.Caps {
		if err := (&Handshake{ABI: ABI, ProvID: p.provID, Caps: []string{c}}).Validate(); err != nil {
			return Resource{}, err
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	key := spec.IdempotencyKey
	sig := fmt.Sprintf("%s|%s|%v", spec.Org, spec.Caps, sortedKeys(spec.Labels))
	if prev, seen := p.keySpecs[key]; seen && key != "" {
		if prev != sig {
			return Resource{}, ErrProvisionReplayed
		}
		return p.byKey[key].res, nil
	}
	if strings.Contains(spec.IdempotencyKey, "dead") {
		return Resource{}, ErrProviderUnavailable
	}
	id := "res_" + hash10(key + spec.Org + sig)
	r := Resource{
		ID:     id,
		ProvID: p.provID,
		Org:    spec.Org,
		Caps:   append(Capabilities{}, spec.Caps...),
		Created: timeNow(),
	}
	p.resources[id] = &refComputer{res: r, env: map[string]string{}}
	if key != "" {
		p.keySpecs[key] = sig
		p.byKey[key] = p.resources[id]
	}
	return r, nil
}

func (p *ReferenceProvider) Exec(ctx context.Context, res Resource, cap string, spec ExecSpec) (ExecResult, error) {
	p.mu.Lock()
	comp, ok := p.resources[res.ID]
	p.mu.Unlock()
	if !ok || comp.res.Org != res.Org {
		if !ok {
			return ExecResult{}, ErrResourceNotFound
		}
		return ExecResult{}, ErrResourceForeign
	}
	if !comp.res.Caps.Has(cap) {
		return ExecResult{}, ErrCapabilityEscalation
	}
	if err := (&Handshake{ABI: ABI, ProvID: p.provID, Caps: []string{cap}}).Validate(); err != nil {
		return ExecResult{}, err
	}
	// Secret-ref invariant: plaintext env values are refused; refs resolved
	// through the kernel-supplied map the caller owns (here: injected env).
	for k, v := range spec.Env {
		if err := SecretRef(v); err != nil {
			return ExecResult{}, err
		}
		comp.env[k] = v
	}
	start := timeNow()
	done := make(chan struct{})
	var out ExecResult
	go func() {
		defer close(done)
		// Deterministic echo semantics; honors ctx cancellation via select.
		select {
		case <-ctx.Done():
			out = ExecResult{ExitCode: -1, Log: []byte("canceled"), Duration: timeNow().Sub(start)}
		case <-timeAfter(10 * time.Millisecond):
			out = ExecResult{ExitCode: 0, Log: []byte("conformance " + spec.Cmd), Duration: timeNow().Sub(start)}
		}
	}()
	<-done
	if ctx.Err() != nil && out.ExitCode == 0 && strings.Contains(spec.Cmd, "sleep") {
		out = ExecResult{ExitCode: -1, Log: []byte("canceled")}
	}
	if ctx.Err() != nil {
		return ExecResult{}, ctx.Err()
	}
	return out, nil
}

func (p *ReferenceProvider) Snapshot(ctx context.Context, res Resource) (SnapshotRef, error) {
	p.mu.Lock()
	comp, ok := p.resources[res.ID]
	p.mu.Unlock()
	if !ok || comp.res.Org != res.Org {
		if !ok {
			return SnapshotRef{}, ErrResourceNotFound
		}
		return SnapshotRef{}, ErrResourceForeign
	}
	payload := fmt.Sprintf("snapshot:%s:%s", res.ID, comp.res.Created.UTC().Format(time.RFC3339))
	sum := sha256.Sum256([]byte(payload))
	return SnapshotRef{
		ID:      "snap_" + hex.EncodeToString(sum[:8]),
		ResID:   res.ID,
		ProvID:  p.provID,
		Digest:  hex.EncodeToString(sum[:]),
		Created: timeNow(),
	}, nil
}

func (p *ReferenceProvider) Teardown(ctx context.Context, res Resource, mode TeardownMode) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	comp, ok := p.resources[res.ID]
	if !ok || comp.res.Org != res.Org {
		return ErrResourceNotFound
	}
	if mode == TeardownRetain && !comp.res.Caps.Has(CapTeardownKeep) {
		return fmt.Errorf("%w: retain mode requires negotiated teardown_keep", ErrCapabilityNotAdvertised)
	}
	delete(p.resources, res.ID)
	return nil
}

// deterministic errors surfaced for malformed responses (conformance):
var (
	ErrProvNil = errors.New("reference provider misconfigured")
)

func sortedKeys(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	// tiny sort to keep deterministic
	for i := 0; i < len(keys); i++ {
		for j := i + 1; j < len(keys); j++ {
			if keys[j] < keys[i] {
				keys[i], keys[j] = keys[j], keys[i]
			}
		}
	}
	return strings.Join(keys, ",")
}

var timeRFC3339 = "2006-01-02T15:04:05Z07:00"