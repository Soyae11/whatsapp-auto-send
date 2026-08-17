package api

import (
	"context"
	"errors"
	"sync"
	"time"

	"wa-shared/senders"
	"wa-shared/store"
	"wa-shared/wa"
)

// errSenderPoolExhausted means every member has failed while serving and been disqualified, so
// no member qualifies as main. The caller must reject the send outright, never fall back.
var errSenderPoolExhausted = errors.New("api: sender pool exhausted")

// errSenderPoolMisconfigured means the sender is declared pool mode but no pool rows exist yet —
// WA_SENDERS says "name:pool" and nobody ever called POST /internal/senders/{name}/pool. Unlike
// errSenderPoolExhausted, this is a deploy-time config error.
var errSenderPoolMisconfigured = errors.New("api: sender declared pool mode but has no pool")

// resolveSendSession overrides sender.SessionID with its pool's current session, for pool-mode
// senders. Mode is decided at config-parse time, never inferred from the database.
//
// Load spreading lives here too: once main's estimated wait crosses poolBusyDelay, new sends
// share the pool by round robin across every member that is neither disqualified nor
// circuit-open, main included. This never changes who is main — only a send failure does.
//
// It returns the pool it read along with the sender, so the rest of the request can decide
// against the same snapshot instead of asking the database again. senderCanSend is the caller
// that needs it: a second read there would be a query per send that this one already paid for,
// and a concurrent handover between the two would leave it rotating against a pool that no
// longer matches the session it was handed. A single-mode sender reads nothing and returns nil.
//
// memo, when non-nil, holds the reads across the items of one batch. The round robin is
// deliberately outside it — sharing a decision is the point of the memo, but sharing the *choice*
// would hand a hundred-item batch to one member and undo the spreading it just decided to do.
func (s *Server) resolveSendSession(ctx context.Context, sender senders.Sender, memo *sendMemo) (senders.Sender, []store.PoolMember, error) {
	if sender.Mode != senders.ModePool {
		return sender, nil, nil
	}
	if s.pools == nil {
		return sender, nil, nil
	}

	routing := memo.routingFor(sender.Name, func() poolRouting {
		return s.poolRoutingFor(ctx, sender.Name)
	})
	if routing.err != nil {
		return sender, routing.members, routing.err
	}

	// Not spreading, or nothing eligible to spread across: main carries it.
	if len(routing.spreadAcross) == 0 {
		sender.SessionID = routing.main
		return sender, routing.members, nil
	}

	n, err := s.nextRoundRobin(ctx, sender.Name, len(routing.spreadAcross))
	if err != nil {
		sender.SessionID = routing.main
		return sender, routing.members, nil
	}
	sender.SessionID = routing.spreadAcross[n]
	return sender, routing.members, nil
}

// poolRouting is what resolveSendSession works out about a sender that does not vary between two
// sends to it: the pool as read, who is main, and — only when main is confirmed backed up — which
// members are eligible to share the load. Which of those a given message goes to does vary, and
// is decided per send.
type poolRouting struct {
	members      []store.PoolMember
	main         string
	spreadAcross []string
	err          error
}

func (s *Server) poolRoutingFor(ctx context.Context, sender string) poolRouting {
	members, err := s.pools.Pool(ctx, sender)
	if err != nil {
		return poolRouting{err: err}
	}
	if len(members) == 0 {
		return poolRouting{err: errSenderPoolMisconfigured}
	}

	mainMember, ok := store.MainOf(members)
	if !ok {
		return poolRouting{members: members, err: errSenderPoolExhausted}
	}
	routing := poolRouting{members: members, main: mainMember.SessionID}

	// Spreading only kicks in once main is *confirmed* backed up past the threshold, never on a
	// guess: an unreadable depth means main, and so does having nothing to read it with.
	if s.enqueuer == nil {
		return routing
	}
	depth, err := s.enqueuer.Depth(ctx, routing.main)
	if err != nil || depth < s.poolBusyDelay {
		return routing
	}

	isOpen := s.openCircuits(ctx, members)
	for _, m := range members {
		if store.EligibleToServe(m, "", isOpen) {
			routing.spreadAcross = append(routing.spreadAcross, m.SessionID)
		}
	}
	return routing
}

