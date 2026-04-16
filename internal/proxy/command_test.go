package proxy

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestExpandCommand(t *testing.T) {
	tests := []struct {
		name   string
		tmpl   string
		agent  string
		prompt string
		want   string
	}{
		{
			name:   "basic substitution",
			tmpl:   "myctl run --agent $AGENT '$PROMPT'",
			agent:  "simplifier",
			prompt: "review this code",
			want:   "myctl run --agent simplifier 'review this code'",
		},
		{
			name:   "prompt with single quotes",
			tmpl:   "run --agent $AGENT '$PROMPT'",
			agent:  "test",
			prompt: "it's a test",
			want:   `run --agent test 'it'"'"'s a test'`,
		},
		{
			name:   "no variables",
			tmpl:   "echo hello",
			agent:  "ignored",
			prompt: "ignored",
			want:   "echo hello",
		},
		{
			name:   "multiple agent refs",
			tmpl:   "$AGENT says $AGENT",
			agent:  "bot",
			prompt: "",
			want:   "bot says bot",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := expandCommand(tt.tmpl, tt.agent, tt.prompt)
			if got != tt.want {
				t.Errorf("expandCommand(%q, %q, %q) = %q, want %q", tt.tmpl, tt.agent, tt.prompt, got, tt.want)
			}
		})
	}
}

