package senders

// Concern: supporting.
//
// The registry behind WA_SENDERS. A sender is the name a consumer addresses
// ("hr-notifications"); it maps to a default lane and an optional dry-run flag, plus either a
// static gateway session id (single mode: "name:session-id") or a pool of sessions managed at
// runtime (pool mode: "name:pool", see wa-shared/store/pools.go — the pool is keyed by this same
// sender name, so there is no separate pool id to configure). Consumers never see a session id
// or a phone number, which is what lets a number be re-paired or replaced without anyone
// redeploying.
