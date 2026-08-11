package slots

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"math/rand/v2"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

//go:embed allocate.lua
var allocateScript string
const KeyPrefix = "wa:slot:"

type Config struct {
	Gap time.Duration

	MinSpread time.Duration
	MaxSpread time.Duration

	Horizon time.Duration

	TTL time.Duration

	// CriticalGap is the minimum spacing used for expedited (critical-lane) sends instead
	// of Gap. It paces critical sends only against each other, never against the ordinary
	// lattice — anchoring it to the next ordinary slot at all, even loosely, drags a
	// "near-term" critical send out to nearly a full Gap away once there's a backlog. It's a
	// short floor, not no floor: still enough that a burst of critical sends fans out
	// instead of landing on one instant.
	CriticalGap time.Duration
}

func DefaultConfig() Config {
	return Config{
		// MinSpread == Gap deliberately: it pins the jitter's negative side at zero (see
		// applyJitter) so two consecutive sends can never land closer together than Gap -
		// (MaxSpread - Gap) = 130s. A lower MinSpread would let one send's positive jitter
		// and the next one's zero jitter cancel down toward that shorter floor — fine at a
		// 6s gap, not fine when the point of the gap is staying clear of a same-number ban.
		Gap:       150 * time.Second,
		MinSpread: 150 * time.Second,
		MaxSpread: 170 * time.Second,
		Horizon:   6 * time.Hour,
		TTL:       7 * time.Hour,

		// Short enough that a critical alert doesn't sit behind a backlog, long enough that
		// a burst of them still looks like separate human sends rather than one instant.
		CriticalGap: 30 * time.Second,
	}
}

func (c Config) Validate() error {
	switch {
	case c.Gap <= 0:
		return errors.New("slot gap must be positive")
	case c.MinSpread <= 0:
		return errors.New("slot min spread must be positive")
	case c.MinSpread > c.Gap:
		return fmt.Errorf("slot min spread (%s) must not exceed gap (%s)", c.MinSpread, c.Gap)
	case c.MaxSpread < c.Gap:
		return fmt.Errorf("slot max spread (%s) must be at least gap (%s)", c.MaxSpread, c.Gap)
	case c.Horizon <= c.Gap:
		return fmt.Errorf("slot horizon (%s) must exceed gap (%s)", c.Horizon, c.Gap)
	case c.TTL <= c.Horizon:
		return fmt.Errorf("slot ttl (%s) must exceed horizon (%s), or a deep backlog loses its slot key", c.TTL, c.Horizon)
	case c.CriticalGap <= 0:
		return errors.New("slot critical gap must be positive")
	case c.CriticalGap > c.Gap:
		return fmt.Errorf("slot critical gap (%s) must not exceed gap (%s), or critical is slower than ordinary traffic", c.CriticalGap, c.Gap)
	}
	return nil
}

type HorizonError struct {
	SessionID string
	Slot      time.Time
	Depth     time.Duration
	Horizon   time.Duration
}

func (e *HorizonError) Error() string {
	return fmt.Sprintf("slot horizon exceeded for session %s: next free slot is %s out, limit is %s",
		e.SessionID, e.Depth.Round(time.Second), e.Horizon)
}

var ErrHorizonExceeded = errors.New("slot horizon exceeded")

func (e *HorizonError) Is(target error) bool { return target == ErrHorizonExceeded }

type Allocator struct {
	rdb    redis.Scripter
	script *redis.Script
	cfg    Config

	now    func() time.Time
	jitter func(min, max time.Duration) time.Duration
}

type Option func(*Allocator)

func WithClock(fn func() time.Time) Option {
	return func(a *Allocator) { a.now = fn }
}

func WithJitter(fn func(min, max time.Duration) time.Duration) Option {
	return func(a *Allocator) { a.jitter = fn }
}

