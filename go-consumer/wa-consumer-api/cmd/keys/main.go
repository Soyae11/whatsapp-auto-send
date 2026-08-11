// keys provisions API keys for wa-consumer-api. Nothing else in this repo creates one:
// store.CreateAPIKey exists but nothing called it, so there was no way to actually mint a
// working credential for a consumer.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"wa-shared/auth"
	"wa-shared/store"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "keys: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		owner       = flag.String("owner", "", "opaque owner id this key belongs to (a wa-console user id for self-service keys)")
		name        = flag.String("name", "", "human-readable name for the key, e.g. wa-console")
		project     = flag.String("project", "", "project/team the key belongs to")
		environment = flag.String("env", "test", "live or test")
		sendersRaw  = flag.String("senders", "", "comma-separated sender names this key may send from")
		rateLimit   = flag.Int("rate-limit", auth.DefaultRateLimit, "requests per minute")
	)
	flag.Parse()

	senders := splitNonEmpty(*sendersRaw)

	databaseURL := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if databaseURL == "" {
		return fmt.Errorf("DATABASE_URL is required (e.g. postgres://dispatcher:...@localhost:5433/dispatcher)")
	}

	env, err := auth.ParseEnvironment(*environment)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	s, err := store.Open(ctx, databaseURL)
	if err != nil {
		return err
	}
	defer s.Close()

	key, secret, err := s.CreateAPIKey(ctx, store.APIKeyInput{
		OwnerID:     *owner,
		Name:        *name,
		Project:     *project,
		Environment: env,
		Senders:     senders,
		RateLimit:   *rateLimit,
	})
	if err != nil {
		return err
	}

	fmt.Printf("id:      %s\n", key.ID)
	fmt.Printf("senders: %s\n", strings.Join(key.Senders, ", "))
	fmt.Printf("secret:  %s\n", secret.Plaintext)
	fmt.Println("\nThe secret is shown once and is not recoverable. Store it now.")
	return nil
}

func splitNonEmpty(raw string) []string {
	var out []string
	for _, s := range strings.Split(raw, ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}
