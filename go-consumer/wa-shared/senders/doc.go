package senders

// Concern: supporting.
//
// The registry behind WA_SENDERS. A sender is the name a consumer addresses
// ("hr-notifications"); it maps to a gateway session id, a default lane, and an optional
// dry-run flag. Consumers never see a session id or a phone number, which is what lets a
// number be re-paired or replaced without anyone redeploying.
