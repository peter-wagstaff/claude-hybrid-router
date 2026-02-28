package translate

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func makeSSE(chunks ...string) string {
	var sb strings.Builder
	for _, c := range chunks {
		sb.WriteString("data: " + c + "\n\n")
	}
	sb.WriteString("data: [DONE]\n\n")
	return sb.String()
}

func chunk(id string, content *string, finishReason *string) string {
	c := OStreamChunk{
		ID: id,
		Choices: []OStreamChoice{{
			Delta:        OStreamDelta{Content: content},
			FinishReason: finishReason,
		}},
	}
	b, _ := json.Marshal(c)
	return string(b)
}

func strPtr(s string) *string { return &s }

func TestStreamTextOnly(t *testing.T) {
	input := makeSSE(
		chunk("resp1", strPtr("Hello"), nil),
		chunk("resp1", strPtr(" world"), nil),
		chunk("resp1", nil, strPtr("stop")),
	)

	var buf bytes.Buffer
	st := NewStreamTranslator("my_model")
	err := st.TranslateStream(strings.NewReader(input), &buf)
	if err != nil {
		t.Fatalf("TranslateStream: %v", err)
	}

	output := buf.String()

	// Check all required events exist
	for _, event := range []string{
		"event: message_start",
		"event: content_block_start",
		"event: content_block_delta",
		"event: content_block_stop",
		"event: message_delta",
		"event: message_stop",
	} {
		if !strings.Contains(output, event) {
			t.Errorf("missing event: %s", event)
		}
	}

	// Check text deltas contain our content
	if !strings.Contains(output, "Hello") {
		t.Error("missing 'Hello' in output")
	}
	if !strings.Contains(output, " world") {
		t.Error("missing ' world' in output")
	}

	// Check model label in message_start
	if !strings.Contains(output, `"model":"my_model"`) {
		t.Error("missing model label in message_start")
	}

	// Check stop_reason is end_turn
	if !strings.Contains(output, `"stop_reason":"end_turn"`) {
		t.Error("missing stop_reason end_turn in message_delta")
	}
}

func TestStreamToolCall(t *testing.T) {
	// First chunk: text
	c1 := chunk("resp1", strPtr("Let me check."), nil)

	// Tool call start
	tc1 := OStreamChunk{
		ID: "resp1",
		Choices: []OStreamChoice{{
			Delta: OStreamDelta{
				ToolCalls: []OStreamToolCall{{
					Index: 0,
					ID:    "call_abc",
					Type:  "function",
					Function: OStreamFuncDelta{
						Name:      "get_weather",
						Arguments: "",
					},
				}},
			},
		}},
	}
	b1, _ := json.Marshal(tc1)

	// Tool call argument fragments
	tc2 := OStreamChunk{
		ID: "resp1",
		Choices: []OStreamChoice{{
			Delta: OStreamDelta{
				ToolCalls: []OStreamToolCall{{
					Index:    0,
					Function: OStreamFuncDelta{Arguments: `{"city":`},
				}},
			},
		}},
	}
	b2, _ := json.Marshal(tc2)

	tc3 := OStreamChunk{
		ID: "resp1",
		Choices: []OStreamChoice{{
			Delta: OStreamDelta{
				ToolCalls: []OStreamToolCall{{
					Index:    0,
					Function: OStreamFuncDelta{Arguments: `"SF"}`},
				}},
			},
		}},
	}
	b3, _ := json.Marshal(tc3)

	// Finish
	c5 := chunk("resp1", nil, strPtr("tool_calls"))

	input := makeSSE(c1, string(b1), string(b2), string(b3), c5)

	var buf bytes.Buffer
	st := NewStreamTranslator("test_model")
	err := st.TranslateStream(strings.NewReader(input), &buf)
	if err != nil {
		t.Fatalf("TranslateStream: %v", err)
	}

	output := buf.String()

	// Should have text block start, text delta, text block stop
	if strings.Count(output, "event: content_block_start") != 2 {
		t.Errorf("expected 2 content_block_start events (text + tool_use), got %d",
			strings.Count(output, "event: content_block_start"))
	}

	// Check tool_use block
	if !strings.Contains(output, `"type":"tool_use"`) {
		t.Error("missing tool_use content block")
	}
	if !strings.Contains(output, `"name":"get_weather"`) {
		t.Error("missing tool name")
	}
	if !strings.Contains(output, `"id":"call_abc"`) {
		t.Error("missing tool call id")
	}

	// Check input_json_delta events
	if !strings.Contains(output, `"type":"input_json_delta"`) {
		t.Error("missing input_json_delta")
	}

	// Check stop_reason is tool_use
	if !strings.Contains(output, `"stop_reason":"tool_use"`) {
		t.Error("missing stop_reason tool_use")
	}
}