func TestStripSystemReminders(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "no reminders",
			input: "hello world",
			want:  "hello world",
		},
		{
			name:  "single reminder",
			input: "before<system-reminder>secret</system-reminder>after",
			want:  "beforeafter",
		},
		{
			name:  "multiple reminders",
			input: "a<system-reminder>x</system-reminder>b<system-reminder>y</system-reminder>c",
			want:  "abc",
		},
		{
			name:  "unclosed tag",
			input: "before<system-reminder>rest of text",
			want:  "before",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stripSystemReminders(tt.input)
			if got != tt.want {
				t.Errorf("stripSystemReminders(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestHasToolResult(t *testing.T) {
	withResult := `{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"output"}]}]}`
	withoutResult := `{"messages":[{"role":"user","content":"hello"}]}`

	if !hasToolResult([]byte(withResult)) {
		t.Error("expected hasToolResult=true for message with tool_result")
	}
	if hasToolResult([]byte(withoutResult)) {
		t.Error("expected hasToolResult=false for plain message")
	}
}

func TestExtractPrompt(t *testing.T) {
	body := `{"messages":[{"role":"user","content":"review this code"}]}`
	got := extractPrompt([]byte(body))
	if got != "review this code" {
		t.Errorf("extractPrompt = %q, want %q", got, "review this code")
	}

	// Content blocks format
	body = `{"messages":[{"role":"user","content":[{"type":"text","text":"hello world"}]}]}`
	got = extractPrompt([]byte(body))
	if got != "hello world" {
		t.Errorf("extractPrompt = %q, want %q", got, "hello world")
	}

	// With system-reminder stripped
	body = `{"messages":[{"role":"user","content":"<system-reminder>ignore</system-reminder>actual prompt"}]}`
	got = extractPrompt([]byte(body))
	if got != "actual prompt" {
		t.Errorf("extractPrompt = %q, want %q", got, "actual prompt")
	}
}

func TestExtractToolResult(t *testing.T) {
	// String content
	body := `{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"command output here"}]}]}`
	got := extractToolResult([]byte(body))
	if got != "command output here" {
		t.Errorf("extractToolResult = %q, want %q", got, "command output here")
	}

	// Array content blocks
	body = `{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":[{"type":"text","text":"line1"},{"type":"text","text":"line2"}]}]}]}`
	got = extractToolResult([]byte(body))
	if got != "line1\nline2" {
		t.Errorf("extractToolResult = %q, want %q", got, "line1\\nline2")
	}

	// Last tool_result wins
	body = `{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"first"},{"type":"tool_result","tool_use_id":"t2","content":"second"}]}]}`
	got = extractToolResult([]byte(body))
	if got != "second" {
		t.Errorf("extractToolResult = %q, want %q", got, "second")
	}
}

func TestCommandBridgeTurn1JSON(t *testing.T) {
	var buf bytes.Buffer
	body := `{"model":"claude-3-5-sonnet-20241022","messages":[{"role":"user","content":"review this code"}],"stream":false}`

	p := &Proxy{}
	p.forwardCommand(&buf, "myctl run --agent $AGENT '$PROMPT'", "simplifier", "my_model", []byte(body), false)

	resp := buf.String()

	// Should contain HTTP header
	if !strings.Contains(resp, "HTTP/1.1 200 OK") {
		t.Error("expected HTTP 200 response")
	}
	if !strings.Contains(resp, "application/json") {
		t.Error("expected JSON content type")
	}

	// Parse the JSON body (after headers)
	parts := strings.SplitN(resp, "\r\n\r\n", 2)
	if len(parts) != 2 {
		t.Fatalf("expected header+body, got: %s", resp)
	}

	var msg map[string]interface{}
	if err := json.Unmarshal([]byte(parts[1]), &msg); err != nil {
		t.Fatalf("JSON parse: %v", err)
	}

	if msg["stop_reason"] != "tool_use" {
		t.Errorf("expected stop_reason=tool_use, got %v", msg["stop_reason"])
	}

	content := msg["content"].([]interface{})
	block := content[0].(map[string]interface{})
	if block["name"] != "Bash" {
		t.Errorf("expected tool name=Bash, got %v", block["name"])
	}

	input := block["input"].(map[string]interface{})
	cmd := input["command"].(string)
	if !strings.Contains(cmd, "simplifier") {
		t.Errorf("command should contain agent name, got: %s", cmd)
	}
	if !strings.Contains(cmd, "review this code") {
		t.Errorf("command should contain prompt, got: %s", cmd)
	}
}

func TestCommandBridgeTurn1SSE(t *testing.T) {
	var buf bytes.Buffer
	body := `{"model":"claude-3-5-sonnet-20241022","messages":[{"role":"user","content":"hello"}],"stream":true}`

	p := &Proxy{}
	p.forwardCommand(&buf, "echo $PROMPT", "agent", "my_model", []byte(body), true)

	resp := buf.String()

	if !strings.Contains(resp, "text/event-stream") {
		t.Error("expected SSE content type")
	}
	if !strings.Contains(resp, "event: message_start") {
		t.Error("expected message_start event")
	}
	if !strings.Contains(resp, "event: content_block_start") {
		t.Error("expected content_block_start event")
	}
	if !strings.Contains(resp, `"tool_use"`) {
		t.Error("expected tool_use in SSE events")
	}
	if !strings.Contains(resp, "event: message_stop") {
		t.Error("expected message_stop event")
	}
}

func TestCommandBridgeTurn2(t *testing.T) {
	var buf bytes.Buffer
	body := `{"model":"claude-3-5-sonnet-20241022","messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"command output here"}]}]}`

	p := &Proxy{}
	p.forwardCommand(&buf, "ignored", "agent", "my_model", []byte(body), false)

	resp := buf.String()
	parts := strings.SplitN(resp, "\r\n\r\n", 2)
	if len(parts) != 2 {
		t.Fatalf("expected header+body, got: %s", resp)
	}

	var msg map[string]interface{}
	if err := json.Unmarshal([]byte(parts[1]), &msg); err != nil {
		t.Fatalf("JSON parse: %v", err)
	}

	if msg["stop_reason"] != "end_turn" {
		t.Errorf("expected stop_reason=end_turn, got %v", msg["stop_reason"])
	}

	content := msg["content"].([]interface{})
	block := content[0].(map[string]interface{})
	if block["text"] != "command output here" {
		t.Errorf("expected tool result text, got %v", block["text"])
	}
}
