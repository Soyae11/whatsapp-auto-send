package store

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"wa-shared/auth"
)

const LastUsedThrottle = time.Minute

const keyColumns = `id, name, project, environment, hint, senders, rate_limit,
		       last_used_at, revoked_at, expires_at, created_at, owner_id`

var ErrKeyRevoked = errors.New("store: api key is already revoked")

type APIKeyInput struct {
	OwnerID     string
	Name        string
	Project     string
	Environment auth.Environment
	Senders     []string
	RateLimit   int
}

func (in APIKeyInput) Validate() error {
	switch {
	case in.OwnerID == "":
		return errors.New("api key owner_id is required")
	case in.Name == "":
		return errors.New("api key name is required")
	case in.Project == "":
		return errors.New("api key project is required")
	case len(in.Senders) == 0:
		return errors.New("an api key with no senders can send nothing; list at least one")
	case in.RateLimit <= 0:
		return fmt.Errorf("rate limit must be positive, got %d", in.RateLimit)
	}
	if _, err := auth.ParseEnvironment(string(in.Environment)); err != nil {
		return err
	}
	for _, s := range in.Senders {
		if s == "" {
			return errors.New("api key sender list contains an empty name")
		}
	}
	return nil
}

func scanKey(sc interface{ Scan(...any) error }) (*auth.Key, error) {
	var k auth.Key
	var ownerID *string
	if err := sc.Scan(&k.ID, &k.Name, &k.Project, &k.Environment, &k.Hint, &k.Senders,
		&k.RateLimit, &k.LastUsedAt, &k.RevokedAt, &k.ExpiresAt, &k.CreatedAt, &ownerID); err != nil {
		return nil, err
	}
	if ownerID != nil {
		k.OwnerID = *ownerID
	}
	return &k, nil
}

func (s *Store) CreateAPIKey(ctx context.Context, in APIKeyInput) (*auth.Key, auth.Secret, error) {
	if err := in.Validate(); err != nil {
		return nil, auth.Secret{}, err
	}
	secret, err := auth.Generate(in.Environment)
	if err != nil {
		return nil, auth.Secret{}, err
	}

	key, err := insertAPIKey(ctx, s.pool, in, secret)
	if err != nil {
		return nil, auth.Secret{}, err
	}
	return key, secret, nil
}

func insertAPIKey(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, in APIKeyInput, secret auth.Secret) (*auth.Key, error) {
	var ownerID *string
	if in.OwnerID != "" {
		ownerID = &in.OwnerID
	}
	key, err := scanKey(q.QueryRow(ctx, `
		INSERT INTO wa_api_keys (id, name, project, environment, key_hash, hint, senders, rate_limit, owner_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		RETURNING `+keyColumns,
		auth.NewID(), in.Name, in.Project, string(in.Environment), secret.Hash, secret.Hint,
		slices.Clone(in.Senders), in.RateLimit, ownerID))
	if err != nil {
		return nil, fmt.Errorf("store: create api key %q: %w", in.Name, err)
	}
	return key, nil
}

func (s *Store) AuthenticateAPIKey(ctx context.Context, hash string) (*auth.Key, error) {
	key, err := scanKey(s.pool.QueryRow(ctx, `
		WITH found AS (
			SELECT id FROM wa_api_keys WHERE key_hash = $1
		), touched AS (
			UPDATE wa_api_keys k
			   SET last_used_at = now()
			  FROM found
			 WHERE k.id = found.id
			   AND (k.last_used_at IS NULL OR k.last_used_at < now() - make_interval(secs => $2))
		)
		SELECT `+keyColumns+` FROM wa_api_keys WHERE key_hash = $1`,
		hash, LastUsedThrottle.Seconds()))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: authenticate api key: %w", err)
	}
	return key, nil
}

