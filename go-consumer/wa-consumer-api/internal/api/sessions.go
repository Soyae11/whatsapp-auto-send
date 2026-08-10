package api

import (
	"context"
	"sync"
	"time"

	"wa-shared/wa"
)

// SessionStatusTTL is how long a sender's status is trusted. Short enough that re-pairing a
// sender takes effect almost immediately, long enough that a burst of sends is one round trip
// to wa-gateway rather than one per message.
const SessionStatusTTL = 3 * time.Second

type SessionStatusSource interface {
	Status(ctx context.Context, sessionID string) (wa.SessionStatus, error)
}

type sessionCache struct {
	client *wa.Client
	ttl    time.Duration
	now    func() time.Time

	mu      sync.Mutex
	entries map[string]sessionEntry
}

type sessionEntry struct {
	status wa.SessionStatus
	at     time.Time
}

func NewSessionCache(client *wa.Client, ttl time.Duration) *sessionCache {
	if ttl <= 0 {
		ttl = SessionStatusTTL
	}
	return &sessionCache{
		client:  client,
		ttl:     ttl,
		now:     time.Now,
		entries: map[string]sessionEntry{},
	}
}

func (c *sessionCache) Status(ctx context.Context, sessionID string) (wa.SessionStatus, error) {
	if cached, ok := c.cached(sessionID); ok {
		return cached, nil
	}

	view, err := c.client.GetSession(ctx, sessionID)
	if err != nil {
		return "", err
	}

	c.mu.Lock()
	c.entries[sessionID] = sessionEntry{status: view.Status, at: c.now()}
	c.mu.Unlock()

	return view.Status, nil
}

func (c *sessionCache) cached(sessionID string) (wa.SessionStatus, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	e, ok := c.entries[sessionID]
	if !ok || c.now().Sub(e.at) > c.ttl {
		return "", false
	}
	return e.status, true
}
