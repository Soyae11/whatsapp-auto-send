package store

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed schema.sql
var schemaSQL string

const migrationLockID int64 = 0x7761_6a6f_6273 // "wajobs"

type Status string

const (
	StatusScheduled Status = "scheduled"

	StatusSending Status = "sending"

	StatusSent Status = "sent"

	StatusRetrying Status = "retrying"

	StatusFailed Status = "failed"

	StatusCancelled Status = "cancelled"
)

var Statuses = []Status{
	StatusScheduled, StatusSending, StatusSent, StatusRetrying, StatusFailed, StatusCancelled,
}

func ParseStatus(s string) (Status, error) {
	for _, known := range Statuses {
		if string(known) == s {
			return known, nil
		}
	}
	return "", fmt.Errorf("unknown status %q", s)
}

type Job struct {
	IdempotencyKey string
	SessionID      string
	To             string
	SourceRef      string

	// Set at enqueue by the public API. The worker paths leave them zero; the upserts
	// below never overwrite a stored value with one, so they survive every attempt.
	PublicID string
	APIKeyID string
	Sender   string
	Priority string
	Metadata map[string]string

	// WAMessageID is set by the worker on a successful send. Receipts arrive keyed on it.
	WAMessageID string
}

type Row struct {
	ID             int64
	IdempotencyKey string
	SessionID      string
	To             string
	SourceRef      string
	Status         Status
	Attempts       int
	ScheduledFor   *time.Time
	LastErrorCode  *string
	LastErrorAt    *time.Time
	SentAt         *time.Time
	CreatedAt      time.Time
	CoalescedRefs  []string
	PublicID       string
	APIKeyID       string
	Sender         string
	Priority       string
	Metadata       map[string]string
	SendingAt      *time.Time
	DeliveredAt    *time.Time
	ReadAt         *time.Time
	CancelledAt    *time.Time
	FailedAt       *time.Time
	WAMessageID    string
}

type Attempt struct {
	Attempt   int
	Outcome   Status
	ErrorCode *string
	At        time.Time
}

var ErrNotFound = errors.New("store: job not found")

type Store struct {
	pool *pgxpool.Pool
}

func Open(ctx context.Context, databaseURL string) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("store: parse database url: %w", err)
	}
	cfg.MaxConns = 10
	cfg.MaxConnLifetime = time.Hour
	cfg.MaxConnIdleTime = 5 * time.Minute

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("store: connect: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("store: ping: %w", err)
	}
	return &Store{pool: pool}, nil
}

func (s *Store) Close() { s.pool.Close() }

func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

