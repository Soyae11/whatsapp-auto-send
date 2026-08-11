CREATE TABLE IF NOT EXISTS wa_jobs (
  id               BIGSERIAL PRIMARY KEY,
  idempotency_key  TEXT NOT NULL UNIQUE,
  session_id       TEXT NOT NULL,
  to_number        TEXT NOT NULL,
  source_ref       TEXT,
  status           TEXT NOT NULL,
  attempts         INT  NOT NULL DEFAULT 0,
  scheduled_for    TIMESTAMPTZ,
  last_error_code  TEXT,
  last_error_at    TIMESTAMPTZ,
  sent_at          TIMESTAMPTZ,
  created_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

ALTER TABLE wa_jobs ADD COLUMN IF NOT EXISTS coalesced_refs TEXT[];

ALTER TABLE wa_jobs ADD COLUMN IF NOT EXISTS public_id  TEXT;
ALTER TABLE wa_jobs ADD COLUMN IF NOT EXISTS api_key_id TEXT;
ALTER TABLE wa_jobs ADD COLUMN IF NOT EXISTS sender     TEXT;
ALTER TABLE wa_jobs ADD COLUMN IF NOT EXISTS priority   TEXT;
ALTER TABLE wa_jobs ADD COLUMN IF NOT EXISTS metadata   JSONB;

-- One column per transition. The public Timestamps object reports only transitions that
-- actually happened, and deriving them from status plus a single mtime cannot answer that:
-- a message that failed after a retry has both a sending_at and a failed_at.
ALTER TABLE wa_jobs ADD COLUMN IF NOT EXISTS sending_at    TIMESTAMPTZ;
ALTER TABLE wa_jobs ADD COLUMN IF NOT EXISTS delivered_at  TIMESTAMPTZ;
ALTER TABLE wa_jobs ADD COLUMN IF NOT EXISTS read_at       TIMESTAMPTZ;
ALTER TABLE wa_jobs ADD COLUMN IF NOT EXISTS cancelled_at  TIMESTAMPTZ;
ALTER TABLE wa_jobs ADD COLUMN IF NOT EXISTS failed_at     TIMESTAMPTZ;

-- wa-gateway's receipts identify a message by the id its send returned, which is the only
-- handle we have to match a delivery_ack back to a job.
ALTER TABLE wa_jobs ADD COLUMN IF NOT EXISTS wa_message_id TEXT;

CREATE UNIQUE INDEX IF NOT EXISTS wa_jobs_public_id_idx ON wa_jobs (public_id)
  WHERE public_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS wa_jobs_wa_message_id_idx ON wa_jobs (wa_message_id)
  WHERE wa_message_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS wa_jobs_api_key_page_idx ON wa_jobs (api_key_id, created_at DESC, id DESC)
  WHERE api_key_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS wa_jobs_session_status_idx ON wa_jobs (session_id, status);

CREATE INDEX IF NOT EXISTS wa_jobs_coalesced_refs_idx ON wa_jobs USING GIN (coalesced_refs);

CREATE INDEX IF NOT EXISTS wa_jobs_source_ref_idx ON wa_jobs (source_ref)
  WHERE source_ref IS NOT NULL;

CREATE INDEX IF NOT EXISTS wa_jobs_created_at_idx ON wa_jobs (created_at DESC);

CREATE TABLE IF NOT EXISTS wa_api_keys (
  id            TEXT PRIMARY KEY,
  name          TEXT NOT NULL,
  project       TEXT NOT NULL,
  environment   TEXT NOT NULL,
  key_hash      TEXT NOT NULL UNIQUE,
  hint          TEXT NOT NULL,
  senders       TEXT[] NOT NULL DEFAULT '{}',
  rate_limit    INT  NOT NULL,
  last_used_at  TIMESTAMPTZ,
  revoked_at    TIMESTAMPTZ,
  expires_at    TIMESTAMPTZ,
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS wa_api_keys_project_idx ON wa_api_keys (project);

-- response is BYTEA, not JSONB. The contract promises a replay is the first response byte for
-- byte, and JSONB round-trips through Postgres's own serialiser: it reorders keys and respaces
-- the output. Nothing ever queries inside this column, so the structured type bought nothing
-- and cost the guarantee.
CREATE TABLE IF NOT EXISTS wa_idempotency (
  api_key_id    TEXT NOT NULL,
  key           TEXT NOT NULL,
  request_hash  TEXT NOT NULL,
  status_code   INT,
  response      BYTEA,
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (api_key_id, key)
);

DO $$
BEGIN
  IF EXISTS (
    SELECT 1 FROM information_schema.columns
     WHERE table_schema = current_schema()
       AND table_name = 'wa_idempotency'
       AND column_name = 'response'
       AND data_type = 'jsonb'
  ) THEN
    ALTER TABLE wa_idempotency
      ALTER COLUMN response TYPE BYTEA USING convert_to(response::text, 'UTF8');
  END IF;
END $$;

CREATE INDEX IF NOT EXISTS wa_idempotency_created_at_idx ON wa_idempotency (created_at);

-- secret is stored in the clear because outbound deliveries have to HMAC with it. Unlike an
-- API key, which is only ever verified, this one has to be reproduced. Protect it at the
-- database layer; it is shown to the consumer once at creation and never again.
CREATE TABLE IF NOT EXISTS wa_webhooks (
  id          TEXT PRIMARY KEY,
  api_key_id  TEXT NOT NULL REFERENCES wa_api_keys (id) ON DELETE CASCADE,
  url         TEXT NOT NULL,
  secret      TEXT NOT NULL,
  events      TEXT[] NOT NULL DEFAULT '{}',
  disabled_at TIMESTAMPTZ,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (api_key_id, url)
);

CREATE INDEX IF NOT EXISTS wa_webhooks_api_key_idx ON wa_webhooks (api_key_id)
  WHERE disabled_at IS NULL;

-- payload is BYTEA for the same reason wa_idempotency.response is: it is the exact body the
-- signature covers. Re-serialising it would invalidate every signature on a replay.
CREATE TABLE IF NOT EXISTS wa_webhook_events (
  id              TEXT PRIMARY KEY,
  webhook_id      TEXT NOT NULL REFERENCES wa_webhooks (id) ON DELETE CASCADE,
  api_key_id      TEXT NOT NULL,
  type            TEXT NOT NULL,
  message_id      TEXT NOT NULL,
  payload         BYTEA NOT NULL,
  status          TEXT NOT NULL,
  attempts        INT NOT NULL DEFAULT 0,
  next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  last_error      TEXT,
  last_status     INT,
  delivered_at    TIMESTAMPTZ,
  created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS wa_webhook_events_due_idx
  ON wa_webhook_events (next_attempt_at)
  WHERE status = 'pending';

CREATE INDEX IF NOT EXISTS wa_webhook_events_dead_idx
  ON wa_webhook_events (api_key_id, created_at DESC)
  WHERE status = 'dead';

-- One event of a type per message per webhook. Receipts are unordered and can repeat, and a
-- duplicate delivery_ack must not become a second message.delivered.
CREATE UNIQUE INDEX IF NOT EXISTS wa_webhook_events_once_idx
  ON wa_webhook_events (webhook_id, message_id, type);

CREATE TABLE IF NOT EXISTS wa_job_attempts (
  id               BIGSERIAL PRIMARY KEY,
  idempotency_key  TEXT NOT NULL REFERENCES wa_jobs (idempotency_key) ON DELETE CASCADE,
  attempt          INT  NOT NULL,
  outcome          TEXT NOT NULL,
  error_code       TEXT,
  at               TIMESTAMPTZ NOT NULL,
  UNIQUE (idempotency_key, attempt)
);

-- A failover retry is a brand-new message (new idempotency key, new row) rather than a mutation
-- of the one that failed — see wa_sender_pools below for why. This just links the two for
-- display; it carries no behaviour of its own.
ALTER TABLE wa_jobs ADD COLUMN IF NOT EXISTS failover_of TEXT REFERENCES wa_jobs (idempotency_key);

-- A sender with no rows here behaves exactly as it does today: one static session, resolved
-- from WA_SENDERS, no extra query. Rows only exist for senders an operator has deliberately
-- pooled.
--
-- disqualified is a *sticky* mark, distinct from is_main=false. Every member starts as a
-- normal, promotable backup; only ever having failed while it was main earns this mark, and
-- once marked it is excluded from auto-promotion until a human clears it (Reinstate) — a
-- backup that's never been tried is still trusted, a session that broke while serving is not.
--
-- At most one main per sender is enforced by the partial unique index below, not by
-- application code: two processes racing a promotion can't both "win".
CREATE TABLE IF NOT EXISTS wa_sender_pools (
  sender        TEXT NOT NULL,
  session_id    TEXT NOT NULL,
  is_main       BOOLEAN NOT NULL DEFAULT false,
  disqualified  BOOLEAN NOT NULL DEFAULT false,
  rank          INT NOT NULL DEFAULT 0,
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (sender, session_id)
);

CREATE UNIQUE INDEX IF NOT EXISTS wa_sender_pools_main_idx ON wa_sender_pools (sender)
  WHERE is_main;

-- wa_senders replaces WA_SENDERS as the source of truth for which sender names exist. It is
-- read by wa-consumer-api's senders.Cache (a poll-refreshed atomic.Pointer, not a query per
-- request — see wa-shared/senders/cache.go) and written only through the /internal/senders
-- admin routes, which wa-console calls on a user's behalf. owner_id is an opaque reference to
-- a wa-console (Laravel) user id — this database has no foreign key to enforce it, since
-- wa-console lives in a separate logical database (see docker/postgres/init).
CREATE TABLE IF NOT EXISTS wa_senders (
  name        TEXT PRIMARY KEY,
  owner_id    TEXT NOT NULL,
  mode        TEXT NOT NULL CHECK (mode IN ('single', 'pool')),
  session_id  TEXT,
  priority    INT NOT NULL DEFAULT 0,
  dry_run     BOOLEAN NOT NULL DEFAULT false,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS wa_senders_owner_idx ON wa_senders (owner_id);

-- Nullable: existing wa_api_keys rows predate ownership and are backfilled by hand once
-- (see rollout notes). New rows created through /internal/keys always set it.
ALTER TABLE wa_api_keys ADD COLUMN IF NOT EXISTS owner_id TEXT;

CREATE INDEX IF NOT EXISTS wa_api_keys_owner_idx ON wa_api_keys (owner_id);
