package translate

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestForceReasoningRequest_InjectPrompt(t *testing.T) {
	tr := newForceReasoningTransform()
	ctx := NewTransformContext("gpt-4", "openai")

	req := map[string]interface{}{
		"model": "gpt-4",
		"messages": []interface{}{
			map[string]interface{}{"role": "system", "content": "You are helpful."},
			map[string]interface{}{"role": "user", "content": "What is 2+2?"},
			map[string]interface{}{"role": "assistant", "content": "4"},
			map[string]interface{}{"role": "user", "content": "Why?"},
		},
	}

	if err := tr.TransformRequest(req, ctx); err != nil {
		t.Fatalf("TransformRequest error: %v", err)
	}

	msgs := req["messages"].([]interface{})

	// Last user message (index 3) should have the prompt appended
	lastUser := msgs[3].(map[string]interface{})
	content := lastUser["content"].(string)
	if !strings.HasSuffix(content, reasoningPrompt) {
		t.Errorf("expected last user message to end with reasoning prompt, got %q", content)
	}
	if !strings.HasPrefix(content, "Why?") {
		t.Errorf("expected last user message to start with original content, got %q", content)
	}

	// Earlier user message (index 1) should be unchanged
	firstUser := msgs[1].(map[string]interface{})
	if firstUser["content"] != "What is 2+2?" {
		t.Errorf("earlier user message should be unchanged, got %q", firstUser["content"])
	}
}

func TestForceReasoningRequest_NoMessages(t *testing.T) {
	tr := newForceReasoningTransform()
	ctx := NewTransformContext("gpt-4", "openai")

	// Empty messages
	req := map[string]interface{}{
		"model":    "gpt-4",
		"messages": []interface{}{},
	}
	if err := tr.TransformRequest(req, ctx); err != nil {
		t.Fatalf("TransformRequest error on empty messages: %v", err)
	}

	// No messages key at all
	req2 := map[string]interface{}{
		"model": "gpt-4",
	}
	if err := tr.TransformRequest(req2, ctx); err != nil {
		t.Fatalf("TransformRequest error on missing messages: %v", err)
	}
}

func TestForceReasoningResponse_ExtractTags(t *testing.T) {
	tr := newForceReasoningTransform()
	ctx := NewTransformContext("gpt-4", "openai")

	body := mustJSON(map[string]interface{}{
		"choices": []interface{}{
			map[string]interface{}{
				"message": map[string]interface{}{
					"role":    "assistant",
					"content": "<reasoning_content>step 1\nstep 2</reasoning_content>The answer is 4.",
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

	thinking, ok := msg["thinking"].(map[string]interface{})
	if !ok {
		t.Fatal("expected message.thinking to be a map")
	}
	if thinking["content"] != "step 1\nstep 2" {
		t.Errorf("thinking.content = %q, want %q", thinking["content"], "step 1\nstep 2")
	}
	if msg["content"] != "The answer is 4." {
		t.Errorf("content = %q, want %q", msg["content"], "The answer is 4.")
	}
}

func TestForceReasoningResponse_NoTags(t *testing.T) {
	tr := newForceReasoningTransform()
	ctx := NewTransformContext("gpt-4", "openai")

	body := mustJSON(map[string]interface{}{
		"choices": []interface{}{
			map[string]interface{}{
				"message": map[string]interface{}{
					"role":    "assistant",
					"content": "just a normal answer",
				},
			},
		},
	})

	result, err := tr.TransformResponse(body, ctx)
	if err != nil {
		t.Fatalf("TransformResponse error: %v", err)
	}

	if string(result) != string(body) {
		t.Errorf("expected body unchanged, got %s", string(result))
	}
}

func TestForceReasoningStream_ExtractTags(t *testing.T) {
	tr := newForceReasoningTransform()
	ctx := NewTransformContext("gpt-4", "openai")

	// Chunk 1: opening tag + thinking content
	chunk1 := mustJSON(map[string]interface{}{
		"choices": []interface{}{
			map[string]interface{}{
				"index": 0,
				"delta": map[string]interface{}{
					"content": "<reasoning_content>step by step",
				},
			},
		},
	})
	results1, err := tr.TransformStreamChunk(chunk1, ctx)
	if err != nil {
		t.Fatalf("chunk1 error: %v", err)
	}

	// Should emit thinking content
	if len(results1) != 1 {
		t.Fatalf("expected 1 chunk from chunk1, got %d", len(results1))
	}
	var parsed1 map[string]interface{}
	if err := json.Unmarshal(results1[0], &parsed1); err != nil {
		t.Fatalf("unmarshal chunk1 result: %v", err)
	}
	delta1 := parsed1["choices"].([]interface{})[0].(map[string]interface{})["delta"].(map[string]interface{})
	th1, ok := delta1["thinking"].(map[string]interface{})
	if !ok {
		t.Fatal("expected thinking in chunk1 output")
	}
	if th1["content"] != "step by step" {
		t.Errorf("thinking.content = %q, want %q", th1["content"], "step by step")
	}

	// Chunk 2: closing tag + answer
	chunk2 := mustJSON(map[string]interface{}{
		"choices": []interface{}{
			map[string]interface{}{
				"index": 0,
				"delta": map[string]interface{}{
					"content": "</reasoning_content>The answer is 4.",
				},
			},
		},
	})
	results2, err := tr.TransformStreamChunk(chunk2, ctx)
	if err != nil {
		t.Fatalf("chunk2 error: %v", err)
	}

	// Should produce: thinking-close (with signature) + content chunk
	if len(results2) < 2 {
		t.Fatalf("expected at least 2 chunks from chunk2, got %d", len(results2))
	}

	var foundSig, foundAnswer bool
	for _, r := range results2 {
		var parsed map[string]interface{}
		if err := json.Unmarshal(r, &parsed); err != nil {
			t.Fatalf("unmarshal error: %v", err)
		}
		choices := parsed["choices"].([]interface{})
		delta := choices[0].(map[string]interface{})["delta"].(map[string]interface{})

		if th, ok := delta["thinking"].(map[string]interface{}); ok {
			if _, ok := th["signature"]; ok {
				foundSig = true
			}
		}
		if c, ok := delta["content"].(string); ok && c == "The answer is 4." {
			foundAnswer = true
		}
	}

	if !foundSig {
		t.Error("expected thinking-close chunk with signature")
	}
	if !foundAnswer {
		t.Error("expected content chunk with 'The answer is 4.'")
	}
	if !ctx.HasTextContent {
		t.Error("expected HasTextContent = true")
	}
}
