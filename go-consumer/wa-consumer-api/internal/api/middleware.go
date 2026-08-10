package api

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"wa-shared/auth"
	"wa-shared/id"
	"wa-shared/store"
)

const (
	HeaderRequestID = "X-Request-Id"

	requestIDPrefix = "req"

	authTimeout = 5 * time.Second
)

type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (w *statusRecorder) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
		w.ResponseWriter.WriteHeader(status)
	}
}

func (w *statusRecorder) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytes += n
	return n, err
}

func (s *Server) withRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := id.New(requestIDPrefix)
		w.Header().Set(HeaderRequestID, requestID)
		next.ServeHTTP(w, r.WithContext(withRequestInfo(r.Context(), requestID)))
	})
}

func (s *Server) withRequestLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		rec := &statusRecorder{ResponseWriter: w}

		next.ServeHTTP(rec, r)

		if rec.status == 0 {
			rec.status = http.StatusOK
		}
		attrs := []any{
			"request_id", requestIDFrom(r.Context()),
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"duration_ms", time.Since(started).Milliseconds(),
		}
		if key := apiKeyFrom(r.Context()); key != nil {
			attrs = append(attrs, "api_key_id", key.ID, "project", key.Project)
		}
		s.log.Info("request", attrs...)
	})
}

func (s *Server) withAPIKey(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := contextWithTimeout(r, authTimeout)
		defer cancel()

		secret, err := auth.Bearer(r.Header.Get("Authorization"))
		if err != nil {
			s.rejectUnauthenticated(w, r, "This endpoint needs an API key. Send it as 'Authorization: Bearer wsk_live_...'.")
			return
		}
		if _, err := auth.SecretEnvironment(secret); err != nil {
			s.rejectUnauthenticated(w, r, "That is not a well-formed API key. Keys look like 'wsk_live_...' or 'wsk_test_...' and are shown once at creation.")
			return
		}

		key, err := s.keys.AuthenticateAPIKey(ctx, auth.Hash(secret))
		if errors.Is(err, store.ErrNotFound) {
			s.rejectUnauthenticated(w, r, "This API key is not recognised. Check for a stray newline or a key from another environment.")
			return
		}
		if err != nil {
			s.log.Error("could not authenticate api key", "request_id", requestIDFrom(r.Context()), "error", err)
			writeProblem(w, r, CodeInternalError, "The request could not be authenticated. It is safe to retry.")
			return
		}
		if !key.Active(time.Now()) {
			s.rejectUnauthenticated(w, r, "This API key has been revoked. Request a new one from the platform team; retrying with this key will keep failing.")
			return
		}

		setAPIKey(r.Context(), key)
		next.ServeHTTP(w, r)
	})
}

func (s *Server) rejectUnauthenticated(w http.ResponseWriter, r *http.Request, detail string) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="wa-dispatcher"`)
	writeProblem(w, r, CodeUnauthenticated, detail)
}

func (s *Server) withRateLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := apiKeyFrom(r.Context())
		if key == nil {
			writeProblem(w, r, CodeInternalError, "The request could not be rate limited. It is safe to retry.")
			return
		}

		ctx, cancel := contextWithTimeout(r, authTimeout)
		defer cancel()

		res, err := s.limiter.Allow(ctx, key.ID, key.RateLimit)
		if err != nil {
			s.log.Error("could not apply the rate limit",
				"request_id", requestIDFrom(r.Context()), "api_key_id", key.ID, "error", err)
			writeProblem(w, r, CodeInternalError, "The request could not be rate limited. It is safe to retry.")
			return
		}

		w.Header().Set("RateLimit-Limit", strconv.Itoa(res.Limit))
		w.Header().Set("RateLimit-Remaining", strconv.Itoa(res.Remaining))
		w.Header().Set("RateLimit-Reset", strconv.Itoa(res.RetryAfter()))

		if !res.Allowed {
			retryAfter := res.RetryAfter()
			w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
			s.log.Warn("rate limited",
				"request_id", requestIDFrom(r.Context()),
				"api_key_id", key.ID,
				"project", key.Project,
				"limit", res.Limit)
			writeProblem(w, r, CodeRateLimited, fmt.Sprintf(
				"This key allows %d requests per minute and has used them. Retry in %d seconds. This is a limit on API calls, not on sending — messages already queued are unaffected.",
				res.Limit, retryAfter))
			return
		}

		next.ServeHTTP(w, r)
	})
}

// corsAllowedHeaders and corsExposedHeaders are fixed lists rather than reflections of
// what a given route needs, because CORS headers are set before routing has happened —
// withCORS runs outside mux, see Routes(). corsExposedHeaders is the load-bearing half:
// without it, RateLimit-*, X-Request-Id, and X-Idempotent-Replay are invisible to browser
// JS even on a successful same-list-origin response.
const (
	corsAllowedHeaders = "Authorization, Content-Type, Idempotency-Key, X-Dry-Run, x-baileys-timestamp, x-baileys-signature"
	corsExposedHeaders = "X-Request-Id, X-Idempotent-Replay, RateLimit-Limit, RateLimit-Remaining, RateLimit-Reset, Retry-After, Allow"
	corsAllowedMethods = "GET, POST, OPTIONS"
	corsMaxAge         = "600"
)

// withCORS answers browser preflights and annotates real responses for origins on an
// explicit allowlist. With no allowlist configured — the default — it does nothing at all,
// so a deployment that never sets CORS_ALLOWED_ORIGINS behaves exactly as it did before this
// existed. It runs outside the routing mux (see Routes) so an OPTIONS preflight is answered
// before withAPIKey or withAdminKey ever see it; a preflight carries no Authorization header
// and would otherwise be rejected as unauthenticated.
func (s *Server) withCORS(next http.Handler) http.Handler {
	if len(s.allowedOrigins) == 0 {
		return next
	}
	allowed := make(map[string]bool, len(s.allowedOrigins))
	for _, o := range s.allowedOrigins {
		allowed[o] = true
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Vary", "Origin")

		origin := r.Header.Get("Origin")
		if origin == "" || !allowed[origin] {
			next.ServeHTTP(w, r)
			return
		}

		w.Header().Set("Access-Control-Allow-Origin", origin)

		if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
			w.Header().Set("Access-Control-Allow-Methods", corsAllowedMethods)
			w.Header().Set("Access-Control-Allow-Headers", corsAllowedHeaders)
			w.Header().Set("Access-Control-Expose-Headers", corsExposedHeaders)
			w.Header().Set("Access-Control-Max-Age", corsMaxAge)
			w.WriteHeader(http.StatusNoContent)
			return
		}

		w.Header().Set("Access-Control-Expose-Headers", corsExposedHeaders)
		next.ServeHTTP(w, r)
	})
}

func (s *Server) withAdminKey(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secret, err := auth.Bearer(r.Header.Get("Authorization"))
		if err != nil || subtle.ConstantTimeCompare([]byte(secret), []byte(s.adminKey)) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="wa-dispatcher-internal"`)
			writeError(w, http.StatusUnauthorized, errorBody{
				ErrorCode: "unauthorized",
				Message:   "operator routes need the admin key in 'Authorization: Bearer <ADMIN_API_KEY>'",
			})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func chain(h http.Handler, middleware ...func(http.Handler) http.Handler) http.Handler {
	for i := len(middleware) - 1; i >= 0; i-- {
		h = middleware[i](h)
	}
	return h
}
