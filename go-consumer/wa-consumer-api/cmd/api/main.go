package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"

	"wa-consumer-api/internal/api"
	"wa-shared/circuit"
	"wa-shared/config"
	"wa-shared/dispatch"
	"wa-shared/logging"
	"wa-shared/notify"
	"wa-shared/ratelimit"
	"wa-shared/senders"
	"wa-shared/slots"
	"wa-shared/store"
	"wa-shared/tasks"
	"wa-shared/wa"
)

// senderRegistryPollInterval is how stale the live registry can be after an admin change that
// isn't followed by an immediate refresh (see api.Server.refreshSenders) — e.g. a change made
// by a different process/replica.
const senderRegistryPollInterval = 5 * time.Second

func loadSenderRegistry(jobs *store.Store, dryRun bool) func(context.Context) (*senders.Registry, error) {
	return func(ctx context.Context) (*senders.Registry, error) {
		rows, err := jobs.ListAllSenders(ctx)
		if err != nil {
			return nil, err
		}
		list := make([]senders.Sender, 0, len(rows))
		for _, row := range rows {
			mode := senders.ModeSingle
			if row.Mode == store.SenderModePool {
				mode = senders.ModePool
			}
			list = append(list, senders.Sender{
				Name:            row.Name,
				Mode:            mode,
				SessionID:       row.SessionID,
				OwnerID:         row.OwnerID,
				DefaultPriority: tasks.PriorityDefault,
				DryRun:          dryRun,
			})
		}
		return senders.New(list)
	}
}

var version = "dev"

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "api: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	log := logging.New(cfg.LogLevel)

	waClient, err := wa.New(cfg.WAServiceURL, cfg.WAServiceAPIKey,
		wa.WithTimeout(cfg.WATimeout),
		wa.WithSendTimeout(cfg.WASendTimeout),
	)
	if err != nil {
		return err
	}

	rdb := redis.NewClient(cfg.RedisOptions())
	defer func() { _ = rdb.Close() }()

	bootCtx, cancelBoot := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancelBoot()
	if err := rdb.Ping(bootCtx).Err(); err != nil {
		return fmt.Errorf("redis unreachable: %w", err)
	}

	allocator, err := slots.New(rdb, cfg.Slots)
	if err != nil {
		return err
	}

	var coalescer *dispatch.Coalescer
	if cfg.Coalesce.Enabled() {
		if coalescer, err = dispatch.NewCoalescer(rdb, cfg.Coalesce); err != nil {
			return err
		}
	}

	asynqClient := asynq.NewClient(cfg.AsynqRedisOpt())
	defer func() { _ = asynqClient.Close() }()

	inspector := asynq.NewInspector(cfg.AsynqRedisOpt())
	defer func() { _ = inspector.Close() }()

	breaker, err := circuit.New(rdb, inspector, cfg.Circuit, log)
	if err != nil {
		return err
	}

	jobs, err := store.Open(bootCtx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer jobs.Close()
	if err := jobs.Migrate(bootCtx); err != nil {
		return err
	}

	limiter, err := ratelimit.New(rdb, cfg.RateLimitWindow)
	if err != nil {
		return err
	}

	loadRegistry := loadSenderRegistry(jobs, cfg.DryRun)
	initialRegistry, err := loadRegistry(bootCtx)
	if err != nil {
		return fmt.Errorf("load sender registry: %w", err)
	}
	senderCache := senders.NewCache(initialRegistry, loadRegistry)

	server := api.New(api.Options{
		Enqueuer:       dispatch.New(asynqClient, inspector, allocator, coalescer, jobs, log),
		WA:             waClient,
		Circuit:        breaker,
		Jobs:           jobs,
		Keys:           jobs,
		Limiter:        limiter,
		Senders:        senderCache,
		SenderStore:    jobs,
		Messages:       jobs,
		Idempotency:    jobs,
		Sessions:       api.NewSessionCache(waClient, api.SessionStatusTTL),
		Webhooks:       jobs,
		Receipts:       jobs,
		Pools:          jobs,
		PoolBusyDelay:  cfg.PoolBusyDelay,
		RedisClient:    rdb,
		Emitter:        notify.NewEmitter(jobs, log),
		Horizon:        cfg.Slots.Horizon,
		AdminKey:       cfg.AdminAPIKey,
		ConsoleKeyID:   cfg.ConsoleKeyID,
		Version:        version,
		Redis:          func(ctx context.Context) error { return rdb.Ping(ctx).Err() },
		Database:       jobs.Ping,
		GatewaySecret:  cfg.GatewayWebhookSecret,
		AllowedOrigins: cfg.CORSOrigins,
		Log:            log,
	})

	mux := http.NewServeMux()
	mux.Handle("/", server.Routes())

	srv := &http.Server{
		Addr:              cfg.APIAddr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go purgeIdempotency(ctx, jobs, log)
	go senderCache.Watch(ctx, senderRegistryPollInterval, func(err error) {
		log.Warn("could not refresh sender registry", "error", err)
	})

	errCh := make(chan error, 1)
	go func() {
		log.Info("api listening",
			"addr", cfg.APIAddr,
			"wa_service_url", cfg.WAServiceURL,
			"slot_gap", cfg.Slots.Gap.String(),
			"slot_spread", cfg.Slots.MinSpread.String()+"–"+cfg.Slots.MaxSpread.String(),
			"slot_horizon", cfg.Slots.Horizon.String(),
			"coalesce_window", cfg.Coalesce.Window.String(),
			"senders", senderCache.Load().Names(),
			"rate_limit_window", cfg.RateLimitWindow.String(),
		)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return fmt.Errorf("listen: %w", err)
	case <-ctx.Done():
		log.Info("shutdown signal received")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	log.Info("api stopped")
	return nil
}

const idempotencyPurgeInterval = time.Hour

// purgeIdempotency drops records past the retention window. The contract promises replay for
// at least 24 hours, so the window is the floor and the sweep interval is slack on top of it.
func purgeIdempotency(ctx context.Context, jobs *store.Store, log *slog.Logger) {
	ticker := time.NewTicker(idempotencyPurgeInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		sweepCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
		removed, err := jobs.PurgeIdempotency(sweepCtx, store.IdempotencyRetention)
		cancel()

		switch {
		case err != nil:
			log.Error("could not purge expired idempotency records", "error", err)
		case removed > 0:
			log.Info("purged expired idempotency records", "removed", removed)
		}
	}
}
