package config

// Concern: supporting.
//
// Every environment variable, validated at boot. Note WA_SESSIONS and WA_SENDERS are two
// lists that must agree: a session is a WhatsApp account on the gateway, a sender is the
// consumer-facing name that maps onto one — either a single static session ("name:session-id")
// or a runtime-managed pool of sessions ("name:pool", see wa-shared/senders). See
// ARCHITECTURE.md's glossary.
