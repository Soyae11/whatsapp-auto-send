package worker

// Concern: pacing (criterion 3).
//
// One asynq.Server per session at Concurrency 1 — the hard "never two sends in flight on one
// WhatsApp account" guarantee — with weighted lanes, plus a supervisor over all sessions.
// handler.go processes a single send; retry.go decides whether a failure is worth retrying
// and how long to wait.
