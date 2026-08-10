package store

// Concern: supporting (persistence for all three concerns).
//
// pgx-backed Postgres with self-applied migrations. Owns wa_jobs (the job lifecycle and its
// attempt history), wa_api_keys, wa_idempotency, and the wa_webhooks / wa_webhook_events pair
// that internal/notify drains. File names here track the tables, which is why webhooks.go
// keeps its name while the package that consumes it is called notify.
