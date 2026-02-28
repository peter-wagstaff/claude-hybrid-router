package translate

import (
	"encoding/json"
	"testing"
)

func TestEnhancetoolResponse_MalformedArgs(t *testing.T) {
	tr := newEnhancetoolTransform()
	ctx := NewTransformContext("gpt-4", "openai")

	body := mustJSON(map[string]interface{}{
		"choices": []interface{}{
			map[string]interface{}{
				"message": map[string]interface{}{
					"role": "assistant",
					"tool_calls": []interface{}{
						map[string]interface{}{
							"id":   "call_1",
							"type": "function",
							"function": map[string]interface{}{
								"name":      "read_file",
								"arguments": `{"path": "/tmp/test.txt",}`,
							},
						},
					},
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
	toolCalls := msg["tool_calls"].([]interface{})
	tc := toolCalls[0].(map[string]interface{})
	fn := tc["function"].(map[string]interface{})
	args := fn["arguments"].(string)

	// Should be valid JSON now (trailing comma removed)
	if !json.Valid([]byte(args)) {
		t.Errorf("expected valid JSON after repair, got: %s", args)
	}

	var argsParsed map[string]interface{}
	if err := json.Unmarshal([]byte(args), &argsParsed); err != nil {
		t.Fatalf("arguments should be parseable: %v", err)
	}
	if argsParsed["path"] != "/tmp/test.txt" {
		t.Errorf("path = %q, want %q", argsParsed["path"], "/tmp/test.txt")
	}
}

func TestEnhancetoolResponse_ValidArgs(t *testing.T) {
	tr := newEnhancetoolTransform()
	ctx := NewTransformContext("gpt-4", "openai")

	validArgs := `{"path":"/tmp/test.txt"}`
	body := mustJSON(map[string]interface{}{
		"choices": []interface{}{
			map[string]interface{}{
				"message": map[string]interface{}{
					"role": "assistant",
					"tool_calls": []interface{}{
						map[string]interface{}{
							"id":   "call_1",
							"type": "function",
							"function": map[string]interface{}{
								"name":      "read_file",
								"arguments": validArgs,
							},
						},
					},
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
	toolCalls := msg["tool_calls"].([]interface{})
	tc := toolCalls[0].(map[string]interface{})
	fn := tc["function"].(map[string]interface{})
	args := fn["arguments"].(string)

	if args != validArgs {
		t.Errorf("expected unchanged args %q, got %q", validArgs, args)
	}
}

func TestEnhancetoolStream_AccumulateAndRepair(t *testing.T) {
	tr := newEnhancetoolTransform()
	ctx := NewTransformContext("gpt-4", "openai")

	// Chunk 1: tool call start with id and name
	startChunk := mustJSON(map[string]interface{}{
		"choices": []interface{}{
			map[string]interface{}{
				"delta": map[string]interface{}{
					"tool_calls": []interface{}{
						map[string]interface{}{
							"index": 0,
							"id":    "call_1",
							"function": map[string]interface{}{
								"name":      "read_file",
								"arguments": "",
							},
						},
					},
				},
			},
		},
	})

	results, err := tr.TransformStreamChunk(startChunk, ctx)
	if err != nil {
		t.Fatalf("start chunk error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 chunk from start, got %d", len(results))
	}

	// Chunk 2: argument fragment — should be suppressed
	argChunk := mustJSON(map[string]interface{}{
		"choices": []interface{}{
			map[string]interface{}{
				"delta": map[string]interface{}{
					"tool_calls": []interface{}{
						map[string]interface{}{
							"index": 0,
							"function": map[string]interface{}{
								"arguments": `{"path": "/tmp/test.txt",}`,
							},
						},
					},
				},
			},
		},
	})

	results, err = tr.TransformStreamChunk(argChunk, ctx)
	if err != nil {
		t.Fatalf("arg chunk error: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected 0 chunks (suppressed), got %d", len(results))
	}

	// Chunk 3: finish_reason = "tool_calls" — should emit repaired tool calls
	finishChunk := mustJSON(map[string]interface{}{
		"choices": []interface{}{
			map[string]interface{}{
				"finish_reason": "tool_calls",
				"delta":         map[string]interface{}{},
			},
		},
	})

	results, err = tr.TransformStreamChunk(finishChunk, ctx)
	if err != nil {
		t.Fatalf("finish chunk error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 chunks (repaired + finish), got %d", len(results))
	}

	// First result should be the repaired tool calls chunk
	var repaired map[string]interface{}
	if err := json.Unmarshal(results[0], &repaired); err != nil {
		t.Fatalf("unmarshal repaired chunk: %v", err)
	}
	choices := repaired["choices"].([]interface{})
	delta := choices[0].(map[string]interface{})["delta"].(map[string]interface{})
	toolCalls := delta["tool_calls"].([]interface{})
	tc := toolCalls[0].(map[string]interface{})
	fn := tc["function"].(map[string]interface{})
	args := fn["arguments"].(string)

	if !json.Valid([]byte(args)) {
		t.Errorf("expected valid JSON after repair, got: %s", args)
	}

	var argsParsed map[string]interface{}
	if err := json.Unmarshal([]byte(args), &argsParsed); err != nil {
		t.Fatalf("repaired arguments not parseable: %v", err)
	}
	if argsParsed["path"] != "/tmp/test.txt" {
		t.Errorf("path = %q, want %q", argsParsed["path"], "/tmp/test.txt")
	}

	// Second result should be the finish chunk
	var finish map[string]interface{}
	if err := json.Unmarshal(results[1], &finish); err != nil {
		t.Fatalf("unmarshal finish chunk: %v", err)
	}
	finishChoices := finish["choices"].([]interface{})
	finishChoice := finishChoices[0].(map[string]interface{})
	if finishChoice["finish_reason"] != "tool_calls" {
		t.Errorf("expected finish_reason=tool_calls, got %v", finishChoice["finish_reason"])
	}
}

func TestEnhancetoolStream_ValidArgsPassthrough(t *testing.T) {
	tr := newEnhancetoolTransform()
	ctx := NewTransformContext("gpt-4", "openai")

	// Start chunk
	startChunk := mustJSON(map[string]interface{}{
		"choices": []interface{}{
			map[string]interface{}{
				"delta": map[string]interface{}{
					"tool_calls": []interface{}{
						map[string]interface{}{
							"index": 0,
							"id":    "call_1",
							"function": map[string]interface{}{
								"name":      "read_file",
								"arguments": "",
							},
						},
					},
				},
			},
		},
	})

	results, err := tr.TransformStreamChunk(startChunk, ctx)
	if err != nil {
		t.Fatalf("start chunk error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 chunk from start, got %d", len(results))
	}

	// Arg fragment with valid JSON
	argChunk := mustJSON(map[string]interface{}{
		"choices": []interface{}{
			map[string]interface{}{
				"delta": map[string]interface{}{
					"tool_calls": []interface{}{
						map[string]interface{}{
							"index": 0,
							"function": map[string]interface{}{
								"arguments": `{"path":"/tmp/test.txt"}`,
							},
						},
					},
				},
			},
		},
	})

	results, err = tr.TransformStreamChunk(argChunk, ctx)
	if err != nil {
		t.Fatalf("arg chunk error: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected 0 chunks (suppressed), got %d", len(results))
	}

	// Finish
	finishChunk := mustJSON(map[string]interface{}{
		"choices": []interface{}{
			map[string]interface{}{
				"finish_reason": "tool_calls",
				"delta":         map[string]interface{}{},
			},
		},
	})

	results, err = tr.TransformStreamChunk(finishChunk, ctx)
	if err != nil {
		t.Fatalf("finish chunk error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 chunks, got %d", len(results))
	}

	// Repaired chunk should have the valid args unchanged
	var repaired map[string]interface{}
	if err := json.Unmarshal(results[0], &repaired); err != nil {
		t.Fatalf("unmarshal repaired chunk: %v", err)
	}
	choices := repaired["choices"].([]interface{})
	delta := choices[0].(map[string]interface{})["delta"].(map[string]interface{})
	toolCalls := delta["tool_calls"].([]interface{})
	tc := toolCalls[0].(map[string]interface{})
	fn := tc["function"].(map[string]interface{})
	args := fn["arguments"].(string)

	if args != `{"path":"/tmp/test.txt"}` {
		t.Errorf("expected unchanged valid args, got: %s", args)
	}
}

func TestEnhancetoolStream_NoToolCalls(t *testing.T) {
	tr := newEnhancetoolTransform()
	ctx := NewTransformContext("gpt-4", "openai")

	chunk := mustJSON(map[string]interface{}{
		"choices": []interface{}{
			map[string]interface{}{
				"delta": map[string]interface{}{
					"content": "Hello world",
				},
			},
		},
	})

	results, err := tr.TransformStreamChunk(chunk, ctx)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 chunk passthrough, got %d", len(results))
	}

	// Should be unchanged
	var parsed map[string]interface{}
	if err := json.Unmarshal(results[0], &parsed); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	choices := parsed["choices"].([]interface{})
	delta := choices[0].(map[string]interface{})["delta"].(map[string]interface{})
	if delta["content"] != "Hello world" {
		t.Errorf("content = %q, want %q", delta["content"], "Hello world")
	}
}
