package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/hibiken/asynq"

	"wa-shared/tasks"
)

const Concurrency = 1

var LaneWeights = map[tasks.Priority]int{
	tasks.PriorityCritical: 6,
	tasks.PriorityDefault:  3,
	tasks.PriorityBulk:     1,
}

const shutdownHeadroom = 30 * time.Second

type Options struct {
	SendTimeout time.Duration
	RetryDelayFunc asynq.RetryDelayFunc
	Log *slog.Logger
	AsynqLogLevel asynq.LogLevel
	DelayedTaskCheckInterval time.Duration
}

func (o Options) withDefaults() Options {
	if o.SendTimeout <= 0 {
		o.SendTimeout = 60 * time.Second
	}
	if o.RetryDelayFunc == nil {
		o.RetryDelayFunc = RetryDelay
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	if o.AsynqLogLevel == 0 {
		o.AsynqLogLevel = asynq.WarnLevel
	}
	if o.DelayedTaskCheckInterval <= 0 {
		o.DelayedTaskCheckInterval = time.Second
	}
	return o
}

func ServerConfig(sessionID string, opt Options) asynq.Config {
	opt = opt.withDefaults()

	queues := make(map[string]int, len(tasks.Priorities))
	for _, p := range tasks.Priorities {
		queues[tasks.QueueFor(sessionID, p)] = LaneWeights[p]
	}

	return asynq.Config{
		Concurrency:     Concurrency,
		Queues:          queues,
		StrictPriority:  false,
		RetryDelayFunc:  opt.RetryDelayFunc,
		ShutdownTimeout: opt.SendTimeout + shutdownHeadroom,

		DelayedTaskCheckInterval: opt.DelayedTaskCheckInterval,
		Logger:                   &asynqLogger{log: opt.Log.With("component", "asynq", "session_id", sessionID)},
		LogLevel:                 opt.AsynqLogLevel,
		ErrorHandler:             errorHandler(sessionID, opt.Log),
	}
}

func errorHandler(sessionID string, log *slog.Logger) asynq.ErrorHandler {
	return asynq.ErrorHandlerFunc(func(ctx context.Context, t *asynq.Task, err error) {
		retried, _ := asynq.GetRetryCount(ctx)
		maxRetry, _ := asynq.GetMaxRetry(ctx)
		queue, _ := asynq.GetQueueName(ctx)

		archived := IsPermanent(err) || retried >= maxRetry
		if !archived {
			return
		}
		log.Error("task archived",
			"session_id", sessionID,
			"queue", queue,
			"type", t.Type(),
			"retried", retried,
			"max_retry", maxRetry,
			"permanent", IsPermanent(err),
			"error", err)
	})
}

type SessionRunner struct {
	sessionID string
	srv       *asynq.Server
	mux       *asynq.ServeMux
	log       *slog.Logger

	mu      sync.Mutex
	started bool
}

func NewSessionRunner(sessionID string, redisOpt asynq.RedisConnOpt, h asynq.Handler, opt Options) (*SessionRunner, error) {
	if sessionID == "" {
		return nil, errors.New("worker: empty session id")
	}
	opt = opt.withDefaults()

	mux := asynq.NewServeMux()
	mux.Handle(tasks.TypeSendMessage, h)

	return &SessionRunner{
		sessionID: sessionID,
		srv:       asynq.NewServer(redisOpt, ServerConfig(sessionID, opt)),
		mux:       mux,
		log:       opt.Log,
	}, nil
}

func (r *SessionRunner) SessionID() string { return r.sessionID }

func (r *SessionRunner) Queues() []string { return tasks.QueuesFor(r.sessionID) }

func (r *SessionRunner) Start() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.started {
		return fmt.Errorf("worker: session %s already started", r.sessionID)
	}
	if err := r.srv.Start(r.mux); err != nil {
		return fmt.Errorf("worker: start session %s: %w", r.sessionID, err)
	}
	r.started = true
	r.log.Info("session runner started",
		"session_id", r.sessionID,
		"queues", r.Queues(),
		"concurrency", Concurrency)
	return nil
}

func (r *SessionRunner) Shutdown() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.started {
		return
	}
	r.srv.Shutdown()
	r.started = false
	r.log.Info("session runner stopped", "session_id", r.sessionID)
}

type Supervisor struct {
	runners []*SessionRunner
	log     *slog.Logger
}

func NewSupervisor(sessions []string, redisOpt asynq.RedisConnOpt, h asynq.Handler, opt Options) (*Supervisor, error) {
	if len(sessions) == 0 {
		return nil, errors.New("worker: no sessions configured")
	}
	opt = opt.withDefaults()

	seen := make(map[string]bool, len(sessions))
	runners := make([]*SessionRunner, 0, len(sessions))
	for _, id := range sessions {
		if seen[id] {
			return nil, fmt.Errorf("worker: session %q listed twice", id)
		}
		seen[id] = true

		r, err := NewSessionRunner(id, redisOpt, h, opt)
		if err != nil {
			return nil, err
		}
		runners = append(runners, r)
	}
	return &Supervisor{runners: runners, log: opt.Log}, nil
}

func (s *Supervisor) Runners() []*SessionRunner { return s.runners }

func (s *Supervisor) Start() error {
	for i, r := range s.runners {
		if err := r.Start(); err != nil {
			for _, started := range s.runners[:i] {
				started.Shutdown()
			}
			return err
		}
	}
	s.log.Info("supervisor started", "sessions", len(s.runners))
	return nil
}

func (s *Supervisor) Shutdown() {
	var wg sync.WaitGroup
	for _, r := range s.runners {
		wg.Add(1)
		go func(r *SessionRunner) {
			defer wg.Done()
			r.Shutdown()
		}(r)
	}
	wg.Wait()
	s.log.Info("supervisor stopped")
}
