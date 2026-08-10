package store

import (
	"context"
	"errors"
	"fmt"
)

var ErrPoolExists = errors.New("store: sender pool already exists")

var ErrPoolNotFound = errors.New("store: sender pool not found")

var ErrMainMember = errors.New("store: cannot remove the current main; promote or dethrone first")

type PoolMember struct {
	Sender       string
	SessionID    string
	IsMain       bool
	Disqualified bool
	Rank         int
}

const poolColumns = `sender, session_id, is_main, disqualified, rank`

func scanPoolMember(sc interface{ Scan(...any) error }) (PoolMember, error) {
	var m PoolMember
	err := sc.Scan(&m.Sender, &m.SessionID, &m.IsMain, &m.Disqualified, &m.Rank)
	return m, err
}

// CreatePool bootstraps a sender's pool. The first session in sessionIDs starts as main; the
// rest start as ordinary, promotable backups. Fails if the sender already has a pool — ongoing
// membership changes go through AddMember/RemoveMember instead.
func (s *Store) CreatePool(ctx context.Context, sender string, sessionIDs []string) error {
	if sender == "" {
		return errors.New("store: pool sender is required")
	}
	if len(sessionIDs) == 0 {
		return errors.New("store: a pool with no sessions can fail over to nothing; list at least one")
	}

	existing, err := s.Pool(ctx, sender)
	if err != nil {
		return err
	}
	if len(existing) > 0 {
		return fmt.Errorf("%w: %s", ErrPoolExists, sender)
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("store: create pool %s: %w", sender, err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	for i, sessionID := range sessionIDs {
		if _, err := tx.Exec(ctx, `
			INSERT INTO wa_sender_pools (sender, session_id, is_main, rank)
			VALUES ($1, $2, $3, $4)`,
			sender, sessionID, i == 0, i); err != nil {
			return fmt.Errorf("store: create pool %s: add %s: %w", sender, sessionID, err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("store: create pool %s: %w", sender, err)
	}
	return nil
}

// Pool returns a sender's members, lowest rank first. Empty (not an error) when the sender has
// no pool.
func (s *Store) Pool(ctx context.Context, sender string) ([]PoolMember, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+poolColumns+` FROM wa_sender_pools WHERE sender = $1 ORDER BY rank`, sender)
	if err != nil {
		return nil, fmt.Errorf("store: pool %s: %w", sender, err)
	}
	defer rows.Close()

	var members []PoolMember
	for rows.Next() {
		m, err := scanPoolMember(rows)
		if err != nil {
			return nil, fmt.Errorf("store: pool %s: %w", sender, err)
		}
		members = append(members, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: pool %s: %w", sender, err)
	}
	return members, nil
}

// CurrentMain reports the session id currently serving as main for sender, distinguishing three
// outcomes callers must handle differently:
//   - hasPool=false: no pool exists; fall back to the static, single-session default.
//   - hasPool=true, sessionID="": a pool exists but every member has failed while serving as
//     main and been disqualified — the pool is exhausted. Reject the send; do not fall back.
//   - hasPool=true, sessionID="<id>": the normal case.
func (s *Store) CurrentMain(ctx context.Context, sender string) (sessionID string, hasPool bool, err error) {
	members, err := s.Pool(ctx, sender)
	if err != nil {
		return "", false, err
	}
	if len(members) == 0 {
		return "", false, nil
	}
	for _, m := range members {
		if m.IsMain {
			return m.SessionID, true, nil
		}
	}
	return "", true, nil
}

// Promote makes newMainSessionID the sender's main, dethroning whichever session holds it now
// (if any). Dethroning disqualifies the old main — see wa_sender_pools' schema comment — while
// promotion clears disqualified on the new one, since being trusted with main again is itself
// the "someone fixed it" signal, whether that trust came from the auto-picker or a manual
// override. Both updates happen in one transaction; the partial unique index on is_main means
// two concurrent promotions can't both win.
func (s *Store) Promote(ctx context.Context, sender, newMainSessionID string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("store: promote %s in %s: %w", newMainSessionID, sender, err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	if _, err := tx.Exec(ctx, `
		UPDATE wa_sender_pools SET is_main = false, disqualified = true, updated_at = now()
		 WHERE sender = $1 AND is_main`, sender); err != nil {
		return fmt.Errorf("store: promote %s in %s: dethrone old main: %w", newMainSessionID, sender, err)
	}

	tag, err := tx.Exec(ctx, `
		UPDATE wa_sender_pools SET is_main = true, disqualified = false, updated_at = now()
		 WHERE sender = $1 AND session_id = $2`, sender, newMainSessionID)
	if err != nil {
		return fmt.Errorf("store: promote %s in %s: %w", newMainSessionID, sender, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: %s has no member %s", ErrPoolNotFound, sender, newMainSessionID)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("store: promote %s in %s: %w", newMainSessionID, sender, err)
	}
	return nil
}

// Disqualify marks one member disqualified — it just failed while serving this sender, whether
// it was main or a load-spread backup. If it was main, main is cleared along with it (the pool
// is left with no main at all, on purpose, when there's no eligible replacement — see
// CurrentMain); if it was a load-spread backup, this just takes it out of rotation and main, if
// any, is untouched.
func (s *Store) Disqualify(ctx context.Context, sender, sessionID string) error {
	if _, err := s.pool.Exec(ctx, `
		UPDATE wa_sender_pools SET is_main = false, disqualified = true, updated_at = now()
		 WHERE sender = $1 AND session_id = $2`, sender, sessionID); err != nil {
		return fmt.Errorf("store: disqualify %s in %s: %w", sessionID, sender, err)
	}
	return nil
}

// Reinstate clears disqualified on one member — "someone fixed it". It does not make the
// member main by itself; it only makes it eligible again, for auto-promotion next time or an
// immediate manual Promote.
func (s *Store) Reinstate(ctx context.Context, sender, sessionID string) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE wa_sender_pools SET disqualified = false, updated_at = now()
		 WHERE sender = $1 AND session_id = $2`, sender, sessionID)
	if err != nil {
		return fmt.Errorf("store: reinstate %s in %s: %w", sessionID, sender, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: %s has no member %s", ErrPoolNotFound, sender, sessionID)
	}
	return nil
}

// AddMember appends a backup. A pool's very first member (added via CreatePool or, for a
// sender with none yet, via AddMember) starts as main.
func (s *Store) AddMember(ctx context.Context, sender, sessionID string) error {
	var nextRank int
	if err := s.pool.QueryRow(ctx,
		`SELECT COALESCE(MAX(rank) + 1, 0) FROM wa_sender_pools WHERE sender = $1`, sender,
	).Scan(&nextRank); err != nil {
		return fmt.Errorf("store: add %s to %s: %w", sessionID, sender, err)
	}

	if _, err := s.pool.Exec(ctx, `
		INSERT INTO wa_sender_pools (sender, session_id, is_main, rank)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (sender, session_id) DO NOTHING`,
		sender, sessionID, nextRank == 0, nextRank); err != nil {
		return fmt.Errorf("store: add %s to %s: %w", sessionID, sender, err)
	}
	return nil
}

// DeletePool drops every row for sender, reverting it to the static registry's single default
// session. Unlike RemoveMember, this is allowed even while a main is set: abandoning pooling
// for a sender entirely — the fix for "I created this pool by mistake" — is a different, safe
// operation from removing one member out from under a pool that keeps existing.
func (s *Store) DeletePool(ctx context.Context, sender string) error {
	if _, err := s.pool.Exec(ctx, `DELETE FROM wa_sender_pools WHERE sender = $1`, sender); err != nil {
		return fmt.Errorf("store: delete pool %s: %w", sender, err)
	}
	return nil
}

// RemoveMember refuses to remove the current main — promote or dethrone first — so a pool
// never loses track of who, if anyone, is supposed to be serving.
func (s *Store) RemoveMember(ctx context.Context, sender, sessionID string) error {
	tag, err := s.pool.Exec(ctx, `
		DELETE FROM wa_sender_pools WHERE sender = $1 AND session_id = $2 AND NOT is_main`,
		sender, sessionID)
	if err != nil {
		return fmt.Errorf("store: remove %s from %s: %w", sessionID, sender, err)
	}
	if tag.RowsAffected() > 0 {
		return nil
	}

	members, err := s.Pool(ctx, sender)
	if err != nil {
		return err
	}
	for _, m := range members {
		if m.SessionID == sessionID && m.IsMain {
			return fmt.Errorf("%w: %s is main for %s", ErrMainMember, sessionID, sender)
		}
	}
	return fmt.Errorf("%w: %s has no member %s", ErrPoolNotFound, sender, sessionID)
}

// PickHealthyBackup picks the lowest-rank member eligible to take over from a session that just
// failed: not the one that failed, not already main, not disqualified, and not reported
// unhealthy by isOpen. Pure — no I/O — so wa-consumer-api and wa-dispatcher share identical
// selection logic; each passes its own circuit-breaker check as isOpen. members is expected in
// rank order, as returned by Pool.
func PickHealthyBackup(members []PoolMember, excludeSessionID string, isOpen func(sessionID string) bool) (sessionID string, ok bool) {
	for _, m := range members {
		if m.SessionID == excludeSessionID || m.IsMain || m.Disqualified {
			continue
		}
		if isOpen != nil && isOpen(m.SessionID) {
			continue
		}
		return m.SessionID, true
	}
	return "", false
}

// Rotate is the one decision both wa-consumer-api (an async rejection, which can only ever
// rotate — the message text is long gone by then) and wa-dispatcher (a synchronous failure,
// which can also resend, using promotedTo) need after a session fails serving sender, so it
// lives here once instead of twice:
//
//   - failedSessionID was main and a healthy backup exists: promote the backup. Returns it.
//   - failedSessionID was main and nothing eligible remains: disqualify it, leaving the pool
//     with no main at all, on purpose — see CurrentMain. Returns "".
//   - failedSessionID was a load-spread backup, not main: disqualify it alone. Whoever is main
//     is untouched, promoted or not. Returns "" — there is nothing to resend as, from this
//     function's point of view; a caller that still wants to try a fresh send picks among the
//     pool's remaining eligible members itself.
//
// A sender with no pool is a no-op (promotedTo "", err nil) — callers don't need to check
// Pool's length themselves first.
func (s *Store) Rotate(ctx context.Context, sender, failedSessionID string, isOpen func(sessionID string) bool) (promotedTo string, err error) {
	members, err := s.Pool(ctx, sender)
	if err != nil {
		return "", err
	}
	if len(members) == 0 {
		return "", nil
	}

	var wasMain bool
	for _, m := range members {
		if m.SessionID == failedSessionID {
			wasMain = m.IsMain
			break
		}
	}

	if wasMain {
		if backup, ok := PickHealthyBackup(members, failedSessionID, isOpen); ok {
			if err := s.Promote(ctx, sender, backup); err != nil {
				return "", err
			}
			return backup, nil
		}
	}

	if err := s.Disqualify(ctx, sender, failedSessionID); err != nil {
		return "", err
	}
	return "", nil
}
