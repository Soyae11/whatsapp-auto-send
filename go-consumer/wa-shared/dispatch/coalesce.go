package dispatch

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"

	"wa-shared/tasks"
)

//go:embed coalesce.lua
var coalesceScript string

const CoalesceKeyPrefix = "wa:coalesce:"

const CoalesceSeparator = "\n\n"

type CoalesceConfig struct {
	Window time.Duration

	MaxMessages int

	MinLead time.Duration
}

func DefaultCoalesceConfig() CoalesceConfig {
	return CoalesceConfig{
		Window:      10 * time.Second,
		MaxMessages: 5,
		MinLead:     2 * time.Second,
	}
}

func (c CoalesceConfig) Enabled() bool { return c.Window > 0 }

func (c CoalesceConfig) Validate() error {
	if !c.Enabled() {
		return nil
	}
	switch {
	case c.MaxMessages < 2:
		return fmt.Errorf("coalesce max messages must be at least 2, got %d", c.MaxMessages)
	case c.MinLead <= 0:
		return errors.New("coalesce min lead must be positive")
	}
	return nil
}

func CoalesceKey(sessionID string, p tasks.Priority, to string) string {
	return CoalesceKeyPrefix + sessionID + ":" + string(p) + ":" + to
}

func coalescable(p tasks.Priority) bool { return p != tasks.PriorityCritical }

type Coalescer struct {
	rdb    redis.Scripter
	script *redis.Script
	cfg    CoalesceConfig
}

func NewCoalescer(rdb redis.Scripter, cfg CoalesceConfig) (*Coalescer, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &Coalescer{rdb: rdb, script: redis.NewScript(coalesceScript), cfg: cfg}, nil
}

type group struct {
	openKey string
	count   int
}

func (c *Coalescer) claim(ctx context.Context, sessionID string, p tasks.Priority, to, key string) (group, error) {
	return c.run(ctx, sessionID, p, to, key, false)
}

func (c *Coalescer) reopen(ctx context.Context, sessionID string, p tasks.Priority, to, key string) error {
	_, err := c.run(ctx, sessionID, p, to, key, true)
	return err
}

func (c *Coalescer) run(ctx context.Context, sessionID string, p tasks.Priority, to, key string, force bool) (group, error) {
	forceArg := "0"
	if force {
		forceArg = "1"
	}

	res, err := c.script.Run(ctx, c.rdb,
		[]string{CoalesceKey(sessionID, p, to)},
		key,
		c.cfg.Window.Milliseconds(),
		c.cfg.MaxMessages,
		forceArg,
	).Slice()
	if err != nil {
		return group{}, fmt.Errorf("dispatch: coalesce claim for session %s: %w", sessionID, err)
	}
	if len(res) != 2 {
		return group{}, fmt.Errorf("dispatch: coalesce claim returned %d values, want 2", len(res))
	}

	openKey, ok := res[0].(string)
	if !ok {
		return group{}, fmt.Errorf("dispatch: coalesce claim returned %T for the open key", res[0])
	}
	count, err := coalesceCount(res[1])
	if err != nil {
		return group{}, fmt.Errorf("dispatch: coalesce claim returned a bad count: %w", err)
	}
	return group{openKey: openKey, count: count}, nil
}

func coalesceCount(v any) (int, error) {
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
