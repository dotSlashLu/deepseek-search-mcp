package mcpserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dotSlashLu/deepseek-search-mcp/internal/deepseek"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestRequireTokenDisabledWhenNoToken(t *testing.T) {
	called := false
	h := RequireToken(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }), "", discardLogger())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/mcp", nil))
	if !called || rec.Code != http.StatusOK {
		t.Errorf("called = %v, status = %d; want true, 200", called, rec.Code)
	}
}

func TestRequireTokenAccepts(t *testing.T) {
	tests := []struct {
		name   string
		header string
		value  string
	}{
		{"bearer", "Authorization", "Bearer s3cret"},
		{"bearer lowercase scheme", "Authorization", "bearer s3cret"},
		{"bearer padded", "Authorization", "Bearer   s3cret  "},
		{"x-api-key", "X-API-Key", "s3cret"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			h := RequireToken(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }), "s3cret", discardLogger())
			req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
			req.Header.Set(tc.header, tc.value)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if !called || rec.Code != http.StatusOK {
				t.Errorf("called = %v, status = %d; want true, 200", called, rec.Code)
			}
		})
	}
}

func TestRequireTokenRejects(t *testing.T) {
	tests := []struct {
		name   string
		header string
		value  string
	}{
		{"no header", "", ""},
		{"wrong bearer", "Authorization", "Bearer nope"},
		{"wrong scheme", "Authorization", "Basic s3cret"},
		{"empty bearer", "Authorization", "Bearer "},
		{"wrong x-api-key", "X-API-Key", "nope"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			h := RequireToken(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }), "s3cret", discardLogger())
			req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
			if tc.header != "" {
				req.Header.Set(tc.header, tc.value)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if called {
				t.Error("next handler was called, want rejection")
			}
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("status = %d, want 401", rec.Code)
			}
			if got := rec.Header().Get("WWW-Authenticate"); !strings.Contains(got, "Bearer") {
				t.Errorf("WWW-Authenticate = %q, want a Bearer challenge", got)
			}
		})
	}
}

func TestHealthz(t *testing.T) {
	handler := NewHTTPHandler(NewServer(Options{Version: "test", API: &fakeAPI{}, Logger: discardLogger()}), HTTPOptions{
		Path:   "/mcp",
		Logger: discardLogger(),
	})
	ts := httptest.NewServer(handler)
	defer ts.Close()

	resp, err := http.Get(ts.URL + HealthPath)
	if err != nil {
		t.Fatalf("GET %s: %v", HealthPath, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}

	post, err := http.Post(ts.URL+HealthPath, "text/plain", nil)
	if err != nil {
		t.Fatalf("POST %s: %v", HealthPath, err)
	}
	defer post.Body.Close()
	if post.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("POST status = %d, want 405", post.StatusCode)
	}
}

func TestHTTPHandlerUnauthenticatedRequestsAreRejected(t *testing.T) {
	handler := NewHTTPHandler(NewServer(Options{Version: "test", API: &fakeAPI{}, Logger: discardLogger()}), HTTPOptions{
		Path:      "/mcp",
		AuthToken: "s3cret",
		Logger:    discardLogger(),
	})
	ts := httptest.NewServer(handler)
	defer ts.Close()

	for _, method := range []string{http.MethodPost, http.MethodGet} {
		req, err := http.NewRequest(method, ts.URL+"/mcp", strings.NewReader("{}"))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s /mcp: %v", method, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s /mcp status = %d, want 401", method, resp.StatusCode)
		}
	}

	// Unknown paths stay 404.
	resp, err := http.Get(ts.URL + "/other")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("GET /other status = %d, want 404", resp.StatusCode)
	}
}

// TestHostHeaderProtectionBehindProxy pins the behaviour a reverse proxy hits:
// the listener is loopback, the forwarded Host is public. Without opting in the
// transport answers 403 (DNS-rebinding protection), which makes the deployment
// look like an auth failure.
func TestHostHeaderProtectionBehindProxy(t *testing.T) {
	newHandler := func(allowNonLocalHost bool) http.Handler {
		return NewHTTPHandler(
			NewServer(Options{Version: "test", API: &fakeAPI{}, Logger: discardLogger()}),
			HTTPOptions{Path: "/mcp", AllowNonLocalHost: allowNonLocalHost, Logger: discardLogger()},
		)
	}
	tests := []struct {
		name          string
		allow         bool
		host          string
		wantForbidden bool
	}{
		{"loopback host by default", false, "", false},
		{"proxied host is rejected by default", false, "mcp.example.com", true},
		{"proxied host is accepted when opted in", true, "mcp.example.com", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ts := httptest.NewServer(newHandler(tc.allow))
			defer ts.Close()

			req, err := http.NewRequest(http.MethodPost, ts.URL+"/mcp", strings.NewReader("{}"))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Content-Type", "application/json")
			if tc.host != "" {
				req.Host = tc.host
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("POST /mcp: %v", err)
			}
			defer resp.Body.Close()
			if got := resp.StatusCode == http.StatusForbidden; got != tc.wantForbidden {
				t.Errorf("Host %q: status = %d, want forbidden = %v", tc.host, resp.StatusCode, tc.wantForbidden)
			}
		})
	}
}

