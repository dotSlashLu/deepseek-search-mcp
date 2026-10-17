package mcpserver

import (
	"encoding/json"
	"fmt"

	"github.com/dotSlashLu/deepseek-search-mcp/internal/deepseek"
	"github.com/google/jsonschema-go/jsonschema"
)

// The schemas advertised to clients start from the schema inferred from the Go
// input and output structs and are then extended with what inference cannot
// express: numeric bounds, length bounds and defaults. Clients that validate
// arguments locally can then reject bad calls before spending a round trip, and
// the model sees the defaults instead of having to infer them from prose.
var (
	searchInputSchema = mustSchema[searchInput](func(s *jsonschema.Schema) {
		p := s.Properties
		p["query"].MinLength = intPtr(1)
		p["query"].MaxLength = intPtr(deepseek.MaxQueryRunes)
		p["count"].Minimum = floatPtr(deepseek.MinCount)
		p["count"].Maximum = floatPtr(deepseek.MaxCount)
		p["count"].Default = rawJSON(10)
		p["ori_question"].MinLength = intPtr(1)
		p["ori_question"].MaxLength = intPtr(deepseek.MaxQueryRunes)
		p["need_content"].Default = rawJSON(false)
		// MaxContentChars is a pointer so that "omitted" (schema default 4000)
		// stays distinguishable from an explicit 0 ("no limit").
		normalizeInteger(p["max_content_chars"])
		p["max_content_chars"].Minimum = floatPtr(0)
		p["max_content_chars"].Default = rawJSON(defaultMaxContentChars)
	})

	searchOutputSchema = mustSchema[searchOutput](func(s *jsonschema.Schema) {
		// A nil result slice is omitted, so the field is never a JSON null.
		s.Properties["results"].Type = "array"
		s.Properties["results"].Types = nil
	})

	fetchInputSchema = mustSchema[fetchInput](func(s *jsonschema.Schema) {
		p := s.Properties
		p["url"].MinLength = intPtr(1)
		p["url"].MaxLength = intPtr(deepseek.MaxURLLength)
		p["max_chars"].Minimum = floatPtr(0)
		p["max_chars"].Default = rawJSON(0)
	})
)

// mustSchema infers the JSON schema of T, applies patch and returns the
// encoded schema. It panics on failure: these schemas are built once at
// start-up, so an error is a programming mistake rather than a runtime one.
func mustSchema[T any](patch func(*jsonschema.Schema)) json.RawMessage {
	s, err := jsonschema.For[T](nil)
	if err != nil {
		panic(fmt.Sprintf("mcpserver: infer schema for %T: %v", *new(T), err))
	}
	if patch != nil {
		patch(s)
	}
	data, err := json.Marshal(s)
	if err != nil {
		panic(fmt.Sprintf("mcpserver: marshal schema for %T: %v", *new(T), err))
	}
	return data
}

// normalizeInteger drops the "null" alternative that schema inference adds for
// pointer fields, so clients see a plain integer.
func normalizeInteger(s *jsonschema.Schema) {
	s.Type = "integer"
	s.Types = nil
}

func intPtr(v int) *int { return &v }

func floatPtr(v int) *float64 {
	f := float64(v)
	return &f
}

func rawJSON(v any) json.RawMessage {
	data, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("mcpserver: marshal default %v: %v", v, err))
	}
	return data
}
