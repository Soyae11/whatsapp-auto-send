package notify

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"wa-shared/store"
)

type fakeQueue struct {
	claimed  []store.WebhookEvent
	claimErr error

	retired     int64
	retireErr   error
	retireCall  int
	retireLimit int
	claimCall   int

	// fullBatches is how many calls report a full batch before the queue reports a short one,
	// which is how the sweep learns it has caught up. Zero means the first call is already short.
	fullBatches int

	// retireErrAfter delays retireErr until this many calls have succeeded, so a mid-sweep
	// failure can be told apart from one on the first pass.
	retireErrAfter int
}

func (f *fakeQueue) ClaimDueEvents(context.Context, time.Time, time.Duration, int) ([]store.WebhookEvent, error) {
	f.claimCall++
	return f.claimed, f.claimErr
}

func (f *fakeQueue) MarkEventDelivered(context.Context, string, int, time.Time) error { return nil }

func (f *fakeQueue) MarkEventFailed(context.Context, string, *int, string, time.Time, bool) error {
	return nil
}

func (f *fakeQueue) DeadLetterDisabledEvents(_ context.Context, _ time.Time, limit int) (int64, error) {
	f.retireCall++
	f.retireLimit = limit

	if f.retireErr != nil && f.retireCall > f.retireErrAfter {
		return 0, f.retireErr
	}
	if f.retireCall <= f.fullBatches {
		return int64(limit), nil
	}
	// Caught up: a batch short of the limit is what ends a sweep.
	return f.retired, nil
}

func testDeliverer(q Queue) *Deliverer {
	return &Deliverer{
		queue: q,
		log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		now:   time.Now,
	}
}

// Events whose webhook was disabled are invisible to ClaimDueEvents, so the sweep is the only
// thing that can retire them. If it stops doing so they stay pending in the table for good.
func TestRetireSweepRetiresEventsOfDisabledWebhooks(t *testing.T) {
	q := &fakeQueue{retired: 3}
	retired, err := testDeliverer(q).RetireSweep(context.Background())
	if err != nil {
		t.Fatalf("RetireSweep: %v", err)
	}
	if retired != 3 {
		t.Errorf("RetireSweep = %d, want 3", retired)
	}
	if q.retireCall != 1 {
		t.Errorf("DeadLetterDisabledEvents called %d times, want 1; a short batch means caught up", q.retireCall)
	}
	if q.retireLimit != RetireBatchSize {
		t.Errorf("sweep limit = %d, want %d; an unbounded statement can hold locks across the table",
			q.retireLimit, RetireBatchSize)
	}
}

// The batch size bounds one statement, not the sweep. A webhook disabled with a large backlog has
// all of it to retire at once, and stopping after one batch would leave the rest for the next
// interval — hours of waiting to retire what one sweep clears in seconds.
func TestRetireSweepKeepsGoingUntilTheBacklogIsDrained(t *testing.T) {
	q := &fakeQueue{fullBatches: 3}
	retired, err := testDeliverer(q).RetireSweep(context.Background())
	if err != nil {
		t.Fatalf("RetireSweep: %v", err)
	}
	if want := int64(RetireBatchSize) * 3; retired != want {
		t.Errorf("RetireSweep = %d, want %d", retired, want)
	}
	if q.retireCall != 4 {
		t.Errorf("DeadLetterDisabledEvents called %d times, want 4 — three full batches then a short one",
			q.retireCall)
	}
}

// A sweep that never sees a short batch must still end, so one runaway backlog cannot hold the
// sweep goroutine forever. The next tick resumes where this one stopped.
func TestRetireSweepStopsAtMaxPasses(t *testing.T) {
	q := &fakeQueue{fullBatches: MaxRetirePasses + 10}
	if _, err := testDeliverer(q).RetireSweep(context.Background()); err != nil {
		t.Fatalf("RetireSweep: %v", err)
	}
	if q.retireCall != MaxRetirePasses {
		t.Errorf("DeadLetterDisabledEvents called %d times, want %d", q.retireCall, MaxRetirePasses)
	}
}

// Retiring is housekeeping. Delivery is the job, and a database that refuses the one must not
// cost us the other — they no longer share a call path at all, and Tick must not have picked the
// sweep back up.
func TestTickDeliversWithoutSweeping(t *testing.T) {
	q := &fakeQueue{retireErr: errors.New("database said no")}
	if err := testDeliverer(q).Tick(context.Background()); err != nil {
		t.Fatalf("Tick returned %v; delivery does not depend on the sweep", err)
	}
	if q.claimCall != 1 {
		t.Errorf("ClaimDueEvents called %d times, want 1", q.claimCall)
	}
	if q.retireCall != 0 {
		t.Errorf("DeadLetterDisabledEvents called %d times from Tick, want 0; the sweep has its own ticker",
			q.retireCall)
	}
}

// A sweep that errors reports what it managed before the failure, so a partial drain is not
// mistaken for nothing having happened.
func TestRetireSweepReportsWhatItRetiredBeforeFailing(t *testing.T) {
	want := errors.New("database said no")
	q := &fakeQueue{fullBatches: 2, retireErrAfter: 1, retireErr: want}

	retired, err := testDeliverer(q).RetireSweep(context.Background())
	if !errors.Is(err, want) {
		t.Fatalf("RetireSweep err = %v, want %v", err, want)
	}
	if retired != int64(RetireBatchSize) {
		t.Errorf("RetireSweep = %d, want %d from the pass that succeeded", retired, RetireBatchSize)
	}
}

// A genuine claim failure is still the sweep's failure, and must reach the caller.
func TestTickReportsClaimFailure(t *testing.T) {
	want := errors.New("claim exploded")
	q := &fakeQueue{claimErr: want}
	if err := testDeliverer(q).Tick(context.Background()); !errors.Is(err, want) {
		t.Errorf("Tick = %v, want %v", err, want)
	}
}
