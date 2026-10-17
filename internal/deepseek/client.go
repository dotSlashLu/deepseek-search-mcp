// Package deepseek is a thin client for the DeepSeek Search and Fetch APIs.
//
// Both endpoints take a JSON body and a bearer token, and both are documented
// at https://api-docs.deepseek.com/guides/search. The client adds a bounded
// retry loop for 408/429/5xx responses and maps the documented status codes to
// actionable error messages.
package deepseek

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Endpoint paths.
const (
	SearchPath = "/v1/tools/search"
	FetchPath  = "/v1/tools/fetch"
)

// Limits.
const (
	// MaxQueryRunes is the documented maximum length of a search query.
	MaxQueryRunes = 128
	// MinCount and MaxCount bound the search count parameter.
	MinCount = 1
	MaxCount = 50
	// MaxURLLength is the documented maximum length of a fetch URL.
	MaxURLLength = 8192
	// maxResponseBytes caps how much of a response body is read into memory.
	maxResponseBytes = 32 << 20
)

// Defaults for the retry loop.
const (
	DefaultRetryBaseDelay = 500 * time.Millisecond
	maxRetryDelay         = 8 * time.Second
)

// userAgent identifies this server to the API.
const userAgent = "deepseek-search-mcp"

// Options configures a Client.
type Options struct {
	// APIKey is the DeepSeek API key. Required.
	APIKey string
	// BaseURL is the API base URL without a trailing slash.
	BaseURL string
	// Timeout bounds a single API call, retries included.
	Timeout time.Duration
	// MaxRetries is the number of retries after a retryable failure.
	MaxRetries int
	// RetryBaseDelay is the first backoff delay; later attempts double it.
	// Defaults to DefaultRetryBaseDelay.
	RetryBaseDelay time.Duration
	// HTTPClient is used for all requests. Defaults to http.DefaultClient.
	HTTPClient *http.Client
	// Logger receives debug logs. Defaults to slog.Default().
	Logger *slog.Logger
}

// Client calls the DeepSeek Search and Fetch APIs.
type Client struct {
	baseURL        string
	apiKey         string
	timeout        time.Duration
	maxRetries     int
	retryBaseDelay time.Duration
	http           *http.Client
	logger         *slog.Logger
}

