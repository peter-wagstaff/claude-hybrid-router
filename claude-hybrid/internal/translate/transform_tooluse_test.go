package translate

import (
	"encoding/json"
	"testing"
)

func TestToolUseRequest_InjectExitTool(t *testing.T) {
	req := map[string]interface{}{
		"model": "test",
		"tools": []interface{}{
			map[string]interface{}{
				"type": "function",
				"function": map[string]interface{}{
					"name":       "Read",
					"parameters": map[string]interface{}{"type": "object"},
				},
			},
		},
	}

	ctx := NewTransformContext("test", "test")
	tr := &toolUseTransform{}

	if err := tr.TransformRequest(req, ctx); err != nil {
		t.Fatalf("TransformRequest error: %v", err)
	}

	// Check tool_choice is "required".
	if req["tool_choice"] != "required" {
		t.Errorf("tool_choice = %v, want \"required\"", req["tool_choice"])
	}

	// Check ExitTool was appended.
	tools := req["tools"].([]interface{})
	if len(tools) != 2 {
		t.Fatalf("tools length = %d, want 2", len(tools))
	}

	last := tools[1].(map[string]interface{})
	fn := last["function"].(map[string]interface{})
	if fn["name"] != "ExitTool" {
		t.Errorf("last tool name = %v, want ExitTool", fn["name"])
	}
	if fn["description"] == nil || fn["description"] == "" {
		t.Error("ExitTool should have a description")
	}
}

func TestToolUseRequest_NoTools(t *testing.T) {
	req := map[string]interface{}{
		"model": "test",
	}

	ctx := NewTransformContext("test", "test")
	tr := &toolUseTransform{}

	if err := tr.TransformRequest(req, ctx); err != nil {
		t.Fatalf("TransformRequest error: %v", err)
	}

	// No changes expected.
	if _, ok := req["tool_choice"]; ok {
		t.Error("tool_choice should not be set when no tools present")
	}
	if _, ok := req["tools"]; ok {
		t.Error("tools should not be set when not originally present")
	}
}

func TestToolUseResponse_InterceptExitTool(t *testing.T) {
	resp := map[string]interface{}{
		"id": "chatcmpl-123",
		"choices": []interface{}{
			map[string]interface{}{
				"finish_reason": "tool_calls",
				"message": map[string]interface{}{
					"role": "assistant",
					"tool_calls": []interface{}{
						map[string]interface{}{
							"id":   "call_1",
							"type": "function",
							"function": map[string]interface{}{
								"name":      "ExitTool",
								"arguments": `{"response":"Hello, world!"}`,
							},
						},
					},
				},
			},
		},
	}

	body, _ := json.Marshal(resp)
	ctx := NewTransformContext("test", "test")
	tr := &toolUseTransform{}

	out, err := tr.TransformResponse(body, ctx)
	if err != nil {
		t.Fatalf("TransformResponse error: %v", err)
	}

	var result map[string]interface{}
	json.Unmarshal(out, &result)

	choices := result["choices"].([]interface{})
	choice := choices[0].(map[string]interface{})
	msg := choice["message"].(map[string]interface{})

	// Content should be the ExitTool response.
	if msg["content"] != "Hello, world!" {
		t.Errorf("content = %v, want \"Hello, world!\"", msg["content"])
	}

	// tool_calls should be removed.
	if _, ok := msg["tool_calls"]; ok {
		t.Error("tool_calls should be deleted")
	}

	// finish_reason should be "stop".
	if choice["finish_reason"] != "stop" {
		t.Errorf("finish_reason = %v, want \"stop\"", choice["finish_reason"])
	}
}

func TestToolUseResponse_NormalToolCall(t *testing.T) {
	resp := map[string]interface{}{
		"id": "chatcmpl-123",
		"choices": []interface{}{
			map[string]interface{}{
				"finish_reason": "tool_calls",
				"message": map[string]interface{}{
					"role": "assistant",
					"tool_calls": []interface{}{
						map[string]interface{}{
							"id":   "call_1",
							"type": "function",
							"function": map[string]interface{}{
								"name":      "Read",
								"arguments": `{"path":"/tmp/foo"}`,
							},
						},
					},
				},
			},
		},
	}

	body, _ := json.Marshal(resp)
	ctx := NewTransformContext("test", "test")
	tr := &toolUseTransform{}

	out, err := tr.TransformResponse(body, ctx)
	if err != nil {
		t.Fatalf("TransformResponse error: %v", err)
	}

	var result map[string]interface{}
	json.Unmarshal(out, &result)

	choices := result["choices"].([]interface{})
	choice := choices[0].(map[string]interface{})
	msg := choice["message"].(map[string]interface{})

	// tool_calls should remain.
	if _, ok := msg["tool_calls"]; !ok {
		t.Error("tool_calls should remain for non-ExitTool calls")
	}

	// finish_reason should remain "tool_calls".
	if choice["finish_reason"] != "tool_calls" {
		t.Errorf("finish_reason = %v, want \"tool_calls\"", choice["finish_reason"])
	}
}

