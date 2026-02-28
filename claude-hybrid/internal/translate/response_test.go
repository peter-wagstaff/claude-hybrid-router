package translate

import (
	"encoding/json"
	"testing"
)

func TestResponseBasicText(t *testing.T) {
	input := `{
		"id": "chatcmpl-abc123",
		"choices": [{
			"message": {"role": "assistant", "content": "Hello there!"},
			"finish_reason": "stop"
		}],
		"usage": {"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15},
		"model": "qwen3:32b"
	}`

	out, err := ResponseToAnthropic([]byte(input), "fast_coder")
	if err != nil {
		t.Fatalf("ResponseToAnthropic: %v", err)
	}

	var resp AResponse
	json.Unmarshal(out, &resp)

	if resp.ID != "msg_chatcmpl-abc123" {
		t.Errorf("unexpected id: %s", resp.ID)
	}
	if resp.Type != "message" {
		t.Errorf("unexpected type: %s", resp.Type)
	}
	if resp.Role != "assistant" {
		t.Errorf("unexpected role: %s", resp.Role)
	}
	if resp.Model != "fast_coder" {
		t.Errorf("expected model label 'fast_coder', got %s", resp.Model)
	}
	if len(resp.Content) != 1 || resp.Content[0].Type != "text" || resp.Content[0].Text != "Hello there!" {
		t.Errorf("unexpected content: %+v", resp.Content)
	}
	if *resp.StopReason != "end_turn" {
		t.Errorf("unexpected stop_reason: %s", *resp.StopReason)
	}
	if resp.Usage.InputTokens != 10 || resp.Usage.OutputTokens != 5 {
		t.Errorf("unexpected usage: %+v", resp.Usage)
	}
}

func TestResponseToolCalls(t *testing.T) {
	input := `{
		"id": "chatcmpl-xyz",
		"choices": [{
			"message": {
				"role": "assistant",
				"content": "I'll check.",
				"tool_calls": [{
					"id": "call_abc123",
					"type": "function",
					"function": {
						"name": "get_weather",
						"arguments": "{\"city\": \"SF\"}"
					}
				}]
			},
			"finish_reason": "tool_calls"
		}],
		"usage": {"prompt_tokens": 20, "completion_tokens": 10, "total_tokens": 30}
	}`

	out, err := ResponseToAnthropic([]byte(input), "my_model")
	if err != nil {
		t.Fatalf("ResponseToAnthropic: %v", err)
	}

	var resp AResponse
	json.Unmarshal(out, &resp)

	if len(resp.Content) != 2 {
		t.Fatalf("expected 2 content blocks (text + tool_use), got %d", len(resp.Content))
	}

	if resp.Content[0].Type != "text" || resp.Content[0].Text != "I'll check." {
		t.Errorf("unexpected text block: %+v", resp.Content[0])
	}

	tu := resp.Content[1]
	if tu.Type != "tool_use" {
		t.Errorf("expected tool_use, got %s", tu.Type)
	}
	if tu.ID != "call_abc123" {
		t.Errorf("unexpected tool id: %s", tu.ID)
	}
	if tu.Name != "get_weather" {
		t.Errorf("unexpected tool name: %s", tu.Name)
	}

	var args map[string]string
	json.Unmarshal(tu.Input, &args)
	if args["city"] != "SF" {
		t.Errorf("unexpected args: %v", args)
	}

	if *resp.StopReason != "tool_use" {
		t.Errorf("expected stop_reason tool_use, got %s", *resp.StopReason)
	}
}

func TestResponseToolIDSanitization(t *testing.T) {
	input := `{
		"id": "resp",
		"choices": [{
			"message": {
				"role": "assistant",
				"content": "",
				"tool_calls": [{
					"id": "func:call.123",
					"type": "function",
					"function": {"name": "test", "arguments": "{}"}
				}]
			},
			"finish_reason": "tool_calls"
		}]
	}`

	out, _ := ResponseToAnthropic([]byte(input), "m")
	var resp AResponse
	json.Unmarshal(out, &resp)

	// Content should only have tool_use (empty text should be omitted)
	if len(resp.Content) != 1 {
		t.Fatalf("expected 1 block (empty text omitted), got %d", len(resp.Content))
	}
	if resp.Content[0].ID != "func_call_123" {
		t.Errorf("expected sanitized ID, got %s", resp.Content[0].ID)
	}
}

func TestResponseFinishReasonMapping(t *testing.T) {
	tests := []struct {
		openai   string
		expected string
	}{
		{"stop", "end_turn"},
		{"tool_calls", "tool_use"},
		{"length", "max_tokens"},
		{"unknown", "end_turn"},
	}

	for _, tt := range tests {
		got := mapFinishReason(tt.openai)
		if got != tt.expected {
			t.Errorf("mapFinishReason(%q) = %q, want %q", tt.openai, got, tt.expected)
		}
	}
}

func TestResponseNoChoices(t *testing.T) {
	input := `{"id": "x", "choices": []}`
	_, err := ResponseToAnthropic([]byte(input), "m")
	if err == nil {
		t.Error("expected error for no choices")
	}
}

func TestFormatError(t *testing.T) {
	out := FormatError("api_error", "connection refused")
	var resp AErrorResponse
	json.Unmarshal(out, &resp)
	if resp.Type != "error" {
		t.Errorf("unexpected type: %s", resp.Type)
	}
	if resp.Error.Type != "api_error" {
		t.Errorf("unexpected error type: %s", resp.Error.Type)
	}
	if resp.Error.Message != "connection refused" {
		t.Errorf("unexpected message: %s", resp.Error.Message)
	}
}
