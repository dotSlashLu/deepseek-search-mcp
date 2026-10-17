package config

import (
	"errors"
	"flag"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// clearEnv blanks every environment variable the parser reads, so tests are not
// affected by the developer's shell.
func clearEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		EnvAPIKey, EnvBaseURL, EnvTransport, EnvTimeout, EnvMaxRetries,
		EnvAddr, EnvPath, EnvAuthToken, EnvAuthRequired, EnvStateless, EnvLogLevel,
	} {
		t.Setenv(key, "")
	}
}

func parse(t *testing.T, args ...string) (Config, error) {
	t.Helper()
	return Parse(args, io.Discard)
}

func TestDefaults(t *testing.T) {
	clearEnv(t)
	cfg, err := parse(t, "--api-key", "k")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.Transport != TransportStdio {
		t.Errorf("Transport = %q, want stdio", cfg.Transport)
	}
	if cfg.BaseURL != DefaultBaseURL {
		t.Errorf("BaseURL = %q, want %q", cfg.BaseURL, DefaultBaseURL)
	}
	if cfg.Timeout != DefaultTimeout || cfg.MaxRetries != DefaultMaxRetries {
		t.Errorf("Timeout/MaxRetries = %v/%d", cfg.Timeout, cfg.MaxRetries)
	}
	if cfg.HTTP.Addr != DefaultHTTPAddr || cfg.HTTP.Path != DefaultHTTPPath {
		t.Errorf("HTTP addr/path = %q/%q", cfg.HTTP.Addr, cfg.HTTP.Path)
	}
	if cfg.HTTP.AuthToken != "" || cfg.HTTP.AuthRequired || cfg.HTTP.Stateless {
		t.Errorf("HTTP = %+v, want zero auth", cfg.HTTP)
	}
	if cfg.HTTP.SessionTimeout != DefaultSessionTTL {
		t.Errorf("SessionTimeout = %v, want %v", cfg.HTTP.SessionTimeout, DefaultSessionTTL)
	}
	if cfg.HTTP.AllowNonLocalHost {
		t.Error("AllowNonLocalHost = true, want false: the DNS-rebinding protection is on by default")
	}
	if cfg.HTTP.JSONResponse {
		t.Error("JSONResponse = true, want false by default")
	}
	if cfg.LogLevel != DefaultLogLevel {
		t.Errorf("LogLevel = %q", cfg.LogLevel)
	}
}

func TestEnvironmentIsUsed(t *testing.T) {
	clearEnv(t)
	t.Setenv(EnvAPIKey, "env-key")
	t.Setenv(EnvBaseURL, "https://example.com/")
	t.Setenv(EnvTransport, "HTTP")
	t.Setenv(EnvTimeout, "5s")
	t.Setenv(EnvMaxRetries, "7")
	t.Setenv(EnvAddr, "0.0.0.0:9000")
	t.Setenv(EnvPath, "mcp")
	t.Setenv(EnvAuthToken, "env-token")
	t.Setenv(EnvSessionTTL, "3m")
	t.Setenv(EnvAllowNonLocalHost, "true")
	t.Setenv(EnvLogLevel, "DEBUG")

	cfg, err := parse(t)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.APIKey != "env-key" || cfg.BaseURL != "https://example.com" {
		t.Errorf("APIKey/BaseURL = %q/%q", cfg.APIKey, cfg.BaseURL)
	}
	if cfg.Transport != TransportHTTP {
		t.Errorf("Transport = %q, want http", cfg.Transport)
	}
	if cfg.Timeout != 5*time.Second || cfg.MaxRetries != 7 {
		t.Errorf("Timeout/MaxRetries = %v/%d", cfg.Timeout, cfg.MaxRetries)
	}
	if cfg.HTTP.Addr != "0.0.0.0:9000" || cfg.HTTP.Path != "/mcp" || cfg.HTTP.AuthToken != "env-token" {
		t.Errorf("HTTP = %+v", cfg.HTTP)
	}
	if cfg.HTTP.SessionTimeout != 3*time.Minute || !cfg.HTTP.AllowNonLocalHost {
		t.Errorf("HTTP = %+v, want a 3m session timeout and non-local Host allowed", cfg.HTTP)
	}
	if cfg.LogLevel != "debug" {
		t.Errorf("LogLevel = %q, want debug", cfg.LogLevel)
	}
}

