package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/dotSlashLu/deepseek-search-mcp/internal/deepseek"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type fakeAPI struct {
	search func(context.Context, deepseek.SearchRequest) (*deepseek.SearchResponse, error)
	fetch  func(context.Context, string) (*deepseek.FetchResponse, error)

	lastSearch deepseek.SearchRequest
	lastURL    string
}

func (f *fakeAPI) Search(ctx context.Context, req deepseek.SearchRequest) (*deepseek.SearchResponse, error) {
	f.lastSearch = req
	if f.search == nil {
		return &deepseek.SearchResponse{}, nil
	}
	return f.search(ctx, req)
}

func (f *fakeAPI) Fetch(ctx context.Context, url string) (*deepseek.FetchResponse, error) {
	f.lastURL = url
	if f.fetch == nil {
		return &deepseek.FetchResponse{}, nil
	}
	return f.fetch(ctx, url)
}

func discardLogger() *slog.Logger { return slog.New(slog.DiscardHandler) }

// connect starts the server on an in-memory transport and returns a connected
// client session.
func connect(t *testing.T, api API) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	server := NewServer(Options{Version: "test", API: api, Logger: discardLogger()})
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "test"}, nil)

	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("server.Connect: %v", err)
	}
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client.Connect: %v", err)
	}
	t.Cleanup(func() {
		_ = session.Close()
		_ = serverSession.Close()
	})
	return session
}

func toolText(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

func structured[T any](t *testing.T, res *mcp.CallToolResult) T {
	t.Helper()
	var out T
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatalf("marshal structured content: %v", err)
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal structured content %s: %v", raw, err)
	}
	return out
}

func TestToolsList(t *testing.T) {
	session := connect(t, &fakeAPI{})
	res, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	byName := map[string]*mcp.Tool{}
	for _, tool := range res.Tools {
		byName[tool.Name] = tool
	}
	if len(byName) != 2 {
		t.Fatalf("tools = %v, want deepseek_search and deepseek_fetch", byName)
	}
	for _, name := range []string{"deepseek_search", "deepseek_fetch"} {
		tool, ok := byName[name]
		if !ok {
			t.Fatalf("tool %q missing", name)
		}
		if tool.Description == "" {
			t.Errorf("tool %q has no description", name)
		}
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint || tool.Annotations.Title == "" {
			t.Errorf("tool %q annotations = %+v, want read-only with a title", name, tool.Annotations)
		}
	}
	schema, ok := byName["deepseek_search"].InputSchema.(map[string]any)
	if !ok {
		t.Fatalf("input schema type = %T", byName["deepseek_search"].InputSchema)
	}
	if schema["type"] != "object" {
		t.Errorf("input schema type = %v, want object", schema["type"])
	}
	required, _ := schema["required"].([]any)
	found := false
	for _, r := range required {
		if r == "query" {
			found = true
		}
	}
	if !found {
		t.Errorf("required = %v, want it to contain query", required)
	}
	if _, ok := byName["deepseek_fetch"].OutputSchema.(map[string]any); !ok {
		t.Errorf("fetch output schema type = %T, want an object schema", byName["deepseek_fetch"].OutputSchema)
	}
}

// TestToolSchemaBounds pins the constraints that let a client reject bad
// arguments without a round trip, and the defaults it can read off the schema.
func TestToolSchemaBounds(t *testing.T) {
	session := connect(t, &fakeAPI{})
	res, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	tools := map[string]*mcp.Tool{}
	for _, tool := range res.Tools {
		tools[tool.Name] = tool
	}
	search := schemaObject(t, tools["deepseek_search"].InputSchema)
	fetch := schemaObject(t, tools["deepseek_fetch"].InputSchema)

	assertBound(t, "search query maxLength", search, "query", "maxLength", float64(deepseek.MaxQueryRunes))
	assertBound(t, "search count minimum", search, "count", "minimum", float64(deepseek.MinCount))
	assertBound(t, "search count maximum", search, "count", "maximum", float64(deepseek.MaxCount))
	assertBound(t, "search count default", search, "count", "default", float64(10))
	assertBound(t, "search max_content_chars type", search, "max_content_chars", "type", "integer")
	assertBound(t, "search max_content_chars minimum", search, "max_content_chars", "minimum", float64(0))
	assertBound(t, "search max_content_chars default", search, "max_content_chars", "default", float64(defaultMaxContentChars))
	assertBound(t, "fetch url maxLength", fetch, "url", "maxLength", float64(deepseek.MaxURLLength))
	assertBound(t, "fetch max_chars maximum-free default", fetch, "max_chars", "default", float64(0))

	// The model must not have to send a value for either limit.
	searchRequired, _ := search["required"].([]any)
	for _, r := range searchRequired {
		if r == "max_content_chars" || r == "count" {
			t.Errorf("%v is required, want it optional with a schema default", r)
		}
	}
	fetchRequired, _ := fetch["required"].([]any)
	if len(fetchRequired) != 1 || fetchRequired[0] != "url" {
		t.Errorf("fetch required = %v, want [url]", fetchRequired)
	}
}

