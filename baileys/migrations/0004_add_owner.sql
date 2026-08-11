-- Nullable: existing rows predate ownership and are backfilled by hand once (see wa-console
-- rollout notes). New rows created through the console always set it. wa-console is the only
-- caller that ever supplies this value — see routes/sessions.ts.
ALTER TABLE wa_sessions ADD COLUMN owner_id TEXT;

CREATE INDEX IF NOT EXISTS wa_sessions_owner_idx ON wa_sessions (owner_id);
