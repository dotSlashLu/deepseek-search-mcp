package deepseek

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func discardLogger() *slog.Logger { return slog.New(slog.DiscardHandler) }

// newTestClient points a client at an httptest server with fast retries.
func newTestClient(t *testing.T, h http.Handler) *Client {
	t.Helper()
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	return New(Options{
		APIKey:         "test-key",
		BaseURL:        ts.URL,
		Timeout:        2 * time.Second,
		MaxRetries:     2,
		RetryBaseDelay: time.Millisecond,
		Logger:         discardLogger(),
	})
}

func TestSearchSuccess(t *testing.T) {
	var (
		gotMethod string
		gotPath   string
		gotAuth   string
		gotCTA    string
		gotAccept string
		gotBody   map[string]any
	)
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotCTA = r.Header.Get("Content-Type")
		gotAccept = r.Header.Get("Accept")
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[{"rerank_score":2.4,"title":"DeepSeek V4 released","url":"https://example.com/v4",
			"snippet":"short","summary":"long","content":"","site_name":"Example News","site_icon":null,
			"date":"2026-06-01T00:00:00+00:00","authority":3}]}`))
	}))

	resp, err := client.Search(context.Background(), SearchRequest{Query: "DeepSeek V4", Count: 5})
	if err != nil {
		t.Fatalf("Search() error: %v", err)
	}
	if gotMethod != http.MethodPost || gotPath != SearchPath {
		t.Errorf("request = %s %s, want POST %s", gotMethod, gotPath, SearchPath)
	}
	if gotAuth != "Bearer test-key" {
		t.Errorf("Authorization = %q, want %q", gotAuth, "Bearer test-key")
	}
	if gotCTA != "application/json" || gotAccept != "application/json" {
		t.Errorf("content negotiation headers = %q / %q", gotCTA, gotAccept)
	}
	if gotBody["query"] != "DeepSeek V4" || gotBody["count"] != float64(5) {
		t.Errorf("request body = %v", gotBody)
	}
	if _, ok := gotBody["need_content"]; ok {
		t.Errorf("body should omit need_content when false: %v", gotBody)
	}
	if len(resp.Results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(resp.Results))
	}
	r := resp.Results[0]
	if r.Title != "DeepSeek V4 released" || r.Authority != 3 || r.RerankScore != 2.4 {
		t.Errorf("unexpected result: %+v", r)
	}
	if r.SiteName == nil || *r.SiteName != "Example News" || r.Date == nil || *r.Date != "2026-06-01T00:00:00+00:00" {
		t.Errorf("optional fields lost: %+v", r)
	}
}

func TestSearchNeedContent(t *testing.T) {
	var gotBody map[string]any
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		_, _ = w.Write([]byte(`{"results":[{"title":"t","url":"u","snippet":"s","summary":"m","content":"body","site_name":null,"site_icon":null,"date":null,"authority":-1,"rerank_score":1.0}]}`))
	}))
	if _, err := client.Search(context.Background(), SearchRequest{Query: "q", NeedContent: true}); err != nil {
		t.Fatalf("Search() error: %v", err)
	}
	if gotBody["need_content"] != true {
		t.Errorf("need_content = %v, want true", gotBody["need_content"])
	}
}

func TestSearchEmptyResultsIsNotAnError(t *testing.T) {
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"results":[]}`))
	}))
	resp, err := client.Search(context.Background(), SearchRequest{Query: "nothing matches"})
	if err != nil {
		t.Fatalf("Search() error: %v", err)
	}
	if len(resp.Results) != 0 {
		t.Errorf("len(results) = %d, want 0", len(resp.Results))
	}
}

func TestSearchInputValidation(t *testing.T) {
	var calls int32
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		_, _ = w.Write([]byte(`{"results":[]}`))
	}))
	tests := []struct {
		name string
		req  SearchRequest
	}{
		{"empty query", SearchRequest{Query: "   "}},
		{"query too long", SearchRequest{Query: strings.Repeat("字", MaxQueryRunes+1)}},
		{"count too large", SearchRequest{Query: "q", Count: MaxCount + 1}},
		{"count negative", SearchRequest{Query: "q", Count: -3}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := client.Search(context.Background(), tc.req); err == nil {
				t.Fatal("Search() = nil error, want validation error")
			}
		})
	}
	if n := atomic.LoadInt32(&calls); n != 0 {
		t.Errorf("server saw %d requests, want 0: validation must happen before the call", n)
	}
}

