package senders

import (
	"context"
	"sync/atomic"
	"time"
)

// Cache holds a Registry that can be refreshed after boot without disrupting readers — the
// DB-backed replacement for a WA_SENDERS registry built once and never touched again. Reads
// (Load) never block on a refresh in progress; a refresh that fails leaves the last-good
// Registry in place rather than clearing it, since a stale sender list is far less harmful to
// an in-flight send than an empty one.
type Cache struct {
	ptr  atomic.Pointer[Registry]
	load func(ctx context.Context) (*Registry, error)
}

// NewCache wraps an already-loaded initial Registry with a loader used for every subsequent
// refresh. load is called with a short-lived context each time — see Refresh and Watch.
func NewCache(initial *Registry, load func(ctx context.Context) (*Registry, error)) *Cache {
	c := &Cache{load: load}
	c.ptr.Store(initial)
	return c
}

// Load returns the current Registry snapshot. Safe to call concurrently with Refresh.
func (c *Cache) Load() *Registry {
	return c.ptr.Load()
}

// Refresh re-runs the loader and swaps in its result. Call this immediately after an admin
// mutation (create/delete sender) for snappier consistency than waiting for the next poll.
func (c *Cache) Refresh(ctx context.Context) error {
	r, err := c.load(ctx)
	if err != nil {
		return err
	}
	c.ptr.Store(r)
	return nil
}

// Watch polls Refresh on interval until ctx is cancelled. onError is called (if non-nil) with
// any refresh failure — the last-good Registry keeps serving in the meantime.
func (c *Cache) Watch(ctx context.Context, interval time.Duration, onError func(error)) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := c.Refresh(ctx); err != nil && onError != nil {
				onError(err)
			}
		}
	}
}
