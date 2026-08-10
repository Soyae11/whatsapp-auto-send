package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"

	"wa-shared/circuit"
	"wa-shared/config"
	"wa-shared/dispatch"
	"wa-shared/logging"
	"wa-shared/slots"
	"wa-shared/store"
	"wa-shared/wa"
	"wa-shared/notify"
	"wa-dispatcher/internal/worker"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "worker: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	log := logging.New(cfg.LogLevel)

	if len(cfg.Sessions) == 0 {
		return fmt.Errorf("WA_SESSIONS is required for the worker: a comma-separated list of session ids to serve")
	}

	waClient, err := wa.New(cfg.WAServiceURL, cfg.WAServiceAPIKey,
		wa.WithTimeout(cfg.WATimeout),
		wa.WithSendTimeout(cfg.WASendTimeout),
	)
	if err != nil {
		return err
	}

	bootCtx, cancelBoot := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancelBoot()

	rdb := redis.NewClient(cfg.RedisOptions())
	defer func() { _ = rdb.Close() }()
	if err := rdb.Ping(bootCtx).Err(); err != nil {
		return fmt.Errorf("redis unreachable: %w", err)
	}

	jobs, err := store.Open(bootCtx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer jobs.Close()
	if err := jobs.Migrate(bootCtx); err != nil {
		return err
	}

	inspector := asynq.NewInspector(cfg.AsynqRedisOpt())
	defer func() { _ = inspector.Close() }()

	breaker, err := circuit.New(rdb, inspector, cfg.Circuit, log)
	if err != nil {
		return err
	}

	watcher, err := circuit.NewWatcher(breaker, waClient, cfg.Sessions, cfg.Watch, log)
	if err != nil {
		return err
	}

	emitter := notify.NewEmitter(jobs, log)
	deliverer := notify.NewDeliverer(jobs, log)

	// The worker otherwise only ever consumes; this is the one path where it also produces —
	// a pool failover's immediate resend via a backup session (see internal/worker/handler.go,
	// failoverPooled). Nil coalescer: coalescing merges sends to the same recipient within a
	// window, which isn't a concern a failover resend has.
	allocator, err := slots.New(rdb, cfg.Slots)
	if err != nil {
		return err
	}
	asynqClient := asynq.NewClient(cfg.AsynqRedisOpt())
	defer func() { _ = asynqClient.Close() }()
	enqueuer := dispatch.New(asynqClient, inspector, allocator, nil, jobs, log)

	handler := worker.NewHandler(waClient, jobs, log,
		worker.WithBreaker(breaker),
		worker.WithEmitter(emitter),
		worker.WithPools(jobs),
		worker.WithEnqueuer(enqueuer),
	)

	supervisor, err := worker.NewSupervisor(cfg.Sessions, cfg.AsynqRedisOpt(), handler, worker.Options{
		SendTimeout: cfg.WASendTimeout,
		Log:         log,
	})
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := supervisor.Start(); err != nil {
		return err
	}
	log.Info("worker started",
		"sessions", cfg.Sessions,
		"redis_addr", cfg.RedisOptions().Addr,
		"wa_service_url", cfg.WAServiceURL,
		"concurrency_per_session", worker.Concurrency,
		"circuit_threshold", cfg.Circuit.Threshold,
		"health_poll_interval", cfg.Watch.Interval.String(),
	)

	watcherDone := make(chan struct{})
	go func() {
		defer close(watcherDone)
		watcher.Run(ctx)
	}()

	// The deliverer lives here rather than in the API so a webhook endpoint that is slow or
	// down never adds latency to a consumer's send. It is unrelated to the send concurrency
	// guarantee: it holds no slot and touches no session queue.
	delivererDone := make(chan struct{})
	go func() {
		defer close(delivererDone)
		deliverer.Run(ctx)
	}()

	<-ctx.Done()
	log.Info("shutdown signal received, waiting for in-flight sends")

	supervisor.Shutdown()
	<-watcherDone
	<-delivererDone
	log.Info("worker stopped")
	return nil
}
