package translate

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestOpenRouterRequest_StripCacheControl(t *testing.T) {
	tr := newOpenRouterTransform()
	ctx := NewTransformContext("meta-llama/llama-3-70b", "openrouter")

	req := map[string]interface{}{
		"messages": []interface{}{
			map[string]interface{}{
				"role": "user",
				"content": []interface{}{
					map[string]interface{}{
						"type":          "text",
						"text":          "hello",
						"cache_control": map[string]interface{}{"type": "ephemeral"},
					},
				},
				"cache_control": map[string]interface{}{"type": "ephemeral"},
			},
			map[string]interface{}{
				"role":    "assistant",
				"content": "hi there",
			},
		},
	}

	if err := tr.TransformRequest(req, ctx); err != nil {
		t.Fatal(err)
	}

	b, _ := json.Marshal(req)
	raw := string(b)

	if strings.Contains(raw, "cache_control") {
		t.Errorf("cache_control should be stripped for non-Claude model, got: %s", raw)
	}
	// content text should still be there
	if !strings.Contains(raw, "hello") {
		t.Error("message content should be preserved")
	}
}

func TestOpenRouterRequest_KeepCacheControlForClaude(t *testing.T) {
	tr := newOpenRouterTransform()
	ctx := NewTransformContext("anthropic/claude-3.5-sonnet", "openrouter")

	req := map[string]interface{}{
		"messages": []interface{}{
			map[string]interface{}{
				"role": "user",
				"content": []interface{}{
					map[string]interface{}{
						"type":          "text",
						"text":          "hello",
						"cache_control": map[string]interface{}{"type": "ephemeral"},
					},
				},
			},
		},
	}

	if err := tr.TransformRequest(req, ctx); err != nil {
		t.Fatal(err)
	}

	b, _ := json.Marshal(req)
	raw := string(b)

	if !strings.Contains(raw, "cache_control") {
		t.Error("cache_control should be kept for Claude model")
	}
}

func TestOpenRouterStream_NumericToolID(t *testing.T) {
	chunk := `{"choices":[{"delta":{"tool_calls":[{"id":"0","type":"function","function":{"name":"test"}}]}}]}`

	tr := newOpenRouterTransform()
	ctx := NewTransformContext("some-model", "openrouter")

	results, err := tr.TransformStreamChunk([]byte(chunk), ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(results))
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(results[0], &parsed); err != nil {
		t.Fatal(err)
	}

	choices := parsed["choices"].([]interface{})
	delta := choices[0].(map[string]interface{})["delta"].(map[string]interface{})
	toolCalls := delta["tool_calls"].([]interface{})
	id := toolCalls[0].(map[string]interface{})["id"].(string)

	if !strings.HasPrefix(id, "call_") {
		t.Errorf("expected id to start with 'call_', got %q", id)
	}
	if len(id) <= 10 {
		t.Errorf("expected id length > 10, got %d", len(id))
	}
}

