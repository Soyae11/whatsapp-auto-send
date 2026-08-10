-- Sessions and the Postgres-backed Baileys auth state.
--
-- Auth state lives here and never on disk: a container restart must not cost a re-pair.
-- Buffers inside creds/keys are stored as BufferJSON markers ({"type":"Buffer","data":<base64>}),
-- so every read must go through BufferJSON.reviver and every write through BufferJSON.replacer.

CREATE TABLE wa_sessions (
  id            TEXT PRIMARY KEY,
  label         TEXT NOT NULL,
  status        TEXT NOT NULL DEFAULT 'new',
  phone_number  TEXT,
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT wa_sessions_status_check
    CHECK (status IN ('new', 'pairing', 'connected', 'disconnected', 'logged_out'))
);

CREATE TABLE wa_auth_creds (
  session_id  TEXT PRIMARY KEY REFERENCES wa_sessions(id) ON DELETE CASCADE,
  creds       JSONB NOT NULL,
  updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- The primary key is also the read path: every get() filters on
-- (session_id, key_type, key_id = ANY($3)), so no extra index is needed.
CREATE TABLE wa_auth_keys (
  session_id  TEXT NOT NULL REFERENCES wa_sessions(id) ON DELETE CASCADE,
  key_type    TEXT NOT NULL,
  key_id      TEXT NOT NULL,
  key_data    JSONB NOT NULL,
  PRIMARY KEY (session_id, key_type, key_id)
);