func (s *Store) GetAPIKey(ctx context.Context, id string) (*auth.Key, error) {
	key, err := scanKey(s.pool.QueryRow(ctx,
		`SELECT `+keyColumns+` FROM wa_api_keys WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	if err != nil {
		return nil, fmt.Errorf("store: get api key %s: %w", id, err)
	}
	return key, nil
}

func (s *Store) ListAPIKeys(ctx context.Context) ([]auth.Key, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+keyColumns+` FROM wa_api_keys ORDER BY project, created_at`)
	if err != nil {
		return nil, fmt.Errorf("store: list api keys: %w", err)
	}
	defer rows.Close()

	var out []auth.Key
	for rows.Next() {
		k, err := scanKey(rows)
		if err != nil {
			return nil, fmt.Errorf("store: list api keys: %w", err)
		}
		out = append(out, *k)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list api keys: %w", err)
	}
	return out, nil
}

// ListAPIKeysByOwner is the self-service view — only the caller's own keys, never anyone
// else's key_hash or plaintext (neither is ever selected by scanKey/keyColumns to begin with).
func (s *Store) ListAPIKeysByOwner(ctx context.Context, ownerID string) ([]auth.Key, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+keyColumns+` FROM wa_api_keys WHERE owner_id = $1 ORDER BY created_at DESC`, ownerID)
	if err != nil {
		return nil, fmt.Errorf("store: list api keys for owner %q: %w", ownerID, err)
	}
	defer rows.Close()

	var out []auth.Key
	for rows.Next() {
		k, err := scanKey(rows)
		if err != nil {
			return nil, fmt.Errorf("store: list api keys for owner %q: %w", ownerID, err)
		}
		out = append(out, *k)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list api keys for owner %q: %w", ownerID, err)
	}
	return out, nil
}

// RevokeAPIKeyByOwner is like RevokeAPIKey but requires ownerID to actually own the key —
// mismatched ownership and a nonexistent id both come back as ErrNotFound, so a caller can't
// distinguish "no such key" from "someone else's key".
func (s *Store) RevokeAPIKeyByOwner(ctx context.Context, id, ownerID string) (*auth.Key, bool, error) {
	key, err := scanKey(s.pool.QueryRow(ctx, `
		UPDATE wa_api_keys
		   SET revoked_at = now()
		 WHERE id = $1 AND owner_id = $2 AND revoked_at IS NULL
		RETURNING `+keyColumns, id, ownerID))
	if err == nil {
		return key, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, false, fmt.Errorf("store: revoke api key %s: %w", id, err)
	}

	key, err = scanKey(s.pool.QueryRow(ctx,
		`SELECT `+keyColumns+` FROM wa_api_keys WHERE id = $1 AND owner_id = $2`, id, ownerID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	if err != nil {
		return nil, false, fmt.Errorf("store: revoke api key %s: %w", id, err)
	}
	return key, false, nil
}

func (s *Store) RevokeAPIKey(ctx context.Context, id string) (*auth.Key, bool, error) {
	key, err := scanKey(s.pool.QueryRow(ctx, `
		UPDATE wa_api_keys
		   SET revoked_at = now()
		 WHERE id = $1 AND revoked_at IS NULL
		RETURNING `+keyColumns, id))
	if err == nil {
		return key, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, false, fmt.Errorf("store: revoke api key %s: %w", id, err)
	}

	key, err = s.GetAPIKey(ctx, id)
	if err != nil {
		return nil, false, err
	}
	return key, false, nil
}

func (s *Store) RotateAPIKey(ctx context.Context, id string, grace time.Duration) (*auth.Key, auth.Secret, error) {
	if grace < 0 {
		return nil, auth.Secret{}, fmt.Errorf("rotation grace must not be negative, got %s", grace)
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, auth.Secret{}, fmt.Errorf("store: rotate api key %s: %w", id, err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	old, err := scanKey(tx.QueryRow(ctx,
		`SELECT `+keyColumns+` FROM wa_api_keys WHERE id = $1 FOR UPDATE`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, auth.Secret{}, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	if err != nil {
		return nil, auth.Secret{}, fmt.Errorf("store: rotate api key %s: %w", id, err)
	}
	if old.RevokedAt != nil {
		return nil, auth.Secret{}, fmt.Errorf("%w: %s", ErrKeyRevoked, id)
	}

	secret, err := auth.Generate(old.Environment)
	if err != nil {
		return nil, auth.Secret{}, err
	}

	fresh, err := insertAPIKey(ctx, tx, APIKeyInput{
		OwnerID:     old.OwnerID,
		Name:        old.Name,
		Project:     old.Project,
		Environment: old.Environment,
		Senders:     old.Senders,
		RateLimit:   old.RateLimit,
	}, secret)
	if err != nil {
		return nil, auth.Secret{}, err
	}

	if _, err := tx.Exec(ctx, `
		UPDATE wa_api_keys
		   SET revoked_at = CASE WHEN $2 = 0 THEN now() ELSE revoked_at END,
		       expires_at = CASE WHEN $2 = 0 THEN expires_at ELSE now() + make_interval(secs => $2) END
		 WHERE id = $1`, id, grace.Seconds()); err != nil {
		return nil, auth.Secret{}, fmt.Errorf("store: rotate api key %s: retire the old key: %w", id, err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, auth.Secret{}, fmt.Errorf("store: rotate api key %s: %w", id, err)
	}
	return fresh, secret, nil
}