// TestModelFacingTextIsVendorNeutral encodes a deliberate choice: descriptions,
// titles and instructions tell the model what a tool does, not whose API backs
// it. The vendor is documented for humans in the README and the logs.
func TestModelFacingTextIsVendorNeutral(t *testing.T) {
	session := connect(t, &fakeAPI{})
	res, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	for _, tool := range res.Tools {
		if strings.Contains(strings.ToLower(tool.Description), "deepseek") {
			t.Errorf("tool %q description mentions the vendor: %q", tool.Name, tool.Description)
		}
		if tool.Annotations != nil && strings.Contains(strings.ToLower(tool.Annotations.Title), "deepseek") {
			t.Errorf("tool %q annotation title mentions the vendor: %q", tool.Name, tool.Annotations.Title)
		}
	}
	init := session.InitializeResult()
	if init == nil {
		t.Fatal("no initialize result")
	}
	if strings.Contains(strings.ToLower(init.Instructions), "deepseek") {
		t.Errorf("server instructions mention the vendor: %q", init.Instructions)
	}
	if init.Instructions == "" {
		t.Error("server instructions are empty")
	}
}

func schemaObject(t *testing.T, schema any) map[string]any {
	t.Helper()
	obj, ok := schema.(map[string]any)
	if !ok {
		t.Fatalf("schema type = %T, want an object", schema)
	}
	return obj
}

func assertBound(t *testing.T, label string, schema map[string]any, property, key string, want any) {
	t.Helper()
	props, _ := schema["properties"].(map[string]any)
	prop, ok := props[property].(map[string]any)
	if !ok {
		t.Fatalf("%s: property %q missing from the schema", label, property)
	}
	got, ok := prop[key]
	if !ok {
		t.Fatalf("%s: property %q has no %q", label, property, key)
	}
	if got != want {
		t.Errorf("%s = %v, want %v", label, got, want)
	}
}

func TestSearchToolCall(t *testing.T) {
	api := &fakeAPI{search: func(_ context.Context, req deepseek.SearchRequest) (*deepseek.SearchResponse, error) {
		site := "Example News"
		date := "2026-06-01T00:00:00+00:00"
		return &deepseek.SearchResponse{Results: []deepseek.SearchResult{{
			RerankScore: 2.4,
			Title:       "DeepSeek V4 released",
			URL:         "https://example.com/v4",
			Snippet:     "V4 is out",
			Summary:     "A longer summary",
			SiteName:    &site,
			Date:        &date,
			Authority:   3,
		}}}, nil
	}}
	session := connect(t, api)

	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "deepseek_search",
		Arguments: map[string]any{"query": "DeepSeek V4", "count": 5, "ori_question": "what shipped?"},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("IsError = true, content = %s", toolText(t, res))
	}
	text := toolText(t, res)
	for _, want := range []string{"Found 1 result(s)", "DeepSeek V4 released", "https://example.com/v4", "authority: 3/4", "relevance: 2.40", "Example News", "A longer summary"} {
		if !strings.Contains(text, want) {
			t.Errorf("result text missing %q:\n%s", want, text)
		}
	}
	if api.lastSearch.Query != "DeepSeek V4" || api.lastSearch.Count != 5 || api.lastSearch.OriQuestion != "what shipped?" {
		t.Errorf("API request = %+v", api.lastSearch)
	}
	if api.lastSearch.NeedContent {
		t.Error("NeedContent should default to false")
	}
	out := structured[searchOutput](t, res)
	if out.Query != "DeepSeek V4" || out.ResultCount != 1 || len(out.Results) != 1 {
		t.Errorf("structured output = %+v", out)
	}
	if out.Results[0].SiteName != "Example News" || out.Results[0].Authority != 3 {
		t.Errorf("structured result = %+v", out.Results[0])
	}
}

