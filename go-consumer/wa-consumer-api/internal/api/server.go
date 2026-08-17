package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"wa-shared/auth"
	"wa-shared/circuit"
	"wa-shared/dispatch"
	"wa-shared/ratelimit"
	"wa-shared/senders"
	"wa-shared/slots"
	"wa-shared/store"
	"wa-shared/tasks"
	"wa-shared/wa"
)

const maxBodyBytes = 1 << 20

type Circuit interface {
	Open(ctx context.Context, sessionID, reason, errorCode string) (bool, error)
	Close(ctx context.Context, sessionID string) (bool, error)
	State(ctx context.Context, sessionID string) (circuit.State, error)
	// States is State for a whole pool in one round trip. Sessions with nothing stored come
	// back as closed circuits, so a caller may index the map for any id it asked about.
	States(ctx context.Context, sessionIDs []string) (map[string]circuit.State, error)
}

type JobStore interface {
	List(ctx context.Context, f store.Filter) ([]store.Row, error)
	Get(ctx context.Context, idempotencyKey string) (*store.Row, error)
	Attempts(ctx context.Context, idempotencyKey string) ([]store.Attempt, error)
}

type KeyStore interface {
	AuthenticateAPIKey(ctx context.Context, hash string) (*auth.Key, error)
	CreateAPIKey(ctx context.Context, in store.APIKeyInput) (*auth.Key, auth.Secret, error)
	ListAPIKeysByOwner(ctx context.Context, ownerID string) ([]auth.Key, error)
	RevokeAPIKeyByOwner(ctx context.Context, id, ownerID string) (*auth.Key, bool, error)
	GrantKeySender(ctx context.Context, id, sender string) error
	RevokeKeySender(ctx context.Context, id, sender string) error
}

// SenderStore is the wa_senders table — see wa-shared/store/senders.go. Distinct from Pools:
// this is which senders exist and who owns them, not a pool-mode sender's session membership.
type SenderStore interface {
	ListAllSenders(ctx context.Context) ([]store.SenderRow, error)
	ListSenders(ctx context.Context, ownerID string) ([]store.SenderRow, error)
	CreateSender(ctx context.Context, in store.SenderInput) (*store.SenderRow, error)
	GetSender(ctx context.Context, name string) (*store.SenderRow, error)
	DeleteSender(ctx context.Context, name, ownerID string) error
}

type MessageStore interface {
	GetByPublicID(ctx context.Context, apiKeyID, publicID string) (*store.Row, error)
	ListMessages(ctx context.Context, f store.MessageFilter) ([]store.Row, bool, error)
	CancelByPublicID(ctx context.Context, apiKeyID, publicID string, at time.Time) (*store.Row, error)
	FailoverOfPublicID(ctx context.Context, apiKeyID, failoverOfKey string) (string, error)
	RetriedByPublicID(ctx context.Context, apiKeyID, idempotencyKey string) (string, error)
}

type IdempotencyStore interface {
	ClaimIdempotencyKey(ctx context.Context, apiKeyID, key, requestHash string) (claimID string, replay *store.IdempotentResponse, err error)
	RecordIdempotentResponse(ctx context.Context, apiKeyID, key, claimID string, statusCode int, body []byte) error
	ReleaseIdempotencyKey(ctx context.Context, apiKeyID, key, claimID string) error
}

// Pools is a sender's main/backup session pool — see wa_sender_pools. A sender with no pool
// rows is unaffected by any of this; pools are opt-in.
type Pools interface {
	Pool(ctx context.Context, sender string) ([]store.PoolMember, error)
	CurrentMain(ctx context.Context, sender string) (sessionID string, hasPool bool, err error)
	CreatePool(ctx context.Context, sender string, sessionIDs []string) error
	DeletePool(ctx context.Context, sender string) error
	Promote(ctx context.Context, sender, newMainSessionID string) error
	Handover(ctx context.Context, sender, newMainSessionID string) error
	Disqualify(ctx context.Context, sender, sessionID string) error
	Reinstate(ctx context.Context, sender, sessionID string) error
	AddMember(ctx context.Context, sender, sessionID string) error
	RemoveMember(ctx context.Context, sender, sessionID string) error
	Rotate(ctx context.Context, sender, failedSessionID string, isOpen func(sessionID string) bool) (promotedTo string, err error)
}

