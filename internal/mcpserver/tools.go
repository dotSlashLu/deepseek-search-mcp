package mcpserver

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/dotSlashLu/deepseek-search-mcp/internal/deepseek"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// defaultMaxContentChars limits the per-result page body included in a search
// response so a single search cannot flood the model context.
const defaultMaxContentChars = 4000

// Tool and parameter descriptions are written for the model that has to decide
// whether and how to call them: capability, when to use, when not to, and what
// a call costs. They deliberately avoid describing the upstream vendor — that
// is operator information and belongs in the README, the logs and --help.

// searchInput is the argument schema of the deepseek_search tool.
type searchInput struct {
	Query        string `json:"query" jsonschema:"the search query, 1 to 128 characters; operators: site:, -site:, -term, -\"phrase\", \"exact phrase\", lang:, after:, before:"`
	Count        int    `json:"count,omitempty" jsonschema:"how many results to return, 1 to 50; defaults to 10"`
	OriQuestion  string `json:"ori_question,omitempty" jsonschema:"the user's original question that led to this search; improves query understanding"`
	Localization string `json:"localization,omitempty" jsonschema:"IP address used to localize results; pass it only when the user supplied one, never guess"`
	NeedContent  bool   `json:"need_content,omitempty" jsonschema:"true also returns each page body in the content field; leave false unless the body itself is needed"`
	// MaxContentChars is a pointer so the handler can tell an omitted value
	// (schema default: 4000) apart from an explicit 0, which means no limit.
	MaxContentChars *int `json:"max_content_chars,omitempty" jsonschema:"character limit per returned page body when need_content is set; 0 means no limit"`
}

// searchOutput is the structured output of the deepseek_search tool.
type searchOutput struct {
	Query       string               `json:"query" jsonschema:"the query that was executed"`
	Count       int                  `json:"count" jsonschema:"the number of results requested"`
	ResultCount int                  `json:"result_count" jsonschema:"how many results were returned"`
	Results     []searchResultOutput `json:"results,omitempty" jsonschema:"result metadata; the readable text lives in the tool content"`
}

type searchResultOutput struct {
	Title       string  `json:"title"`
	URL         string  `json:"url"`
	Snippet     string  `json:"snippet,omitempty" jsonschema:"short summary intended for display"`
	SiteName    string  `json:"site_name,omitempty"`
	Date        string  `json:"date,omitempty" jsonschema:"publication time in UTC (RFC 3339), empty when unknown"`
	Authority   int     `json:"authority" jsonschema:"authority tier from 0 to 4; -1 means unknown"`
	RerankScore float64 `json:"rerank_score" jsonschema:"relevance score from 0 to 3; 1.5 or above is high quality"`
}

// fetchInput is the argument schema of the deepseek_fetch tool.
type fetchInput struct {
	URL      string `json:"url" jsonschema:"absolute HTTP or HTTPS URL of the page to fetch, at most 8192 characters"`
	MaxChars int    `json:"max_chars,omitempty" jsonschema:"maximum characters of Markdown content to return; 0 means no limit"`
}

// fetchOutput is the structured output of the deepseek_fetch tool.
type fetchOutput struct {
	URL          string `json:"url" jsonschema:"the URL that was fetched"`
	Title        string `json:"title" jsonschema:"the page title, empty when unavailable"`
	ContentChars int    `json:"content_chars" jsonschema:"length in characters of the content returned in the tool content"`
	Truncated    bool   `json:"truncated" jsonschema:"whether the content was cut at max_chars"`
}

func readOnlyAnnotations(title string) *mcp.ToolAnnotations {
	openWorld := true
	return &mcp.ToolAnnotations{
		Title:           title,
		ReadOnlyHint:    true,
		IdempotentHint:  true,
		OpenWorldHint:   &openWorld,
		DestructiveHint: boolPtr(false),
	}
}

func boolPtr(b bool) *bool { return &b }