// New builds a Client. The options are expected to be pre-validated by the
// caller; a missing base URL falls back to the public API.
func New(opts Options) *Client {
	base := strings.TrimRight(strings.TrimSpace(opts.BaseURL), "/")
	if base == "" {
		base = "https://api.deepseek.com"
	}
	hc := opts.HTTPClient
	if hc == nil {
		hc = &http.Client{}
	}
	delay := opts.RetryBaseDelay
	if delay <= 0 {
		delay = DefaultRetryBaseDelay
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Client{
		baseURL:        base,
		apiKey:         opts.APIKey,
		timeout:        opts.Timeout,
		maxRetries:     max(opts.MaxRetries, 0),
		retryBaseDelay: delay,
		http:           hc,
		logger:         logger,
	}
}

// Search calls POST /v1/tools/search and returns the ranked results.
func (c *Client) Search(ctx context.Context, req SearchRequest) (*SearchResponse, error) {
	if strings.TrimSpace(req.Query) == "" {
		return nil, errors.New("query must not be empty")
	}
	if n := len([]rune(req.Query)); n > MaxQueryRunes {
		return nil, fmt.Errorf("query is %d characters long, the maximum is %d", n, MaxQueryRunes)
	}
	if req.Count != 0 && (req.Count < MinCount || req.Count > MaxCount) {
		return nil, fmt.Errorf("count must be between %d and %d, got %d", MinCount, MaxCount, req.Count)
	}
	var out SearchResponse
	if err := c.post(ctx, SearchPath, req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Fetch calls POST /v1/tools/fetch and returns the page title and Markdown
// content.
func (c *Client) Fetch(ctx context.Context, rawURL string) (*FetchResponse, error) {
	if err := ValidateURL(rawURL); err != nil {
		return nil, err
	}
	var out FetchResponse
	if err := c.post(ctx, FetchPath, FetchRequest{URL: rawURL}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ValidateURL checks the documented constraints of the Fetch API's url field:
// at most MaxURLLength characters, an HTTP or HTTPS scheme, a host, and no
// embedded credentials.
func ValidateURL(rawURL string) error {
	if strings.TrimSpace(rawURL) == "" {
		return errors.New("url must not be empty")
	}
	if len(rawURL) > MaxURLLength {
		return fmt.Errorf("url is %d characters long, the maximum is %d", len(rawURL), MaxURLLength)
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid url %q: %w", rawURL, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("invalid url %q: scheme must be http or https", rawURL)
	}
	if u.Host == "" {
		return fmt.Errorf("invalid url %q: missing host", rawURL)
	}
	if u.User != nil {
		return fmt.Errorf("invalid url %q: must not contain a username or password", rawURL)
	}
	return nil
}

// attemptResult is the outcome of a single HTTP attempt.
type attemptResult struct {
	body       []byte
	err        error
	retryAfter time.Duration
	retryable  bool
}

func (c *Client) post(ctx context.Context, path string, payload, out any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode %s request: %w", path, err)
	}

	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	endpoint := c.baseURL + path
	for attempt := 1; ; attempt++ {
		start := time.Now()
		res := c.attempt(ctx, path, endpoint, body)
		if res.err == nil {
			c.logger.Debug("deepseek request succeeded",
				"path", path, "attempt", attempt, "elapsed", time.Since(start).String())
			if err := json.Unmarshal(res.body, out); err != nil {
				return fmt.Errorf("decode %s response: %w", path, err)
			}
			return nil
		}
		if !res.retryable || attempt > c.maxRetries {
			return res.err
		}
		delay := c.backoff(attempt, res.retryAfter)
		c.logger.Debug("deepseek request failed, retrying",
			"path", path, "attempt", attempt, "delay", delay.String(), "error", res.err)
		if err := sleepContext(ctx, delay); err != nil {
			return res.err
		}
	}
}

// attempt performs one HTTP request. A nil error means HTTP 200 and a body in
// res.body.
func (c *Client) attempt(ctx context.Context, path, endpoint string, body []byte) attemptResult {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return attemptResult{err: fmt.Errorf("build %s request: %w", path, err)}
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent)

	resp, err := c.http.Do(req)
	if err != nil {
		// A canceled or expired context is terminal: do not retry.
		done := ctx.Err() != nil
		return attemptResult{err: c.transportError(ctx, path, err), retryable: !done}
	}
	defer resp.Body.Close()

	respBody, err := readLimited(resp.Body, maxResponseBytes)
	if err != nil {
		return attemptResult{err: fmt.Errorf("read %s response: %w", path, err), retryable: true}
	}
	if resp.StatusCode == http.StatusOK {
		return attemptResult{body: respBody}
	}

	apiErr := &APIError{
		Endpoint:   path,
		StatusCode: resp.StatusCode,
		RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After")),
	}
	var env errorEnvelope
	if err := json.Unmarshal(respBody, &env); err == nil {
		apiErr.Code = env.Error.Code
		apiErr.Type = env.Error.Type
		apiErr.Message = env.Error.Message
	}
	if strings.TrimSpace(apiErr.Message) == "" {
		apiErr.Body = truncate(strings.TrimSpace(string(respBody)), 500)
	}
	return attemptResult{err: apiErr, retryable: apiErr.retryable(), retryAfter: apiErr.RetryAfter}
}

// transportError turns a client-side failure into an actionable error.
func (c *Client) transportError(ctx context.Context, path string, err error) error {
	switch {
	case errors.Is(err, context.DeadlineExceeded), errors.Is(ctx.Err(), context.DeadlineExceeded):
		return fmt.Errorf("deepseek: POST %s timed out after %s", path, c.timeout)
	case errors.Is(err, context.Canceled), errors.Is(ctx.Err(), context.Canceled):
		return fmt.Errorf("deepseek: POST %s canceled: %w", path, err)
	default:
		return fmt.Errorf("deepseek: POST %s failed: %w", path, err)
	}
}

// backoff returns the delay before the given attempt (1-based), honouring a
// Retry-After header when present.
func (c *Client) backoff(attempt int, retryAfter time.Duration) time.Duration {
	if retryAfter > 0 {
		return retryAfter
	}
	if attempt > 10 {
		attempt = 10
	}
	d := c.retryBaseDelay << (attempt - 1)
	if d > maxRetryDelay || d <= 0 {
		d = maxRetryDelay
	}
	// Full jitter over the second half of the interval, so the delay stays
	// between 50% and 100% of d.
	return d/2 + time.Duration(rand.Int64N(int64(d/2)+1))
}

func sleepContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// readLimited reads at most limit bytes and fails when the body is larger.
func readLimited(r io.Reader, limit int64) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("response body exceeds %d bytes", limit)
	}
	return b, nil
}

// parseRetryAfter understands both the delta-seconds and the HTTP-date form.
func parseRetryAfter(v string) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil {
		if secs <= 0 {
			return 0
		}
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}

// truncate shortens s to at most max bytes without splitting a rune, appending
// an ellipsis when it cut something.
func truncate(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return strings.TrimSpace(s[:cut]) + "..."
}