// TestHTTPSessionTimeoutClosesIdleSessions guards against unbounded session
// growth in a long-running deployment.
func TestHTTPSessionTimeoutClosesIdleSessions(t *testing.T) {
	handler := NewHTTPHandler(
		NewServer(Options{Version: "test", API: &fakeAPI{}, Logger: discardLogger()}),
		HTTPOptions{Path: "/mcp", SessionTimeout: 200 * time.Millisecond, Logger: discardLogger()},
	)
	ts := httptest.NewServer(handler)
	defer ts.Close()

	init := mcpPost(t, ts.URL+"/mcp", "", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"timeout-test","version":"0"}}}`)
	sessionID := init.Header.Get("Mcp-Session-Id")
	init.Body.Close()
	if sessionID == "" {
		t.Fatal("initialize returned no Mcp-Session-Id")
	}

	alive := mcpPost(t, ts.URL+"/mcp", sessionID, `{"jsonrpc":"2.0","id":2,"method":"ping"}`)
	alive.Body.Close()
	if alive.StatusCode == http.StatusNotFound {
		t.Fatalf("session was closed while still active (status %d)", alive.StatusCode)
	}

	time.Sleep(600 * time.Millisecond)
	expired := mcpPost(t, ts.URL+"/mcp", sessionID, `{"jsonrpc":"2.0","id":3,"method":"ping"}`)
	defer expired.Body.Close()
	if expired.StatusCode != http.StatusNotFound {
		t.Errorf("status after the idle timeout = %d, want 404 (session closed)", expired.StatusCode)
	}
}

// mcpPost sends a JSON-RPC message to the MCP endpoint.
func mcpPost(t *testing.T, url, sessionID, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if sessionID != "" {
		req.Header.Set("Mcp-Session-Id", sessionID)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	return resp
}

// TestJSONResponseMode checks the opt-in that keeps responses off SSE streams,
// which is what a reverse proxy is happiest with.
func TestJSONResponseMode(t *testing.T) {
	handler := NewHTTPHandler(
		NewServer(Options{Version: "test", API: &fakeAPI{}, Logger: discardLogger()}),
		HTTPOptions{Path: "/mcp", JSONResponse: true, Stateless: true, Logger: discardLogger()},
	)
	ts := httptest.NewServer(handler)
	defer ts.Close()

	resp := mcpPost(t, ts.URL+"/mcp", "", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	defer resp.Body.Close()
	if got := resp.Header.Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
}

// headerRoundTripper adds a fixed header to every request.
type headerRoundTripper struct {
	base  http.RoundTripper
	name  string
	value string
}

func (h headerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.Header.Set(h.name, h.value)
	base := h.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(clone)
}

func TestHTTPEndToEnd(t *testing.T) {
	for _, stateless := range []bool{false, true} {
		name := "stateful"
		if stateless {
			name = "stateless"
		}
		t.Run(name, func(t *testing.T) {
			api := &fakeAPI{fetch: func(context.Context, string) (*deepseek.FetchResponse, error) {
				return &deepseek.FetchResponse{Title: "Fetched", Content: "Body"}, nil
			}}
			handler := NewHTTPHandler(
				NewServer(Options{Version: "test", API: api, Logger: discardLogger()}),
				HTTPOptions{Path: "/mcp", AuthToken: "s3cret", Stateless: stateless, Logger: discardLogger()},
			)
			ts := httptest.NewServer(handler)
			defer ts.Close()

			client := mcp.NewClient(&mcp.Implementation{Name: "http-test-client", Version: "test"}, nil)
			transport := &mcp.StreamableClientTransport{
				Endpoint:             ts.URL + "/mcp",
				DisableStandaloneSSE: true,
				HTTPClient: &http.Client{Transport: headerRoundTripper{
					name:  "Authorization",
					value: "Bearer s3cret",
				}},
			}
			ctx := context.Background()
			session, err := client.Connect(ctx, transport, nil)
			if err != nil {
				t.Fatalf("Connect: %v", err)
			}
			defer session.Close()

			tools, err := session.ListTools(ctx, nil)
			if err != nil {
				t.Fatalf("ListTools: %v", err)
			}
			if len(tools.Tools) != 2 {
				t.Fatalf("tools = %d, want 2", len(tools.Tools))
			}

			res, err := session.CallTool(ctx, &mcp.CallToolParams{
				Name:      "deepseek_fetch",
				Arguments: map[string]any{"url": "https://example.com"},
			})
			if err != nil {
				t.Fatalf("CallTool: %v", err)
			}
			if res.IsError {
				t.Fatalf("IsError = true: %s", toolText(t, res))
			}
			if !strings.Contains(toolText(t, res), "Fetched") {
				t.Errorf("text = %q", toolText(t, res))
			}
		})
	}
}
