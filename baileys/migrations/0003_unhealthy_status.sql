-- A session whose socket is up but whose sends keep failing is not "connected" in any
-- useful sense. The Go worker reads this status to pause that session's queue rather than
-- draining retries into a dying socket.

ALTER TABLE wa_sessions DROP CONSTRAINT wa_sessions_status_check;

ALTER TABLE wa_sessions ADD CONSTRAINT wa_sessions_status_check
  CHECK (status IN ('new', 'pairing', 'connected', 'disconnected', 'logged_out', 'unhealthy'));
