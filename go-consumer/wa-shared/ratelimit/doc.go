package ratelimit

// Concern: supporting (consumer API).
//
// A per-API-key fixed-window counter in Redis. The limit comes from the key record, so it is
// a property of who is calling rather than a global setting.