type RateLimiter interface {
	Allow(ctx context.Context, keyID string, limit int) (ratelimit.Result, error)
}

// Probe reports whether a dependency is usable. A nil Probe reads as healthy, so a caller
// that cannot cheaply check one does not have to claim it is broken.
type Probe func(context.Context) error

type Options struct {
	Enqueuer    *dispatch.Enqueuer
	WA          *wa.Client
	Circuit     Circuit
	Jobs        JobStore
	Keys        KeyStore
	Limiter     RateLimiter
	Senders     *senders.Cache
	SenderStore SenderStore
	Messages    MessageStore
	Idempotency IdempotencyStore
	Sessions    SessionStatusSource
	Webhooks    WebhookStore
	Receipts    Receipts
	Pools       Pools
	Emitter     Emitter
	Horizon     time.Duration
	AdminKey    string
	// ConsoleKeyID is wa-console's own sending key id — see config.Config.ConsoleKeyID. Empty
	// disables the auto-grant/revoke on sender create/delete.
	ConsoleKeyID string
	Version      string

	// PoolBusyDelay is how backed-up a pool's main session's estimated wait has to be before
	// load spreading kicks in and starts sharing new sends across the rest of the pool. Zero
	// uses a sane default — see WA_POOL_BUSY_DELAY.
	PoolBusyDelay time.Duration

	// Redis is load-bearing for a send and Database is not, so an unreachable Redis is
	// reported as `down` with a 503 while an unreachable Postgres is only `degraded`.
	Redis    Probe
	Database Probe

	// GatewaySecret is wa-gateway's WEBHOOK_SECRET. Receipts are refused without it, since
	// an unauthenticated receipt endpoint would let anyone mark any message delivered.
	GatewaySecret string

	// AllowedOrigins is an exact-match CORS allowlist for browser callers. Empty (the
	// default) sends no Access-Control-* headers at all — see withCORS in middleware.go.
	AllowedOrigins []string

	// Redis is the shared client backing pool load-spreading's round-robin counter — the
	// same connection info already used for slots/coalescing/circuit state, kept separate
	// from the Redis field above (a health probe, not a real client).
	RedisClient redis.Cmdable

	Log *slog.Logger
}

type Server struct {
	enqueuer       *dispatch.Enqueuer
	wa             *wa.Client
	circuit        Circuit
	jobs           JobStore
	keys           KeyStore
	limiter        RateLimiter
	senders        *senders.Cache
	senderStore    SenderStore
	messages       MessageStore
	idempotency    IdempotencyStore
	sessions       SessionStatusSource
	webhooks       WebhookStore
	receipts       Receipts
	pools          Pools
	poolBusyDelay  time.Duration
	rdb            redis.Cmdable
	emitter        Emitter
	horizon        time.Duration
	adminKey       string
	consoleKeyID   string
	version        string
	redis          Probe
	database       Probe
	gatewaySecret  string
	allowedOrigins []string
	log            *slog.Logger
}

// DefaultPoolBusyDelay is how backed-up a pool's main session has to be, estimated, before load
// spreading starts sharing new sends across the rest of the pool.
const DefaultPoolBusyDelay = 60 * time.Second

func New(o Options) *Server {
	horizon := o.Horizon
	if horizon <= 0 {
		horizon = slots.DefaultConfig().Horizon
	}
	poolBusyDelay := o.PoolBusyDelay
	if poolBusyDelay <= 0 {
		poolBusyDelay = DefaultPoolBusyDelay
	}
	return &Server{
		enqueuer:       o.Enqueuer,
		wa:             o.WA,
		circuit:        o.Circuit,
		jobs:           o.Jobs,
		keys:           o.Keys,
		limiter:        o.Limiter,
		senders:        o.Senders,
		senderStore:    o.SenderStore,
		messages:       o.Messages,
		idempotency:    o.Idempotency,
		sessions:       o.Sessions,
		webhooks:       o.Webhooks,
		receipts:       o.Receipts,
		pools:          o.Pools,
		poolBusyDelay:  poolBusyDelay,
		rdb:            o.RedisClient,
		emitter:        o.Emitter,
		horizon:        horizon,
		adminKey:       o.AdminKey,
		consoleKeyID:   o.ConsoleKeyID,
		version:        o.Version,
		redis:          o.Redis,
		database:       o.Database,
		gatewaySecret:  o.GatewaySecret,
		allowedOrigins: o.AllowedOrigins,
		log:            o.Log,
	}
}

