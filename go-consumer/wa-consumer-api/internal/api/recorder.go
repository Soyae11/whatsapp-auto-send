package api

import (
	"bytes"
	"encoding/json"
	"net/http"
)

// problemRecorder captures what a handler would have written, so a batch item can be rejected
// through exactly the same code paths a single send uses and still end up as one entry in a
// 207 rather than a whole-response error.
type problemRecorder struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (w *problemRecorder) Header() http.Header { return w.header }

func (w *problemRecorder) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}

func (w *problemRecorder) Write(b []byte) (int, error) { return w.body.Write(b) }

func (w *problemRecorder) problem(r *http.Request) *problem {
	var p problem
	if err := json.Unmarshal(w.body.Bytes(), &p); err == nil && p.Code != "" {
		return &p
	}
	fallback := newProblem(r, CodeInternalError,
		"This item could not be queued and the reason could not be read back. It is safe to retry it under its own idempotency key.")
	return &fallback
}
