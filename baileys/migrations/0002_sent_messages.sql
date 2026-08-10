-- Every send attempt, successful or not.
--
-- `idempotency_key` is UNIQUE and doubles as a claim: a row is inserted as 'pending' before
-- the socket is touched, so two concurrent requests with the same key cannot both send.

CREATE TABLE wa_sent_messages (
  id               BIGSERIAL PRIMARY KEY,
  session_id       TEXT NOT NULL REFERENCES wa_sessions(id),
  idempotency_key  TEXT NOT NULL UNIQUE,
  to_jid           TEXT NOT NULL,
  payload          JSONB NOT NULL,
  status           TEXT NOT NULL,
  wa_message_id    TEXT,
  error_code       TEXT,
  error_detail     TEXT,
  created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT wa_sent_messages_status_check
    CHECK (status IN ('pending', 'sent', 'failed'))
);

-- Phase 5 reads per-session send history for health and metrics.
CREATE INDEX wa_sent_messages_session_created_idx
  ON wa_sent_messages (session_id, created_at DESC);