// Routes assembles the four surfaces this service exposes. They differ in who may call them
// and how that is proved, which is the single most confusing thing about this package, so
// each one is registered by its own function below and named for its audience:
//
//	/v1/*                            consumer API key + per-key rate limit
//	/internal/*                      ADMIN_API_KEY
//	POST /internal/gateway/events    wa-gateway's HMAC over the raw body — no key at all
//	GET /v1/health                   unauthenticated
//
// Registration order is load-bearing and the sub-functions preserve it.
func (s *Server) Routes() http.Handler {
	consumer := s.consumerRoutes()
	operator := s.operatorRoutes()

	mux := http.NewServeMux()
	s.registerPublicRoutes(mux)
	s.registerIntakeRoutes(mux)
	mux.Handle("/v1/", chain(consumer, s.withAPIKey, s.withRateLimit))
	mux.Handle("/internal/", chain(operator, s.withAdminKey))
	mux.Handle("/", consumer)

	// withCORS sits outside mux so a preflight OPTIONS is answered before withAPIKey or
	// withAdminKey run — a preflight carries no Authorization header and would otherwise be
	// rejected as unauthenticated. It sits inside withRequestLog so preflights still get an
	// X-Request-Id and a log line.
	return chain(mux, s.withRequestID, s.withRequestLog, s.withCORS)
}

// consumerRoutes is the public API: the six documented /v1 endpoints other developers build
// against. Authenticated by a consumer API key and rate limited per key by the caller.
func (s *Server) consumerRoutes() *http.ServeMux {
	consumer := http.NewServeMux()
	consumer.HandleFunc("POST /v1/messages", s.handleSend)
	consumer.HandleFunc("/v1/messages", s.methodNotAllowed(http.MethodPost, http.MethodGet))
	consumer.HandleFunc("GET /v1/messages", s.handleListMessages)
	consumer.HandleFunc("POST /v1/messages/batch", s.handleSendBatch)
	// Registered per method rather than as a bare "/v1/messages/batch": a method-less
	// literal pattern overlaps "GET /v1/messages/{id}" — more specific in its path, more
	// general in its methods — and ServeMux panics on that pair at registration time,
	// which takes the whole API process down at boot.
	for _, method := range []string{
		http.MethodGet, http.MethodPut, http.MethodPatch,
		http.MethodDelete, http.MethodHead, http.MethodOptions,
	} {
		consumer.HandleFunc(method+" /v1/messages/batch", s.methodNotAllowed(http.MethodPost))
	}
	consumer.HandleFunc("GET /v1/messages/{id}", s.handleGetMessage)
	consumer.HandleFunc("/v1/messages/{id}", s.methodNotAllowed(http.MethodGet))
	consumer.HandleFunc("POST /v1/messages/{id}/cancel", s.handleCancelMessage)
	consumer.HandleFunc("/v1/messages/{id}/cancel", s.methodNotAllowed(http.MethodPost))
	consumer.HandleFunc("GET /v1/senders", s.handleListSenders)
	consumer.HandleFunc("/v1/senders", s.methodNotAllowed(http.MethodGet))
	consumer.HandleFunc("/", s.notFound)

	return consumer
}

