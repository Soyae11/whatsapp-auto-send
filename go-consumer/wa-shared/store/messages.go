package store

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	DefaultPageLimit = 50
	MaxPageLimit     = 200
)

// ErrNotCancellable means the message was found but has moved past the point where stopping
// it is possible.
var ErrNotCancellable = errors.New("store: message can no longer be cancelled")

// GetByPublicID reads one message, scoped to the key that sent it. A message belonging to
// another key is reported as missing rather than forbidden: whether some other project's
// message exists is not this caller's business.
func (s *Store) GetByPublicID(ctx context.Context, apiKeyID, publicID string) (*Row, error) {
	r, err := scanJob(s.pool.QueryRow(ctx,
		`SELECT `+jobColumns+` FROM wa_jobs WHERE public_id = $1 AND api_key_id = $2`,
		publicID, apiKeyID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, publicID)
	}
	if err != nil {
		return nil, fmt.Errorf("store: get message %s: %w", publicID, err)
	}
	return r, nil
}

// GetByWAMessageID finds the job a wa-gateway receipt refers to. Receipts carry no api key,
// so this one is deliberately unscoped — its caller is the gateway intake, not a consumer.
func (s *Store) GetByWAMessageID(ctx context.Context, waMessageID string) (*Row, error) {
	r, err := scanJob(s.pool.QueryRow(ctx,
		`SELECT `+jobColumns+` FROM wa_jobs WHERE wa_message_id = $1`, waMessageID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("%w: wa message %s", ErrNotFound, waMessageID)
	}
	if err != nil {
		return nil, fmt.Errorf("store: get by wa message id %s: %w", waMessageID, err)
	}
	return r, nil
}

type MessageFilter struct {
	APIKeyID  string
	Reference string
	Sender    string
	Statuses  []Status
	After     time.Time
	Before    time.Time
	Limit     int
	Cursor    Cursor
}

// Cursor points at the last row of the previous page. Keying on (created_at, id) rather than
// id alone keeps ordering stable: public ids are random within a millisecond, so they cannot
// be the sort key, and created_at alone is not unique.
type Cursor struct {
	CreatedAt time.Time
	ID        int64
}

func (c Cursor) IsZero() bool { return c.ID == 0 }

func (c Cursor) Encode() string {
	raw := strconv.FormatInt(c.CreatedAt.UTC().UnixNano(), 10) + ":" + strconv.FormatInt(c.ID, 10)
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func ParseCursor(s string) (Cursor, error) {
	if s == "" {
		return Cursor{}, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return Cursor{}, errors.New("cursor is not valid base64url")
	}
	nanos, id, ok := strings.Cut(string(raw), ":")
	if !ok {
		return Cursor{}, errors.New("cursor is malformed")
	}
	n, err := strconv.ParseInt(nanos, 10, 64)
	if err != nil {
		return Cursor{}, errors.New("cursor carries an unreadable timestamp")
	}
	i, err := strconv.ParseInt(id, 10, 64)
	if err != nil || i <= 0 {
		return Cursor{}, errors.New("cursor carries an unreadable position")
	}
	return Cursor{CreatedAt: time.Unix(0, n).UTC(), ID: i}, nil
}

// ListMessages returns one page, newest first, always scoped to the calling key. It asks for
// one row more than the limit so the caller can tell a full page from a last page without a
// second count query.
func (s *Store) ListMessages(ctx context.Context, f MessageFilter) (rows []Row, hasMore bool, err error) {
	if f.APIKeyID == "" {
		return nil, false, errors.New("store: list messages without an api key id would cross tenants")
	}

	where := []string{"api_key_id = $1"}
	args := []any{f.APIKeyID}
	add := func(clause string, arg any) {
		args = append(args, arg)
		where = append(where, fmt.Sprintf(clause, len(args)))
	}

	if f.Reference != "" {
		args = append(args, f.Reference)
		where = append(where, fmt.Sprintf(
			"(source_ref = $%d OR $%d = ANY(COALESCE(coalesced_refs, '{}'::TEXT[])))", len(args), len(args)))
	}
	if f.Sender != "" {
		add("sender = $%d", f.Sender)
	}
	if len(f.Statuses) > 0 {
		add("status = ANY($%d)", f.Statuses)
	}
	if !f.After.IsZero() {
		add("created_at >= $%d", f.After)
	}
	if !f.Before.IsZero() {
		add("created_at < $%d", f.Before)
	}
	if !f.Cursor.IsZero() {
		args = append(args, f.Cursor.CreatedAt, f.Cursor.ID)
		where = append(where, fmt.Sprintf("(created_at, id) < ($%d, $%d)", len(args)-1, len(args)))
	}

	limit := f.Limit
	switch {
	case limit <= 0:
		limit = DefaultPageLimit
	case limit > MaxPageLimit:
		limit = MaxPageLimit
	}
	args = append(args, limit+1)

	q := `SELECT ` + jobColumns + ` FROM wa_jobs WHERE ` + strings.Join(where, " AND ") +
		fmt.Sprintf(" ORDER BY created_at DESC, id DESC LIMIT $%d", len(args))

	result, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, false, fmt.Errorf("store: list messages: %w", err)
	}
	defer result.Close()

	out := make([]Row, 0, limit)
	for result.Next() {
		r, err := scanJob(result)
		if err != nil {
			return nil, false, fmt.Errorf("store: list messages: %w", err)
		}
		out = append(out, *r)
	}
	if err := result.Err(); err != nil {
		return nil, false, fmt.Errorf("store: list messages: %w", err)
	}

	if len(out) > limit {
		return out[:limit], true, nil
	}
	return out, false, nil
}

// CancelByPublicID stops a message that has not been picked up yet, and reports what it found
// so the caller can tell "already cancelled" (a no-op success) from "too late" (a 409).
//
// The status check is inside the UPDATE, so a worker picking the message up concurrently
// either loses the row to this update or wins and leaves it uncancellable — there is no
// window where both happen.
func (s *Store) CancelByPublicID(ctx context.Context, apiKeyID, publicID string, at time.Time) (*Row, error) {
	r, err := scanJob(s.pool.QueryRow(ctx, `
		WITH target AS (
			SELECT id FROM wa_jobs WHERE public_id = $1 AND api_key_id = $2
		), updated AS (
			UPDATE wa_jobs
			   SET status = $3, cancelled_at = $4
			 WHERE id IN (SELECT id FROM target)
			   AND status = ANY($5)
			RETURNING `+jobColumns+`
		)
		SELECT `+jobColumns+` FROM updated
		UNION ALL
		SELECT `+jobColumns+` FROM wa_jobs
		 WHERE id IN (SELECT id FROM target)
		   AND NOT EXISTS (SELECT 1 FROM updated)
		 LIMIT 1`,
		publicID, apiKeyID, StatusCancelled, at, []Status{StatusScheduled, StatusRetrying}))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, publicID)
	}
	if err != nil {
		return nil, fmt.Errorf("store: cancel message %s: %w", publicID, err)
	}

	// Cancelling an already-cancelled message is a no-op success, so a retried cancel is
	// safe. Anything else that did not move is past the point of stopping.
	if r.Status != StatusCancelled {
		return r, ErrNotCancellable
	}
	return r, nil
}

