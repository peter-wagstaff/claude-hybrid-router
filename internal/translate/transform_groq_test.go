package translate

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestGroqRequest_StripSchema(t *testing.T) {
	tr := newGroqTransform()
	ctx := NewTransformContext("model", "groq")

	req := map[string]interface{}{
		"tools": []interface{}{
			map[string]interface{}{
				"type": "function",
				"function": map[string]interface{}{
					"name": "test",
					"parameters": map[string]interface{}{
						"$schema": "http://json-schema.org/draft-07/schema#",
						"type":    "object",
					},
				},
			},
		},
	}

	if err := tr.TransformRequest(req, ctx); err != nil {
		t.Fatalf("TransformRequest error: %v", err)
	}

	// Check $schema stripped from tool parameters.
	tool := req["tools"].([]interface{})[0].(map[string]interface{})
	params := tool["function"].(map[string]interface{})["parameters"].(map[string]interface{})
	if _, ok := params["$schema"]; ok {
		t.Error("expected $schema stripped from tool parameters")
	}
	// type should remain.
	if params["type"] != "object" {
		t.Errorf("expected type=object, got %v", params["type"])
	}
}

func TestGroqStream_NumericToolID(t *testing.T) {
	tr := newGroqTransform()
	ctx := NewTransformContext("model", "groq")

	chunk := map[string]interface{}{
		"choices": []interface{}{
			map[string]interface{}{
				"index": float64(0),
				"delta": map[string]interface{}{
					"tool_calls": []interface{}{
						map[string]interface{}{
							"id":   "1",
							"type": "function",
						},
					},
				},
			},
		},
	}
	data, _ := json.Marshal(chunk)

	results, err := tr.TransformStreamChunk(data, ctx)
	if err != nil {
		t.Fatalf("TransformStreamChunk error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}

	var out map[string]interface{}
	if err := json.Unmarshal(results[0], &out); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	tc := out["choices"].([]interface{})[0].(map[string]interface{})["delta"].(map[string]interface{})["tool_calls"].([]interface{})[0].(map[string]interface{})
	id := tc["id"].(string)
	if !strings.HasPrefix(id, "call_") {
		t.Errorf("expected id to start with call_, got %q", id)
	}
	if id == "1" {
		t.Error("expected numeric id to be replaced")
	}
	// call_ + 24 hex chars
	if len(id) != 5+24 {
		t.Errorf("expected id length 29, got %d (%q)", len(id), id)
	}
}

func TestGroqRequest_NoCacheControlNoTools(t *testing.T) {
	tr := newGroqTransform()
	ctx := NewTransformContext("model", "groq")

	req := map[string]interface{}{
		"messages": []interface{}{
			map[string]interface{}{
				"role":    "user",
				"content": "hello",
			},
		},
		"model": "test",
	}

	// Take a snapshot.
	before, _ := json.Marshal(req)

	if err := tr.TransformRequest(req, ctx); err != nil {
		t.Fatalf("TransformRequest error: %v", err)
	}

	after, _ := json.Marshal(req)
	if string(before) != string(after) {
		t.Errorf("expected unchanged request:\nbefore: %s\nafter:  %s", before, after)
	}
}

func TestGroqStream_NonNumericIDUnchanged(t *testing.T) {
	tr := newGroqTransform()
	ctx := NewTransformContext("model", "groq")

	chunk := map[string]interface{}{
		"choices": []interface{}{
			map[string]interface{}{
				"index": float64(0),
				"delta": map[string]interface{}{
					"tool_calls": []interface{}{
						map[string]interface{}{
							"id":   "call_xyz",
							"type": "function",
						},
					},
				},
			},
		},
	}
	data, _ := json.Marshal(chunk)

	results, err := tr.TransformStreamChunk(data, ctx)
	if err != nil {
		t.Fatalf("TransformStreamChunk error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}

	var out map[string]interface{}
	if err := json.Unmarshal(results[0], &out); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	tc := out["choices"].([]interface{})[0].(map[string]interface{})["delta"].(map[string]interface{})["tool_calls"].([]interface{})[0].(map[string]interface{})
	if tc["id"] != "call_xyz" {
		t.Errorf("expected id unchanged, got %q", tc["id"])
	}
}
