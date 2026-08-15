package api

import (
	"context"
	"errors"

	"wa-shared/senders"
	"wa-shared/store"
	"wa-shared/wa"
)

// errSenderPoolExhausted means the sender has a pool but no member currently qualifies as
// main — every session has failed while serving and been disqualified. The caller must reject
// the send outright, never fall back to anything.
var errSenderPoolExhausted = errors.New("api: sender pool exhausted")

// errSenderPoolMisconfigured means the sender is declared pool mode (senders.ModePool) but no
// pool rows exist for it yet — an operator changed WA_SENDERS to "name:pool" without ever
// calling POST /internal/senders/{name}/pool to create the pool. Unlike errSenderPoolExhausted
// (a pool that existed and failed), this is a deploy-time config error.
var errSenderPoolMisconfigured = errors.New("api: sender declared pool mode but has no pool")

// resolveSendSession overrides sender.SessionID with its pool's current session, for pool-mode
// senders. Single-mode senders are returned unchanged without ever touching the pool store —
// mode is decided once, at config-parse time, not inferred from what happens to be in the
// database.
//
// Load spreading (batch sends and a busy queue are the same signal — see pools.go's package
// comment) lives here too: once main's estimated wait crosses poolBusyDelay, new sends share
// the pool by simple round robin across every member that is neither disqualified nor
// circuit-open, main included — it is still useful capacity, just no longer exclusive. This
// never changes who is main; that only happens on an actual send failure (see the dispatcher
// and receipts.go).
func (s *Server) resolveSendSession(ctx context.Context, sender senders.Sender) (senders.Sender, error) {
	if sender.Mode != senders.ModePool {
		return sender, nil
	}
	if s.pools == nil {
		return sender, nil
	}

	members, err := s.pools.Pool(ctx, sender.Name)
	if err != nil {
		return sender, err
	}
	if len(members) == 0 {
		return sender, errSenderPoolMisconfigured
	}

	var main string
	for _, m := range members {
		if m.IsMain {
			main = m.SessionID
			break
		}
	}
	if main == "" {
		return sender, errSenderPoolExhausted
	}

	// Uncertain or comfortably short: use main. Spreading only kicks in once we've *confirmed*
	// it is backed up past the threshold — never on a guess.
	if depth, err := s.enqueuer.Depth(ctx, main); err != nil || depth < s.poolBusyDelay {
		sender.SessionID = main
		return sender, nil
	}

	isOpen := s.isCircuitOpen(ctx)
	eligible := make([]string, 0, len(members))
	for _, m := range members {
		if m.Disqualified || isOpen(m.SessionID) {
			continue
		}
		eligible = append(eligible, m.SessionID)
	}
	if len(eligible) == 0 {
		sender.SessionID = main
		return sender, nil
	}

	n, err := s.nextRoundRobin(ctx, sender.Name, len(eligible))
	if err != nil {
		sender.SessionID = main
		return sender, nil
	}
	sender.SessionID = eligible[n]
	return sender, nil
}

// rotatePoolAfterFailure reacts to a session that just failed serving sender. This is the async
// path (a post-sent rejection) — it can only ever rotate, never resend the rejected message
// itself (its text is long gone by the time a receipt arrives). See the dispatcher's
// synchronous path for the one case that does resend.
//
// errorCode is the rejection's reason, and only reasons that indict the session rotate anything.
// A receipt saying the recipient is not on WhatsApp is about that phone number, not the socket
// that carried it; rotating on one would disqualify a healthy member per bad number until the
// pool is exhausted. The synchronous path gates on the same question via Verdict.TripsCircuit —
// wa.FaultsSession is what they share, since a receipt never has an error to classify.
func (s *Server) rotatePoolAfterFailure(ctx context.Context, sender, failedSessionID, errorCode string) {
	if s.pools == nil {
		return
	}
	if !wa.FaultsSessionCode(errorCode) {
		return
	}
	if known, err := s.senders.Load().Get(sender); err != nil || known.Mode != senders.ModePool {
		return
	}
	if _, err := s.pools.Rotate(ctx, sender, failedSessionID, s.isCircuitOpen(ctx)); err != nil {
		s.log.Error("could not rotate pool after a send failure",
			"sender", sender, "session_id", failedSessionID, "error", err)
	}
}

// rotateToHealthyMember rotates sender's pool away from failedSessionID and reports which session
// should serve the send instead. Rotate alone cannot answer that: it returns "" both when the
// pool is genuinely out of options and when failedSessionID was only a load-spread backup, which
// it disqualifies while leaving a perfectly healthy main standing. Re-reading the pool afterwards
// collapses the two into the question the caller actually has — is there anywhere left to send?
func (s *Server) rotateToHealthyMember(ctx context.Context, sender, failedSessionID string) (string, bool) {
	isOpen := s.isCircuitOpen(ctx)

	promotedTo, err := s.pools.Rotate(ctx, sender, failedSessionID, isOpen)
	if err != nil {
		s.log.Error("could not rotate pool for a logged-out sender",
			"sender", sender, "session_id", failedSessionID, "error", err)
		return "", false
	}
	if promotedTo != "" {
		return promotedTo, true
	}

	members, err := s.pools.Pool(ctx, sender)
	if err != nil {
		s.log.Error("could not re-read pool after rotating", "sender", sender, "error", err)
		return "", false
	}
	return store.PickHealthyMember(members, failedSessionID, isOpen)
}

// isCircuitOpen adapts the circuit breaker to PickHealthyBackup's isOpen callback shape.
func (s *Server) isCircuitOpen(ctx context.Context) func(sessionID string) bool {
	return func(sessionID string) bool {
		if s.circuit == nil {
			return false
		}
		state, err := s.circuit.State(ctx, sessionID)
		return err == nil && state.Open
	}
}

// nextRoundRobin advances a per-sender counter shared across every wa-consumer-api process
// (Redis, not process memory — matching how circuit/slots/coalescing state already work), and
// returns an index into a candidate list of length n.
func (s *Server) nextRoundRobin(ctx context.Context, sender string, n int) (int, error) {
	if s.rdb == nil || n <= 0 {
		return 0, errors.New("round robin unavailable")
	}
	v, err := s.rdb.Incr(ctx, "wa:pool:rr:"+sender).Result()
	if err != nil {
		return 0, err
	}
	return int((v - 1) % int64(n)), nil
}
