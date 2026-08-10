package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/hibiken/asynq"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"wa-shared/circuit"
	"wa-shared/dispatch"
	"wa-shared/ratelimit"
	"wa-shared/senders"
	"wa-shared/slots"
)

const minAPIKeyLen = 16

type Config struct {
	RedisURL        string
	DatabaseURL     string
	WAServiceURL    string
	WAServiceAPIKey string

	AdminAPIKey string

	// GatewayWebhookSecret is wa-gateway's WEBHOOK_SECRET, used to verify the receipts it
	// posts to us. Empty means the receipt endpoint refuses everything, so delivered and
	// read simply never arrive — which is a degradation, not a failure.
	GatewayWebhookSecret string

	// CORSOrigins is an exact-match allowlist for browser callers. Empty means no
	// Access-Control-* headers are sent at all, which is today's behaviour and stays the
	// default — this is opt-in per deployment, not a blanket "open the API to browsers".
	CORSOrigins []string

	APIAddr  string
	LogLevel string

	WATimeout     time.Duration
	WASendTimeout time.Duration

	Slots slots.Config

	Coalesce dispatch.CoalesceConfig

	Circuit circuit.Config
	Watch   circuit.WatchConfig

	Sessions []string

	Senders *senders.Registry

	RateLimitWindow time.Duration

	redisOpt *redis.Options
}

func (c *Config) RedisOptions() *redis.Options {
	o := *c.redisOpt
	return &o
}

func (c *Config) AsynqRedisOpt() asynq.RedisClientOpt {
	return asynq.RedisClientOpt{
		Addr:     c.redisOpt.Addr,
		Username: c.redisOpt.Username,
		Password: c.redisOpt.Password,
		DB:       c.redisOpt.DB,
	}
}

