package translate

import (
	"encoding/json"
	"testing"
)

func TestThinkTagResponse_Extract(t *testing.T) {
	tr := newThinkTagTransform()
	ctx := NewTransformContext("qwen3", "ollama")

	body := mustJSON(map[string]interface{}{
		"choices": []interface{}{
			map[string]interface{}{
				"message": map[string]interface{}{
					"role":    "assistant",
					"content": "<think>reasoning</think>answer",
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
	if thinking["content"] != "reasoning" {
		t.Errorf("thinking.content = %q, want %q", thinking["content"], "reasoning")
	}
	if msg["content"] != "answer" {
		t.Errorf("content = %q, want %q", msg["content"], "answer")
	}
}

func TestThinkTagResponse_NoThinkTag(t *testing.T) {
	tr := newThinkTagTransform()
	ctx := NewTransformContext("qwen3", "ollama")

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

func TestThinkTagStream_FullTagInOneChunk(t *testing.T) {
	tr := newThinkTagTransform()
	ctx := NewTransformContext("qwen3", "ollama")

	chunk := mustJSON(map[string]interface{}{
		"choices": []interface{}{
			map[string]interface{}{
				"index": 0,
				"delta": map[string]interface{}{
					"content": "<think>reasoning</think>answer",
				},
			},
		},
	})

	results, err := tr.TransformStreamChunk(chunk, ctx)
	if err != nil {
		t.Fatalf("TransformStreamChunk error: %v", err)
	}

	// Expect: thinking chunk, thinking-close chunk, content chunk
	if len(results) < 2 {
		t.Fatalf("expected at least 2 chunks, got %d", len(results))
	}

	// Find thinking content
	var foundThinking, foundContent bool
	for _, r := range results {
		var parsed map[string]interface{}
		if err := json.Unmarshal(r, &parsed); err != nil {
			t.Fatalf("unmarshal error: %v", err)
		}
		choices := parsed["choices"].([]interface{})
		delta := choices[0].(map[string]interface{})["delta"].(map[string]interface{})

		if th, ok := delta["thinking"].(map[string]interface{}); ok {
			if th["content"] == "reasoning" {
				foundThinking = true
			}
		}
		if c, ok := delta["content"].(string); ok && c == "answer" {
			foundContent = true
		}
	}

	if !foundThinking {
		t.Error("expected a thinking chunk with content 'reasoning'")
	}
	if !foundContent {
		t.Error("expected a content chunk with 'answer'")
	}
}

func TestThinkTagStream_SplitAcrossChunks(t *testing.T) {
	tr := newThinkTagTransform()
	ctx := NewTransformContext("qwen3", "ollama")

	// Chunk 1: opening tag
	chunk1 := mustJSON(map[string]interface{}{
		"choices": []interface{}{
			map[string]interface{}{
				"index": 0,
				"delta": map[string]interface{}{
					"content": "<think>",
				},
			},
		},
	})
	results1, err := tr.TransformStreamChunk(chunk1, ctx)
	if err != nil {
		t.Fatalf("chunk1 error: %v", err)
	}
	// Opening tag alone may produce empty or a thinking chunk with empty content
	_ = results1

	// Chunk 2: thinking content
	chunk2 := mustJSON(map[string]interface{}{
		"choices": []interface{}{
			map[string]interface{}{
				"index": 0,
				"delta": map[string]interface{}{
					"content": "step by step",
				},
			},
		},
	})
	results2, err := tr.TransformStreamChunk(chunk2, ctx)
	if err != nil {
		t.Fatalf("chunk2 error: %v", err)
	}

	// Should emit as thinking content
	if len(results2) != 1 {
		t.Fatalf("expected 1 chunk from chunk2, got %d", len(results2))
	}
	var parsed2 map[string]interface{}
	if err := json.Unmarshal(results2[0], &parsed2); err != nil {
		t.Fatalf("unmarshal chunk2 result: %v", err)
	}
	choices2 := parsed2["choices"].([]interface{})
	delta2 := choices2[0].(map[string]interface{})["delta"].(map[string]interface{})
	th2, ok := delta2["thinking"].(map[string]interface{})
	if !ok {
		t.Fatal("expected thinking in chunk2 output")
	}
	if th2["content"] != "step by step" {
		t.Errorf("thinking.content = %q, want %q", th2["content"], "step by step")
	}

	// Chunk 3: closing tag + answer
	chunk3 := mustJSON(map[string]interface{}{
		"choices": []interface{}{
			map[string]interface{}{
				"index": 0,
				"delta": map[string]interface{}{
					"content": "</think>answer",
				},
			},
		},
	})
	results3, err := tr.TransformStreamChunk(chunk3, ctx)
	if err != nil {
		t.Fatalf("chunk3 error: %v", err)
	}

	// Should produce: thinking-close (with signature) + content chunk
	if len(results3) < 2 {
		t.Fatalf("expected at least 2 chunks from chunk3, got %d", len(results3))
	}

	// Check thinking-close has signature
	var foundSig, foundAnswer bool
	for _, r := range results3 {
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
		if c, ok := delta["content"].(string); ok && c == "answer" {
			foundAnswer = true
		}
	}

	if !foundSig {
		t.Error("expected thinking-close chunk with signature")
	}
	if !foundAnswer {
		t.Error("expected content chunk with 'answer'")
	}
	if !ctx.HasTextContent {
		t.Error("expected HasTextContent = true")
	}
}

func TestThinkTagStream_NoThinkTag(t *testing.T) {
	tr := newThinkTagTransform()
	ctx := NewTransformContext("qwen3", "ollama")

	chunk := mustJSON(map[string]interface{}{
		"choices": []interface{}{
			map[string]interface{}{
				"delta": map[string]interface{}{
					"content": "normal text",
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

	var parsed map[string]interface{}
	if err := json.Unmarshal(results[0], &parsed); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	choices := parsed["choices"].([]interface{})
	delta := choices[0].(map[string]interface{})["delta"].(map[string]interface{})
	if delta["content"] != "normal text" {
		t.Errorf("content = %q, want %q", delta["content"], "normal text")
	}
	if !ctx.HasTextContent {
		t.Error("expected HasTextContent = true")
	}
}

func TestThinkTagStream_HandleFinal(t *testing.T) {
	tr := newThinkTagTransform()
	ctx := NewTransformContext("qwen3", "ollama")

	// Chunk 1: <think> + reasoning
	chunk1 := mustJSON(map[string]interface{}{
		"choices": []interface{}{
			map[string]interface{}{
				"index": 0,
				"delta": map[string]interface{}{
					"content": "<think>reasoning",
				},
			},
		},
	})
	_, err := tr.TransformStreamChunk(chunk1, ctx)
	if err != nil {
		t.Fatalf("chunk1 error: %v", err)
	}

	// Chunk 2: </think> only
	chunk2 := mustJSON(map[string]interface{}{
		"choices": []interface{}{
			map[string]interface{}{
				"index": 0,
				"delta": map[string]interface{}{
					"content": "</think>",
				},
			},
		},
	})
	_, err = tr.TransformStreamChunk(chunk2, ctx)
	if err != nil {
		t.Fatalf("chunk2 error: %v", err)
	}

	// Chunk 3: final content (exercises handleFinal)
	chunk3 := mustJSON(map[string]interface{}{
		"choices": []interface{}{
			map[string]interface{}{
				"index": 0,
				"delta": map[string]interface{}{
					"content": "the answer",
				},
			},
		},
	})
	results3, err := tr.TransformStreamChunk(chunk3, ctx)
	if err != nil {
		t.Fatalf("chunk3 error: %v", err)
	}

	if len(results3) != 1 {
		t.Fatalf("expected 1 chunk from chunk3, got %d", len(results3))
	}
	var parsed map[string]interface{}
	json.Unmarshal(results3[0], &parsed)
	delta := parsed["choices"].([]interface{})[0].(map[string]interface{})["delta"].(map[string]interface{})
	if delta["content"] != "the answer" {
		t.Errorf("content = %q, want %q", delta["content"], "the answer")
	}
	if !ctx.HasTextContent {
		t.Error("expected HasTextContent = true after handleFinal")
	}
}

func TestThinkTagStream_PartialTag(t *testing.T) {
	tr := newThinkTagTransform()
	ctx := NewTransformContext("qwen3", "ollama")

	// Chunk 1: text + partial <think> tag
	chunk1 := mustJSON(map[string]interface{}{
		"choices": []interface{}{
			map[string]interface{}{
				"index": 0,
				"delta": map[string]interface{}{
					"content": "hello<thi",
				},
			},
		},
	})
	results1, err := tr.TransformStreamChunk(chunk1, ctx)
	if err != nil {
		t.Fatalf("chunk1 error: %v", err)
	}

	// Should emit "hello" as content (partial tag buffered)
	if len(results1) != 1 {
		t.Fatalf("expected 1 chunk from chunk1, got %d", len(results1))
	}
	var parsed1 map[string]interface{}
	json.Unmarshal(results1[0], &parsed1)
	delta1 := parsed1["choices"].([]interface{})[0].(map[string]interface{})["delta"].(map[string]interface{})
	if delta1["content"] != "hello" {
		t.Errorf("chunk1 content = %q, want %q", delta1["content"], "hello")
	}

	// Chunk 2: rest of tag + reasoning + close + answer
	chunk2 := mustJSON(map[string]interface{}{
		"choices": []interface{}{
			map[string]interface{}{
				"index": 0,
				"delta": map[string]interface{}{
					"content": "nk>reasoning</think>answer",
				},
			},
		},
	})
	results2, err := tr.TransformStreamChunk(chunk2, ctx)
	if err != nil {
		t.Fatalf("chunk2 error: %v", err)
	}

	var foundThinking, foundAnswer bool
	for _, r := range results2 {
		var parsed map[string]interface{}
		json.Unmarshal(r, &parsed)
		choices := parsed["choices"].([]interface{})
		delta := choices[0].(map[string]interface{})["delta"].(map[string]interface{})

		if th, ok := delta["thinking"].(map[string]interface{}); ok {
			if th["content"] == "reasoning" {
				foundThinking = true
			}
		}
		if c, ok := delta["content"].(string); ok && c == "answer" {
			foundAnswer = true
		}
	}
	if !foundThinking {
		t.Error("expected thinking chunk with 'reasoning'")
	}
	if !foundAnswer {
		t.Error("expected content chunk with 'answer'")
	}
}

func TestThinkTagStream_ThinkingOnly(t *testing.T) {
	tr := newThinkTagTransform()
	ctx := NewTransformContext("qwen3", "ollama")

	chunk := mustJSON(map[string]interface{}{
		"choices": []interface{}{
			map[string]interface{}{
				"index": 0,
				"delta": map[string]interface{}{
					"content": "<think>reasoning</think>",
				},
			},
		},
	})

	results, err := tr.TransformStreamChunk(chunk, ctx)
	if err != nil {
		t.Fatalf("error: %v", err)
	}

	// Should have thinking chunk(s) + thinking-close, but no content chunk
	var foundThinking, foundContent bool
	for _, r := range results {
		var parsed map[string]interface{}
		if err := json.Unmarshal(r, &parsed); err != nil {
			t.Fatalf("unmarshal error: %v", err)
		}
		choices := parsed["choices"].([]interface{})
		delta := choices[0].(map[string]interface{})["delta"].(map[string]interface{})

		if th, ok := delta["thinking"].(map[string]interface{}); ok {
			if th["content"] == "reasoning" {
				foundThinking = true
			}
		}
		if c, ok := delta["content"].(string); ok && c != "" {
			foundContent = true
		}
	}

	if !foundThinking {
		t.Error("expected thinking chunk with 'reasoning'")
	}
	if foundContent {
		t.Error("expected no content chunk when only thinking is present")
	}
}
