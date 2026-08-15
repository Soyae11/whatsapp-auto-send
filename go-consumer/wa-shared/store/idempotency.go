package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// IdempotencyRetention is the minimum window over which a replayed key returns the original
// response. Past it the same key is treated as a new request, which the contract documents.
const IdempotencyRetention = 24 * time.Hour

// IdempotencyInFlightTimeout caps how long a claim with no recorded response can keep answering
// "still being processed". A request that claims a key and then dies before recording — the
// process was killed mid-batch, or the database refused the write — would otherwise leave the key
// unusable for the whole of IdempotencyRetention: no response to replay and no way to retry,
// which is the worst of both. Past this window the claim is treated as abandoned and the next
// request may take it over.
//
// It is set far longer than any single request should take, because the cost of being wrong runs
// one way: too short and a slow batch's own retry could re-send its messages, while too long only
// makes a caller wait to retry after a failure that is already rare.
const IdempotencyInFlightTimeout = 5 * time.Minute

// ErrIdempotencyConflict means the key was reused with a different request body. That is
// always a caller bug — returning the first response would hide it.
var ErrIdempotencyConflict = errors.New("store: idempotency key reused with a different payload")

// ErrIdempotencyInFlight means a request under this key is still being processed. The claim
// row exists but no response has been recorded against it yet.
var ErrIdempotencyInFlight = errors.New("store: idempotency key is still in flight")

type IdempotentResponse struct {
	StatusCode int
	Body       []byte
}

// HashRequest fingerprints a request body so a replay under the same key can be told apart
// from a reuse with different content.
func HashRequest(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

// ClaimIdempotencyKey takes ownership of a key for this request, or reports what the key is
// already being used for. A recorded response comes back exactly as it was stored — see the
// note on the response column in schema.sql.
//
// It returns a nil response when the caller has just claimed the key and should go on to do
// the work. It returns a non-nil response when this is a replay and that response should be
// returned verbatim. It returns ErrIdempotencyConflict when the stored hash differs, and
// ErrIdempotencyInFlight when an earlier request holds the claim but has not finished.
//
// "Has not finished" is bounded by IdempotencyInFlightTimeout: a claim past it that still carries
// no response is taken to have been abandoned, and this call claims it afresh. A key whose
// request died before recording is therefore usable again in minutes rather than being unusable
// until the retention sweep. A recorded response is never taken over, at any age.
func (s *Store) ClaimIdempotencyKey(ctx context.Context, apiKeyID, key, requestHash string) (*IdempotentResponse, error) {
	var (
		claimed    bool
		storedHash string
		statusCode *int
		body       []byte
	)

	// One statement so two concurrent requests under one key cannot both see it free. The
	// insert wins for exactly one of them; the other falls through to the second branch and
	// reads what is already stored. A data-modifying CTE is invisible to the rest of the
	// query, so exactly one branch yields a row.
	//
	// The DO UPDATE takes over a claim that was abandoned — no response recorded, and older than
	// IdempotencyInFlightTimeout — which reads as a fresh claim from here on, since resetting
	// created_at and request_hash is exactly what an insert would have left. Its WHERE is what
	// keeps that narrow: a claim that is genuinely still running, or one that already carries a
	// response, fails the condition, updates nothing, returns no row, and falls through to the
	// second branch precisely as it did under DO NOTHING. Overwriting the hash means a key
	// reclaimed this way no longer reports a conflict against the abandoned request's body,
	// which is the intent — that request left no response for a mismatch to protect.
	err := s.pool.QueryRow(ctx, `
		WITH claim AS (
			INSERT INTO wa_idempotency (api_key_id, key, request_hash)
			VALUES ($1, $2, $3)
			ON CONFLICT (api_key_id, key) DO UPDATE
			   SET request_hash = EXCLUDED.request_hash,
			       created_at   = now()
			 WHERE wa_idempotency.status_code IS NULL
			   AND wa_idempotency.created_at < now() - $4::interval
			RETURNING request_hash, status_code, response
		)
		SELECT true, request_hash, status_code, response FROM claim
		UNION ALL
		SELECT false, request_hash, status_code, response
		  FROM wa_idempotency
		 WHERE api_key_id = $1 AND key = $2
		 LIMIT 1`,
		apiKeyID, key, requestHash, IdempotencyInFlightTimeout.String()).Scan(&claimed, &storedHash, &statusCode, &body)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("store: claim idempotency key %s: the claim vanished between insert and read", key)
	}
	if err != nil {
		return nil, fmt.Errorf("store: claim idempotency key %s: %w", key, err)
	}

	switch {
	case storedHash != requestHash:
		return nil, ErrIdempotencyConflict
	case claimed:
		return nil, nil
	case statusCode == nil:
		return nil, ErrIdempotencyInFlight
	}
	return &IdempotentResponse{StatusCode: *statusCode, Body: body}, nil
}

// RecordIdempotentResponse stores the response a claimed key should replay from here on.
func (s *Store) RecordIdempotentResponse(ctx context.Context, apiKeyID, key string, statusCode int, body []byte) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE wa_idempotency
		   SET status_code = $3, response = $4
		 WHERE api_key_id = $1 AND key = $2`,
		apiKeyID, key, statusCode, body)
	if err != nil {
		return fmt.Errorf("store: record idempotent response %s: %w", key, err)
	}
	return nil
}

// ReleaseIdempotencyKey drops a claim whose work did not produce a replayable response, so a
// caller retrying after a failure is not told its own key conflicts with itself.
func (s *Store) ReleaseIdempotencyKey(ctx context.Context, apiKeyID, key string) error {
	_, err := s.pool.Exec(ctx, `
		DELETE FROM wa_idempotency
		 WHERE api_key_id = $1 AND key = $2 AND status_code IS NULL`,
		apiKeyID, key)
	if err != nil {
		return fmt.Errorf("store: release idempotency key %s: %w", key, err)
	}
	return nil
}

// PurgeIdempotency drops records past the retention window and returns how many went.
func (s *Store) PurgeIdempotency(ctx context.Context, olderThan time.Duration) (int64, error) {
	tag, err := s.pool.Exec(ctx,
		`DELETE FROM wa_idempotency WHERE created_at < now() - $1::interval`,
		olderThan.String())
	if err != nil {
		return 0, fmt.Errorf("store: purge idempotency: %w", err)
	}
	return tag.RowsAffected(), nil
}