func Load() (*Config, error) {
	var problems []string
	fail := func(format string, args ...any) {
		problems = append(problems, fmt.Sprintf(format, args...))
	}

	c := &Config{
		RedisURL:        strings.TrimSpace(os.Getenv("REDIS_URL")),
		DatabaseURL:     strings.TrimSpace(os.Getenv("DATABASE_URL")),
		WAServiceURL:    strings.TrimSpace(os.Getenv("WA_SERVICE_URL")),
		WAServiceAPIKey: os.Getenv("WA_SERVICE_API_KEY"),

		GatewayWebhookSecret: os.Getenv("WA_GATEWAY_WEBHOOK_SECRET"),
		AdminAPIKey:          os.Getenv("ADMIN_API_KEY"),
		APIAddr:              envOr("API_ADDR", ":8080"),
		LogLevel:             strings.ToLower(envOr("LOG_LEVEL", "info")),
	}

	switch {
	case c.RedisURL == "":
		fail("REDIS_URL is required (e.g. redis://redis:6379/0)")
	default:
		opt, err := redis.ParseURL(c.RedisURL)
		if err != nil {
			fail("REDIS_URL is not a valid redis url: %v", err)
		} else {
			c.redisOpt = opt
		}
	}

	switch {
	case c.DatabaseURL == "":
		fail("DATABASE_URL is required (e.g. postgres://user:pass@postgres:5432/dispatcher)")
	default:
		if _, err := pgxpool.ParseConfig(c.DatabaseURL); err != nil {
			fail("DATABASE_URL is not a valid postgres url: %v", err)
		}
	}

	switch {
	case c.WAServiceURL == "":
		fail("WA_SERVICE_URL is required (e.g. http://wa-gateway:3000)")
	default:
		u, err := url.Parse(c.WAServiceURL)
		switch {
		case err != nil:
			fail("WA_SERVICE_URL is not a valid url: %v", err)
		case u.Scheme != "http" && u.Scheme != "https":
			fail("WA_SERVICE_URL must be http or https, got %q", u.Scheme)
		case u.Host == "":
			fail("WA_SERVICE_URL has no host")
		default:
			c.WAServiceURL = strings.TrimRight(u.String(), "/")
		}
	}

	switch {
	case c.WAServiceAPIKey == "":
		fail("WA_SERVICE_API_KEY is required")
	case len(c.WAServiceAPIKey) < minAPIKeyLen:
		fail("WA_SERVICE_API_KEY must be at least %d characters (wa-gateway rejects shorter keys)", minAPIKeyLen)
	}

	switch {
	case c.AdminAPIKey == "":
		fail("ADMIN_API_KEY is required; it guards the /internal operator routes, which can pause a session and requeue a send")
	case len(c.AdminAPIKey) < minAPIKeyLen:
		fail("ADMIN_API_KEY must be at least %d characters", minAPIKeyLen)
	case c.AdminAPIKey == c.WAServiceAPIKey:
		fail("ADMIN_API_KEY must differ from WA_SERVICE_API_KEY; they guard different trust boundaries")
	}

	if !validLogLevels[c.LogLevel] {
		fail("LOG_LEVEL must be one of debug, info, warn, error; got %q", c.LogLevel)
	}

	var err error
	if c.WATimeout, err = durationOr("WA_TIMEOUT", 30*time.Second); err != nil {
		fail("%v", err)
	}
	if c.WASendTimeout, err = durationOr("WA_SEND_TIMEOUT", 60*time.Second); err != nil {
		fail("%v", err)
	}

	c.Slots = slots.DefaultConfig()
	if c.Slots.Gap, err = durationOr("SLOT_GAP", c.Slots.Gap); err != nil {
		fail("%v", err)
	}
	if c.Slots.MinSpread, err = durationOr("SLOT_MIN_SPREAD", c.Slots.MinSpread); err != nil {
		fail("%v", err)
	}
	if c.Slots.MaxSpread, err = durationOr("SLOT_MAX_SPREAD", c.Slots.MaxSpread); err != nil {
		fail("%v", err)
	}
	if c.Slots.Horizon, err = durationOr("SLOT_HORIZON", c.Slots.Horizon); err != nil {
		fail("%v", err)
	}
	if c.Slots.TTL, err = durationOr("SLOT_TTL", c.Slots.TTL); err != nil {
		fail("%v", err)
	}
	if err := c.Slots.Validate(); err != nil && len(problems) == 0 {
		fail("%v", err)
	}

	c.Coalesce = dispatch.DefaultCoalesceConfig()
	if c.Coalesce.Window, err = durationOrZero("COALESCE_WINDOW", c.Coalesce.Window); err != nil {
		fail("%v", err)
	}
	if c.Coalesce.MaxMessages, err = intOr("COALESCE_MAX_MESSAGES", c.Coalesce.MaxMessages); err != nil {
		fail("%v", err)
	}
	if c.Coalesce.MinLead, err = durationOr("COALESCE_MIN_LEAD", c.Coalesce.MinLead); err != nil {
		fail("%v", err)
	}
	if err := c.Coalesce.Validate(); err != nil && len(problems) == 0 {
		fail("%v", err)
	}

	c.Circuit = circuit.DefaultConfig()
	if c.Circuit.Threshold, err = intOr("CIRCUIT_THRESHOLD", c.Circuit.Threshold); err != nil {
		fail("%v", err)
	}
	if c.Circuit.FailureTTL, err = durationOr("CIRCUIT_FAILURE_TTL", c.Circuit.FailureTTL); err != nil {
		fail("%v", err)
	}
	if err := c.Circuit.Validate(); err != nil && len(problems) == 0 {
		fail("%v", err)
	}

	c.Watch = circuit.DefaultWatchConfig()
	if c.Watch.Interval, err = durationOr("HEALTH_POLL_INTERVAL", c.Watch.Interval); err != nil {
		fail("%v", err)
	}
	if c.Watch.PollTimeout, err = durationOr("HEALTH_POLL_TIMEOUT", c.Watch.PollTimeout); err != nil {
		fail("%v", err)
	}
	if c.Watch.HealthyPolls, err = intOr("HEALTH_HEALTHY_POLLS", c.Watch.HealthyPolls); err != nil {
		fail("%v", err)
	}
	if err := c.Watch.Validate(); err != nil && len(problems) == 0 {
		fail("%v", err)
	}

	if c.Sessions, err = parseSessions(os.Getenv("WA_SESSIONS")); err != nil {
		fail("%v", err)
	}

	if c.CORSOrigins, err = parseCORSOrigins(os.Getenv("CORS_ALLOWED_ORIGINS")); err != nil {
		fail("%v", err)
	}

	if c.Senders, err = senders.Parse(os.Getenv("WA_SENDERS"), c.Sessions); err != nil {
		fail("WA_SENDERS: %v", err)
	}

	dryRun, err := boolOr("WA_DRY_RUN", false)
	if err != nil {
		fail("%v", err)
	}
	if dryRun && c.Senders != nil {
		c.Senders.ForceDryRun()
	}

	if c.RateLimitWindow, err = durationOr("RATE_LIMIT_WINDOW", ratelimit.DefaultWindow); err != nil {
		fail("%v", err)
	}

	if len(problems) > 0 {
		return nil, fmt.Errorf("invalid configuration:\n  - %s", strings.Join(problems, "\n  - "))
	}
	return c, nil
}

