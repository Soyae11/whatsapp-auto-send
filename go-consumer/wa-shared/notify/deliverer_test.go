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

	retired    int64
	retireErr  error
	retireCall int
	claimCall  int
}

func (f *fakeQueue) ClaimDueEvents(context.Context, time.Time, time.Duration, int) ([]store.WebhookEvent, error) {
	f.claimCall++
	return f.claimed, f.claimErr
}

func (f *fakeQueue) MarkEventDelivered(context.Context, string, int, time.Time) error { return nil }

func (f *fakeQueue) MarkEventFailed(context.Context, string, *int, string, time.Time, bool) error {
	return nil
}

func (f *fakeQueue) DeadLetterDisabledEvents(context.Context, time.Time) (int64, error) {
	f.retireCall++
	return f.retired, f.retireErr
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
func TestTickRetiresEventsOfDisabledWebhooks(t *testing.T) {
	q := &fakeQueue{retired: 3}
	if err := testDeliverer(q).Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if q.retireCall != 1 {
		t.Errorf("DeadLetterDisabledEvents called %d times, want 1", q.retireCall)
	}
}

// Retiring is housekeeping. Delivery is the job, and a database that refuses the one must not
// cost us the other.
func TestTickDeliversEvenWhenRetiringFails(t *testing.T) {
	q := &fakeQueue{retireErr: errors.New("database said no")}
	if err := testDeliverer(q).Tick(context.Background()); err != nil {
		t.Fatalf("Tick returned %v; a failed retire must not abort the sweep", err)
	}
	if q.claimCall != 1 {
		t.Errorf("ClaimDueEvents called %d times, want 1", q.claimCall)
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
