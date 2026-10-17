// Package mcpserver exposes the DeepSeek Search and Fetch APIs as MCP tools and
// serves them over stdio or Streamable HTTP.
package mcpserver

import (
	"context"
	"log/slog"

	"github.com/dotSlashLu/deepseek-search-mcp/internal/deepseek"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ServerName is the MCP implementation name reported to clients.
const ServerName = "deepseek-search"

// API is the subset of the DeepSeek client used by the tools. It is an
// interface so tests can substitute a fake.
type API interface {
	Search(ctx context.Context, req deepseek.SearchRequest) (*deepseek.SearchResponse, error)
	Fetch(ctx context.Context, url string) (*deepseek.FetchResponse, error)
}

// Options configures the MCP server.
type Options struct {
	// Version is the build version reported to clients.
	Version string
	// API performs the DeepSeek calls.
	API API
	// Logger receives server logs. Defaults to slog.Default().
	Logger *slog.Logger
}

const instructions = `Two read-only web tools: one searches for pages, one fetches a single page as Markdown.

Prefer the summaries that a search returns and fetch only when the full text of a page matters. Every successful call is billed and rate limited to 5 requests per second per account, so run few, well-targeted queries and use the site:, lang:, after: and before: operators to narrow them.

When a tool reports an error, read the message: it names the cause and whether retrying can help. Cite the URL and publication date of the pages you rely on, and treat authority and relevance scores as hints about how much to trust a result.`

// NewServer builds the MCP server with both tools registered.
func NewServer(opts Options) *mcp.Server {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	if opts.API == nil {
		panic("mcpserver: Options.API must not be nil")
	}
	s := mcp.NewServer(&mcp.Implementation{
		Name:        ServerName,
		Title:       "DeepSeek Search & Fetch",
		Version:     opts.Version,
		Description: "Web search and page fetching via the DeepSeek Search and Fetch APIs.",
	}, &mcp.ServerOptions{
		Instructions: instructions,
		Logger:       logger,
	})

	mcp.AddTool(s, searchTool(), searchHandler(opts.API, logger))
	mcp.AddTool(s, fetchTool(), fetchHandler(opts.API, logger))
	return s
}