var validLogLevels = map[string]bool{"debug": true, "info": true, "warn": true, "error": true}

func parseSessions(raw string) ([]string, error) {
	var out []string
	seen := make(map[string]bool)
	for _, part := range strings.Split(raw, ",") {
		id := strings.TrimSpace(part)
		if id == "" {
			continue
		}
		if strings.ContainsAny(id, ": \t\n") {
			return nil, fmt.Errorf("WA_SESSIONS: session id %q must not contain a colon or whitespace", id)
		}
		if seen[id] {
			return nil, fmt.Errorf("WA_SESSIONS: session id %q is listed twice", id)
		}
		seen[id] = true
		out = append(out, id)
	}
	return out, nil
}

// parseCORSOrigins reads a comma-separated exact-match allowlist. A wildcard is rejected
// outright: this API is authenticated by a bearer header, and a wildcard origin paired with
// a header-based credential is the combination browsers' same-origin policy exists to
// prevent — allowing it here would be a silent way to defeat it.
func parseCORSOrigins(raw string) ([]string, error) {
	var out []string
	seen := make(map[string]bool)
	for _, part := range strings.Split(raw, ",") {
		origin := strings.TrimSpace(part)
		if origin == "" {
			continue
		}
		if origin == "*" {
			return nil, errors.New("CORS_ALLOWED_ORIGINS must not be '*'; list exact origins such as http://localhost:5500")
		}
		u, err := url.Parse(origin)
		if err != nil || u.Scheme == "" || u.Host == "" || u.Path != "" {
			return nil, fmt.Errorf("CORS_ALLOWED_ORIGINS: %q is not a bare origin (scheme://host[:port], no path)", origin)
		}
		if seen[origin] {
			continue
		}
		seen[origin] = true
		out = append(out, origin)
	}
	return out, nil
}

func envOr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func intOr(key string, def int) (int, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return def, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s is not a whole number, got %q", key, raw)
	}
	if n <= 0 {
		return 0, fmt.Errorf("%s must be positive, got %q", key, raw)
	}
	return n, nil
}

func boolOr(key string, def bool) (bool, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return def, nil
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("%s is not a boolean, got %q; want true or false", key, raw)
	}
	return v, nil
}

func durationOrZero(key string, def time.Duration) (time.Duration, error) {
	if strings.TrimSpace(os.Getenv(key)) == "0" {
		return 0, nil
	}
	return durationOr(key, def)
}

func durationOr(key string, def time.Duration) (time.Duration, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return def, nil
	}
	if n, err := strconv.Atoi(raw); err == nil {
		if n <= 0 {
			return 0, fmt.Errorf("%s must be positive, got %q", key, raw)
		}
		return time.Duration(n) * time.Second, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%s is not a duration (want e.g. 45s or 2m), got %q", key, raw)
	}
	if d <= 0 {
		return 0, errors.New(key + " must be positive")
	}
	return d, nil
}
