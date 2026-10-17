// Command deepseek-search is an MCP server exposing the DeepSeek Search and
// Fetch APIs over stdio or Streamable HTTP.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/dotSlashLu/deepseek-search-mcp/internal/config"
	"github.com/dotSlashLu/deepseek-search-mcp/internal/deepseek"
	"github.com/dotSlashLu/deepseek-search-mcp/internal/mcpserver"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// version is injected at build time with
// -ldflags "-X main.version=<tag>".
var version = "dev"

// shutdownTimeout bounds the graceful shutdown of the HTTP transport.
const shutdownTimeout = 10 * time.Second

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		fmt.Fprintf(os.Stderr, "deepseek-search: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	cfg, err := config.Parse(args, stderr)
	if err != nil {
		return err
	}
	if cfg.ShowVersion {
		fmt.Fprintf(stdout, "deepseek-search %s\n", version)
		return nil
	}

	level, err := cfg.SlogLevel()
	if err != nil {
		return err
	}
	// Logs always go to stderr: in stdio mode stdout is the protocol channel.
	logger := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: level}))

	client := deepseek.New(deepseek.Options{
		APIKey:     cfg.APIKey,
		BaseURL:    cfg.BaseURL,
		Timeout:    cfg.Timeout,
		MaxRetries: cfg.MaxRetries,
		Logger:     logger,
	})
	srv := mcpserver.NewServer(mcpserver.Options{
		Version: version,
		API:     client,
		Logger:  logger,
	})

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	switch cfg.Transport {
	case config.TransportHTTP:
		return serveHTTP(ctx, cfg, srv, logger, stderr)
	default:
		logger.Info("serving MCP over stdio",
			"version", version, "base_url", cfg.BaseURL, "timeout", cfg.Timeout.String())
		if err := srv.Run(ctx, &mcp.StdioTransport{}); err != nil {
			return fmt.Errorf("stdio transport: %w", err)
		}
		return nil
	}
}

func serveHTTP(ctx context.Context, cfg config.Config, srv *mcp.Server, logger *slog.Logger, stderr io.Writer) error {
	if cfg.HTTP.AuthToken == "" {
		fmt.Fprintf(stderr, "deepseek-search: warning: no HTTP auth token configured, the MCP endpoint is open to anyone "+
			"who can reach %s (set --http-auth-token or MCP_HTTP_AUTH_TOKEN, or bind to a private address)\n", cfg.HTTP.Addr)
	}

	handler := mcpserver.NewHTTPHandler(srv, mcpserver.HTTPOptions{
		Path:              cfg.HTTP.Path,
		AuthToken:         cfg.HTTP.AuthToken,
		Stateless:         cfg.HTTP.Stateless,
		JSONResponse:      cfg.HTTP.JSONResponse,
		SessionTimeout:    cfg.HTTP.SessionTimeout,
		AllowNonLocalHost: cfg.HTTP.AllowNonLocalHost,
		Logger:            logger,
	})

	httpServer := &http.Server{
		Addr:    cfg.HTTP.Addr,
		Handler: handler,
		// No WriteTimeout: Streamable HTTP responses may stay open.
		ReadHeaderTimeout: 15 * time.Second,
		IdleTimeout:       2 * time.Minute,
		BaseContext:       func(net.Listener) context.Context { return ctx },
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
	}

	scheme := "http"
	logger.Info("serving MCP over HTTP",
		"url", fmt.Sprintf("%s://%s%s", scheme, cfg.HTTP.Addr, cfg.HTTP.Path),
		"health", fmt.Sprintf("%s://%s%s", scheme, cfg.HTTP.Addr, mcpserver.HealthPath),
		"auth", authLabel(cfg.HTTP.AuthToken),
		"stateless", cfg.HTTP.Stateless,
		"json_response", cfg.HTTP.JSONResponse,
		"session_timeout", cfg.HTTP.SessionTimeout.String(),
		"allow_non_local_host", cfg.HTTP.AllowNonLocalHost,
		"version", version,
		"base_url", cfg.BaseURL)

	errCh := make(chan error, 1)
	go func() {
		err := httpServer.ListenAndServe()
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		errCh <- err
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		logger.Info("shutting down", "timeout", shutdownTimeout.String())
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("shutdown http server: %w", err)
		}
		return nil
	}
}

func authLabel(token string) string {
	if token == "" {
		return "disabled"
	}
	return "bearer token required"
}
