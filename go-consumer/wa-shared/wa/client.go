package wa

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	DefaultTimeout = 30 * time.Second

	DefaultSendTimeout = 60 * time.Second
)

const maxErrorBody = 1 << 16 // 64 KiB

type Client struct {
	baseURL     string
	apiKey      string
	hc          *http.Client
	timeout     time.Duration
	sendTimeout time.Duration
}

type Option func(*Client)

func WithTimeout(d time.Duration) Option {
	return func(c *Client) { c.timeout = d }
}

func WithSendTimeout(d time.Duration) Option {
	return func(c *Client) { c.sendTimeout = d }
}

func New(baseURL, apiKey string, opts ...Option) (*Client, error) {
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		return nil, fmt.Errorf("wa: parse base url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("wa: base url must be http or https, got %q", baseURL)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("wa: base url has no host: %q", baseURL)
	}
	if apiKey == "" {
		return nil, errors.New("wa: api key is empty")
	}

	c := &Client{
		baseURL:     strings.TrimRight(u.String(), "/"),
		apiKey:      apiKey,
		timeout:     DefaultTimeout,
		sendTimeout: DefaultSendTimeout,
	}
	for _, o := range opts {
		o(c)
	}
	if c.hc == nil {
		c.hc = newHTTPClient()
	}
	return c, nil
}

func newHTTPClient() *http.Client {
	t := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   5 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		MaxIdleConns:          32,
		MaxIdleConnsPerHost:   8,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
	return &http.Client{
		Transport: t,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

type SendRequest struct {
	IdempotencyKey string `json:"idempotencyKey"`
	To             string `json:"to"`
	Type           string `json:"type"`
	Text           string `json:"text"`
}

type SendResult struct {
	IdempotencyKey string `json:"idempotencyKey"`
	To     string `json:"to"`
	Status string `json:"status"`
	WAMessageID *string `json:"waMessageId"`
	Deduplicated bool `json:"deduplicated"`
}

type SessionStatus string

const (
	StatusNew          SessionStatus = "new"
	StatusPairing      SessionStatus = "pairing"
	StatusConnected    SessionStatus = "connected"
	StatusDisconnected SessionStatus = "disconnected"
	StatusLoggedOut    SessionStatus = "logged_out"
	StatusUnhealthy SessionStatus = "unhealthy"
)

func (s SessionStatus) CanSend() bool {
	return s == StatusConnected || s == StatusUnhealthy
}

type SessionHealth struct {
	SocketConnected      bool       `json:"socketConnected"`
	LastConnectedAt      *time.Time `json:"lastConnectedAt"`
	LastSuccessfulSendAt *time.Time `json:"lastSuccessfulSendAt"`
	LastFailedSendAt     *time.Time `json:"lastFailedSendAt"`
	ConsecutiveFailures int     `json:"consecutiveFailures"`
	LastErrorCode       *string `json:"lastErrorCode"`
	ReconnectAttempts   int     `json:"reconnectAttempts"`
	NextReconnectInMs   *int64  `json:"nextReconnectInMs"`
}

type SessionView struct {
	ID          string        `json:"id"`
	Label       string        `json:"label"`
	Status      SessionStatus `json:"status"`
	PhoneNumber *string       `json:"phoneNumber"`
	HasQR       bool          `json:"hasQr"`
	CreatedAt   time.Time     `json:"createdAt"`
	UpdatedAt   time.Time     `json:"updatedAt"`
	Health      SessionHealth `json:"health"`
}

type Health struct {
	Status string `json:"status"` // "ok" | "degraded"
	DB     string `json:"db"`     // "ok" | "down"
	Uptime int64  `json:"uptime"` // whole seconds
}

func (h Health) OK() bool { return h.Status == "ok" && h.DB == "ok" }

func (c *Client) Send(ctx context.Context, sessionID string, req SendRequest) (*SendResult, error) {
	if sessionID == "" {
		return nil, fmt.Errorf("wa: send: %w: empty session id", ErrInvalidPayload)
	}
	var out SendResult
	err := c.do(ctx, c.sendTimeout, http.MethodPost,
		"/sessions/"+url.PathEscape(sessionID)+"/send", req, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) GetSession(ctx context.Context, sessionID string) (*SessionView, error) {
	if sessionID == "" {
		return nil, fmt.Errorf("wa: get session: %w: empty session id", ErrInvalidPayload)
	}
	var out SessionView
	err := c.do(ctx, c.timeout, http.MethodGet,
		"/sessions/"+url.PathEscape(sessionID), nil, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) ListSessions(ctx context.Context) ([]SessionView, error) {
	var out struct {
		Sessions []SessionView `json:"sessions"`
	}
	if err := c.do(ctx, c.timeout, http.MethodGet, "/sessions", nil, &out); err != nil {
		return nil, err
	}
	return out.Sessions, nil
}

func (c *Client) Health(ctx context.Context) (*Health, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/health", nil)
	if err != nil {
		return nil, fmt.Errorf("wa: health: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("wa: health: %w", err)
	}
	defer drainAndClose(resp.Body)

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusServiceUnavailable {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
		return nil, decodeError(resp.StatusCode, body)
	}

	var h Health
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
	if err != nil {
		return nil, fmt.Errorf("wa: health: read body: %w", err)
	}
	if err := json.Unmarshal(body, &h); err != nil {
		return nil, fmt.Errorf("wa: health: decode body: %w", err)
	}
	return &h, nil
}

func (c *Client) do(ctx context.Context, timeout time.Duration, method, path string, body, out any) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("wa: %s %s: encode body: %w", method, path, err)
		}
		rdr = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, rdr)
	if err != nil {
		return fmt.Errorf("wa: %s %s: build request: %w", method, path, err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.hc.Do(req)
	if err != nil {
		return fmt.Errorf("wa: %s %s: %w", method, path, err)
	}
	defer drainAndClose(resp.Body)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
		return decodeError(resp.StatusCode, b)
	}

	if out == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("wa: %s %s: decode response: %w", method, path, err)
	}
	return nil
}

func decodeError(status int, body []byte) *APIError {
	var eb errorBody
	if err := json.Unmarshal(body, &eb); err != nil || eb.ErrorCode == "" {
		return unexpectedResponse(status, string(body))
	}
	return newAPIError(status, eb)
}

func drainAndClose(rc io.ReadCloser) {
	_, _ = io.Copy(io.Discard, io.LimitReader(rc, maxErrorBody))
	_ = rc.Close()
}