func TestSearchToolEmptyResults(t *testing.T) {
	session := connect(t, &fakeAPI{})
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "deepseek_search",
		Arguments: map[string]any{"query": "nothing at all"},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("empty results must not be an error: %s", toolText(t, res))
	}
	if !strings.Contains(toolText(t, res), "No results found") {
		t.Errorf("text = %q, want a no-results notice", toolText(t, res))
	}
}

func TestSearchToolTruncatesContent(t *testing.T) {
	api := &fakeAPI{search: func(_ context.Context, req deepseek.SearchRequest) (*deepseek.SearchResponse, error) {
		if !req.NeedContent {
			t.Error("expected need_content to be forwarded")
		}
		return &deepseek.SearchResponse{Results: []deepseek.SearchResult{{
			Title:   "long page",
			URL:     "https://example.com/long",
			Content: strings.Repeat("x", 5000),
		}}}, nil
	}}
	session := connect(t, api)
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "deepseek_search",
		Arguments: map[string]any{"query": "q", "need_content": true, "max_content_chars": 100},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	text := toolText(t, res)
	if !strings.Contains(text, "truncated at 100 characters") {
		t.Errorf("text = %q, want a truncation marker", text)
	}
	if strings.Count(text, "x") > 200 {
		t.Errorf("content was not truncated: %d x characters", strings.Count(text, "x"))
	}
}

func TestSearchToolDefaultContentLimit(t *testing.T) {
	api := &fakeAPI{search: func(_ context.Context, _ deepseek.SearchRequest) (*deepseek.SearchResponse, error) {
		return &deepseek.SearchResponse{Results: []deepseek.SearchResult{{
			Title:   "long page",
			URL:     "https://example.com/long",
			Content: strings.Repeat("y", defaultMaxContentChars+1000),
		}}}, nil
	}}
	session := connect(t, api)
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "deepseek_search",
		Arguments: map[string]any{"query": "q", "need_content": true},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !strings.Contains(toolText(t, res), "truncated at 4000 characters") {
		t.Errorf("default limit not applied: %q", toolText(t, res)[:200])
	}
}

// TestSearchToolNoContentLimit covers the explicit "0 means no limit" escape
// hatch, which only works because the field is a pointer: the schema default
// (4000) fills an omitted value.
func TestSearchToolNoContentLimit(t *testing.T) {
	api := &fakeAPI{search: func(_ context.Context, _ deepseek.SearchRequest) (*deepseek.SearchResponse, error) {
		return &deepseek.SearchResponse{Results: []deepseek.SearchResult{{
			Title:   "long page",
			URL:     "https://example.com/long",
			Content: strings.Repeat("w", defaultMaxContentChars+1000),
		}}}, nil
	}}
	session := connect(t, api)
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "deepseek_search",
		Arguments: map[string]any{"query": "q", "need_content": true, "max_content_chars": 0},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	text := toolText(t, res)
	if strings.Contains(text, "truncated") {
		t.Errorf("max_content_chars=0 should return the whole body: %q", text[:120])
	}
	if got := strings.Count(text, "w"); got != defaultMaxContentChars+1000 {
		t.Errorf("content length = %d, want %d", got, defaultMaxContentChars+1000)
	}
}

func TestSearchToolNegativeContentLimitIsRejected(t *testing.T) {
	session := connect(t, &fakeAPI{})
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "deepseek_search",
		Arguments: map[string]any{"query": "q", "need_content": true, "max_content_chars": -5},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !res.IsError {
		t.Error("a negative max_content_chars should be rejected by the schema")
	}
}

func TestSearchToolAPIErrorIsAToolError(t *testing.T) {
	api := &fakeAPI{search: func(context.Context, deepseek.SearchRequest) (*deepseek.SearchResponse, error) {
		return nil, &deepseek.APIError{Endpoint: deepseek.SearchPath, StatusCode: 402, Message: "Insufficient Balance"}
	}}
	session := connect(t, api)
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "deepseek_search",
		Arguments: map[string]any{"query": "q"},
	})
	if err != nil {
		t.Fatalf("CallTool returned a protocol error, want a tool error: %v", err)
	}
	if !res.IsError {
		t.Fatal("IsError = false, want true")
	}
	text := toolText(t, res)
	for _, want := range []string{"insufficient account balance", "Insufficient Balance", "HTTP 402"} {
		if !strings.Contains(text, want) {
			t.Errorf("error text = %q, want it to contain %q", text, want)
		}
	}
}

