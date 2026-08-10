package slots

// Concern: pacing (criterion 3) — this is the pacing core.
//
// A Redis Lua allocator handing out monotonically increasing future send times per session,
// spaced by a jittered gap. The jitter matters: a constant interval is its own machine
// signature. Exceeding the horizon is refused rather than queued, so nobody is silently
// promised a send an hour out.