// rotatePoolAfterFailure reacts to a session that just failed serving sender. This is the async
// path (a post-sent rejection): it can only rotate, never resend, since the message text is long
// gone by the time a receipt arrives.
//
// Only reasons that indict the session rotate anything. A receipt naming the recipient is about
// that phone number, not the socket that carried it; rotating on one would burn a healthy member
// per bad number until the pool is exhausted. wa-gateway maps WhatsApp's ack error into these
// codes — see mapAckError there, which is what keeps this gate more than decorative.
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

// rotationProbeBudget caps how long rotateToConnectedMember spends asking wa-gateway which pool
// members are logged in, leaving the rest of senderCanSend's three seconds for the rotation write
// that follows.
//
// It is a budget for the whole walk, which is why the walk asks every member at once. Probing in
// rank order and stopping at the first connected member sounds cheaper, but it makes the budget a
// per-pool latency limit rather than a per-probe one: the members are asked about exactly when
// wa-gateway is degraded enough to be reporting logged_out, so a pool big enough that the slow
// answers add up past the budget could never reach the members further down, and every send would
// be refused with a connected session sitting at the bottom of the pool. Asked concurrently, one
// budget covers a pool of any size.
//
// Some answers do come from sessionCache, which is keyed per session id and shared process-wide,
// so a walk repeated inside SessionStatusTTL is largely free. That is a bonus, not the plan: a
// probe cut short by the budget caches nothing, so the members that most need re-asking are
// exactly the ones the cache cannot help with.
const rotationProbeBudget = 1500 * time.Millisecond

// rotateToConnectedMember hands sender's pool over from failedSessionID to a member that is
// actually logged in, and reports which session should serve the send instead — answering the
// question the pool store cannot: is the session it picked really connected? A member that is
// logged out but has never failed a send is neither disqualified nor circuit-open, so without the
// status check a send could be rotated onto a second dead session and accepted.
//
// Nothing here disqualifies anyone, failedSessionID included. Disqualified is permanent until
// someone calls Reinstate, and every fact this path has came from a status read taken exactly
// when wa-gateway is degraded enough to report logged_out — far too weak a signal to retire a
// member on. Two shapes of that mistake are worth naming, because both were live:
//
//   - Feeding the logged-out members back to Rotate as ineligible meant a failed main with no
//     connected backup fell through to Disqualify, leaving the pool with no main at all. Every
//     later send then answered sender_pool_exhausted — a permanent outage needing a manual
//     promotion — while the sessions themselves came back on their own seconds later.
//   - Disqualifying a load-spread backup on the same read spread that outage over several
//     requests instead of one, retiring the pool a member at a time.
//
// So: a connected member is found, or the pool is left exactly as it was and the caller refuses
// the send. A refusal costs one message; a wrongly retired pool costs every message after it.
//
// A status read that errors is taken as usable, matching how senderCanSend treats the same
// unknown: refusing on a failed health check is the worse way to be wrong.
func (s *Server) rotateToConnectedMember(ctx context.Context, sender, failedSessionID string, members []store.PoolMember) (string, bool) {
	next, found := s.firstConnectedMember(ctx, members, failedSessionID, s.openCircuits(ctx, members))
	if !found {
		return "", false
	}

	// Only main is worth a write. A logged-out load-spread backup is simply routed around: it
	// holds no title to hand over, and this send has somewhere better to go either way. A session
	// that is not in the pool at all takes this branch too, and should — there is nothing to hand
	// over on its behalf either.
	if !store.IsMain(members, failedSessionID) {
		return next, true
	}

	if err := s.pools.Handover(ctx, sender, next); err != nil {
		s.log.Error("could not hand over main for a logged-out sender",
			"sender", sender, "session_id", failedSessionID, "rotated_to", next, "error", err)
		return "", false
	}
	return next, true
}

