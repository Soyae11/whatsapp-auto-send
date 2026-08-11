package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

const (
	SenderModeSingle = "single"
	SenderModePool   = "pool"
)

var ErrSenderExists = errors.New("store: sender already exists")

var ErrSenderNotFound = errors.New("store: sender not found")

type SenderRow struct {
	Name      string
	OwnerID   string
	Mode      string
	SessionID string
	Priority  int
	DryRun    bool
}

type SenderInput struct {
	OwnerID   string
	Name      string
	Mode      string
	SessionID string
}

const senderColumns = `name, owner_id, mode, session_id, priority, dry_run`

func scanSender(sc interface{ Scan(...any) error }) (SenderRow, error) {
	var row SenderRow
	var sessionID *string
	if err := sc.Scan(&row.Name, &row.OwnerID, &row.Mode, &sessionID, &row.Priority, &row.DryRun); err != nil {
		return SenderRow{}, err
	}
	if sessionID != nil {
		row.SessionID = *sessionID
	}
	return row, nil
}

// CreateSender inserts a new sender row, owned by in.OwnerID. Fails with ErrSenderExists if the
// name is already taken — sender names are global, not per-owner, since they are also the
// consumer-facing routing key any API key sends against.
func (s *Store) CreateSender(ctx context.Context, in SenderInput) (*SenderRow, error) {
	var sessionID *string
	if in.SessionID != "" {
		sessionID = &in.SessionID
	}

	row, err := scanSender(s.pool.QueryRow(ctx, `
		INSERT INTO wa_senders (name, owner_id, mode, session_id)
		VALUES ($1, $2, $3, $4)
		RETURNING `+senderColumns,
		in.Name, in.OwnerID, in.Mode, sessionID))
	if err != nil {
		if isUniqueViolation(err) {
			return nil, fmt.Errorf("%w: %s", ErrSenderExists, in.Name)
		}
		return nil, fmt.Errorf("store: create sender %q: %w", in.Name, err)
	}
	return &row, nil
}

// ListSenders returns only the senders owned by ownerID — the view a wa-console user sees.
func (s *Store) ListSenders(ctx context.Context, ownerID string) ([]SenderRow, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+senderColumns+` FROM wa_senders WHERE owner_id = $1 ORDER BY name`, ownerID)
	if err != nil {
		return nil, fmt.Errorf("store: list senders for owner %q: %w", ownerID, err)
	}
	defer rows.Close()
	return collectSenders(rows)
}

// ListAllSenders returns every sender regardless of owner. This is what the live send path's
// registry cache loads on every refresh — a send has to resolve any sender any key may use,
// not just the caller's own.
func (s *Store) ListAllSenders(ctx context.Context) ([]SenderRow, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+senderColumns+` FROM wa_senders ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("store: list all senders: %w", err)
	}
	defer rows.Close()
	return collectSenders(rows)
}

func collectSenders(rows pgx.Rows) ([]SenderRow, error) {
	var out []SenderRow
	for rows.Next() {
		row, err := scanSender(rows)
		if err != nil {
			return nil, fmt.Errorf("store: scan sender: %w", err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list senders: %w", err)
	}
	return out, nil
}

// GetSender looks up a sender by name regardless of owner — used where ownership is checked
// separately against the caller (e.g. pool admin routes).
func (s *Store) GetSender(ctx context.Context, name string) (*SenderRow, error) {
	row, err := scanSender(s.pool.QueryRow(ctx,
		`SELECT `+senderColumns+` FROM wa_senders WHERE name = $1`, name))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("%w: %s", ErrSenderNotFound, name)
	}
	if err != nil {
		return nil, fmt.Errorf("store: get sender %q: %w", name, err)
	}
	return &row, nil
}

// DeleteSender removes a sender and any pool it has, but only if ownerID actually owns it —
// callers get ErrSenderNotFound either when the name doesn't exist or when it belongs to
// someone else, deliberately not distinguishing the two so ownership is never leaked.
func (s *Store) DeleteSender(ctx context.Context, name, ownerID string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("store: delete sender %q: %w", name, err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	tag, err := tx.Exec(ctx, `DELETE FROM wa_senders WHERE name = $1 AND owner_id = $2`, name, ownerID)
	if err != nil {
		return fmt.Errorf("store: delete sender %q: %w", name, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: %s", ErrSenderNotFound, name)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM wa_sender_pools WHERE sender = $1`, name); err != nil {
		return fmt.Errorf("store: delete sender %q: clear its pool: %w", name, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("store: delete sender %q: %w", name, err)
	}
	return nil
}

func isUniqueViolation(err error) bool {
	var pgErr interface{ SQLState() string }
	return errors.As(err, &pgErr) && pgErr.SQLState() == "23505"
}
