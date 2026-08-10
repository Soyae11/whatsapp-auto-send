package worker

import (
	"errors"
	"math/rand/v2"
	"time"

	"github.com/hibiken/asynq"

	"wa-shared/wa"
)

const (
	MaxRetryDelay       = 10 * time.Minute
	RetryJitterFraction = 0.2
)

type Backoff struct {
	Base time.Duration
	Cap  time.Duration
}

type Verdict struct {
	Retryable bool
	Alert     bool
	Backoff   Backoff

	// TripsCircuit separates a sick session from a bad request. A recipient who is not on
	// WhatsApp says nothing about the socket, and five of them in a row must not pause a
	// healthy session's queues.
	TripsCircuit bool
}

var (
	backoffDefault      = Backoff{Base: 10 * time.Second, Cap: MaxRetryDelay}
	backoffDisconnected = Backoff{Base: 30 * time.Second, Cap: MaxRetryDelay}
	backoffRateLimited  = Backoff{Base: 5 * time.Minute, Cap: 30 * time.Minute}
)

// Classify consults the table first, then lets wa-gateway veto a retry: the service returns
// an explicit retryable flag and rule 3 says trust it. Neither source alone is enough — the
// table cannot know about codes added later, and the flag cannot know how long to wait.
func Classify(err error) Verdict {
	v := classify(err)
	if !wa.IsRetryable(err) {
		v.Retryable = false
	}
	return v
}

func classify(err error) Verdict {
	switch {
	case err == nil:
		return Verdict{}

	case errors.Is(err, wa.ErrSessionLoggedOut):
		return Verdict{Alert: true, TripsCircuit: true}

	case errors.Is(err, wa.ErrNotOnWhatsApp):
		return Verdict{}

	case errors.Is(err, wa.ErrInvalidPayload),
		errors.Is(err, wa.ErrPayloadTooLarge),
		errors.Is(err, wa.ErrUnsupportedMediaType):
		return Verdict{Alert: true}

	case errors.Is(err, wa.ErrUnauthorized),
		errors.Is(err, wa.ErrSessionNotFound):
		return Verdict{Alert: true, TripsCircuit: true}

	case errors.Is(err, wa.ErrSessionNotConnected):
		return Verdict{Retryable: true, TripsCircuit: true, Backoff: backoffDisconnected}

	case errors.Is(err, wa.ErrRateLimited):
		return Verdict{Retryable: true, TripsCircuit: true, Backoff: backoffRateLimited}

	case errors.Is(err, wa.ErrSendFailed),
		errors.Is(err, wa.ErrSendInProgress),
		errors.Is(err, wa.ErrUpstreamTimeout),
		errors.Is(err, wa.ErrInternal):
		return Verdict{Retryable: true, TripsCircuit: true, Backoff: backoffDefault}

	default:
		return Verdict{Retryable: true, TripsCircuit: true, Backoff: backoffDefault}
	}
}

func RetryDelay(n int, err error, _ *asynq.Task) time.Duration {
	b := Classify(err).Backoff
	if b.Base <= 0 {
		b = backoffDefault
	}
	return jitterDelay(exponential(b, n), RetryJitterFraction)
}

func exponential(b Backoff, n int) time.Duration {
	if n < 0 {
		n = 0
	}
	if n > 20 {
		return b.Cap
	}
	d := b.Base << uint(n)
	if d > b.Cap || d <= 0 {
		return b.Cap
	}
	return d
}

func jitterDelay(d time.Duration, frac float64) time.Duration {
	if frac <= 0 {
		return d
	}
	span := float64(d) * frac
	out := time.Duration(float64(d) + (rand.Float64()*2-1)*span)
	if out < time.Second {
		return time.Second
	}
	return out
}
