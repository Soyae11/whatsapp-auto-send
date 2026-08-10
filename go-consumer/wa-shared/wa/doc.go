package wa

// Concern: supporting (the client to wa-gateway).
//
// Send, session status, list, health — four calls, which is the entire dispatcher-facing
// surface of the gateway. The session lifecycle (pair, restart, logout) is deliberately absent:
// that is the console's job under its own scoped credential, and this client's credential is
// not permitted to perform it. receipts.go verifies the signature on events coming back.
