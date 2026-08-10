package api

import (
	"context"
	"net/http"
	"time"

	"wa-shared/auth"
)

type ctxKey int

const ctxInfo ctxKey = iota

type requestInfo struct {
	id  string
	key *auth.Key
}

func contextWithTimeout(r *http.Request, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), d)
}

func withRequestInfo(ctx context.Context, requestID string) context.Context {
	return context.WithValue(ctx, ctxInfo, &requestInfo{id: requestID})
}

func infoFrom(ctx context.Context) *requestInfo {
	info, _ := ctx.Value(ctxInfo).(*requestInfo)
	return info
}

func requestIDFrom(ctx context.Context) string {
	if info := infoFrom(ctx); info != nil {
		return info.id
	}
	return ""
}

func apiKeyFrom(ctx context.Context) *auth.Key {
	if info := infoFrom(ctx); info != nil {
		return info.key
	}
	return nil
}

func setAPIKey(ctx context.Context, key *auth.Key) {
	if info := infoFrom(ctx); info != nil {
		info.key = key
	}
}
