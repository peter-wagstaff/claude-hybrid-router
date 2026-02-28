package translate

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
)

// openRouterTransform handles OpenRouter quirks: numeric tool IDs,
// reasoning field naming, cache_control for non-Claude models, index bumping,
// and finish_reason correction on usage chunks.
type openRouterTransform struct {
	hasToolCall bool // tracks whether any tool call was seen in the stream
}

func newOpenRouterTransform() *openRouterTransform {
	return &openRouterTransform{}
}

func (o *openRouterTransform) Name() string { return "openrouter" }

func (o *openRouterTransform) TransformRequest(req map[string]interface{}, ctx *TransformContext) error {
	if strings.Contains(strings.ToLower(ctx.ModelName), "claude") {
		return nil
	}
	// Strip cache_control from messages for non-Claude models.
	msgs, ok := req["messages"].([]interface{})
	if !ok {
		return nil
	}
	for _, m := range msgs {
		msg, ok := m.(map[string]interface{})
		if !ok {
			continue
		}
		delete(msg, "cache_control")
		parts, ok := msg["content"].([]interface{})
		if !ok {
			continue
		}
		for _, p := range parts {
			part, ok := p.(map[string]interface{})
			if !ok {
				continue
			}
			delete(part, "cache_control")
		}
	}
	return nil
}

func (o *openRouterTransform) TransformResponse(body []byte, ctx *TransformContext) ([]byte, error) {
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

	changed := false

	// Fix numeric tool IDs.
	if toolCalls, ok := msg["tool_calls"].([]interface{}); ok {
		for _, tc := range toolCalls {
			tcMap, ok := tc.(map[string]interface{})
			if !ok {
				continue
			}
			if fixNumericToolID(tcMap) {
				changed = true
			}
		}
	}

	// Rename reasoning → reasoning_content.
	if v, ok := msg["reasoning"]; ok {
		msg["reasoning_content"] = v
		delete(msg, "reasoning")
		changed = true
	}

	if !changed {
		return body, nil
	}
	return json.Marshal(parsed)
}

func (o *openRouterTransform) TransformStreamChunk(data []byte, ctx *TransformContext) ([][]byte, error) {
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

	changed := false

	delta, _ := choice["delta"].(map[string]interface{})
	if delta != nil {
		// Fix numeric tool IDs in delta.tool_calls.
		if toolCalls, ok := delta["tool_calls"].([]interface{}); ok {
			o.hasToolCall = true
			for _, tc := range toolCalls {
				tcMap, ok := tc.(map[string]interface{})
				if !ok {
					continue
				}
				if fixNumericToolID(tcMap) {
					changed = true
				}
			}

			// Index bump when text content already seen.
			if ctx.HasTextContent {
				if idx, ok := choice["index"].(float64); ok {
					choice["index"] = idx + 1
					changed = true
				}
			}
		}

		// Rename reasoning → reasoning_content in delta.
		if v, ok := delta["reasoning"]; ok {
			delta["reasoning_content"] = v
			delete(delta, "reasoning")
			changed = true
		}
	}

	// Fix finish_reason on usage chunk. OpenRouter sometimes reports "stop"
	// on the usage chunk even when tool_calls happened.
	if _, hasUsage := parsed["usage"]; hasUsage {
		if fr, ok := choice["finish_reason"].(string); ok && o.hasToolCall && fr != "tool_calls" {
			choice["finish_reason"] = "tool_calls"
			changed = true
		}
	}

	if !changed {
		return [][]byte{data}, nil
	}
	out, err := json.Marshal(parsed)
	if err != nil {
		return [][]byte{data}, nil
	}
	return [][]byte{out}, nil
}

// fixNumericToolID checks if the "id" field is numeric and replaces it with a random call ID.
// Returns true if a change was made.
func fixNumericToolID(tc map[string]interface{}) bool {
	idVal, ok := tc["id"]
	if !ok {
		return false
	}
	idStr, ok := idVal.(string)
	if !ok {
		return false
	}
	if _, err := strconv.Atoi(idStr); err != nil {
		return false
	}
	tc["id"] = "call_" + randomHex(12)
	return true
}

// randomHex returns a hex string of 2*n characters from n random bytes.
func randomHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func init() {
	RegisterTransform("openrouter", func() Transformer {
		return newOpenRouterTransform()
	})
}
