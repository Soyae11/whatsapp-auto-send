package tasks

// Concern: job queue (criterion 2).
//
// The naming contract shared by the API and the worker: the task type, the payload struct,
// the three lanes, and QueueFor/QueuesFor. Both processes must agree here exactly — a payload
// that differs between them is the failure the compose env anchor exists to prevent.
