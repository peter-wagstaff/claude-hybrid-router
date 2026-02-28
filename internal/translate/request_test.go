package translate

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRequestBasic(t *testing.T) {
	input := `{
		"model": "claude-sonnet-4-20250514",
		"system": "You are helpful",
		"messages": [{"role": "user", "content": "hello"}],
		"max_tokens": 1024,
		"temperature": 0.7
	}`

	out, err := RequestToOpenAI([]byte(input), "qwen3:32b", 0)
	if err != nil {
		t.Fatalf("RequestToOpenAI: %v", err)
	}

	var req ORequest
	json.Unmarshal(out, &req)

	if req.Model != "qwen3:32b" {
		t.Errorf("expected qwen3:32b, got %s", req.Model)
	}
	if len(req.Messages) != 2 {
		t.Fatalf("expected 2 messages (system + user), got %d", len(req.Messages))
	}
	if req.Messages[0].Role != "system" || req.Messages[0].Content != "You are helpful" {
		t.Errorf("unexpected system message: %+v", req.Messages[0])
	}
	if req.Messages[1].Role != "user" || req.Messages[1].Content != "hello" {
		t.Errorf("unexpected user message: %+v", req.Messages[1])
	}
	if req.MaxTokens != 1024 {
		t.Errorf("unexpected max_completion_tokens: %d", req.MaxTokens)
	}
	if req.Temperature == nil || *req.Temperature != 0.7 {
		t.Errorf("unexpected temperature")
	}
}

func TestRequestSystemArray(t *testing.T) {
	input := `{
		"model": "x",
		"system": [{"type": "text", "text": "First"}, {"type": "text", "text": "Second"}],
		"messages": [{"role": "user", "content": "hi"}]
	}`

	out, err := RequestToOpenAI([]byte(input), "model", 0)
	if err != nil {
		t.Fatalf("RequestToOpenAI: %v", err)
	}

	var req ORequest
	json.Unmarshal(out, &req)

	if req.Messages[0].Content != "First\nSecond" {
		t.Errorf("system text not joined: %q", req.Messages[0].Content)
	}
}

func TestRequestToolDefinitions(t *testing.T) {
	input := `{
		"model": "x",
		"messages": [{"role": "user", "content": "hi"}],
		"tools": [{
			"name": "get_weather",
			"description": "Get weather",
			"input_schema": {"type": "object", "properties": {"city": {"type": "string"}}}
		}]
	}`

	out, err := RequestToOpenAI([]byte(input), "model", 0)
	if err != nil {
		t.Fatalf("RequestToOpenAI: %v", err)
	}

	var req ORequest
	json.Unmarshal(out, &req)

	if len(req.Tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(req.Tools))
	}
	if req.Tools[0].Type != "function" {
		t.Errorf("expected type function")
	}
	if req.Tools[0].Function.Name != "get_weather" {
		t.Errorf("unexpected name: %s", req.Tools[0].Function.Name)
	}
	if req.Tools[0].Function.Description != "Get weather" {
		t.Errorf("unexpected description")
	}
}

func TestRequestToolUseInAssistant(t *testing.T) {
	input := `{
		"model": "x",
		"messages": [
			{"role": "user", "content": "what's the weather?"},
			{"role": "assistant", "content": [
				{"type": "text", "text": "Let me check."},
				{"type": "tool_use", "id": "toolu_123", "name": "get_weather", "input": {"city": "SF"}}
			]},
			{"role": "user", "content": [
				{"type": "tool_result", "tool_use_id": "toolu_123", "content": "72°F sunny"}
			]}
		]
	}`

	out, err := RequestToOpenAI([]byte(input), "model", 0)
	if err != nil {
		t.Fatalf("RequestToOpenAI: %v", err)
	}

	var req ORequest
	json.Unmarshal(out, &req)

	// user, assistant (with tool_calls), tool
	if len(req.Messages) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(req.Messages))
	}

	assistant := req.Messages[1]
	if assistant.Role != "assistant" {
		t.Errorf("expected assistant, got %s", assistant.Role)
	}
	if assistant.Content != "Let me check." {
		t.Errorf("unexpected content: %q", assistant.Content)
	}
	if len(assistant.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(assistant.ToolCalls))
	}
	tc := assistant.ToolCalls[0]
	if tc.ID != "toolu_123" || tc.Function.Name != "get_weather" {
		t.Errorf("unexpected tool call: %+v", tc)
	}
	// Arguments should be a JSON string
	var args map[string]string
	if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
		t.Errorf("arguments not valid JSON: %v", err)
	}
	if args["city"] != "SF" {
		t.Errorf("unexpected args: %v", args)
	}

	toolMsg := req.Messages[2]
	if toolMsg.Role != "tool" || toolMsg.ToolCallID != "toolu_123" {
		t.Errorf("unexpected tool message: %+v", toolMsg)
	}
	if toolMsg.Content != "72°F sunny" {
		t.Errorf("unexpected tool content: %q", toolMsg.Content)
	}
}

