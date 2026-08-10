package dispatch

// Concern: job queue (criterion 2).
//
// Where a validated send becomes a scheduled asynq task: enqueuer.go reserves a slot and
// enqueues, coalesce.go merges repeats to the same recipient, manual.go is the operator's
// retry and cancel. The queue naming contract itself lives in internal/tasks.