func TestStreamToolIDSanitized(t *testing.T) {
	tc := OStreamChunk{
		ID: "resp1",
		Choices: []OStreamChoice{{
			Delta: OStreamDelta{
				ToolCalls: []OStreamToolCall{{
					Index: 0,
					ID:    "func:call.123",
					Type:  "function",
					Function: OStreamFuncDelta{
						Name:      "test",
						Arguments: "{}",
					},
				}},
			},
		}},
	}
	b, _ := json.Marshal(tc)

	input := makeSSE(string(b), chunk("resp1", nil, strPtr("tool_calls")))

	var buf bytes.Buffer
	st := NewStreamTranslator("m")
	st.TranslateStream(strings.NewReader(input), &buf)

	if !strings.Contains(buf.String(), `"id":"func_call_123"`) {
		t.Error("tool ID not sanitized in stream")
	}
}

func TestStreamEmptyContent(t *testing.T) {
	// Some backends send empty string content deltas — should be ignored
	input := makeSSE(
		chunk("resp1", strPtr(""), nil),
		chunk("resp1", strPtr("Hi"), nil),
		chunk("resp1", nil, strPtr("stop")),
	)

	var buf bytes.Buffer
	st := NewStreamTranslator("m")
	st.TranslateStream(strings.NewReader(input), &buf)

	output := buf.String()
	// Should only have 1 content_block_start (the empty delta shouldn't start a block)
	if strings.Count(output, "event: content_block_start") != 1 {
		t.Errorf("expected 1 content_block_start, got %d",
			strings.Count(output, "event: content_block_start"))
	}
}

func TestStreamUsage(t *testing.T) {
	// Usage chunk (from stream_options include_usage)
	usageChunk := OStreamChunk{
		ID:      "resp1",
		Choices: []OStreamChoice{},
		Usage:   &OUsage{PromptTokens: 42, CompletionTokens: 10, TotalTokens: 52},
	}
	b, _ := json.Marshal(usageChunk)

	input := makeSSE(
		chunk("resp1", strPtr("Hi"), nil),
		chunk("resp1", nil, strPtr("stop")),
		string(b),
	)

	var buf bytes.Buffer
	st := NewStreamTranslator("m")
	st.TranslateStream(strings.NewReader(input), &buf)

	output := buf.String()
	if !strings.Contains(output, `"output_tokens":10`) {
		t.Error("missing output_tokens in message_delta")
	}
}

func TestStreamMessageID(t *testing.T) {
	input := makeSSE(
		chunk("chatcmpl-abc", strPtr("Hi"), nil),
		chunk("chatcmpl-abc", nil, strPtr("stop")),
	)

	var buf bytes.Buffer
	st := NewStreamTranslator("m")
	st.TranslateStream(strings.NewReader(input), &buf)

	if !strings.Contains(buf.String(), `"id":"msg_chatcmpl-abc"`) {
		t.Error("message ID not prefixed with msg_")
	}
}

func TestStreamConsecutiveDropAbort(t *testing.T) {
	// 3 unparseable SSE lines without [DONE] — should trigger consecutive drop abort
	input := "data: {bad\n\ndata: {bad\n\ndata: {bad\n\n"

	var buf bytes.Buffer
	st := NewStreamTranslator("m")
	err := st.TranslateStream(strings.NewReader(input), &buf)

	if err == nil {
		t.Fatal("expected error from consecutive unparseable chunks")
	}
	if !strings.Contains(err.Error(), "consecutive") {
		t.Errorf("error = %q, want to contain 'consecutive'", err.Error())
	}
}

func TestStreamTransformBadOutputAbort(t *testing.T) {
	// Valid JSON input chunks, but a transform that returns unparseable output chunks.
	// This exercises the consecutive drop path at lines 139-143 in stream.go.
	input := makeSSE(
		chunk("resp1", strPtr("Hello"), nil),
	)

	// Transform returns 3 unparseable chunks from a single input chunk
	badOutputTransform := &mockTransformer{
		name: "badoutput",
		streamChunkFn: func(data []byte, ctx *TransformContext) ([][]byte, error) {
			return [][]byte{
				[]byte("{bad"),
				[]byte("{bad"),
				[]byte("{bad"),
			}, nil
		},
	}

	chain := NewTransformChain(badOutputTransform)
	ctx := NewTransformContext("m", "p")

	var buf bytes.Buffer
	st := NewStreamTranslator("m")
	st.SetTransformChain(chain, ctx)
	err := st.TranslateStream(strings.NewReader(input), &buf)

	if err == nil {
		t.Fatal("expected error from consecutive unparseable transformed chunks")
	}
	if !strings.Contains(err.Error(), "consecutive") {
		t.Errorf("error = %q, want to contain 'consecutive'", err.Error())
	}
}