// firstConnectedMember returns the lowest-rank member eligible to serve that answers as something
// other than logged out. Main counts — only a promotion has reason to exclude it, and this is also
// how a send that load spreading had merely routed to a downed backup finds its way back to a
// perfectly healthy main.
//
// Every candidate is asked at once and the answers are read back in rank order, so the result is
// the same one a sequential walk would give, in the time of the slowest probe rather than the sum
// of all of them. See rotationProbeBudget for why that is correctness and not tuning.
//
// Running out of budget is not an answer. A Status call cut short by probeCtx fails with no
// evidence either way, and reading that as "usable" would hand back a member that may well be
// logged out — the hole the probing exists to close. Those members are left out of the result, so
// a walk that learned nothing refuses the send and the caller's retry gets to probe again.
func (s *Server) firstConnectedMember(ctx context.Context, members []store.PoolMember, failedSessionID string, isOpen func(string) bool) (string, bool) {
	candidates := make([]store.PoolMember, 0, len(members))
	for _, m := range members {
		if store.EligibleToServe(m, failedSessionID, isOpen) {
			candidates = append(candidates, m)
		}
	}
	if len(candidates) == 0 {
		return "", false
	}

	probeCtx, cancel := context.WithTimeout(ctx, rotationProbeBudget)
	defer cancel()

	// One slot per candidate, written by that candidate's goroutine alone.
	connected := make([]bool, len(candidates))
	var wg sync.WaitGroup
	for i, m := range candidates {
		wg.Add(1)
		go func() {
			defer wg.Done()

			view, err := s.sessions.Status(probeCtx, m.SessionID)
			switch {
			case err == nil:
				connected[i] = view != wa.StatusLoggedOut
			case probeCtx.Err() != nil:
				// Out of budget: nothing was learned about this member, so it stays out.
			default:
				// A status read that failed for its own reasons is taken as usable, matching how
				// senderCanSend treats the same unknown: refusing on a failed health check is the
				// worse way to be wrong.
				connected[i] = true
			}
		}()
	}
	wg.Wait()

	for i, m := range candidates {
		if connected[i] {
			return m.SessionID, true
		}
	}

	if probeCtx.Err() != nil {
		s.log.Warn("ran out of budget probing pool members, refusing rather than guessing",
			"sender", candidates[0].Sender, "session_id", failedSessionID, "probed", len(candidates))
	}
	return "", false
}

// openCircuits reads the circuit of every pool member in one round trip and answers the isOpen
// callback the store's pickers take from that snapshot. The per-session form below costs a Redis
// round trip each time it is asked, and the pool paths ask once per member, twice per send — so
// a send to an eight-member pool spent sixteen sequential round trips inside senderCanSend's
// three seconds before this existed.
//
// An unreadable breaker reports every session closed rather than failing the caller: refusing
// every member of a pool because the circuit store is unreachable is the worse way to be wrong,
// which is the same stance the per-session form takes on an error.
func (s *Server) openCircuits(ctx context.Context, members []store.PoolMember) func(sessionID string) bool {
	if s.circuit == nil {
		return func(string) bool { return false }
	}

	ids := make([]string, 0, len(members))
	for _, m := range members {
		ids = append(ids, m.SessionID)
	}

	states, err := s.circuit.States(ctx, ids)
	if err != nil {
		s.log.Warn("could not read circuit state for a pool, treating every member as closed",
			"members", len(ids), "error", err)
		return func(string) bool { return false }
	}
	return func(sessionID string) bool { return states[sessionID].Open }
}

// isCircuitOpen adapts the circuit breaker to the isOpen callback pickHealthy takes, one session
// at a time. It is for callers that do not hold the pool — store.Rotate reads its own members —
// so they cannot ask for the whole set up front; anything that does hold it wants openCircuits.
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
// (Redis, not process memory, as circuit/slots/coalescing state already are) and returns an
// index into a candidate list of length n.
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