func (s *Store) Migrate(ctx context.Context) error {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("store: migrate: acquire: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", migrationLockID); err != nil {
		return fmt.Errorf("store: migrate: lock: %w", err)
	}
	defer func() {
		_, _ = conn.Exec(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock($1)", migrationLockID)
	}()

	if _, err := conn.Exec(ctx, schemaSQL); err != nil {
		return fmt.Errorf("store: migrate: %w", err)
	}
	return nil
}

func (s *Store) MarkScheduled(ctx context.Context, j Job, scheduledFor time.Time) error {
	metadata, err := marshalMetadata(j.Metadata)
	if err != nil {
		return fmt.Errorf("store: mark scheduled %s: %w", j.IdempotencyKey, err)
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO wa_jobs (idempotency_key, session_id, to_number, source_ref, status, scheduled_for,
		                     public_id, api_key_id, sender, priority, metadata)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		ON CONFLICT (idempotency_key) DO NOTHING`,
		j.IdempotencyKey, j.SessionID, j.To, nullable(j.SourceRef), StatusScheduled, scheduledFor,
		nullable(j.PublicID), nullable(j.APIKeyID), nullable(j.Sender), nullable(j.Priority), metadata)
	if err != nil {
		return fmt.Errorf("store: mark scheduled %s: %w", j.IdempotencyKey, err)
	}
	return nil
}

func (s *Store) BeginAttempt(ctx context.Context, j Job) (attempts int, err error) {
	// sending_at records the first pickup, not the latest: a consumer wants to know when
	// this message first started moving, and the retry history lives in wa_job_attempts.
	err = s.pool.QueryRow(ctx, `
		INSERT INTO wa_jobs (idempotency_key, session_id, to_number, source_ref, status, attempts, sending_at)
		VALUES ($1, $2, $3, $4, $5, 1, now())
		ON CONFLICT (idempotency_key) DO UPDATE
		   SET status     = EXCLUDED.status,
		       attempts   = wa_jobs.attempts + 1,
		       sending_at = COALESCE(wa_jobs.sending_at, EXCLUDED.sending_at)
		RETURNING attempts`,
		j.IdempotencyKey, j.SessionID, j.To, nullable(j.SourceRef), StatusSending).Scan(&attempts)
	if err != nil {
		return 0, fmt.Errorf("store: begin attempt %s: %w", j.IdempotencyKey, err)
	}
	return attempts, nil
}

func (s *Store) MarkSent(ctx context.Context, j Job, sentAt time.Time) error {
	_, err := s.pool.Exec(ctx, `
		WITH job AS (
			INSERT INTO wa_jobs (idempotency_key, session_id, to_number, source_ref, status, attempts, sent_at, wa_message_id)
			VALUES ($1, $2, $3, $4, $5, 1, $6, $7)
			ON CONFLICT (idempotency_key) DO UPDATE
			   SET status        = EXCLUDED.status,
			       sent_at       = EXCLUDED.sent_at,
			       wa_message_id = COALESCE(EXCLUDED.wa_message_id, wa_jobs.wa_message_id)
			RETURNING idempotency_key, attempts
		)
		INSERT INTO wa_job_attempts (idempotency_key, attempt, outcome, at)
		SELECT idempotency_key, attempts, $5, $6 FROM job
		ON CONFLICT (idempotency_key, attempt) DO UPDATE
		   SET outcome    = EXCLUDED.outcome,
		       error_code = EXCLUDED.error_code,
		       at         = EXCLUDED.at`,
		j.IdempotencyKey, j.SessionID, j.To, nullable(j.SourceRef), StatusSent, sentAt, nullable(j.WAMessageID))
	if err != nil {
		return fmt.Errorf("store: mark sent %s: %w", j.IdempotencyKey, err)
	}
	return nil
}

func (s *Store) MarkFailed(ctx context.Context, j Job, errorCode string, permanent bool, at time.Time) error {
	status := StatusRetrying
	if permanent {
		status = StatusFailed
	}
	// failed_at is set only on the terminal failure. A retryable one leaves the message in
	// `queued` publicly, and stamping failed_at there would report a terminal transition
	// that has not happened.
	_, err := s.pool.Exec(ctx, `
		WITH job AS (
			INSERT INTO wa_jobs (idempotency_key, session_id, to_number, source_ref, status, attempts, last_error_code, last_error_at, failed_at)
			VALUES ($1, $2, $3, $4, $5, 1, $6, $7, CASE WHEN $5 = $8 THEN $7::TIMESTAMPTZ END)
			ON CONFLICT (idempotency_key) DO UPDATE
			   SET status          = EXCLUDED.status,
			       last_error_code = EXCLUDED.last_error_code,
			       last_error_at   = EXCLUDED.last_error_at,
			       failed_at       = CASE WHEN EXCLUDED.status = $8 THEN EXCLUDED.last_error_at ELSE wa_jobs.failed_at END
			RETURNING idempotency_key, attempts
		)
		INSERT INTO wa_job_attempts (idempotency_key, attempt, outcome, error_code, at)
		SELECT idempotency_key, attempts, $8, $6, $7 FROM job
		ON CONFLICT (idempotency_key, attempt) DO UPDATE
		   SET outcome    = EXCLUDED.outcome,
		       error_code = EXCLUDED.error_code,
		       at         = EXCLUDED.at`,
		j.IdempotencyKey, j.SessionID, j.To, nullable(j.SourceRef), status, nullable(errorCode), at, StatusFailed)
	if err != nil {
		return fmt.Errorf("store: mark failed %s: %w", j.IdempotencyKey, err)
	}
	return nil
}

// MarkCoalesced records that a second send was merged into an open one, and returns the open
// job's public id. The caller answers with that id, so a consumer that polls the message it
// was handed sees the merged message's real fate rather than a 404.
func (s *Store) MarkCoalesced(ctx context.Context, idempotencyKey, sourceRef string) (string, error) {
	if sourceRef != "" {
		_, err := s.pool.Exec(ctx, `
			UPDATE wa_jobs
			   SET coalesced_refs = array_append(coalesced_refs, $2)
			 WHERE idempotency_key = $1
			   AND source_ref IS DISTINCT FROM $2
			   AND NOT ($2 = ANY(COALESCE(coalesced_refs, '{}'::TEXT[])))`,
			idempotencyKey, sourceRef)
		if err != nil {
			return "", fmt.Errorf("store: mark coalesced %s: %w", idempotencyKey, err)
		}
	}

	return s.PublicIDFor(ctx, idempotencyKey)
}

// PublicIDFor returns the message id recorded against a gateway key, or the empty string when
// no row carries one. A missing id is not an error: rows written before API Phase 2, and rows
// written by the worker rather than the API, have none.
func (s *Store) PublicIDFor(ctx context.Context, idempotencyKey string) (string, error) {
	var publicID *string
	err := s.pool.QueryRow(ctx,
		`SELECT public_id FROM wa_jobs WHERE idempotency_key = $1`, idempotencyKey).Scan(&publicID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("store: public id for %s: %w", idempotencyKey, err)
	}
	if publicID == nil {
		return "", nil
	}
	return *publicID, nil
}

func (s *Store) MarkRequeued(ctx context.Context, j Job, scheduledFor time.Time) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO wa_jobs (idempotency_key, session_id, to_number, source_ref, status, scheduled_for)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (idempotency_key) DO UPDATE
		   SET status        = EXCLUDED.status,
		       scheduled_for = EXCLUDED.scheduled_for`,
		j.IdempotencyKey, j.SessionID, j.To, nullable(j.SourceRef), StatusScheduled, scheduledFor)
	if err != nil {
		return fmt.Errorf("store: mark requeued %s: %w", j.IdempotencyKey, err)
	}
	return nil
}

func (s *Store) MarkCancelled(ctx context.Context, idempotencyKey string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE wa_jobs
		   SET status = $2
		 WHERE idempotency_key = $1
		   AND status <> $3`,
		idempotencyKey, StatusCancelled, StatusSent)
	if err != nil {
		return fmt.Errorf("store: mark cancelled %s: %w", idempotencyKey, err)
	}
	return nil
}

const jobColumns = `id, idempotency_key, session_id, to_number, source_ref, status, attempts,
	       scheduled_for, last_error_code, last_error_at, sent_at, created_at, coalesced_refs,
	       public_id, api_key_id, sender, priority, metadata,
	       sending_at, delivered_at, read_at, cancelled_at, failed_at, wa_message_id`

func scanJob(sc interface{ Scan(...any) error }) (*Row, error) {
	var r Row
	var sourceRef, publicID, apiKeyID, sender, priority, waMessageID *string
	var metadata []byte
	if err := sc.Scan(&r.ID, &r.IdempotencyKey, &r.SessionID, &r.To, &sourceRef, &r.Status, &r.Attempts,
		&r.ScheduledFor, &r.LastErrorCode, &r.LastErrorAt, &r.SentAt, &r.CreatedAt, &r.CoalescedRefs,
		&publicID, &apiKeyID, &sender, &priority, &metadata,
		&r.SendingAt, &r.DeliveredAt, &r.ReadAt, &r.CancelledAt, &r.FailedAt, &waMessageID); err != nil {
		return nil, err
	}
	for dst, src := range map[*string]*string{
		&r.SourceRef:   sourceRef,
		&r.PublicID:    publicID,
		&r.APIKeyID:    apiKeyID,
		&r.Sender:      sender,
		&r.Priority:    priority,
		&r.WAMessageID: waMessageID,
	} {
		if src != nil {
			*dst = *src
		}
	}
	if len(metadata) > 0 {
		if err := json.Unmarshal(metadata, &r.Metadata); err != nil {
			return nil, fmt.Errorf("store: metadata for %s: %w", r.IdempotencyKey, err)
		}
	}
	return &r, nil
}

func marshalMetadata(m map[string]string) ([]byte, error) {
	if len(m) == 0 {
		return nil, nil
	}
	return json.Marshal(m)
}

func (s *Store) Get(ctx context.Context, idempotencyKey string) (*Row, error) {
	r, err := scanJob(s.pool.QueryRow(ctx,
		`SELECT `+jobColumns+` FROM wa_jobs WHERE idempotency_key = $1`, idempotencyKey))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, idempotencyKey)
	}
	if err != nil {
		return nil, fmt.Errorf("store: get %s: %w", idempotencyKey, err)
	}
	return r, nil
}

const (
	DefaultListLimit = 50
	MaxListLimit     = 500
)

type Filter struct {
	SessionID string
	Status    Status
	SourceRef string
	Since     time.Time
	Until     time.Time
	Limit     int
	Before    int64
}

func (s *Store) List(ctx context.Context, f Filter) ([]Row, error) {
	where := []string{}
	args := []any{}
	add := func(clause string, arg any) {
		args = append(args, arg)
		where = append(where, fmt.Sprintf(clause, len(args)))
	}

	if f.SessionID != "" {
		add("session_id = $%d", f.SessionID)
	}
	if f.Status != "" {
		add("status = $%d", f.Status)
	}
	if f.SourceRef != "" {
		args = append(args, f.SourceRef)
		where = append(where, fmt.Sprintf(
			"(source_ref = $%d OR $%d = ANY(COALESCE(coalesced_refs, '{}'::TEXT[])))", len(args), len(args)))
	}
	if !f.Since.IsZero() {
		add("created_at >= $%d", f.Since)
	}
	if !f.Until.IsZero() {
		add("created_at < $%d", f.Until)
	}
	if f.Before > 0 {
		add("id < $%d", f.Before)
	}

	limit := f.Limit
	switch {
	case limit <= 0:
		limit = DefaultListLimit
	case limit > MaxListLimit:
		limit = MaxListLimit
	}
	args = append(args, limit)

	q := `SELECT ` + jobColumns + ` FROM wa_jobs`
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += fmt.Sprintf(" ORDER BY id DESC LIMIT $%d", len(args))

	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list jobs: %w", err)
	}
	defer rows.Close()

	out := make([]Row, 0, limit)
	for rows.Next() {
		r, err := scanJob(rows)
		if err != nil {
			return nil, fmt.Errorf("store: list jobs: %w", err)
		}
		out = append(out, *r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list jobs: %w", err)
	}
	return out, nil
}

func (s *Store) Attempts(ctx context.Context, idempotencyKey string) ([]Attempt, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT attempt, outcome, error_code, at
		  FROM wa_job_attempts
		 WHERE idempotency_key = $1
		 ORDER BY attempt`, idempotencyKey)
	if err != nil {
		return nil, fmt.Errorf("store: attempts for %s: %w", idempotencyKey, err)
	}
	defer rows.Close()

	var out []Attempt
	for rows.Next() {
		var a Attempt
		if err := rows.Scan(&a.Attempt, &a.Outcome, &a.ErrorCode, &a.At); err != nil {
			return nil, fmt.Errorf("store: attempts for %s: %w", idempotencyKey, err)
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: attempts for %s: %w", idempotencyKey, err)
	}
	return out, nil
}

type Stat struct {
	SessionID string
	Status    Status
	ErrorCode string
	Count     int64
}

func (s *Store) Stats(ctx context.Context) ([]Stat, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT session_id, status, COALESCE(last_error_code, ''), count(*)
		  FROM wa_jobs
		 GROUP BY 1, 2, 3`)
	if err != nil {
		return nil, fmt.Errorf("store: job stats: %w", err)
	}
	defer rows.Close()

	var out []Stat
	for rows.Next() {
		var st Stat
		if err := rows.Scan(&st.SessionID, &st.Status, &st.ErrorCode, &st.Count); err != nil {
			return nil, fmt.Errorf("store: job stats: %w", err)
		}
		out = append(out, st)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: job stats: %w", err)
	}
	return out, nil
}

func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