// Receipt is one of the transitions wa-gateway reports after a send.
type Receipt string

const (
	ReceiptDelivered Receipt = "delivered"
	ReceiptRead      Receipt = "read"
)

// ApplyReceipt records a delivery or read receipt and reports whether it changed anything.
//
// Receipts are unordered, so a `read` that arrives before its `delivered` must not be undone
// by the late one, and a repeat must not re-fire a webhook. Both are handled by only ever
// filling a column that is still null, and by treating `read` as implying `delivered`.
func (s *Store) ApplyReceipt(ctx context.Context, waMessageID string, kind Receipt, at time.Time) (changed bool, row *Row, err error) {
	var column string
	switch kind {
	case ReceiptDelivered:
		column = "delivered_at"
	case ReceiptRead:
		column = "read_at"
	default:
		return false, nil, fmt.Errorf("store: unknown receipt %q", kind)
	}

	// A read implies the message reached the device, so it backfills delivered_at when no
	// delivery receipt ever arrived — which is common, since receipts are best-effort.
	backfill := ""
	if kind == ReceiptRead {
		backfill = ", delivered_at = COALESCE(delivered_at, $3)"
	}

	r, err := scanJob(s.pool.QueryRow(ctx, `
		UPDATE wa_jobs
		   SET `+column+` = $3`+backfill+`
		 WHERE wa_message_id = $1
		   AND status = $2
		   AND `+column+` IS NULL
		RETURNING `+jobColumns,
		waMessageID, StatusSent, at))
	if err == nil {
		return true, r, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return false, nil, fmt.Errorf("store: apply %s receipt for %s: %w", kind, waMessageID, err)
	}

	// Nothing moved: either this receipt already landed, or there is no such message.
	existing, err := s.GetByWAMessageID(ctx, waMessageID)
	if err != nil {
		return false, nil, err
	}
	return false, existing, nil
}