func searchTool() *mcp.Tool {
	return &mcp.Tool{
		Name: "deepseek_search",
		Description: "Search the web and return a ranked list of pages. Each result carries a title, URL, site, " +
			"publication date, authority tier (0-4), relevance score (0-3), a display snippet and a longer summary. " +
			"Use this to find pages, then fetch a result URL to read one page in full. " +
			"Query operators: site:, -site:, -term, -\"phrase\", \"exact phrase\", lang:, after:, before:. " +
			"Set need_content=true only when the page body itself is required. " +
			"Prefer few, well-targeted queries: every successful call is billed and limited to 5 requests per second per account.",
		InputSchema:  searchInputSchema,
		OutputSchema: searchOutputSchema,
		Annotations:  readOnlyAnnotations("Web Search"),
	}
}

func fetchTool() *mcp.Tool {
	return &mcp.Tool{
		Name: "deepseek_fetch",
		Description: "Fetch one web page and return its title plus the full page content as Markdown. " +
			"Use it when the full text of a page matters; search results already carry a summary. " +
			"Fails when the site blocks the request, the URL is unreachable, the content type is unsupported, " +
			"or no text can be extracted. " +
			"Every successful call is billed and limited to 5 requests per second per account.",
		InputSchema: fetchInputSchema,
		Annotations: readOnlyAnnotations("Page Fetch"),
	}
}

func searchHandler(api API, logger *slog.Logger) mcp.ToolHandlerFor[searchInput, searchOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in searchInput) (*mcp.CallToolResult, searchOutput, error) {
		req := deepseek.SearchRequest{
			Query:        strings.TrimSpace(in.Query),
			Count:        in.Count,
			OriQuestion:  strings.TrimSpace(in.OriQuestion),
			Localization: strings.TrimSpace(in.Localization),
			NeedContent:  in.NeedContent,
		}
		if req.Query == "" {
			return nil, searchOutput{}, fmt.Errorf("query must not be empty")
		}
		if req.Count < deepseek.MinCount || req.Count > deepseek.MaxCount {
			return nil, searchOutput{}, fmt.Errorf("count must be between %d and %d, got %d", deepseek.MinCount, deepseek.MaxCount, req.Count)
		}
		if req.OriQuestion != "" && len([]rune(req.OriQuestion)) > deepseek.MaxQueryRunes {
			return nil, searchOutput{}, fmt.Errorf("ori_question must be at most %d characters", deepseek.MaxQueryRunes)
		}

		maxContent := defaultMaxContentChars
		if in.MaxContentChars != nil {
			maxContent = *in.MaxContentChars
		}

		logger.Debug("deepseek_search", "query", req.Query, "count", req.Count, "need_content", req.NeedContent)
		resp, err := api.Search(ctx, req)
		if err != nil {
			return nil, searchOutput{}, err
		}
		if resp == nil {
			resp = &deepseek.SearchResponse{}
		}

		out := searchOutput{Query: req.Query, Count: req.Count, ResultCount: len(resp.Results)}
		for _, r := range resp.Results {
			meta := searchResultOutput{
				Title:       r.Title,
				URL:         r.URL,
				Snippet:     r.Snippet,
				Authority:   r.Authority,
				RerankScore: r.RerankScore,
			}
			if r.SiteName != nil {
				meta.SiteName = *r.SiteName
			}
			if r.Date != nil {
				meta.Date = *r.Date
			}
			out.Results = append(out.Results, meta)
		}

		text := renderSearch(req, resp, maxContent)
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}, out, nil
	}
}

