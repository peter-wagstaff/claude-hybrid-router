package translate

import (
	"encoding/json"
	"testing"
)

func mustJSON(v interface{}) []byte {
	b, _ := json.Marshal(v)
	return b
}

func TestReasoningStreamChunk_ReasoningContent(t *testing.T) {
	tr := newReasoningTransform()
	ctx := NewTransformContext("deepseek-r1", "ollama")

	chunk := mustJSON(map[string]interface{}{
		"choices": []interface{}{
			map[string]interface{}{
				"delta": map[string]interface{}{
					"reasoning_content": "Let me think...",
				},
			},
		},
	})

	results, err := tr.TransformStreamChunk(chunk, ctx)
	if err != nil {
		t.Fatalf("TransformStreamChunk error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(results))
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(results[0], &parsed); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	choices := parsed["choices"].([]interface{})
	delta := choices[0].(map[string]interface{})["delta"].(map[string]interface{})

	// Should have thinking, not reasoning_content
	if _, ok := delta["reasoning_content"]; ok {
		t.Error("reasoning_content should be removed from delta")
	}
	thinking, ok := delta["thinking"].(map[string]interface{})
	if !ok {
		t.Fatal("expected delta.thinking to be a map")
	}
	if thinking["content"] != "Let me think..." {
		t.Errorf("thinking.content = %q, want %q", thinking["content"], "Let me think...")
	}

	// Should accumulate in context
	if ctx.ReasoningContent.String() != "Let me think..." {
		t.Errorf("ctx.ReasoningContent = %q, want %q", ctx.ReasoningContent.String(), "Let me think...")
	}
}

func TestReasoningStreamChunk_Boundary(t *testing.T) {
	tr := newReasoningTransform()
	ctx := NewTransformContext("deepseek-r1", "ollama")

	// First: reasoning chunk to accumulate content
	reasoningChunk := mustJSON(map[string]interface{}{
		"choices": []interface{}{
			map[string]interface{}{
				"delta": map[string]interface{}{
					"reasoning_content": "step 1",
				},
			},
		},
	})
	_, err := tr.TransformStreamChunk(reasoningChunk, ctx)
	if err != nil {
		t.Fatalf("reasoning chunk error: %v", err)
	}

	// Second: content chunk — triggers boundary
	contentChunk := mustJSON(map[string]interface{}{
		"choices": []interface{}{
			map[string]interface{}{
				"index": 0,
				"delta": map[string]interface{}{
					"content": "Hello!",
				},
			},
		},
	})

	results, err := tr.TransformStreamChunk(contentChunk, ctx)
	if err != nil {
		t.Fatalf("content chunk error: %v", err)
	}

	if len(results) != 2 {
		t.Fatalf("expected 2 chunks at boundary, got %d", len(results))
	}

	// Chunk 1: thinking-close with signature
	var close map[string]interface{}
	if err := json.Unmarshal(results[0], &close); err != nil {
		t.Fatalf("unmarshal close chunk: %v", err)
	}
	closeChoices := close["choices"].([]interface{})
	closeDelta := closeChoices[0].(map[string]interface{})["delta"].(map[string]interface{})
	closeThinking, ok := closeDelta["thinking"].(map[string]interface{})
	if !ok {
		t.Fatal("expected thinking in close chunk")
	}
	if _, ok := closeThinking["signature"]; !ok {
		t.Error("expected signature in thinking-close chunk")
	}

	// Chunk 2: content with index incremented
	var content map[string]interface{}
	if err := json.Unmarshal(results[1], &content); err != nil {
		t.Fatalf("unmarshal content chunk: %v", err)
	}
	contentChoices := content["choices"].([]interface{})
	choice := contentChoices[0].(map[string]interface{})
	idx, ok := choice["index"].(float64)
	if !ok {
		t.Fatal("expected index in content chunk")
	}
	if idx != 1 {
		t.Errorf("content chunk index = %v, want 1", idx)
	}
	contentDelta := choice["delta"].(map[string]interface{})
	if contentDelta["content"] != "Hello!" {
		t.Errorf("content = %q, want %q", contentDelta["content"], "Hello!")
	}

	// Context flags
	if !ctx.ReasoningComplete {
		t.Error("expected ReasoningComplete = true")
	}
	if !ctx.HasTextContent {
		t.Error("expected HasTextContent = true")
	}
}

func TestReasoningStreamChunk_NoReasoning(t *testing.T) {
	tr := newReasoningTransform()
	ctx := NewTransformContext("gpt-4", "openai")

	chunk := mustJSON(map[string]interface{}{
		"choices": []interface{}{
			map[string]interface{}{
				"delta": map[string]interface{}{
					"content": "Just text",
				},
			},
		},
	})

	results, err := tr.TransformStreamChunk(chunk, ctx)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(results))
	}

	// Should be unchanged
	var parsed map[string]interface{}
	if err := json.Unmarshal(results[0], &parsed); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	choices := parsed["choices"].([]interface{})
	delta := choices[0].(map[string]interface{})["delta"].(map[string]interface{})
	if delta["content"] != "Just text" {
		t.Errorf("content = %q, want %q", delta["content"], "Just text")
	}

	if !ctx.HasTextContent {
		t.Error("expected HasTextContent = true for content chunk")
	}
}

