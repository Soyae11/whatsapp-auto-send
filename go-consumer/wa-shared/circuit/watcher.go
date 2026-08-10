package circuit

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"wa-shared/wa"
)

type SessionSource interface {
	ListSessions(ctx context.Context) ([]wa.SessionView, error)
}

type WatchConfig struct {
	Interval     time.Duration
	HealthyPolls int
	PollTimeout  time.Duration
}

func DefaultWatchConfig() WatchConfig {
	return WatchConfig{
		Interval:     20 * time.Second,
		HealthyPolls: 2,
		PollTimeout:  10 * time.Second,
	}
}

func (c WatchConfig) Validate() error {
	switch {
	case c.Interval <= 0:
		return errors.New("health poll interval must be positive")
	case c.HealthyPolls < 2:
		return errors.New("health poll streak must be at least 2, or one lucky poll unpauses a flapping session")
	case c.PollTimeout <= 0:
		return errors.New("health poll timeout must be positive")
	}
	return nil
}

type Watcher struct {
	breaker  *Breaker
	sessions SessionSource
	ids      []string
	cfg      WatchConfig
	log      *slog.Logger

	streak  map[string]int
	probing bool
}

func NewWatcher(b *Breaker, sessions SessionSource, ids []string, cfg WatchConfig, log *slog.Logger) (*Watcher, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if b == nil || sessions == nil {
		return nil, errors.New("circuit: watcher needs a breaker and a session source")
	}
	if len(ids) == 0 {
		return nil, errors.New("circuit: watcher needs at least one session")
	}
	if log == nil {
		log = slog.Default()
	}

	return &Watcher{
		breaker:  b,
		sessions: sessions,
		ids:      ids,
		cfg:      cfg,
		log:      log,
		streak:   make(map[string]int, len(ids)),
		probing:  true,
	}, nil
}

func (w *Watcher) Run(ctx context.Context) {
	t := time.NewTicker(w.cfg.Interval)
	defer t.Stop()

	w.log.Info("circuit watcher started",
		"sessions", w.ids,
		"interval", w.cfg.Interval.String(),
		"healthy_polls_to_close", w.cfg.HealthyPolls)

	for {
		select {
		case <-ctx.Done():
			w.log.Info("circuit watcher stopped")
			return
		case <-t.C:
			w.Poll(ctx)
		}
	}
}

func (w *Watcher) Poll(ctx context.Context) {
	pollCtx, cancel := context.WithTimeout(ctx, w.cfg.PollTimeout)
	defer cancel()

	views, err := w.sessions.ListSessions(pollCtx)
	if err != nil {
		if w.probing {
			w.log.Warn("health poll failed, circuits stay as they are", "error", err)
			w.probing = false
		}
		clear(w.streak)
		return
	}
	if !w.probing {
		w.log.Info("health poll recovered")
		w.probing = true
	}

	healthy := make(map[string]bool, len(views))
	for _, v := range views {
		healthy[v.ID] = v.Status == wa.StatusConnected && v.Health.SocketConnected
	}

	for _, id := range w.ids {
		w.evaluate(ctx, id, healthy[id])
	}
}

func (w *Watcher) evaluate(ctx context.Context, sessionID string, healthy bool) {
	state, err := w.breaker.State(ctx, sessionID)
	if err != nil {
		w.log.Error("could not read circuit state", "session_id", sessionID, "error", err)
		return
	}

	// A manual pause is a human decision and outlives any number of healthy polls.
	if !state.Open || state.Reason == ReasonManual {
		delete(w.streak, sessionID)
		return
	}

	if !healthy {
		delete(w.streak, sessionID)
		return
	}

	w.streak[sessionID]++
	if w.streak[sessionID] < w.cfg.HealthyPolls {
		w.log.Info("session healthy, waiting for a longer streak before closing the circuit",
			"session_id", sessionID,
			"streak", w.streak[sessionID],
			"need", w.cfg.HealthyPolls)
		return
	}

	delete(w.streak, sessionID)
	if _, err := w.breaker.Close(ctx, sessionID); err != nil {
		w.log.Error("could not close circuit", "session_id", sessionID, "error", err)
	}
}
