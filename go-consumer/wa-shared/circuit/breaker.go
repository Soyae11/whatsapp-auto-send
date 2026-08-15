package circuit

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"

	"wa-shared/tasks"
)

//go:embed breaker.lua
var failureScript string

//go:embed open.lua
var openScript string

const KeyPrefix = "wa:circuit:"

const (
	ReasonFailures = "consecutive_failures"
	ReasonManual   = "manual"
)

type Config struct {
	Threshold  int
	FailureTTL time.Duration
}

func DefaultConfig() Config {
	return Config{
		Threshold:  5,
		FailureTTL: 30 * time.Minute,
	}
}

func (c Config) Validate() error {
	switch {
	case c.Threshold <= 0:
		return errors.New("circuit threshold must be positive")
	case c.FailureTTL <= 0:
		return errors.New("circuit failure ttl must be positive")
	}
	return nil
}

type State struct {
	SessionID string    `json:"session_id"`
	Open      bool      `json:"open"`
	Reason    string    `json:"reason,omitempty"`
	ErrorCode string    `json:"error_code,omitempty"`
	OpenedAt  time.Time `json:"opened_at,omitzero"`
	Failures  int       `json:"failures"`
}

type QueueInspector interface {
	PauseQueue(qname string) error
	UnpauseQueue(qname string) error
}

type Breaker struct {
	rdb        redis.Cmdable
	inspector  QueueInspector
	script     *redis.Script
	openScript *redis.Script
	cfg        Config
	log        *slog.Logger
}

func New(rdb redis.Cmdable, inspector QueueInspector, cfg Config, log *slog.Logger) (*Breaker, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if rdb == nil || inspector == nil {
		return nil, errors.New("circuit: redis client and inspector are both required")
	}
	if log == nil {
		log = slog.Default()
	}

	return &Breaker{
		rdb:        rdb,
		inspector:  inspector,
		script:     redis.NewScript(failureScript),
		openScript: redis.NewScript(openScript),
		cfg:        cfg,
		log:        log,
	}, nil
}

func StateKey(sessionID string) string    { return KeyPrefix + sessionID }
func FailuresKey(sessionID string) string { return KeyPrefix + sessionID + ":failures" }

func (b *Breaker) Failure(ctx context.Context, sessionID, errorCode string) (opened bool, err error) {
	if sessionID == "" {
		return false, errors.New("circuit: empty session id")
	}

	state := State{
		SessionID: sessionID,
		Open:      true,
		Reason:    ReasonFailures,
		ErrorCode: errorCode,
		OpenedAt:  time.Now().UTC(),
		Failures:  b.cfg.Threshold,
	}
	encoded, err := json.Marshal(state)
	if err != nil {
		return false, fmt.Errorf("circuit: encode state: %w", err)
	}

	res, err := b.script.Run(ctx, b.rdb,
		[]string{FailuresKey(sessionID), StateKey(sessionID)},
		b.cfg.Threshold,
		b.cfg.FailureTTL.Milliseconds(),
		encoded,
	).Slice()
	if err != nil {
		return false, fmt.Errorf("circuit: record failure for %s: %w", sessionID, err)
	}
	if len(res) != 2 {
		return false, fmt.Errorf("circuit: failure script returned %d values, want 2", len(res))
	}

	failures, err := parseInt(res[0])
	if err != nil {
		return false, fmt.Errorf("circuit: failure script returned a bad count: %w", err)
	}
	crossed, err := parseInt(res[1])
	if err != nil {
		return false, fmt.Errorf("circuit: failure script returned a bad flag: %w", err)
	}

	if crossed != 1 {
		return false, nil
	}

	b.ensurePaused(sessionID)
	b.log.Error("circuit opened",
		"alert", true,
		"session_id", sessionID,
		"reason", ReasonFailures,
		"error_code", errorCode,
		"failures", failures,
		"threshold", b.cfg.Threshold,
		"queues", tasks.QueuesFor(sessionID))

	return true, nil
}

func (b *Breaker) Success(ctx context.Context, sessionID string) error {
	if sessionID == "" {
		return errors.New("circuit: empty session id")
	}
	if err := b.rdb.Del(ctx, FailuresKey(sessionID)).Err(); err != nil {
		return fmt.Errorf("circuit: reset failures for %s: %w", sessionID, err)
	}
	return nil
}