func TestRequestToolResultContentArray(t *testing.T) {
	input := `{
		"model": "x",
		"messages": [
			{"role": "user", "content": [
				{"type": "tool_result", "tool_use_id": "t1", "content": [
					{"type": "text", "text": "line 1"},
					{"type": "text", "text": "line 2"}
				]}
			]}
		]
	}`

	out, err := RequestToOpenAI([]byte(input), "model", 0)
	if err != nil {
		t.Fatalf("RequestToOpenAI: %v", err)
	}

	var req ORequest
	json.Unmarshal(out, &req)

	if req.Messages[0].Content != "line 1\nline 2" {
		t.Errorf("unexpected tool result content: %q", req.Messages[0].Content)
	}
}

func TestRequestToolChoiceAuto(t *testing.T) {
	input := `{"model":"x","messages":[{"role":"user","content":"hi"}],"tool_choice":{"type":"auto"}}`
	out, _ := RequestToOpenAI([]byte(input), "m", 0)
	var m map[string]interface{}
	json.Unmarshal(out, &m)
	if m["tool_choice"] != "auto" {
		t.Errorf("expected auto, got %v", m["tool_choice"])
	}
}

func TestRequestToolChoiceAny(t *testing.T) {
	input := `{"model":"x","messages":[{"role":"user","content":"hi"}],"tool_choice":{"type":"any"}}`
	out, _ := RequestToOpenAI([]byte(input), "m", 0)
	var m map[string]interface{}
	json.Unmarshal(out, &m)
	if m["tool_choice"] != "required" {
		t.Errorf("expected required, got %v", m["tool_choice"])
	}
}

func TestRequestToolChoiceSpecific(t *testing.T) {
	input := `{"model":"x","messages":[{"role":"user","content":"hi"}],"tool_choice":{"type":"tool","name":"get_weather"}}`
	out, _ := RequestToOpenAI([]byte(input), "m", 0)
	var m map[string]interface{}
	json.Unmarshal(out, &m)
	tc := m["tool_choice"].(map[string]interface{})
	if tc["type"] != "function" {
		t.Errorf("expected function type")
	}
	fn := tc["function"].(map[string]interface{})
	if fn["name"] != "get_weather" {
		t.Errorf("expected get_weather")
	}
}

func TestRequestStopSequences(t *testing.T) {
	input := `{"model":"x","messages":[{"role":"user","content":"hi"}],"stop_sequences":["END","STOP"]}`
	out, _ := RequestToOpenAI([]byte(input), "m", 0)
	var req ORequest
	json.Unmarshal(out, &req)
	if len(req.Stop) != 2 || req.Stop[0] != "END" {
		t.Errorf("unexpected stop: %v", req.Stop)
	}
}

func TestRequestStreaming(t *testing.T) {
	input := `{"model":"x","messages":[{"role":"user","content":"hi"}],"stream":true}`
	out, _ := RequestToOpenAI([]byte(input), "m", 0)
	var m map[string]interface{}
	json.Unmarshal(out, &m)
	if m["stream"] != true {
		t.Error("expected stream true")
	}
	so, ok := m["stream_options"].(map[string]interface{})
	if !ok {
		t.Fatal("missing stream_options")
	}
	if so["include_usage"] != true {
		t.Error("expected include_usage true")
	}
}