func TestOpenRouterStream_ReasoningFieldRename(t *testing.T) {
	chunk := `{"choices":[{"delta":{"reasoning":"thinking about it"}}]}`

	tr := newOpenRouterTransform()
	ctx := NewTransformContext("some-model", "openrouter")

	results, err := tr.TransformStreamChunk([]byte(chunk), ctx)
	if err != nil {
		t.Fatal(err)
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(results[0], &parsed); err != nil {
		t.Fatal(err)
	}

	choices := parsed["choices"].([]interface{})
	delta := choices[0].(map[string]interface{})["delta"].(map[string]interface{})

	if _, ok := delta["reasoning"]; ok {
		t.Error("reasoning field should be renamed, not present")
	}
	if v, ok := delta["reasoning_content"].(string); !ok || v != "thinking about it" {
		t.Errorf("expected reasoning_content='thinking about it', got %v", delta["reasoning_content"])
	}
}

func TestOpenRouterStream_NonNumericToolIDUnchanged(t *testing.T) {
	chunk := `{"choices":[{"delta":{"tool_calls":[{"id":"call_abc123","type":"function"}]}}]}`

	tr := newOpenRouterTransform()
	ctx := NewTransformContext("some-model", "openrouter")

	results, err := tr.TransformStreamChunk([]byte(chunk), ctx)
	if err != nil {
		t.Fatal(err)
	}

	var parsed map[string]interface{}
	json.Unmarshal(results[0], &parsed)

	choices := parsed["choices"].([]interface{})
	delta := choices[0].(map[string]interface{})["delta"].(map[string]interface{})
	toolCalls := delta["tool_calls"].([]interface{})
	id := toolCalls[0].(map[string]interface{})["id"].(string)

	if id != "call_abc123" {
		t.Errorf("non-numeric id should be unchanged, got %q", id)
	}
}

func TestOpenRouterResponse_NumericToolID(t *testing.T) {
	body := `{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"42","type":"function","function":{"name":"test","arguments":"{}"}}]}}]}`

	tr := newOpenRouterTransform()
	ctx := NewTransformContext("some-model", "openrouter")

	result, err := tr.TransformResponse([]byte(body), ctx)
	if err != nil {
		t.Fatal(err)
	}

	var parsed map[string]interface{}
	json.Unmarshal(result, &parsed)

	choices := parsed["choices"].([]interface{})
	msg := choices[0].(map[string]interface{})["message"].(map[string]interface{})
	toolCalls := msg["tool_calls"].([]interface{})
	id := toolCalls[0].(map[string]interface{})["id"].(string)

	if !strings.HasPrefix(id, "call_") {
		t.Errorf("expected id to start with 'call_', got %q", id)
	}
	if len(id) <= 10 {
		t.Errorf("expected id length > 10, got %d", len(id))
	}
}

func TestOpenRouterResponse_ReasoningRename(t *testing.T) {
	body := `{"choices":[{"message":{"role":"assistant","content":"answer","reasoning":"thought process"}}]}`

	tr := newOpenRouterTransform()
	ctx := NewTransformContext("some-model", "openrouter")

	result, err := tr.TransformResponse([]byte(body), ctx)
	if err != nil {
		t.Fatal(err)
	}

	var parsed map[string]interface{}
	json.Unmarshal(result, &parsed)

	choices := parsed["choices"].([]interface{})
	msg := choices[0].(map[string]interface{})["message"].(map[string]interface{})

	if _, ok := msg["reasoning"]; ok {
		t.Error("reasoning should be renamed")
	}
	if v, ok := msg["reasoning_content"].(string); !ok || v != "thought process" {
		t.Errorf("expected reasoning_content='thought process', got %v", msg["reasoning_content"])
	}
}

func TestOpenRouterStream_IndexBump(t *testing.T) {
	chunk := `{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0}]}}]}`

	tr := newOpenRouterTransform()
	ctx := NewTransformContext("some-model", "openrouter")
	ctx.HasTextContent = true

	results, err := tr.TransformStreamChunk([]byte(chunk), ctx)
	if err != nil {
		t.Fatal(err)
	}

	var parsed map[string]interface{}
	json.Unmarshal(results[0], &parsed)

	choices := parsed["choices"].([]interface{})
	idx := choices[0].(map[string]interface{})["index"].(float64)

	if idx != 1 {
		t.Errorf("expected index bumped to 1, got %v", idx)
	}
}

func TestOpenRouterStream_FixFinishReasonOnUsageChunk(t *testing.T) {
	tr := newOpenRouterTransform()
	ctx := NewTransformContext("model", "openrouter")

	// First: a tool call chunk (so hasToolCall gets set)
	toolChunk := `{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"0","type":"function","function":{"name":"test"}}]}}]}`
	tr.TransformStreamChunk([]byte(toolChunk), ctx)

	// Usage chunk with wrong finish_reason "stop" instead of "tool_calls"
	usageChunk := `{"choices":[{"finish_reason":"stop","delta":{}}],"usage":{"prompt_tokens":10,"completion_tokens":5}}`
	results, err := tr.TransformStreamChunk([]byte(usageChunk), ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(results))
	}

	var parsed map[string]interface{}
	json.Unmarshal(results[0], &parsed)
	choices := parsed["choices"].([]interface{})
	fr := choices[0].(map[string]interface{})["finish_reason"].(string)
	if fr != "tool_calls" {
		t.Errorf("expected finish_reason corrected to 'tool_calls', got %q", fr)
	}
}

func TestOpenRouterStream_NoFinishReasonFixWithoutToolCalls(t *testing.T) {
	tr := newOpenRouterTransform()
	ctx := NewTransformContext("model", "openrouter")

	// Usage chunk with "stop" — no prior tool calls, should stay "stop"
	usageChunk := `{"choices":[{"finish_reason":"stop","delta":{}}],"usage":{"prompt_tokens":10,"completion_tokens":5}}`
	results, _ := tr.TransformStreamChunk([]byte(usageChunk), ctx)

	var parsed map[string]interface{}
	json.Unmarshal(results[0], &parsed)
	choices := parsed["choices"].([]interface{})
	fr := choices[0].(map[string]interface{})["finish_reason"].(string)
	if fr != "stop" {
		t.Errorf("expected finish_reason to stay 'stop', got %q", fr)
	}
}

func TestOpenRouterStream_NoIndexBumpWithoutTextContent(t *testing.T) {
	chunk := `{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0}]}}]}`

	tr := newOpenRouterTransform()
	ctx := NewTransformContext("some-model", "openrouter")
	ctx.HasTextContent = false

	results, err := tr.TransformStreamChunk([]byte(chunk), ctx)
	if err != nil {
		t.Fatal(err)
	}

	var parsed map[string]interface{}
	json.Unmarshal(results[0], &parsed)

	choices := parsed["choices"].([]interface{})
	idx := choices[0].(map[string]interface{})["index"].(float64)

	if idx != 0 {
		t.Errorf("expected index unchanged at 0, got %v", idx)
	}
}