func TestFlagsOverrideEnvironment(t *testing.T) {
	clearEnv(t)
	t.Setenv(EnvAPIKey, "env-key")
	t.Setenv(EnvBaseURL, "https://env.example.com")
	t.Setenv(EnvTransport, "http")
	t.Setenv(EnvTimeout, "5s")

	cfg, err := parse(t,
		"--api-key", "flag-key",
		"--base-url", "https://flag.example.com/",
		"--transport", "stdio",
		"--timeout", "90s",
	)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.APIKey != "flag-key" {
		t.Errorf("APIKey = %q, want the flag value", cfg.APIKey)
	}
	if cfg.BaseURL != "https://flag.example.com" {
		t.Errorf("BaseURL = %q, want the flag value without a trailing slash", cfg.BaseURL)
	}
	if cfg.Transport != TransportStdio {
		t.Errorf("Transport = %q, want the flag value", cfg.Transport)
	}
	if cfg.Timeout != 90*time.Second {
		t.Errorf("Timeout = %v, want the flag value", cfg.Timeout)
	}
}

func TestValidationErrors(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		args []string
		want string
	}{
		{name: "missing api key", want: EnvAPIKey},
		{name: "bad transport", args: []string{"--api-key", "k", "--transport", "sse"}, want: "invalid transport"},
		{name: "bad base url", args: []string{"--api-key", "k", "--base-url", "not a url"}, want: "base-url"},
		{name: "relative base url", args: []string{"--api-key", "k", "--base-url", "example.com"}, want: "scheme"},
		{name: "zero timeout", args: []string{"--api-key", "k", "--timeout", "0s"}, want: "--timeout"},
		{name: "negative retries", args: []string{"--api-key", "k", "--max-retries", "-1"}, want: "--max-retries"},
		{
			name: "negative session timeout",
			args: []string{"--api-key", "k", "--transport", "http", "--http-session-timeout", "-1m"},
			want: "--http-session-timeout",
		},
		{name: "bad log level", args: []string{"--api-key", "k", "--log-level", "verbose"}, want: "--log-level"},
		{
			name: "http auth required without token",
			args: []string{"--api-key", "k", "--transport", "http", "--http-auth-required"},
			want: EnvAuthToken,
		},
		{name: "unexpected argument", args: []string{"--api-key", "k", "serve"}, want: "unexpected argument"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			clearEnv(t)
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			_, err := parse(t, tc.args...)
			if err == nil {
				t.Fatal("Parse() = nil error, want a validation error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, want it to mention %q", err, tc.want)
			}
		})
	}
}

func TestVersionSkipsValidation(t *testing.T) {
	clearEnv(t)
	cfg, err := parse(t, "--version")
	if err != nil {
		t.Fatalf("Parse(--version): %v", err)
	}
	if !cfg.ShowVersion {
		t.Error("ShowVersion = false, want true")
	}
}

func TestHelp(t *testing.T) {
	clearEnv(t)
	var out strings.Builder
	_, err := Parse([]string{"-h"}, &out)
	if !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("Parse(-h) error = %v, want flag.ErrHelp", err)
	}
	if !strings.Contains(out.String(), "api-key") {
		t.Errorf("usage output does not list the flags:\n%s", out.String())
	}
}

func TestSlogLevel(t *testing.T) {
	tests := map[string]slog.Level{
		"debug": slog.LevelDebug,
		"info":  slog.LevelInfo,
		"warn":  slog.LevelWarn,
		"error": slog.LevelError,
	}
	for in, want := range tests {
		got, err := Config{LogLevel: in}.SlogLevel()
		if err != nil {
			t.Errorf("SlogLevel(%q) error: %v", in, err)
		}
		if got != want {
			t.Errorf("SlogLevel(%q) = %v, want %v", in, got, want)
		}
	}
	if _, err := (Config{LogLevel: "loud"}).SlogLevel(); err == nil {
		t.Error("SlogLevel(loud) = nil error, want an error")
	}
}

func TestNormalizePath(t *testing.T) {
	tests := map[string]string{"": DefaultHTTPPath, "mcp": "/mcp", "/mcp": "/mcp", " /rpc ": "/rpc"}
	for in, want := range tests {
		if got := normalizePath(in); got != want {
			t.Errorf("normalizePath(%q) = %q, want %q", in, got, want)
		}
	}
}