func TestSearchToolValidation(t *testing.T) {
	session := connect(t, &fakeAPI{})
	tests := []struct {
		name      string
		arguments map[string]any
	}{
		{"missing query", map[string]any{}},
		{"empty query", map[string]any{"query": "  "}},
		{"count out of range", map[string]any{"query": "q", "count": 51}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
				Name:      "deepseek_search",
				Arguments: tc.arguments,
			})
			if err != nil {
				t.Fatalf("CallTool() error: %v", err)
			}
			if !res.IsError {
				t.Fatalf("IsError = false, want a validation error: %s", toolText(t, res))
			}
			if strings.TrimSpace(toolText(t, res)) == "" {
				t.Error("validation error carries no message")
			}
		})
	}
}

func TestFetchToolCall(t *testing.T) {
	api := &fakeAPI{fetch: func(_ context.Context, url string) (*deepseek.FetchResponse, error) {
		return &deepseek.FetchResponse{Title: "Example article", Content: "# Example article\n\nBody."}, nil
	}}
	session := connect(t, api)
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "deepseek_fetch",
		Arguments: map[string]any{"url": "https://example.com/article"},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("IsError = true: %s", toolText(t, res))
	}
	text := toolText(t, res)
	if !strings.Contains(text, "Title: Example article") || !strings.Contains(text, "Body.") {
		t.Errorf("text = %q", text)
	}
	if api.lastURL != "https://example.com/article" {
		t.Errorf("fetched URL = %q", api.lastURL)
	}
	out := structured[fetchOutput](t, res)
	if out.Title != "Example article" || out.Truncated || out.ContentChars == 0 {
		t.Errorf("structured output = %+v", out)
	}
}

func TestFetchToolTruncation(t *testing.T) {
	api := &fakeAPI{fetch: func(context.Context, string) (*deepseek.FetchResponse, error) {
		return &deepseek.FetchResponse{Title: "", Content: strings.Repeat("z", 100)}, nil
	}}
	session := connect(t, api)
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "deepseek_fetch",
		Arguments: map[string]any{"url": "https://example.com", "max_chars": 10},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	text := toolText(t, res)
	if !strings.Contains(text, "truncated at 10 characters") || !strings.Contains(text, "(untitled)") {
		t.Errorf("text = %q", text)
	}
	out := structured[fetchOutput](t, res)
	if !out.Truncated || out.ContentChars != 10 {
		t.Errorf("structured output = %+v", out)
	}
}

func TestFetchToolURLValidation(t *testing.T) {
	session := connect(t, &fakeAPI{})
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "deepseek_fetch",
		Arguments: map[string]any{"url": "not-a-url"},
	})
	if err != nil {
		t.Fatalf("CallTool() error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("IsError = false, want a validation error: %s", toolText(t, res))
	}
}

func TestFetchToolPropagatesAPIError(t *testing.T) {
	api := &fakeAPI{fetch: func(context.Context, string) (*deepseek.FetchResponse, error) {
		return nil, errors.New("deepseek: POST /v1/tools/fetch failed: HTTP 404 (no usable page content could be retrieved)")
	}}
	session := connect(t, api)
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "deepseek_fetch",
		Arguments: map[string]any{"url": "https://example.com/missing"},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !res.IsError || !strings.Contains(toolText(t, res), "HTTP 404") {
		t.Errorf("IsError = %v, text = %q", res.IsError, toolText(t, res))
	}
}

func TestTruncateRunes(t *testing.T) {
	tests := []struct {
		name      string
		in        string
		max       int
		want      string
		truncated bool
	}{
		{"no limit", "hello", 0, "hello", false},
		{"negative limit", "hello", -1, "hello", false},
		{"fits", "hello", 5, "hello", false},
		{"cut ascii", "hello", 3, "hel", true},
		{"cut multibyte", "中文测试", 2, "中文", true},
		{"empty", "", 10, "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, truncated := truncateRunes(tc.in, tc.max)
			if got != tc.want || truncated != tc.truncated {
				t.Errorf("truncateRunes(%q, %d) = %q, %v; want %q, %v", tc.in, tc.max, got, truncated, tc.want, tc.truncated)
			}
		})
	}
}

func TestOneLine(t *testing.T) {
	if got := oneLine("a\nb\t c  d"); got != "a b c d" {
		t.Errorf("oneLine() = %q", got)
	}
}