func TestReasoningResponseNonStreaming(t *testing.T) {
	tr := newReasoningTransform()
	ctx := NewTransformContext("deepseek-r1", "ollama")

	body := mustJSON(map[string]interface{}{
		"choices": []interface{}{
			map[string]interface{}{
				"message": map[string]interface{}{
					"role":              "assistant",
					"content":           "The answer is 42.",
					"reasoning_content": "I need to calculate...",
				},
			},
		},
	})

	result, err := tr.TransformResponse(body, ctx)
	if err != nil {
		t.Fatalf("TransformResponse error: %v", err)
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(result, &parsed); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	choices := parsed["choices"].([]interface{})
	msg := choices[0].(map[string]interface{})["message"].(map[string]interface{})

	if _, ok := msg["reasoning_content"]; ok {
		t.Error("reasoning_content should be removed")
	}
	thinking, ok := msg["thinking"].(map[string]interface{})
	if !ok {
		t.Fatal("expected message.thinking to be a map")
	}
	if thinking["content"] != "I need to calculate..." {
		t.Errorf("thinking.content = %q, want %q", thinking["content"], "I need to calculate...")
	}
	if msg["content"] != "The answer is 42." {
		t.Errorf("content = %q, want %q", msg["content"], "The answer is 42.")
	}
}

func TestReasoningResponseNonStreaming_NoReasoning(t *testing.T) {
	tr := newReasoningTransform()
	ctx := NewTransformContext("gpt-4", "openai")

	body := mustJSON(map[string]interface{}{
		"choices": []interface{}{
			map[string]interface{}{
				"message": map[string]interface{}{
					"role":    "assistant",
					"content": "Hello!",
				},
			},
		},
	})

	result, err := tr.TransformResponse(body, ctx)
	if err != nil {
		t.Fatalf("TransformResponse error: %v", err)
	}

	// Should be unchanged
	if string(result) != string(body) {
		t.Errorf("expected body unchanged, got %s", string(result))
	}
}

func TestReasoningRequest_BudgetTokens(t *testing.T) {
	tr := newReasoningTransform()
	ctx := NewTransformContext("deepseek-r1", "ollama")

	req := map[string]interface{}{
		"model": "deepseek-r1",
		"reasoning": map[string]interface{}{
			"max_tokens": float64(8192),
		},
	}

	if err := tr.TransformRequest(req, ctx); err != nil {
		t.Fatalf("TransformRequest error: %v", err)
	}

	if _, ok := req["reasoning"]; ok {
		t.Error("reasoning should be removed from request")
	}

	thinking, ok := req["thinking"].(map[string]interface{})
	if !ok {
		t.Fatal("expected thinking to be set")
	}
	if thinking["type"] != "enabled" {
		t.Errorf("thinking.type = %q, want %q", thinking["type"], "enabled")
	}
	if thinking["budget_tokens"] != float64(8192) {
		t.Errorf("thinking.budget_tokens = %v, want 8192", thinking["budget_tokens"])
	}
}

func TestReasoningRequest_NoReasoning(t *testing.T) {
	tr := newReasoningTransform()
	ctx := NewTransformContext("gpt-4", "openai")

	req := map[string]interface{}{
		"model": "gpt-4",
	}

	if err := tr.TransformRequest(req, ctx); err != nil {
		t.Fatalf("TransformRequest error: %v", err)
	}

	if _, ok := req["thinking"]; ok {
		t.Error("thinking should not be set when no reasoning present")
	}
}
