package translate

import "encoding/json"

// toolUseTransform injects a synthetic ExitTool into tool lists and intercepts
// ExitTool calls in responses, converting them to plain text content.
// This allows models that struggle with tool-required mode to "escape" by
// calling ExitTool with a plain text response.
type toolUseTransform struct{}

func (t *toolUseTransform) Name() string { return "tooluse" }

// exitToolDef is the ExitTool appended to the tools array.
var exitToolDef = map[string]interface{}{
	"type": "function",
	"function": map[string]interface{}{
		"name":        "ExitTool",
		"description": "Use this when no other tool applies. The response argument is forwarded directly to the user.",
		"parameters": map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"response": map[string]interface{}{"type": "string"},
			},
			"required": []string{"response"},
		},
	},
}

// TransformRequest appends ExitTool to tools and sets tool_choice to "required".
func (t *toolUseTransform) TransformRequest(req map[string]interface{}, ctx *TransformContext) error {
	tools, ok := req["tools"].([]interface{})
	if !ok || len(tools) == 0 {
		return nil
	}

	req["tools"] = append(tools, exitToolDef)
	req["tool_choice"] = "required"
	return nil
}

// TransformResponse intercepts ExitTool calls and converts them to plain content.
func (t *toolUseTransform) TransformResponse(body []byte, ctx *TransformContext) ([]byte, error) {
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
	if !ok || len(toolCalls) == 0 {
		return body, nil
	}
	tc, ok := toolCalls[0].(map[string]interface{})
	if !ok {
		return body, nil
	}
	fn, ok := tc["function"].(map[string]interface{})
	if !ok {
		return body, nil
	}
	name, _ := fn["name"].(string)
	if name != "ExitTool" {
		return body, nil
	}

	// Extract response from arguments JSON.
	content := ""
	if argsStr, ok := fn["arguments"].(string); ok {
		var args map[string]interface{}
		if json.Unmarshal([]byte(argsStr), &args) == nil {
			if r, ok := args["response"].(string); ok {
				content = r
			}
		}
	}

	msg["content"] = content
	delete(msg, "tool_calls")
	choice["finish_reason"] = "stop"

	out, err := json.Marshal(parsed)
	if err != nil {
		return body, nil
	}
	return out, nil
}

// TransformStreamChunk intercepts ExitTool in streaming and converts to text content.
func (t *toolUseTransform) TransformStreamChunk(data []byte, ctx *TransformContext) ([][]byte, error) {
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

	// Check for finish_reason while ExitTool is active.
	if fr, ok := choice["finish_reason"].(string); ok && fr != "" && ctx.ExitToolIndex >= 0 {
		// Parse accumulated args to extract response.
		content := ""
		var args map[string]interface{}
		if json.Unmarshal([]byte(ctx.ExitToolArgs.String()), &args) == nil {
			if r, ok := args["response"].(string); ok {
				content = r
			}
		}

		// Emit a content chunk with the response text.
		emitChunk := map[string]interface{}{
			"choices": []interface{}{
				map[string]interface{}{
					"delta": map[string]interface{}{
						"role":    "assistant",
						"content": content,
					},
					"finish_reason": "stop",
				},
			},
		}
		out, err := json.Marshal(emitChunk)
		if err != nil {
			return [][]byte{data}, nil
		}
		return [][]byte{out}, nil
	}

	delta, ok := choice["delta"].(map[string]interface{})
	if !ok {
		return [][]byte{data}, nil
	}

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

	// Check if this is a new tool call with ExitTool name.
	if fn, ok := tc["function"].(map[string]interface{}); ok {
		if name, ok := fn["name"].(string); ok && name == "ExitTool" {
			ctx.ExitToolIndex = idx
			// Suppress this chunk.
			return nil, nil
		}
	}

	// If we're tracking ExitTool and this chunk's index matches, accumulate args.
	if ctx.ExitToolIndex >= 0 && idx == ctx.ExitToolIndex {
		if fn, ok := tc["function"].(map[string]interface{}); ok {
			if args, ok := fn["arguments"].(string); ok {
				ctx.ExitToolArgs.WriteString(args)
			}
		}
		// Suppress.
		return nil, nil
	}

	return [][]byte{data}, nil
}

func init() {
	RegisterTransform("tooluse", func() Transformer {
		return &toolUseTransform{}
	})
}