func fetchHandler(api API, logger *slog.Logger) mcp.ToolHandlerFor[fetchInput, fetchOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in fetchInput) (*mcp.CallToolResult, fetchOutput, error) {
		rawURL := strings.TrimSpace(in.URL)
		if err := deepseek.ValidateURL(rawURL); err != nil {
			return nil, fetchOutput{}, err
		}
		logger.Debug("deepseek_fetch", "url", rawURL, "max_chars", in.MaxChars)
		resp, err := api.Fetch(ctx, rawURL)
		if err != nil {
			return nil, fetchOutput{}, err
		}
		if resp == nil {
			resp = &deepseek.FetchResponse{}
		}

		content, truncated := truncateRunes(resp.Content, in.MaxChars)
		title := strings.TrimSpace(resp.Title)
		if title == "" {
			title = "(untitled)"
		}

		var b strings.Builder
		fmt.Fprintf(&b, "Title: %s\nURL: %s\n", title, rawURL)
		if truncated {
			fmt.Fprintf(&b, "(content truncated to %d characters)\n", in.MaxChars)
		}
		b.WriteString("\n")
		b.WriteString(content)
		if truncated {
			b.WriteString(truncationMarker(in.MaxChars))
		}

		out := fetchOutput{
			URL:          rawURL,
			Title:        strings.TrimSpace(resp.Title),
			ContentChars: utf8.RuneCountInString(content),
			Truncated:    truncated,
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: b.String()}}}, out, nil
	}
}

// renderSearch formats search results as Markdown for the model and the user.
func renderSearch(req deepseek.SearchRequest, resp *deepseek.SearchResponse, maxContentChars int) string {
	var b strings.Builder
	if len(resp.Results) == 0 {
		fmt.Fprintf(&b, "No results found for %s.", strconv.Quote(req.Query))
		b.WriteString("\nA successful search without matches is not an error; try broader terms, drop restrictive operators, or rephrase the query.")
		return b.String()
	}

	fmt.Fprintf(&b, "Found %d result(s) for %s", len(resp.Results), strconv.Quote(req.Query))
	if req.Count > 0 && len(resp.Results) >= req.Count {
		fmt.Fprintf(&b, " (count limit %d reached — more may exist)", req.Count)
	}
	b.WriteString(".\n")

	for i, r := range resp.Results {
		fmt.Fprintf(&b, "\n### %d. %s\n", i+1, fallback(strings.TrimSpace(r.Title), "(untitled)"))
		fmt.Fprintf(&b, "- URL: %s\n", r.URL)
		meta := make([]string, 0, 4)
		if r.SiteName != nil && strings.TrimSpace(*r.SiteName) != "" {
			meta = append(meta, "site: "+strings.TrimSpace(*r.SiteName))
		}
		if r.Date != nil && strings.TrimSpace(*r.Date) != "" {
			meta = append(meta, "published: "+strings.TrimSpace(*r.Date))
		}
		meta = append(meta, "authority: "+authorityLabel(r.Authority))
		meta = append(meta, fmt.Sprintf("relevance: %.2f", r.RerankScore))
		fmt.Fprintf(&b, "- %s\n", strings.Join(meta, " | "))
		if s := strings.TrimSpace(r.Snippet); s != "" {
			fmt.Fprintf(&b, "- Snippet: %s\n", oneLine(s))
		}
		if s := strings.TrimSpace(r.Summary); s != "" {
			fmt.Fprintf(&b, "- Summary: %s\n", oneLine(s))
		}
		if c := strings.TrimSpace(r.Content); c != "" {
			content, truncated := truncateRunes(c, maxContentChars)
			if truncated {
				fmt.Fprintf(&b, "\nPage content (truncated to %d characters):\n\n%s\n%s\n", maxContentChars, content, truncationMarker(maxContentChars))
			} else {
				fmt.Fprintf(&b, "\nPage content:\n\n%s\n", content)
			}
		}
	}
	return b.String()
}

func authorityLabel(authority int) string {
	if authority < 0 {
		return "unknown"
	}
	return fmt.Sprintf("%d/4", authority)
}

func fallback(s, alt string) string {
	if s == "" {
		return alt
	}
	return s
}

// oneLine collapses newlines so a snippet stays on a single Markdown line.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func truncationMarker(max int) string {
	return fmt.Sprintf("\n\n[... truncated at %d characters; call deepseek_fetch on the URL for the full page ...]", max)
}

// truncateRunes cuts s to at most max characters (runes), never splitting a
// rune. A non-positive max means no limit.
func truncateRunes(s string, max int) (string, bool) {
	if max <= 0 {
		return s, false
	}
	runes := 0
	for i := range s {
		if runes == max {
			return s[:i], true
		}
		runes++
	}
	return s, false
}
