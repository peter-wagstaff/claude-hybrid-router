package translate

import (
	"encoding/json"
	"fmt"
	"time"
)

// reasoningTransform converts reasoning_content (DeepSeek R1, Qwen QwQ, etc.)
// into Anthropic-style thinking blocks.
type reasoningTransform struct{}

func newReasoningTransform() *reasoningTransform {
	return &reasoningTransform{}
}

func (r *reasoningTransform) Name() string { return "reasoning" }

// TransformRequest maps reasoning.max_tokens → thinking.budget_tokens.
func (r *reasoningTransform) TransformRequest(req map[string]interface{}, ctx *TransformContext) error {
	reasoning, ok := req["reasoning"].(map[string]interface{})
	if !ok {
		return nil
	}
	maxTokens, ok := reasoning["max_tokens"]
	if !ok {
		return nil
	}
	req["thinking"] = map[string]interface{}{
		"type":          "enabled",
		"budget_tokens": maxTokens,
	}
	delete(req, "reasoning")
	return nil
}

// TransformResponse moves reasoning_content from message to thinking in non-streaming responses.
func (r *reasoningTransform) TransformResponse(body []byte, ctx *TransformContext) ([]byte, error) {
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
	rc, ok := msg["reasoning_content"].(string)
	if !ok {
		return body, nil
	}

	msg["thinking"] = map[string]interface{}{
		"content": rc,
	}
	delete(msg, "reasoning_content")

	out, err := json.Marshal(parsed)
	if err != nil {
		return body, nil
	}
	return out, nil
}

// TransformStreamChunk rewrites reasoning_content deltas to thinking deltas
// and emits a thinking-close chunk at the reasoning→content boundary.
func (r *reasoningTransform) TransformStreamChunk(data []byte, ctx *TransformContext) ([][]byte, error) {
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

	// Case 1: reasoning_content present — rewrite to thinking delta.
	if rc, ok := delta["reasoning_content"].(string); ok {
		delta["thinking"] = map[string]interface{}{
			"content": rc,
		}
		delete(delta, "reasoning_content")
		ctx.ReasoningContent.WriteString(rc)

		out, err := json.Marshal(parsed)
		if err != nil {
			return nil, fmt.Errorf("marshal reasoning chunk: %w", err)
		}
		return [][]byte{out}, nil
	}

	// Case 2: content present AND reasoning was accumulated AND not yet closed.
	if _, hasContent := delta["content"]; hasContent {
		if ctx.ReasoningContent.Len() > 0 && !ctx.ReasoningComplete {
			ctx.ReasoningComplete = true
			ctx.HasTextContent = true

			// Thinking-close chunk with timestamp signature.
			closeChunk, err := json.Marshal(map[string]interface{}{
				"choices": []interface{}{
					map[string]interface{}{
						"delta": map[string]interface{}{
							"thinking": map[string]interface{}{
								"signature": fmt.Sprintf("<%d>", time.Now().UnixMilli()),
							},
						},
					},
				},
			})
			if err != nil {
				return nil, fmt.Errorf("marshal thinking-close: %w", err)
			}

			// Increment index on the content chunk.
			if idx, ok := choice["index"].(float64); ok {
				choice["index"] = idx + 1
			}
			contentChunk, err := json.Marshal(parsed)
			if err != nil {
				return nil, fmt.Errorf("marshal content chunk: %w", err)
			}

			return [][]byte{closeChunk, contentChunk}, nil
		}

		// Case 3: content present, no prior reasoning — pass through.
		ctx.HasTextContent = true
		return [][]byte{data}, nil
	}

	// Otherwise — pass through.
	return [][]byte{data}, nil
}

func init() {
	RegisterTransform("reasoning", func() Transformer {
		return newReasoningTransform()
	})
}
