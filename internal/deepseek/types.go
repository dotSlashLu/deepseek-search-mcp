package deepseek

import (
	"fmt"
	"net/http"
	"strings"
	"time"
)

// SearchRequest is the request body of POST /v1/tools/search.
//
// See https://api-docs.deepseek.com/api/search.
type SearchRequest struct {
	// Query must contain 1 to 128 Unicode characters and cannot be all
	// whitespace. Supports the operators site:, -site:, -term, -"phrase",
	// "exact phrase", lang:, after: and before:.
	Query string `json:"query"`
	// Count is the number of results to return, between 1 and 50. Zero means
	// "let the API decide" (default 10).
	Count int `json:"count,omitempty"`
	// OriQuestion is the user's original question that triggered the search.
	OriQuestion string `json:"ori_question,omitempty"`
	// Localization is an optional IP address used to localize results.
	Localization string `json:"localization,omitempty"`
	// NeedContent asks the API to fill SearchResult.Content.
	NeedContent bool `json:"need_content,omitempty"`
}

// SearchResponse is the response body of POST /v1/tools/search.
type SearchResponse struct {
	Results []SearchResult `json:"results"`
}

// SearchResult is a single entry of a search response.
type SearchResult struct {
	// RerankScore is a relevance score from 0 to 3; >= 1.5 means high quality.
	RerankScore float64 `json:"rerank_score"`
	Title       string  `json:"title"`
	URL         string  `json:"url"`
	Snippet     string  `json:"snippet"`
	Summary     string  `json:"summary"`
	// Content is the page body; empty unless NeedContent was set and content
	// was available.
	Content  string  `json:"content"`
	SiteName *string `json:"site_name"`
	SiteIcon *string `json:"site_icon"`
	// Date is the publication time in UTC (RFC 3339), or nil when unknown.
	Date *string `json:"date"`
	// Authority is the authority tier from 0 to 4; -1 means unknown.
	Authority int `json:"authority"`
}

// FetchRequest is the request body of POST /v1/tools/fetch.
type FetchRequest struct {
	// URL must be an HTTP or HTTPS URL with a host, at most 8192 characters and
	// without embedded credentials.
	URL string `json:"url"`
}

// FetchResponse is the response body of POST /v1/tools/fetch.
type FetchResponse struct {
	// Title is empty when the page title could not be determined.
	Title string `json:"title"`
	// Content is the page content as Markdown.
	Content string `json:"content"`
}

// errorEnvelope is the OpenAI-compatible error format returned by DeepSeek.
type errorEnvelope struct {
	Error struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Param   any    `json:"param"`
		Code    string `json:"code"`
	} `json:"error"`
}

// APIError is a non-2xx response from the DeepSeek API. It carries the HTTP
// status as well as the parsed error body when available.
type APIError struct {
	// Endpoint is the request path, e.g. "/v1/tools/fetch".
	Endpoint string
	// StatusCode is the HTTP status code.
	StatusCode int
	// Code, Type and Message come from the response body's error object.
	Code    string
	Type    string
	Message string
	// Body is a truncated raw response body, used when the error object could
	// not be parsed.
	Body string
	// RetryAfter is the parsed Retry-After header, if any.
	RetryAfter time.Duration
}

// Error implements the error interface. The message lists the concrete HTTP
// failure, a human-readable hint for the documented status codes and the
// upstream message.
func (e *APIError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "deepseek: POST %s failed: HTTP %d", e.Endpoint, e.StatusCode)
	if hint := StatusHint(e.StatusCode); hint != "" {
		b.WriteString(" (" + hint + ")")
	}
	msg := strings.TrimSpace(e.Message)
	if msg == "" {
		msg = strings.TrimSpace(e.Body)
	}
	if msg != "" {
		b.WriteString(": " + msg)
	}
	return b.String()
}

// retryable reports whether the request may be retried as documented.
func (e *APIError) retryable() bool {
	switch {
	case e.StatusCode == http.StatusRequestTimeout, // 408
		e.StatusCode == http.StatusTooManyRequests, // 429
		e.StatusCode >= 500:                        // 5xx
		return true
	default:
		return false
	}
}

// StatusHint returns a short explanation of a DeepSeek Search/Fetch status
// code, as documented at https://api-docs.deepseek.com/guides/search.
func StatusHint(status int) string {
	switch status {
	case http.StatusBadRequest:
		return "invalid request"
	case http.StatusUnauthorized:
		return "API key missing or invalid"
	case http.StatusPaymentRequired:
		return "insufficient account balance"
	case http.StatusForbidden:
		return "fetch rejected by the security policy"
	case http.StatusNotFound:
		return "no usable page content could be retrieved"
	case http.StatusRequestTimeout:
		return "reading the request body timed out"
	case http.StatusRequestEntityTooLarge:
		return "request body too large"
	case http.StatusTooManyRequests:
		return "rate limit exceeded (Search and Fetch allow 5 QPS per account)"
	case http.StatusInternalServerError:
		return "DeepSeek internal error"
	case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return "upstream unavailable, try again later"
	default:
		return ""
	}
}