// operatorRoutes is the control plane wa-console drives: inspect and steer the queue. It is
// not part of the consumer contract and deliberately absent from openapi.yaml — speccheck
// only audits /v1, so these routes being undocumented is a decision, not an oversight.
// Authenticated by ADMIN_API_KEY, which is never handed to a consuming project.
func (s *Server) operatorRoutes() *http.ServeMux {
	operator := http.NewServeMux()
	operator.HandleFunc("GET /internal/jobs", s.handleListJobs)
	operator.HandleFunc("GET /internal/jobs/{key}", s.handleGetJob)
	operator.HandleFunc("POST /internal/jobs/{key}/retry", s.handleRetryJob)
	operator.HandleFunc("POST /internal/jobs/{key}/cancel", s.handleCancelJob)
	operator.HandleFunc("GET /internal/webhooks/events", s.handleListWebhookEvents)
	operator.HandleFunc("POST /internal/webhooks/events/{id}/replay", s.handleReplayWebhookEvent)
	operator.HandleFunc("GET /internal/sessions/{id}/circuit", s.handleCircuitState)
	operator.HandleFunc("POST /internal/sessions/{id}/pause", s.handlePause)
	operator.HandleFunc("POST /internal/sessions/{id}/resume", s.handleResume)
	operator.HandleFunc("GET /internal/senders/{name}/pool", s.handleGetPool)
	operator.HandleFunc("POST /internal/senders/{name}/pool", s.handleCreatePool)
	operator.HandleFunc("DELETE /internal/senders/{name}/pool", s.handleDeletePool)
	operator.HandleFunc("POST /internal/senders/{name}/pool/members", s.handleAddPoolMember)
	operator.HandleFunc("DELETE /internal/senders/{name}/pool/members/{sessionId}", s.handleRemovePoolMember)
	operator.HandleFunc("POST /internal/senders/{name}/pool/promote", s.handlePromotePoolMember)
	operator.HandleFunc("POST /internal/senders/{name}/pool/members/{sessionId}/reinstate", s.handleReinstatePoolMember)
	operator.HandleFunc("GET /internal/senders", s.handleListSendersAdmin)
	operator.HandleFunc("POST /internal/senders", s.handleCreateSender)
	operator.HandleFunc("DELETE /internal/senders/{name}", s.handleDeleteSender)
	operator.HandleFunc("GET /internal/keys", s.handleListKeys)
	operator.HandleFunc("POST /internal/keys", s.handleCreateKey)
	operator.HandleFunc("POST /internal/keys/{id}/revoke", s.handleRevokeKey)

	return operator
}

// registerPublicRoutes mounts what anyone who can reach the port may call. Health sits here
// rather than behind the /v1 chain on purpose: a monitor that needs an API key to ask whether
// the service is up cannot report the case where key lookup itself is broken.
func (s *Server) registerPublicRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/health", s.handleHealth)
}

// registerIntakeRoutes mounts the machine-to-machine ingress from wa-gateway. This is neither
// a consumer nor an operator surface: it is authenticated by wa-gateway's own signature over
// the raw body rather than by the admin key, so it cannot sit behind withAdminKey. Registered
// on the outer mux, where the more specific pattern wins over the "/internal/" prefix.
func (s *Server) registerIntakeRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /internal/gateway/events", s.handleGatewayEvent)
}

func (s *Server) notFound(w http.ResponseWriter, r *http.Request) {
	writeProblem(w, r, CodeNotFound, "No endpoint at "+r.Method+" "+r.URL.Path+".")
}

func (s *Server) methodNotAllowed(allowed ...string) http.HandlerFunc {
	allow := strings.Join(allowed, ", ")
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Allow", allow)
		writeProblem(w, r, CodeMethodNotAllowed, r.URL.Path+" accepts "+allow+", not "+r.Method+".")
	}
}