func TestToolUseStream_InterceptExitTool(t *testing.T) {
	ctx := NewTransformContext("test", "test")
	tr := &toolUseTransform{}

	// Chunk 1: ExitTool name — should be suppressed.
	chunk1 := map[string]interface{}{
		"choices": []interface{}{
			map[string]interface{}{
				"delta": map[string]interface{}{
					"tool_calls": []interface{}{
						map[string]interface{}{
							"index": 0.0,
							"id":    "call_1",
							"type":  "function",
							"function": map[string]interface{}{
								"name": "ExitTool",
							},
						},
					},
				},
			},
		},
	}
	data1, _ := json.Marshal(chunk1)
	out1, err := tr.TransformStreamChunk(data1, ctx)
	if err != nil {
		t.Fatalf("chunk1 error: %v", err)
	}
	if len(out1) != 0 {
		t.Errorf("chunk1: expected suppression (0 chunks), got %d", len(out1))
	}
	if ctx.ExitToolIndex != 0 {
		t.Errorf("ExitToolIndex = %d, want 0", ctx.ExitToolIndex)
	}

	// Chunk 2: argument fragment — should be suppressed.
	chunk2 := map[string]interface{}{
		"choices": []interface{}{
			map[string]interface{}{
				"delta": map[string]interface{}{
					"tool_calls": []interface{}{
						map[string]interface{}{
							"index": 0.0,
							"function": map[string]interface{}{
								"arguments": `{"respo`,
							},
						},
					},
				},
			},
		},
	}
	data2, _ := json.Marshal(chunk2)
	out2, err := tr.TransformStreamChunk(data2, ctx)
	if err != nil {
		t.Fatalf("chunk2 error: %v", err)
	}
	if len(out2) != 0 {
		t.Errorf("chunk2: expected suppression, got %d chunks", len(out2))
	}

	// Chunk 3: more argument fragments.
	chunk3 := map[string]interface{}{
		"choices": []interface{}{
			map[string]interface{}{
				"delta": map[string]interface{}{
					"tool_calls": []interface{}{
						map[string]interface{}{
							"index": 0.0,
							"function": map[string]interface{}{
								"arguments": `nse":"Hi there"}`,
							},
						},
					},
				},
			},
		},
	}
	data3, _ := json.Marshal(chunk3)
	out3, err := tr.TransformStreamChunk(data3, ctx)
	if err != nil {
		t.Fatalf("chunk3 error: %v", err)
	}
	if len(out3) != 0 {
		t.Errorf("chunk3: expected suppression, got %d chunks", len(out3))
	}

	// Chunk 4: finish_reason — should emit content chunk.
	chunk4 := map[string]interface{}{
		"choices": []interface{}{
			map[string]interface{}{
				"delta":         map[string]interface{}{},
				"finish_reason": "tool_calls",
			},
		},
	}
	data4, _ := json.Marshal(chunk4)
	out4, err := tr.TransformStreamChunk(data4, ctx)
	if err != nil {
		t.Fatalf("chunk4 error: %v", err)
	}
	if len(out4) != 1 {
		t.Fatalf("chunk4: expected 1 chunk, got %d", len(out4))
	}

	var result map[string]interface{}
	json.Unmarshal(out4[0], &result)

	choices := result["choices"].([]interface{})
	choice := choices[0].(map[string]interface{})
	delta := choice["delta"].(map[string]interface{})

	if delta["content"] != "Hi there" {
		t.Errorf("content = %v, want \"Hi there\"", delta["content"])
	}
	if delta["role"] != "assistant" {
		t.Errorf("role = %v, want \"assistant\"", delta["role"])
	}
	if choice["finish_reason"] != "stop" {
		t.Errorf("finish_reason = %v, want \"stop\"", choice["finish_reason"])
	}
}
