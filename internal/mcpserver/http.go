package mcpserver

import (
	"crypto/subtle"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// HTTPOptions configures the Streamable HTTP transport.
type HTTPOptions struct {
	// Path is the URL path of the MCP endpoint, e.g. "/mcp".
	Path string
	// AuthToken, when non-empty, must be presented as a bearer token by every
	// request to the MCP endpoint.
	AuthToken string
	// Stateless disables MCP session tracking.
	Stateless bool
	// JSONResponse answers with application/json instead of an SSE stream.
	JSONResponse bool
	// SessionTimeout closes MCP sessions that have been idle for this long.
	// A zero timeout keeps them until the process exits.
	SessionTimeout time.Duration
	// AllowNonLocalHost disables the transport's DNS-rebinding protection.
	// Enable it when a reverse proxy forwards the public Host header to a
	// loopback listener: without it every such request gets a 403.
	AllowNonLocalHost bool
	// Logger receives transport logs.
	Logger *slog.Logger
}

// HealthPath is the unauthenticated liveness endpoint.
const HealthPath = "/healthz"

// NewHTTPHandler serves the MCP endpoint at Path plus an unauthenticated
// /healthz endpoint.
func NewHTTPHandler(server *mcp.Server, opts HTTPOptions) http.Handler {
	path := strings.TrimSpace(opts.Path)
	if path == "" {
		path = "/mcp"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}

	mcpHandler := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return server },
		&mcp.StreamableHTTPOptions{
			Stateless:                  opts.Stateless,
			JSONResponse:               opts.JSONResponse,
			SessionTimeout:             opts.SessionTimeout,
			DisableLocalhostProtection: opts.AllowNonLocalHost,
			Logger:                     opts.Logger,
		},
	)
	guarded := RequireToken(mcpHandler, opts.AuthToken, opts.Logger)

	mux := http.NewServeMux()
	mux.Handle(path, guarded)
	if !strings.HasSuffix(path, "/") {
		// Also accept a trailing slash, some clients append one.
		mux.Handle(path+"/", guarded)
	}
	mux.HandleFunc(HealthPath, handleHealth)
	return mux
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok\n"))
}

// RequireToken wraps next with a bearer-token check. An empty token disables
// authentication and passes every request through.
func RequireToken(next http.Handler, token string, logger *slog.Logger) http.Handler {
	if token == "" {
		return next
	}
	want := []byte(token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !tokenMatches(r, want) {
			if logger != nil {
				logger.Warn("rejected unauthenticated request",
					"remote", r.RemoteAddr, "method", r.Method, "path", r.URL.Path)
			}
			w.Header().Set("WWW-Authenticate", `Bearer realm="deepseek-search"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// tokenMatches accepts either "Authorization: Bearer <token>" or
// "X-API-Key: <token>", compared in constant time.
func tokenMatches(r *http.Request, want []byte) bool {
	if h := strings.TrimSpace(r.Header.Get("Authorization")); h != "" {
		if rest, ok := cutScheme(h, "bearer"); ok {
			if subtle.ConstantTimeCompare([]byte(rest), want) == 1 {
				return true
			}
		}
	}
	if k := strings.TrimSpace(r.Header.Get("X-API-Key")); k != "" {
		if subtle.ConstantTimeCompare([]byte(k), want) == 1 {
			return true
		}
	}
	return false
}

// cutScheme strips a case-insensitive "<scheme> " prefix and returns the rest.
func cutScheme(header, scheme string) (string, bool) {
	if len(header) <= len(scheme)+1 || !strings.EqualFold(header[:len(scheme)], scheme) {
		return "", false
	}
	rest := strings.TrimSpace(header[len(scheme):])
	if rest == "" {
		return "", false
	}
	return rest, true
}