func New(rdb redis.Scripter, cfg Config, opts ...Option) (*Allocator, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	a := &Allocator{
		rdb:    rdb,
		script: redis.NewScript(allocateScript),
		cfg:    cfg,
		now:    time.Now,
		jitter: uniformJitter,
	}
	for _, o := range opts {
		o(a)
	}
	return a, nil
}

func Key(sessionID string) string { return KeyPrefix + sessionID }

func HeadKey(sessionID string) string { return KeyPrefix + sessionID + ":head" }

func (a *Allocator) Next(ctx context.Context, sessionID string, expedite bool) (time.Time, bool, error) {
	if sessionID == "" {
		return time.Time{}, false, errors.New("slots: empty session id")
	}

	expediteArg := "0"
	if expedite {
		expediteArg = "1"
	}

	now := a.now()
	res, err := a.script.Run(ctx, a.rdb,
		[]string{Key(sessionID), HeadKey(sessionID)},
		now.UnixMilli(),
		a.cfg.Gap.Milliseconds(),
		a.cfg.Horizon.Milliseconds(),
		a.cfg.TTL.Milliseconds(),
		expediteArg,
		a.cfg.CriticalGap.Milliseconds(),
	).Slice()
	if err != nil {
		return time.Time{}, false, fmt.Errorf("slots: allocate for session %s: %w", sessionID, err)
	}
	if len(res) != 3 {
		return time.Time{}, false, fmt.Errorf("slots: allocate returned %d values, want 3", len(res))
	}

	slotMS, err := parseInt64(res[0])
	if err != nil {
		return time.Time{}, false, fmt.Errorf("slots: allocate returned a bad slot: %w", err)
	}
	rejected, err := parseInt64(res[1])
	if err != nil {
		return time.Time{}, false, fmt.Errorf("slots: allocate returned a bad flag: %w", err)
	}
	expedited, err := parseInt64(res[2])
	if err != nil {
		return time.Time{}, false, fmt.Errorf("slots: allocate returned a bad flag: %w", err)
	}

	slot := time.UnixMilli(slotMS)
	if rejected == 1 {
		return time.Time{}, false, &HorizonError{
			SessionID: sessionID,
			Slot:      slot,
			Depth:     slot.Sub(now),
			Horizon:   a.cfg.Horizon,
		}
	}

	return a.applyJitter(slot, now, expedited == 1), expedited == 1, nil
}

func (a *Allocator) applyJitter(slot, now time.Time, expedited bool) time.Time {
	low, high := a.cfg.MinSpread-a.cfg.Gap, a.cfg.MaxSpread-a.cfg.Gap
	if expedited {
		bound := a.cfg.CriticalGap / 4
		low = max(low, -bound)
		high = min(high, bound)
	}

	out := slot.Add(a.jitter(low, high))
	if out.Before(now) {
		return now
	}
	return out
}

func uniformJitter(min, max time.Duration) time.Duration {
	if max <= min {
		return min
	}
	return min + rand.N(max-min+1)
}

func (a *Allocator) Peek(ctx context.Context, sessionID string) (depth time.Duration, err error) {
	res, err := a.rdb.Eval(ctx, `return redis.call('GET', KEYS[1]) or '0'`,
		[]string{Key(sessionID)}).Result()
	if err != nil {
		return 0, fmt.Errorf("slots: peek session %s: %w", sessionID, err)
	}
	stored, err := parseInt64(res)
	if err != nil {
		return 0, fmt.Errorf("slots: peek session %s: %w", sessionID, err)
	}
	d := time.UnixMilli(stored).Sub(a.now())
	if d < 0 {
		return 0, nil
	}
	return d, nil
}

func parseInt64(v any) (int64, error) {
	switch t := v.(type) {
	case string:
		return strconv.ParseInt(t, 10, 64)
	case int64:
		return t, nil
	case int:
		return int64(t), nil
	default:
		return 0, fmt.Errorf("unexpected redis reply type %T", v)
	}
}
