package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"wa-shared/id"
)

// IdempotencyRetention is the minimum window over which a replayed key returns the original
// response. Past it the same key is treated as a new request, which the contract documents.
const IdempotencyRetention = 24 * time.Hour

// IdempotencyInFlightTimeout caps how long a claim with no recorded response keeps answering
// "still being processed". A request that claims a key and then dies before recording would
// otherwise leave the key unusable for all of IdempotencyRetention: no response to replay and no
// way to retry. Past this window the claim is abandoned and the next request may take it over.
//
// It is far longer than any single request should take, because the cost of being wrong runs one
// way: too short and a slow batch's own retry re-sends its messages, while too long only makes a
// caller wait after a failure that is already rare.
const IdempotencyInFlightTimeout = 5 * time.Minute

// ErrIdempotencyConflict means the key was reused with a different request body. That is
// always a caller bug — returning the first response would hide it.
var ErrIdempotencyConflict = errors.New("store: idempotency key reused with a different payload")

// ErrIdempotencyInFlight means a request under this key is still being processed. The claim
// row exists but no response has been recorded against it yet.
var ErrIdempotencyInFlight = errors.New("store: idempotency key is still in flight")

// ErrIdempotencyClaimTaken means the claim this request held was taken over by another one after
// IdempotencyInFlightTimeout, so the write was refused rather than allowed to overwrite the
// taker's. It is a fact about who owns the key, not a failure of the work the caller just did.
var ErrIdempotencyClaimTaken = errors.New("store: idempotency claim was taken over by a later request")

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
// already being used for.
//
// A nil response means the caller just claimed the key and should do the work; the claim id it
// gets back fences its later writes — see RecordIdempotentResponse. A non-nil response is a replay
// and should be returned verbatim. ErrIdempotencyConflict means the stored hash differs;
// ErrIdempotencyInFlight means an earlier request holds the claim and has not finished.
//
// "Has not finished" is bounded by IdempotencyInFlightTimeout — past it a claim carrying no
// response is taken as abandoned and reclaimed here. A recorded response is never taken over.
func (s *Store) ClaimIdempotencyKey(ctx context.Context, apiKeyID, key, requestHash string) (claimID string, replay *IdempotentResponse, err error) {
	var (
		claimed    bool
		storedHash string
		statusCode *int
		body       []byte
	)

	// Fresh per attempt, and stored by both branches of the upsert, so a takeover moves ownership
	// of the row rather than sharing it: the request that was taken over still holds the previous
	// id and every write it makes from here on matches nothing.
	mine := id.New("clm")

	// One statement so two concurrent requests under one key cannot both see it free. The insert
	// wins for exactly one of them; the other falls through to the second branch and reads what
	// is already stored.
	//
	// The DO UPDATE takes over a claim that was abandoned — no response recorded, and older than
	// IdempotencyInFlightTimeout — which reads as a fresh claim from here on, since resetting
	// created_at and request_hash is what an insert would have left. Its WHERE keeps that narrow:
	// a claim still running, or one already carrying a response, fails the condition and updates
	// nothing. Overwriting the hash means a key reclaimed this way no longer conflicts against the
	// abandoned request's body, which is the intent — it left no response for a mismatch to guard.
	//
	// NOT EXISTS is what makes the union unambiguous. On a takeover the pre-existing row is
	// visible to the second branch as well (both read the same snapshot), so without it both
	// branches yield a row and LIMIT 1 silently picks whichever the planner emits first — and the
	// wrong pick reports ErrIdempotencyInFlight to the caller that actually holds the claim.
	err = s.pool.QueryRow(ctx, `
		WITH claim AS (
			INSERT INTO wa_idempotency (api_key_id, key, request_hash, claim_id)
			VALUES ($1, $2, $3, $5)
			ON CONFLICT (api_key_id, key) DO UPDATE
			   SET request_hash = EXCLUDED.request_hash,
			       claim_id     = EXCLUDED.claim_id,
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
		   AND NOT EXISTS (SELECT 1 FROM claim)
		 LIMIT 1`,
		apiKeyID, key, requestHash, IdempotencyInFlightTimeout.String(), mine).Scan(&claimed, &storedHash, &statusCode, &body)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil, fmt.Errorf("store: claim idempotency key %s: the claim vanished between insert and read", key)
	}
	if err != nil {
		return "", nil, fmt.Errorf("store: claim idempotency key %s: %w", key, err)
	}

	switch {
	case storedHash != requestHash:
		return "", nil, ErrIdempotencyConflict
	case claimed:
		return mine, nil, nil
	case statusCode == nil:
		return "", nil, ErrIdempotencyInFlight
	}
	return "", &IdempotentResponse{StatusCode: *statusCode, Body: body}, nil
}

// RecordIdempotentResponse stores the response a claimed key should replay from here on, if this
// request still holds the claim it was given.
//
// ErrIdempotencyClaimTaken means it does not: the request ran past IdempotencyInFlightTimeout and
// a retry took the key over, re-did the work and answered its own caller. Recording anyway would
// replace that caller's response — its message ids among them — with this one's. Nothing is
// written, and the caller should answer normally: its own work is done, and the duplicate send the
// takeover caused is the cost the timeout knowingly accepts.
func (s *Store) RecordIdempotentResponse(ctx context.Context, apiKeyID, key, claimID string, statusCode int, body []byte) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE wa_idempotency
		   SET status_code = $4, response = $5
		 WHERE api_key_id = $1 AND key = $2 AND claim_id = $3`,
		apiKeyID, key, claimID, statusCode, body)
	if err != nil {
		return fmt.Errorf("store: record idempotent response %s: %w", key, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: %s", ErrIdempotencyClaimTaken, key)
	}
	return nil
}

// ReleaseIdempotencyKey drops a claim whose work did not produce a replayable response, so a
// caller retrying after a failure is not told its own key conflicts with itself.
//
// Fenced by claimID for the same reason recording is, and here the unfenced version was the more
// expensive one: a request whose claim had been taken over would delete the *taker's* in-flight
// row, leaving the key free for a third request to claim and send a third copy of the message.
func (s *Store) ReleaseIdempotencyKey(ctx context.Context, apiKeyID, key, claimID string) error {
	_, err := s.pool.Exec(ctx, `
		DELETE FROM wa_idempotency
		 WHERE api_key_id = $1 AND key = $2 AND claim_id = $3 AND status_code IS NULL`,
		apiKeyID, key, claimID)
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
