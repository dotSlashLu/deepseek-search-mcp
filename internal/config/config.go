// Package config resolves the server configuration from command-line flags and
// environment variables.
//
// Precedence is: flags > environment variables > built-in defaults. This is
// implemented by seeding each flag's default value from the environment, so
// parsing a flag always overrides the environment.
package config

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// Environment variables read by the server.
const (
	EnvAPIKey            = "DEEPSEEK_API_KEY"
	EnvBaseURL           = "DEEPSEEK_BASE_URL"
	EnvTransport         = "MCP_TRANSPORT"
	EnvTimeout           = "MCP_TIMEOUT"
	EnvMaxRetries        = "MCP_MAX_RETRIES"
	EnvAddr              = "MCP_HTTP_ADDR"
	EnvPath              = "MCP_HTTP_PATH"
	EnvAuthToken         = "MCP_HTTP_AUTH_TOKEN"
	EnvAuthRequired      = "MCP_HTTP_AUTH_REQUIRED"
	EnvStateless         = "MCP_HTTP_STATELESS"
	EnvJSONResponse      = "MCP_HTTP_JSON_RESPONSE"
	EnvSessionTTL        = "MCP_HTTP_SESSION_TIMEOUT"
	EnvAllowNonLocalHost = "MCP_HTTP_ALLOW_NON_LOCAL_HOST"
	EnvLogLevel          = "MCP_LOG_LEVEL"
)

// Defaults.
const (
	DefaultTransport   = TransportStdio
	DefaultBaseURL     = "https://api.deepseek.com"
	DefaultTimeout     = 60 * time.Second
	DefaultMaxRetries  = 2
	DefaultHTTPAddr    = "127.0.0.1:8080"
	DefaultHTTPPath    = "/mcp"
	DefaultLogLevel    = "info"
	DefaultReadTimeout = 60 * time.Second
	// DefaultSessionTTL closes MCP sessions that have been idle for this long,
	// so a long-running server does not accumulate them.
	DefaultSessionTTL   = 30 * time.Minute
	MaxFetchURLLength   = 8192
	MaxSearchQueryRunes = 128
)

// Transport is the MCP transport to serve.
type Transport string

// Supported transports.
const (
	TransportStdio Transport = "stdio"
	TransportHTTP  Transport = "http"
)

// Config is the fully resolved server configuration.
type Config struct {
	Transport   Transport
	APIKey      string
	BaseURL     string
	Timeout     time.Duration
	MaxRetries  int
	LogLevel    string
	ShowVersion bool
	HTTP        HTTPConfig
}

// HTTPConfig configures the Streamable HTTP transport.
type HTTPConfig struct {
	// Addr is the TCP address to listen on.
	Addr string
	// Path is the URL path serving the MCP endpoint, e.g. "/mcp".
	Path string
	// AuthToken, when non-empty, is required as a bearer token on the MCP
	// endpoint.
	AuthToken string
	// AuthRequired refuses to start in HTTP mode when AuthToken is empty.
	AuthRequired bool
	// Stateless disables MCP session tracking.
	Stateless bool
	// JSONResponse answers with application/json instead of an SSE stream.
	// Fine for request/response-only servers; avoids streaming quirks behind
	// proxies.
	JSONResponse bool
	// SessionTimeout closes idle MCP sessions; zero keeps them forever.
	SessionTimeout time.Duration
	// AllowNonLocalHost disables the SDK's DNS-rebinding protection, which
	// rejects requests that arrive on a loopback listener but carry a
	// non-loopback Host header. Reverse proxies that preserve the original Host
	// (the usual setup) need this.
	AllowNonLocalHost bool
}

