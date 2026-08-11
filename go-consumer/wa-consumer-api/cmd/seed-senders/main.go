// seed-senders is a one-time bridge from the old WA_SENDERS env var to the wa_senders table.
// Run it once during rollout to a dynamic registry (see wa-shared/senders/cache.go), then drop
// WA_SENDERS from .env — the database is the source of truth from then on. Safe to re-run:
// existing names are skipped, not overwritten.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"wa-shared/senders"
	"wa-shared/store"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "seed-senders: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		waSenders  = flag.String("wa-senders", os.Getenv("WA_SENDERS"), "the WA_SENDERS value to seed from")
		waSessions = flag.String("wa-sessions", os.Getenv("WA_SESSIONS"), "the WA_SESSIONS value, for single-mode validation")
		owner      = flag.String("owner", "", "owner id every seeded sender is assigned to")
	)
	flag.Parse()

	if strings.TrimSpace(*owner) == "" {
		return fmt.Errorf("-owner is required: every wa_senders row needs an owner_id")
	}

	var sessions []string
	for _, id := range strings.Split(*waSessions, ",") {
		if id = strings.TrimSpace(id); id != "" {
			sessions = append(sessions, id)
		}
	}

	registry, err := senders.Parse(*waSenders, sessions)
	if err != nil {
		return fmt.Errorf("parse WA_SENDERS: %w", err)
	}

	databaseURL := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if databaseURL == "" {
		return fmt.Errorf("DATABASE_URL is required (e.g. postgres://dispatcher:...@localhost:5433/dispatcher)")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	s, err := store.Open(ctx, databaseURL)
	if err != nil {
		return err
	}
	defer s.Close()

	for _, name := range registry.Names() {
		sender, err := registry.Get(name)
		if err != nil {
			return err
		}
		mode := store.SenderModeSingle
		if sender.Mode == senders.ModePool {
			mode = store.SenderModePool
		}

		_, err = s.CreateSender(ctx, store.SenderInput{
			OwnerID:   *owner,
			Name:      sender.Name,
			Mode:      mode,
			SessionID: sender.SessionID,
		})
		switch {
		case err == nil:
			fmt.Printf("seeded %s (%s)\n", sender.Name, mode)
		case errors.Is(err, store.ErrSenderExists):
			fmt.Printf("skipped %s: already exists\n", sender.Name)
		default:
			return fmt.Errorf("seed %q: %w", sender.Name, err)
		}
	}
	return nil
}
