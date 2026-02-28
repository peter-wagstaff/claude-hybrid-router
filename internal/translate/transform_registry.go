package translate

import "fmt"

// transformRegistry maps transform names to constructor functions.
var transformRegistry = map[string]func() Transformer{}

// RegisterTransform registers a Transformer constructor under the given name.
func RegisterTransform(name string, ctor func() Transformer) {
	transformRegistry[name] = ctor
}

// BuildChain creates a TransformChain from a list of registered transform names.
// Returns an error if any name is not found in the registry.
func BuildChain(names []string) (*TransformChain, error) {
	ts := make([]Transformer, len(names))
	for i, name := range names {
		ctor, ok := transformRegistry[name]
		if !ok {
			return nil, fmt.Errorf("unknown transform: %q", name)
		}
		ts[i] = ctor()
	}
	return NewTransformChain(ts...), nil
}

// schemaTransform wraps a SchemaTransformer into the Transformer interface.
type schemaTransform struct {
	name    string
	cleaner SchemaTransformer
}

func (s *schemaTransform) Name() string { return s.name }

func (s *schemaTransform) TransformRequest(req map[string]interface{}, ctx *TransformContext) error {
	tools, ok := req["tools"].([]interface{})
	if !ok {
		return nil
	}
	for _, t := range tools {
		tool, ok := t.(map[string]interface{})
		if !ok {
			continue
		}
		fn, ok := tool["function"].(map[string]interface{})
		if !ok {
			continue
		}
		params, ok := fn["parameters"].(map[string]interface{})
		if !ok {
			continue
		}
		s.cleaner.CleanSchema(params)
	}
	return nil
}

func (s *schemaTransform) TransformResponse(body []byte, ctx *TransformContext) ([]byte, error) {
	return body, nil
}

func (s *schemaTransform) TransformStreamChunk(data []byte, ctx *TransformContext) ([][]byte, error) {
	return [][]byte{data}, nil
}

func init() {
	RegisterTransform("schema:generic", func() Transformer {
		return &schemaTransform{
			name:    "schema:generic",
			cleaner: &fieldStripper{fields: []string{"additionalProperties", "$schema", "strict"}},
		}
	})
	RegisterTransform("schema:openai", func() Transformer {
		return &schemaTransform{
			name:    "schema:openai",
			cleaner: &fieldStripper{fields: []string{"strict"}},
		}
	})
	RegisterTransform("schema:gemini", func() Transformer {
		return &schemaTransform{
			name:    "schema:gemini",
			cleaner: &geminiTransformer{},
		}
	})
	RegisterTransform("schema:ollama", func() Transformer {
		return &schemaTransform{
			name:    "schema:ollama",
			cleaner: &fieldStripper{fields: []string{"additionalProperties", "$schema", "strict"}},
		}
	})
}