// Parse resolves configuration from args (without the program name) and the
// environment. Usage output is written to out. It returns flag.ErrHelp when the
// user asked for help.
func Parse(args []string, out io.Writer) (Config, error) {
	var cfg Config

	fs := flag.NewFlagSet("deepseek-search", flag.ContinueOnError)
	fs.SetOutput(out)
	fs.Usage = func() {
		fmt.Fprint(out, usage)
		fs.PrintDefaults()
	}

	fs.StringVar(&cfg.APIKey, "api-key", envString(EnvAPIKey, ""),
		"DeepSeek API key (env "+EnvAPIKey+"); required unless --version is used")
	fs.StringVar(&cfg.BaseURL, "base-url", envString(EnvBaseURL, DefaultBaseURL),
		"DeepSeek API base URL (env "+EnvBaseURL+")")
	fs.DurationVar(&cfg.Timeout, "timeout", envDuration(EnvTimeout, DefaultTimeout),
		"timeout for a single DeepSeek API request, retries included (env "+EnvTimeout+")")
	fs.IntVar(&cfg.MaxRetries, "max-retries", envInt(EnvMaxRetries, DefaultMaxRetries),
		"number of retries for retryable failures (408/429/5xx) (env "+EnvMaxRetries+")")
	fs.StringVar(&cfg.LogLevel, "log-level", envString(EnvLogLevel, DefaultLogLevel),
		"log level: debug, info, warn or error (env "+EnvLogLevel+")")
	fs.BoolVar(&cfg.ShowVersion, "version", false, "print version and exit")

	transport := fs.String("transport", envString(EnvTransport, string(DefaultTransport)),
		"transport to serve: stdio or http (env "+EnvTransport+")")
	fs.StringVar(&cfg.HTTP.Addr, "addr", envString(EnvAddr, DefaultHTTPAddr),
		"listen address for the http transport (env "+EnvAddr+")")
	fs.StringVar(&cfg.HTTP.Path, "http-path", envString(EnvPath, DefaultHTTPPath),
		"URL path of the MCP endpoint for the http transport (env "+EnvPath+")")
	fs.StringVar(&cfg.HTTP.AuthToken, "http-auth-token", envString(EnvAuthToken, ""),
		"bearer token required by the http transport (env "+EnvAuthToken+")")
	fs.BoolVar(&cfg.HTTP.AuthRequired, "http-auth-required", envBool(EnvAuthRequired, false),
		"refuse to start the http transport when no auth token is configured (env "+EnvAuthRequired+")")
	fs.BoolVar(&cfg.HTTP.Stateless, "stateless", envBool(EnvStateless, false),
		"serve the http transport without MCP sessions (env "+EnvStateless+")")
	fs.BoolVar(&cfg.HTTP.JSONResponse, "http-json-response", envBool(EnvJSONResponse, false),
		"answer http requests with application/json instead of an SSE stream (env "+EnvJSONResponse+")")
	fs.DurationVar(&cfg.HTTP.SessionTimeout, "http-session-timeout", envDuration(EnvSessionTTL, DefaultSessionTTL),
		"close an MCP session after this much idle time, 0 to never close (env "+EnvSessionTTL+")")
	fs.BoolVar(&cfg.HTTP.AllowNonLocalHost, "http-allow-non-local-host", envBool(EnvAllowNonLocalHost, false),
		"accept requests whose Host header is not loopback although the listener is, needed behind a reverse "+
			"proxy that preserves Host (env "+EnvAllowNonLocalHost+")")

	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}
	if fs.NArg() > 0 {
		return Config{}, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}

	cfg.Transport = Transport(strings.ToLower(strings.TrimSpace(*transport)))
	cfg.LogLevel = strings.ToLower(strings.TrimSpace(cfg.LogLevel))
	cfg.BaseURL = strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	cfg.APIKey = strings.TrimSpace(cfg.APIKey)
	cfg.HTTP.AuthToken = strings.TrimSpace(cfg.HTTP.AuthToken)
	cfg.HTTP.Path = normalizePath(cfg.HTTP.Path)

	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// Validate reports whether the configuration is usable.
func (c Config) Validate() error {
	if c.ShowVersion {
		return nil
	}
	switch c.Transport {
	case TransportStdio, TransportHTTP:
	default:
		return fmt.Errorf("invalid transport %q: want %q or %q", c.Transport, TransportStdio, TransportHTTP)
	}
	if c.APIKey == "" {
		return fmt.Errorf("no DeepSeek API key: pass --api-key or set %s", EnvAPIKey)
	}
	u, err := url.Parse(c.BaseURL)
	if err != nil {
		return fmt.Errorf("invalid --base-url %q: %w", c.BaseURL, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("invalid --base-url %q: scheme must be http or https", c.BaseURL)
	}
	if u.Host == "" {
		return fmt.Errorf("invalid --base-url %q: missing host", c.BaseURL)
	}
	if c.Timeout <= 0 {
		return errors.New("--timeout must be positive")
	}
	if c.MaxRetries < 0 {
		return errors.New("--max-retries must not be negative")
	}
	if _, err := c.SlogLevel(); err != nil {
		return err
	}
	if c.Transport == TransportHTTP {
		if c.HTTP.Addr == "" {
			return errors.New("--addr must not be empty")
		}
		if c.HTTP.Path == "" || !strings.HasPrefix(c.HTTP.Path, "/") {
			return errors.New("--http-path must start with /")
		}
		if c.HTTP.AuthRequired && c.HTTP.AuthToken == "" {
			return fmt.Errorf("--http-auth-required is set but no auth token was configured: pass --http-auth-token or set %s", EnvAuthToken)
		}
		if c.HTTP.SessionTimeout < 0 {
			return errors.New("--http-session-timeout must not be negative (use 0 to keep sessions open)")
		}
	}
	return nil
}

// SlogLevel converts the configured log level to a slog.Level.
func (c Config) SlogLevel() (slog.Level, error) {
	switch c.LogLevel {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("invalid --log-level %q: want debug, info, warn or error", c.LogLevel)
	}
}

const usage = `deepseek-search - MCP server for the DeepSeek Search and Fetch APIs

Usage:
  deepseek-search [flags]

Flags:
`

func normalizePath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return DefaultHTTPPath
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return p
}

func envString(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && strings.TrimSpace(v) != "" {
		return v
	}
	return def
}

func envDuration(key string, def time.Duration) time.Duration {
	v, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(v) == "" {
		return def
	}
	d, err := time.ParseDuration(strings.TrimSpace(v))
	if err != nil {
		return def
	}
	return d
}

func envInt(key string, def int) int {
	v, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(v) == "" {
		return def
	}
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil {
		return def
	}
	return n
}

func envBool(key string, def bool) bool {
	v, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(v) == "" {
		return def
	}
	b, err := strconv.ParseBool(strings.TrimSpace(v))
	if err != nil {
		return def
	}
	return b
}
