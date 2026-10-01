package workspace

import (
	"context"
	"errors"
	"fmt"
	"time"
)

type ReapResult struct {
	Scanned int
	Expired int
	Reaped  int
	Failed  int
}

type Reaper struct {
	registry  Registry
	providers map[string]Provider
}

func NewReaper(registry Registry, providers map[string]Provider) (*Reaper, error) {
	if registry == nil {
		return nil, fmt.Errorf("%w: workspace registry is required", ErrMalformed)
	}
	if len(providers) == 0 {
		return nil, fmt.Errorf("%w: workspace providers are required", ErrMalformed)
	}
	copyProviders := make(map[string]Provider, len(providers))
	for id, p := range providers {
		if id == "" || p == nil || p.ID() != id {
			return nil, fmt.Errorf("%w: invalid workspace provider registration %q", ErrMalformed, id)
		}
		copyProviders[id] = p
	}
	return &Reaper{registry: registry, providers: copyProviders}, nil
}

func (r *Reaper) Sweep(ctx context.Context, now time.Time) (ReapResult, error) {
	var result ReapResult
	items, err := r.registry.List(ctx)
	if err != nil {
		return result, err
	}
	result.Scanned = len(items)
	for _, ws := range items {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if ws.ExpiresAt.IsZero() || ws.ExpiresAt.After(now) {
			continue
		}
		result.Expired++
		p := r.providers[ws.ProviderID]
		if p == nil {
			result.Failed++
			continue
		}
		err := p.Destroy(ctx, ws)
		if err != nil && !errors.Is(err, ErrNotFound) {
			result.Failed++
			continue
		}
		if err := r.registry.Delete(ctx, ws.ID); err != nil && !errors.Is(err, ErrNotFound) {
			result.Failed++
			continue
		}
		result.Reaped++
	}
	return result, nil
}
