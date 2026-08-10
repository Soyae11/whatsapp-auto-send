package circuit

// Concern: pacing (criterion 3) — the ban-avoidance kill switch.
//
// Counts consecutive failures per session and, past a threshold, pauses that session's three
// asynq lanes. The watcher polls wa-gateway health and needs a streak of good polls before
// resuming, so a flapping number does not un-pause on one lucky reading.