type errorBody struct {
	ErrorCode string `json:"error_code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

func (s *Server) resolveSender(w http.ResponseWriter, r *http.Request, name string) (senders.Sender, bool) {
	if name == "" {
		writeFieldProblem(w, r, CodeInvalidRequest,
			"'sender' is required. It names a logical sender such as 'hr-notifications', never a phone number or session.",
			[]fieldError{{Field: "/sender", Detail: "required"}})
		return senders.Sender{}, false
	}

	key := apiKeyFrom(r.Context())
	if key == nil {
		writeProblem(w, r, CodeInternalError, "The request could not be authorised. It is safe to retry.")
		return senders.Sender{}, false
	}

	sender, err := s.senders.Load().Get(name)
	if err != nil {
		writeFieldProblem(w, r, CodeUnknownSender,
			"No sender named "+quoteOrEmpty(name)+". This key may use: "+strings.Join(key.Senders, ", ")+".",
			[]fieldError{{Field: "/sender", Detail: "unknown sender"}})
		return senders.Sender{}, false
	}

	if !key.MaySend(sender.Name) {
		s.log.Warn("sender not permitted for key",
			"request_id", requestIDFrom(r.Context()),
			"api_key_id", key.ID,
			"project", key.Project,
			"sender", sender.Name)
		writeFieldProblem(w, r, CodeSenderNotPermitted,
			"This key may not send from "+quoteOrEmpty(sender.Name)+". It is allowed: "+strings.Join(key.Senders, ", ")+
				". Ask the platform team to widen the key, or send from one it already has.",
			[]fieldError{{Field: "/sender", Detail: "not permitted for this api key"}})
		return senders.Sender{}, false
	}

	return sender, true
}

func quoteOrEmpty(s string) string {
	if s == "" {
		return "an empty value"
	}
	return `'` + s + `'`
}

type circuitResponse struct {
	circuit.State
	Queues  []string `json:"queues"`
	Changed bool     `json:"changed"`
}

func (s *Server) handleCircuitState(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := contextWithTimeout(r, 5*time.Second)
	defer cancel()

	id := r.PathValue("id")
	state, err := s.circuit.State(ctx, id)
	if err != nil {
		s.circuitError(w, "read circuit state", id, err)
		return
	}
	writeJSON(w, http.StatusOK, circuitResponse{State: state, Queues: tasks.QueuesFor(id)})
}

func (s *Server) handlePause(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := contextWithTimeout(r, 10*time.Second)
	defer cancel()

	id := r.PathValue("id")
	changed, err := s.circuit.Open(ctx, id, circuit.ReasonManual, "")
	if err != nil {
		s.circuitError(w, "pause session", id, err)
		return
	}
	if changed {
		s.log.Warn("session paused by hand", "alert", true, "session_id", id)
	}
	s.respondWithState(w, r, id, changed)
}

func (s *Server) handleResume(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := contextWithTimeout(r, 10*time.Second)
	defer cancel()

	id := r.PathValue("id")
	changed, err := s.circuit.Close(ctx, id)
	if err != nil {
		s.circuitError(w, "resume session", id, err)
		return
	}
	if changed {
		s.log.Warn("session resumed by hand", "alert", true, "session_id", id)
	}
	s.respondWithState(w, r, id, changed)
}

func (s *Server) respondWithState(w http.ResponseWriter, r *http.Request, id string, changed bool) {
	ctx, cancel := contextWithTimeout(r, 5*time.Second)
	defer cancel()

	state, err := s.circuit.State(ctx, id)
	if err != nil {
		s.circuitError(w, "read circuit state", id, err)
		return
	}
	writeJSON(w, http.StatusOK, circuitResponse{State: state, Queues: tasks.QueuesFor(id), Changed: changed})
}

func (s *Server) circuitError(w http.ResponseWriter, what, sessionID string, err error) {
	s.log.Error("could not "+what, "session_id", sessionID, "error", err)
	writeError(w, http.StatusInternalServerError, errorBody{
		ErrorCode: "internal_error",
		Message:   "could not " + what,
		Retryable: true,
	})
}

const (
	HealthOK       = "ok"
	HealthDegraded = "degraded"
	HealthDown     = "down"
)

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := contextWithTimeout(r, 5*time.Second)
	defer cancel()

	body := map[string]any{"status": HealthOK}
	if s.version != "" {
		body["version"] = s.version
	}

	if h, err := s.wa.Health(ctx); err != nil {
		s.log.Warn("wa-gateway health probe failed", "error", err)
		body["wa_gateway"] = map[string]any{"reachable": false, "error": err.Error()}
		body["status"] = HealthDegraded
	} else {
		body["wa_gateway"] = map[string]any{
			"reachable": true,
			"status":    h.Status,
			"db":        h.DB,
			"uptime":    h.Uptime,
		}
	}

	if reachable, detail := probe(ctx, s.database); !reachable {
		s.log.Warn("database health probe failed", "error", detail)
		body["database"] = map[string]any{"reachable": false, "error": detail}
		body["status"] = HealthDegraded
	} else {
		body["database"] = map[string]any{"reachable": true}
	}

	// Redis is the one dependency a send cannot survive: slot allocation and the enqueue
	// both go through it, so an unreachable Redis means the API cannot accept anything.
	if reachable, detail := probe(ctx, s.redis); !reachable {
		s.log.Error("redis health probe failed", "alert", true, "error", detail)
		body["redis"] = map[string]any{"reachable": false, "error": detail}
		body["status"] = HealthDown
		writeJSON(w, http.StatusServiceUnavailable, body)
		return
	}
	body["redis"] = map[string]any{"reachable": true}

	writeJSON(w, http.StatusOK, body)
}

func probe(ctx context.Context, p Probe) (bool, string) {
	if p == nil {
		return true, ""
	}
	if err := p(ctx); err != nil {
		return false, err.Error()
	}
	return true, ""
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, body errorBody) {
	writeJSON(w, status, body)
}
