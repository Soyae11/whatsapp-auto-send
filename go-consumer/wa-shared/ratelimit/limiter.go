package ratelimit

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

//go:embed limit.lua
var limitScript string

const KeyPrefix = "wa:ratelimit:"

const DefaultWindow = time.Minute

func Key(keyID string) string { return KeyPrefix + keyID }

type Limiter struct {
	rdb    redis.Scripter
	script *redis.Script
	window time.Duration
}

type Result struct {
	Allowed   bool
	Limit     int
	Remaining int
	Reset     time.Duration
}

func (r Result) RetryAfter() int {
	secs := int((r.Reset + time.Second - 1) / time.Second)
	if secs < 1 {
		return 1
	}
	return secs
}

func New(rdb redis.Scripter, window time.Duration) (*Limiter, error) {
	if window <= 0 {
		return nil, fmt.Errorf("ratelimit: window must be positive, got %s", window)
	}
	return &Limiter{rdb: rdb, script: redis.NewScript(limitScript), window: window}, nil
}

func (l *Limiter) Allow(ctx context.Context, keyID string, limit int) (Result, error) {
	if keyID == "" {
		return Result{}, errors.New("ratelimit: empty key id")
	}
	if limit <= 0 {
		return Result{}, fmt.Errorf("ratelimit: limit must be positive, got %d", limit)
	}

	res, err := l.script.Run(ctx, l.rdb, []string{Key(keyID)}, limit, l.window.Milliseconds()).Slice()
	if err != nil {
		return Result{}, fmt.Errorf("ratelimit: check %s: %w", keyID, err)
	}
	if len(res) != 3 {
		return Result{}, fmt.Errorf("ratelimit: script returned %d values, want 3", len(res))
	}

	allowed, err := parseInt64(res[0])
	if err != nil {
		return Result{}, fmt.Errorf("ratelimit: bad allowed flag: %w", err)
	}
	remaining, err := parseInt64(res[1])
	if err != nil {
		return Result{}, fmt.Errorf("ratelimit: bad remaining count: %w", err)
	}
	ttl, err := parseInt64(res[2])
	if err != nil {
		return Result{}, fmt.Errorf("ratelimit: bad ttl: %w", err)
	}

	return Result{
		Allowed:   allowed == 1,
		Limit:     limit,
		Remaining: int(remaining),
		Reset:     time.Duration(ttl) * time.Millisecond,
	}, nil
}

func parseInt64(v any) (int64, error) {
	switch t := v.(type) {
	case int64:
		return t, nil
	case int:
		return int64(t), nil
	case string:
		return strconv.ParseInt(t, 10, 64)
	default:
		return 0, fmt.Errorf("unexpected redis reply type %T", v)
	}
}
