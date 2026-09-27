-- Ownership belonged to the removed operator console. The gateway has a single API key and
-- no notion of tenants, so the column is dead weight.
DROP INDEX IF EXISTS wa_sessions_owner_idx;
ALTER TABLE wa_sessions DROP COLUMN IF EXISTS owner_id;