func TestFetchURLValidation(t *testing.T) {
	var calls int32
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		_, _ = w.Write([]byte(`{"title":"t","content":"c"}`))
	}))
	tests := []struct {
		name string
		url  string
	}{
		{"empty", ""},
		{"no scheme", "example.com/page"},
		{"ftp scheme", "ftp://example.com/page"},
		{"credentials", "https://user:pass@example.com/page"},
		{"too long", "https://example.com/" + strings.Repeat("a", MaxURLLength)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := client.Fetch(context.Background(), tc.url); err == nil {
				t.Fatal("Fetch() = nil error, want validation error")
			}
		})
	}
	if n := atomic.LoadInt32(&calls); n != 0 {
		t.Errorf("server saw %d requests, want 0", n)
	}
}

func TestFetchSuccess(t *testing.T) {
	var gotBody map[string]any
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != FetchPath {
			t.Errorf("path = %q, want %q", r.URL.Path, FetchPath)
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		_, _ = w.Write([]byte(`{"title":"Example article","content":"# Example article\n\nBody."}`))
	}))
	resp, err := client.Fetch(context.Background(), "https://example.com/article")
	if err != nil {
		t.Fatalf("Fetch() error: %v", err)
	}
	if gotBody["url"] != "https://example.com/article" {
		t.Errorf("request body = %v", gotBody)
	}
	if resp.Title != "Example article" || !strings.Contains(resp.Content, "Body.") {
		t.Errorf("unexpected response: %+v", resp)
	}
}

func TestAPIErrorMapping(t *testing.T) {
	tests := []struct {
		status    int
		wantHint  string
		wantRetry bool
		body      string
	}{
		{http.StatusBadRequest, "invalid request", false, `{"error":{"message":"'count' must be between 1 and 50","type":"invalid_request_error","param":null,"code":"invalid_request_error"}}`},
		{http.StatusUnauthorized, "API key missing or invalid", false, `{"error":{"message":"Authentication Fails","type":"authentication_error"}}`},
		{http.StatusPaymentRequired, "insufficient account balance", false, `{"error":{"message":"Insufficient Balance"}}`},
		{http.StatusForbidden, "security policy", false, `{"error":{"message":"blocked"}}`},
		{http.StatusNotFound, "no usable page content", false, `{"error":{"message":"no content"}}`},
		{http.StatusRequestTimeout, "timed out", true, `{"error":{"message":"timeout"}}`},
		{http.StatusRequestEntityTooLarge, "too large", false, `{"error":{"message":"too large"}}`},
		{http.StatusTooManyRequests, "rate limit exceeded", true, `{"error":{"message":"too many requests"}}`},
		{http.StatusInternalServerError, "internal error", true, `{"error":{"message":"boom"}}`},
		{http.StatusGatewayTimeout, "upstream unavailable", true, `{"error":{"message":"gateway timeout"}}`},
	}
	for _, tc := range tests {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			var calls int32
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				atomic.AddInt32(&calls, 1)
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer ts.Close()
			// No retries: assert the mapping and the retryability classification.
			client := New(Options{APIKey: "k", BaseURL: ts.URL, Timeout: time.Second, MaxRetries: 0, Logger: discardLogger()})

			_, err := client.Search(context.Background(), SearchRequest{Query: "q"})
			if err == nil {
				t.Fatal("Search() = nil error, want API error")
			}
			var apiErr *APIError
			if !errors.As(err, &apiErr) {
				t.Fatalf("error type = %T, want *APIError: %v", err, err)
			}
			if apiErr.StatusCode != tc.status {
				t.Errorf("StatusCode = %d, want %d", apiErr.StatusCode, tc.status)
			}
			if !strings.Contains(err.Error(), tc.wantHint) {
				t.Errorf("error = %q, want it to mention %q", err, tc.wantHint)
			}
			if apiErr.retryable() != tc.wantRetry {
				t.Errorf("retryable() = %v, want %v", apiErr.retryable(), tc.wantRetry)
			}
			if n := atomic.LoadInt32(&calls); n != 1 {
				t.Errorf("attempts = %d, want 1", n)
			}
			if !strings.HasPrefix(err.Error(), "deepseek: POST "+SearchPath) {
				t.Errorf("error should name the endpoint: %q", err)
			}
		})
	}
}

func TestRetriesOnServerErrorThenSucceeds(t *testing.T) {
	var calls int32
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) <= 2 {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":{"message":"temporary"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"results":[]}`))
	}))
	if _, err := client.Search(context.Background(), SearchRequest{Query: "q"}); err != nil {
		t.Fatalf("Search() error: %v", err)
	}
	if n := atomic.LoadInt32(&calls); n != 3 {
		t.Errorf("attempts = %d, want 3", n)
	}
}