// Open opens a session's circuit and reports whether this call is what changed the state.
//
// A circuit that is already open is normally left exactly as it is. The exception is a manual
// pause, which takes over a circuit the breaker had tripped by itself: the two look identical
// from the queues' side, but only one survives the watcher. Watcher.evaluate closes any circuit
// whose reason is not ReasonManual once the session polls healthy again, so a pause that failed to
// overwrite an automatic reason would be undone a few polls later — the session resumes sending
// while an operator has every reason to believe they stopped it. The takeover is one-way: nothing
// automatic overwrites a manual pause, which continues to need an explicit Close.
func (b *Breaker) Open(ctx context.Context, sessionID, reason, errorCode string) (opened bool, err error) {
	if sessionID == "" {
		return false, errors.New("circuit: empty session id")
	}

	state := State{
		SessionID: sessionID,
		Open:      true,
		Reason:    reason,
		ErrorCode: errorCode,
		OpenedAt:  time.Now().UTC(),
	}
	encoded, err := json.Marshal(state)
	if err != nil {
		return false, fmt.Errorf("circuit: encode state: %w", err)
	}

	set, err := b.openScript.Run(ctx, b.rdb,
		[]string{StateKey(sessionID)},
		encoded,
		reason,
		ReasonManual,
	).Int()
	if err != nil {
		return false, fmt.Errorf("circuit: open %s: %w", sessionID, err)
	}

	b.ensurePaused(sessionID)
	if set == 1 {
		b.log.Error("circuit opened",
			"alert", true,
			"session_id", sessionID,
			"reason", reason,
			"error_code", errorCode,
			"queues", tasks.QueuesFor(sessionID))
	}
	return set == 1, nil
}

func (b *Breaker) Close(ctx context.Context, sessionID string) (closed bool, err error) {
	if sessionID == "" {
		return false, errors.New("circuit: empty session id")
	}

	// Claim the transition before touching the queues. Unpausing first meant a complaint
	// from any one lane aborted the close with the state key still set, leaving a circuit
	// that could never close while its lanes quietly drained.
	removed, err := b.rdb.Del(ctx, StateKey(sessionID)).Result()
	if err != nil {
		return false, fmt.Errorf("circuit: close %s: %w", sessionID, err)
	}

	b.ensureUnpaused(sessionID)

	if err := b.rdb.Del(ctx, FailuresKey(sessionID)).Err(); err != nil {
		return removed == 1, fmt.Errorf("circuit: reset failures for %s: %w", sessionID, err)
	}

	if removed == 1 {
		b.log.Info("circuit closed",
			"alert", true,
			"session_id", sessionID,
			"queues", tasks.QueuesFor(sessionID))
	}
	return removed == 1, nil
}

func (b *Breaker) State(ctx context.Context, sessionID string) (State, error) {
	if sessionID == "" {
		return State{}, errors.New("circuit: empty session id")
	}

	vals, err := b.rdb.MGet(ctx, StateKey(sessionID), FailuresKey(sessionID)).Result()
	if err != nil {
		return State{}, fmt.Errorf("circuit: state for %s: %w", sessionID, err)
	}
	if len(vals) != 2 {
		return State{}, fmt.Errorf("circuit: state for %s returned %d values, want 2", sessionID, len(vals))
	}

	out := State{SessionID: sessionID}

	if raw, ok := vals[0].(string); ok {
		if err := json.Unmarshal([]byte(raw), &out); err != nil {
			return State{}, fmt.Errorf("circuit: decode state for %s: %w", sessionID, err)
		}
		out.SessionID = sessionID
		out.Open = true
	}

	if raw, ok := vals[1].(string); ok {
		n, err := strconv.Atoi(raw)
		if err != nil {
			return State{}, fmt.Errorf("circuit: decode failures for %s: %w", sessionID, err)
		}
		out.Failures = n
	}

	return out, nil
}

func (b *Breaker) ensurePaused(sessionID string) {
	for _, q := range tasks.QueuesFor(sessionID) {
		if err := b.inspector.PauseQueue(q); err != nil {
			b.log.Debug("pause reported an error, lane taken as already paused",
				"queue", q, "error", err)
		}
	}
}

func (b *Breaker) ensureUnpaused(sessionID string) {
	for _, q := range tasks.QueuesFor(sessionID) {
		if err := b.inspector.UnpauseQueue(q); err != nil {
			b.log.Debug("unpause reported an error, lane taken as already running",
				"queue", q, "error", err)
		}
	}
}

func parseInt(v any) (int, error) {
	switch t := v.(type) {
	case string:
		return strconv.Atoi(t)
	case int64:
		return int(t), nil
	case int:
		return t, nil
	default:
		return 0, fmt.Errorf("unexpected redis reply type %T", v)
	}
}