func TestRequestMultipleToolResults(t *testing.T) {
	// Multiple tool_result blocks in one user message → separate tool messages
	input := `{
		"model": "x",
		"messages": [
			{"role": "user", "content": [
				{"type": "tool_result", "tool_use_id": "t1", "content": "result 1"},
				{"type": "tool_result", "tool_use_id": "t2", "content": "result 2"}
			]}
		]
	}`

	out, err := RequestToOpenAI([]byte(input), "model", 0)
	if err != nil {
		t.Fatalf("RequestToOpenAI: %v", err)
	}

	var req ORequest
	json.Unmarshal(out, &req)

	if len(req.Messages) != 2 {
		t.Fatalf("expected 2 tool messages, got %d", len(req.Messages))
	}
	if req.Messages[0].Role != "tool" || req.Messages[0].ToolCallID != "t1" {
		t.Errorf("unexpected first tool message: %+v", req.Messages[0])
	}
	if req.Messages[1].Role != "tool" || req.Messages[1].ToolCallID != "t2" {
		t.Errorf("unexpected second tool message: %+v", req.Messages[1])
	}
}

func TestRequestToolSchemaStripping(t *testing.T) {
	// Schema stripping is now handled by the transform chain, not RequestToOpenAI.
	// This test verifies that RequestToOpenAI + schema:generic chain strips schemas correctly.
	input := `{
		"model": "x",
		"messages": [{"role": "user", "content": "hi"}],
		"tools": [{
			"name": "Read",
			"description": "Read a file",
			"input_schema": {
				"type": "object",
				"additionalProperties": false,
				"strict": true,
				"$schema": "http://json-schema.org/draft-07/schema#",
				"properties": {
					"file_path": {
						"type": "string",
						"additionalProperties": false
					},
					"options": {
						"type": "object",
						"additionalProperties": false,
						"properties": {
							"encoding": {"type": "string"}
						}
					}
				},
				"required": ["file_path"]
			}
		}]
	}`

	out, err := RequestToOpenAI([]byte(input), "model", 0)
	if err != nil {
		t.Fatalf("RequestToOpenAI: %v", err)
	}

	// Run through schema:generic chain
	chain, _ := BuildChain([]string{"schema:generic"})
	ctx := NewTransformContext("model", "provider")
	var oaiReq map[string]interface{}
	json.Unmarshal(out, &oaiReq)
	chain.RunRequest(oaiReq, ctx)
	out, _ = json.Marshal(oaiReq)

	var req ORequest
	json.Unmarshal(out, &req)

	params := string(req.Tools[0].Function.Parameters)

	// Should be stripped at all levels
	if strings.Contains(params, "additionalProperties") {
		t.Error("additionalProperties not stripped from schema")
	}
	if strings.Contains(params, "strict") {
		t.Error("strict not stripped from schema")
	}
	if strings.Contains(params, "$schema") {
		t.Error("$schema not stripped from schema")
	}

	// Should preserve required fields
	if !strings.Contains(params, "required") {
		t.Error("required field incorrectly stripped")
	}
	if !strings.Contains(params, "file_path") {
		t.Error("properties incorrectly stripped")
	}
}

func TestRequestToolSchemaArrayItems(t *testing.T) {
	input := `{
		"model": "x",
		"messages": [{"role": "user", "content": "hi"}],
		"tools": [{
			"name": "test",
			"description": "test",
			"input_schema": {
				"type": "object",
				"properties": {
					"items": {
						"type": "array",
						"items": {
							"type": "object",
							"additionalProperties": false,
							"properties": {"name": {"type": "string"}}
						}
					}
				}
			}
		}]
	}`

	out, _ := RequestToOpenAI([]byte(input), "model", 0)

	// Run through schema:generic chain
	chain, _ := BuildChain([]string{"schema:generic"})
	ctx := NewTransformContext("model", "provider")
	var oaiReq map[string]interface{}
	json.Unmarshal(out, &oaiReq)
	chain.RunRequest(oaiReq, ctx)
	out, _ = json.Marshal(oaiReq)

	if strings.Contains(string(out), "additionalProperties") {
		t.Error("additionalProperties not stripped from nested array items")
	}
}