func TestRetriesAreBounded(t *testing.T) {
	var calls int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer ts.Close()
	client := New(Options{APIKey: "k", BaseURL: ts.URL, Timeout: 5 * time.Second, MaxRetries: 1, RetryBaseDelay: time.Millisecond, Logger: discardLogger()})
	if _, err := client.Search(context.Background(), SearchRequest{Query: "q"}); err == nil {
		t.Fatal("Search() = nil error, want failure")
	}
	if n := atomic.LoadInt32(&calls); n != 2 {
		t.Errorf("attempts = %d, want 2 (1 try + 1 retry)", n)
	}
}

func TestClientErrorsAreNotRetried(t *testing.T) {
	var calls int32
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"bad query"}}`))
	}))
	if _, err := client.Search(context.Background(), SearchRequest{Query: "q"}); err == nil {
		t.Fatal("Search() = nil error, want failure")
	}
	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Errorf("attempts = %d, want 1: 400 must not be retried", n)
	}
}

func TestTimeoutIsNotRetried(t *testing.T) {
	var calls int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		time.Sleep(300 * time.Millisecond)
		_, _ = w.Write([]byte(`{"results":[]}`))
	}))
	defer ts.Close()
	client := New(Options{APIKey: "k", BaseURL: ts.URL, Timeout: 50 * time.Millisecond, MaxRetries: 2, RetryBaseDelay: time.Millisecond, Logger: discardLogger()})
	_, err := client.Fetch(context.Background(), "https://example.com")
	if err == nil {
		t.Fatal("Fetch() = nil error, want timeout")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Errorf("error = %q, want it to mention a timeout", err)
	}
	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Errorf("attempts = %d, want 1: an expired context must not be retried", n)
	}
}

func TestCanceledContext(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
	}))
	defer ts.Close()
	client := New(Options{APIKey: "k", BaseURL: ts.URL, Timeout: time.Second, Logger: discardLogger()})
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	_, err := client.Fetch(ctx, "https://example.com")
	if err == nil || !strings.Contains(err.Error(), "canceled") {
		t.Errorf("error = %v, want a cancellation error", err)
	}
}

func TestParseRetryAfter(t *testing.T) {
	tests := []struct {
		in   string
		want time.Duration
	}{
		{"", 0},
		{"0", 0},
		{"-1", 0},
		{"2", 2 * time.Second},
		{"junk", 0},
	}
	for _, tc := range tests {
		if got := parseRetryAfter(tc.in); got != tc.want {
			t.Errorf("parseRetryAfter(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
	if got := parseRetryAfter(time.Now().Add(time.Minute).UTC().Format(http.TimeFormat)); got <= 0 {
		t.Errorf("parseRetryAfter(http date) = %v, want a positive duration", got)
	}
}

func TestBackoffGrowsAndHonoursRetryAfter(t *testing.T) {
	c := New(Options{APIKey: "k", BaseURL: "https://example.com", Timeout: time.Second, RetryBaseDelay: 100 * time.Millisecond, Logger: discardLogger()})
	first := c.backoff(1, 0)
	if first < 50*time.Millisecond || first > 100*time.Millisecond {
		t.Errorf("backoff(1) = %v, want between 50ms and 100ms", first)
	}
	if got := c.backoff(3, 0); got <= first {
		t.Errorf("backoff(3) = %v, want more than backoff(1) = %v", got, first)
	}
	if got := c.backoff(1, 7*time.Second); got != 7*time.Second {
		t.Errorf("backoff with Retry-After = %v, want 7s", got)
	}
	if got := c.backoff(50, 0); got > maxRetryDelay {
		t.Errorf("backoff(50) = %v, want it capped at %v", got, maxRetryDelay)
	}
}

func TestReadLimitedRejectsOversizedBody(t *testing.T) {
	if _, err := readLimited(strings.NewReader("0123456789"), 5); err == nil {
		t.Error("readLimited() = nil error, want a size error")
	}
	b, err := readLimited(strings.NewReader("01234"), 5)
	if err != nil || string(b) != "01234" {
		t.Errorf("readLimited() = %q, %v", b, err)
	}
}

func TestStatusHint(t *testing.T) {
	if StatusHint(http.StatusPaymentRequired) == "" {
		t.Error("StatusHint(402) is empty")
	}
	if StatusHint(418) != "" {
		t.Error("StatusHint(418) should be empty")
	}
}
