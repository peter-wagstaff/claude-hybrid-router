package translate

import (
	"encoding/json"
	"fmt"
	"sort"
)

const maxToolCallBufferSize = 1 << 20 // 1MB

// enhancetoolTransform repairs malformed tool call JSON arguments from LLMs.
type enhancetoolTransform struct{}

func newEnhancetoolTransform() *enhancetoolTransform {
	return &enhancetoolTransform{}
}

func (e *enhancetoolTransform) Name() string { return "enhancetool" }

// TransformRequest is a no-op.
func (e *enhancetoolTransform) TransformRequest(_ map[string]interface{}, _ *TransformContext) error {
	return nil
}

// TransformResponse repairs tool call arguments in non-streaming responses.
func (e *enhancetoolTransform) TransformResponse(body []byte, ctx *TransformContext) ([]byte, error) {
	var parsed map[string]interface{}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return body, nil
	}

	choices, ok := parsed["choices"].([]interface{})
	if !ok || len(choices) == 0 {
		return body, nil
	}
	choice, ok := choices[0].(map[string]interface{})
	if !ok {
		return body, nil
	}
	msg, ok := choice["message"].(map[string]interface{})
	if !ok {
		return body, nil
	}
	toolCalls, ok := msg["tool_calls"].([]interface{})
	if !ok {
		return body, nil
	}

	changed := false
	for _, tc := range toolCalls {
		tcMap, ok := tc.(map[string]interface{})
		if !ok {
			continue
		}
		fn, ok := tcMap["function"].(map[string]interface{})
		if !ok {
			continue
		}
		args, ok := fn["arguments"].(string)
		if !ok {
			continue
		}
		fixed := FixJSON(args)
		if fixed != args {
			fn["arguments"] = fixed
			changed = true
		}
	}

	if !changed {
		return body, nil
	}

	out, err := json.Marshal(parsed)
	if err != nil {
		return body, nil
	}
	return out, nil
}

// TransformStreamChunk buffers tool call arguments and repairs them on finish.
func (e *enhancetoolTransform) TransformStreamChunk(data []byte, ctx *TransformContext) ([][]byte, error) {
	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return [][]byte{data}, nil
	}

	choices, ok := parsed["choices"].([]interface{})
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

	// Check for finish_reason = "tool_calls" — flush all buffers.
	if fr, ok := choice["finish_reason"].(string); ok && fr == "tool_calls" {
		if len(ctx.ToolCallBuffers) == 0 {
			return [][]byte{data}, nil
		}

		// Build repaired tool calls chunk.
		repairedChunk, err := e.flushBuffers(ctx)
		if err != nil {
			return nil, err
		}
		return [][]byte{repairedChunk, data}, nil
	}

	// Check for tool_calls in delta.
	tcArr, ok := delta["tool_calls"].([]interface{})
	if !ok || len(tcArr) == 0 {
		return [][]byte{data}, nil
	}

	tc, ok := tcArr[0].(map[string]interface{})
	if !ok {
		return [][]byte{data}, nil
	}

	idx := 0
	if idxVal, ok := tc["index"].(float64); ok {
		idx = int(idxVal)
	}

	// New tool call start: has "id" field.
	if id, ok := tc["id"].(string); ok {
		name := ""
		if fn, ok := tc["function"].(map[string]interface{}); ok {
			if n, ok := fn["name"].(string); ok {
				name = n
			}
		}
		ctx.ToolCallBuffers[idx] = &ToolCallBuffer{
			ID:   id,
			Name: name,
		}

		// Pass through with arguments cleared.
		if fn, ok := tc["function"].(map[string]interface{}); ok {
			fn["arguments"] = ""
		}
		out, err := json.Marshal(parsed)
		if err != nil {
			return [][]byte{data}, nil
		}
		return [][]byte{out}, nil
	}

	// Argument fragment: no id, just function.arguments.
	if fn, ok := tc["function"].(map[string]interface{}); ok {
		if args, ok := fn["arguments"].(string); ok {
			buf, exists := ctx.ToolCallBuffers[idx]
			if !exists {
				// No buffer for this index — pass through.
				return [][]byte{data}, nil
			}
			buf.Arguments.WriteString(args)

			// Buffer guard: flush if exceeds 1MB.
			if buf.Arguments.Len() > maxToolCallBufferSize {
				repairedChunk, err := e.flushBuffers(ctx)
				if err != nil {
					return nil, err
				}
				return [][]byte{repairedChunk}, nil
			}

			// Suppress the chunk.
			return nil, nil
		}
	}

	return [][]byte{data}, nil
}

// flushBuffers builds a single chunk containing all repaired tool calls.
func (e *enhancetoolTransform) flushBuffers(ctx *TransformContext) ([]byte, error) {
	// Sort indices for deterministic output.
	indices := make([]int, 0, len(ctx.ToolCallBuffers))
	for idx := range ctx.ToolCallBuffers {
		indices = append(indices, idx)
	}
	sort.Ints(indices)

	toolCalls := make([]interface{}, 0, len(indices))
	for _, idx := range indices {
		buf := ctx.ToolCallBuffers[idx]
		repaired := FixJSON(buf.Arguments.String())
		toolCalls = append(toolCalls, map[string]interface{}{
			"index": idx,
			"id":    buf.ID,
			"function": map[string]interface{}{
				"name":      buf.Name,
				"arguments": repaired,
			},
		})
	}

	// Clear buffers.
	for k := range ctx.ToolCallBuffers {
		delete(ctx.ToolCallBuffers, k)
	}

	chunk := map[string]interface{}{
		"choices": []interface{}{
			map[string]interface{}{
				"delta": map[string]interface{}{
					"tool_calls": toolCalls,
				},
			},
		},
	}

	out, err := json.Marshal(chunk)
	if err != nil {
		return nil, fmt.Errorf("marshal repaired tool calls: %w", err)
	}
	return out, nil
}

func init() {
	RegisterTransform("enhancetool", func() Transformer {
		return newEnhancetoolTransform()
	})
}
