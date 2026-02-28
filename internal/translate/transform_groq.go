package translate

import (
	"encoding/json"
	"strconv"
)

// groqTransform strips cache_control, $schema from requests and fixes numeric tool call IDs.
type groqTransform struct{}

func newGroqTransform() *groqTransform {
	return &groqTransform{}
}

func (g *groqTransform) Name() string { return "groq" }

// TransformRequest strips $schema from tool parameters.
// Note: cache_control stripping is handled by the cleancache transform.
func (g *groqTransform) TransformRequest(req map[string]interface{}, ctx *TransformContext) error {
	// Strip $schema from tool parameters.
	if tools, ok := req["tools"].([]interface{}); ok {
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
			delete(params, "$schema")
		}
	}

	return nil
}

// TransformResponse is a passthrough.
func (g *groqTransform) TransformResponse(body []byte, ctx *TransformContext) ([]byte, error) {
	return body, nil
}

// TransformStreamChunk fixes numeric tool call IDs and adjusts choice index.
func (g *groqTransform) TransformStreamChunk(data []byte, ctx *TransformContext) ([][]byte, error) {
	var chunk map[string]interface{}
	if err := json.Unmarshal(data, &chunk); err != nil {
		return [][]byte{data}, nil
	}

	choices, ok := chunk["choices"].([]interface{})
	if !ok || len(choices) == 0 {
		return [][]byte{data}, nil
	}
	choice, ok := choices[0].(map[string]interface{})
	if !ok {
		return [][]byte{data}, nil
	}
	delta, ok := choice["delta"].(map[string]interface{})
	if !ok {
		return [][]byte{data}, nil
	}
	toolCalls, ok := delta["tool_calls"].([]interface{})
	if !ok || len(toolCalls) == 0 {
		return [][]byte{data}, nil
	}

	modified := false

	// Fix numeric tool call ID.
	if tc, ok := toolCalls[0].(map[string]interface{}); ok {
		if id, ok := tc["id"].(string); ok {
			if _, err := strconv.Atoi(id); err == nil {
				tc["id"] = "call_" + randomHex(12)
				modified = true
			}
		}
	}

	// Adjust index if there's prior text content.
	if ctx.HasTextContent {
		idx, _ := choice["index"].(float64)
		choice["index"] = idx + 1
		modified = true
	}

	if !modified {
		return [][]byte{data}, nil
	}

	out, err := json.Marshal(chunk)
	if err != nil {
		return [][]byte{data}, nil
	}
	return [][]byte{out}, nil
}

func init() {
	RegisterTransform("groq", func() Transformer {
		return newGroqTransform()
	})
}
