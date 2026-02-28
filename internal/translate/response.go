package translate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// OpenAI response types

// OResponse is an OpenAI Chat Completion response.
type OResponse struct {
	ID      string    `json:"id"`
	Choices []OChoice `json:"choices"`
	Usage   *OUsage   `json:"usage,omitempty"`
	Model   string    `json:"model"`
}

// OChoice is a choice in an OpenAI response.
type OChoice struct {
	Message      OMessage `json:"message"`
	FinishReason string   `json:"finish_reason"`
}

// OUsage is token usage from OpenAI.
type OUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// Anthropic response types

// AResponse is an Anthropic Messages response.
type AResponse struct {
	ID           string              `json:"id"`
	Type         string              `json:"type"`
	Role         string              `json:"role"`
	Content      []AResponseBlock    `json:"content"`
	Model        string              `json:"model"`
	StopReason   *string             `json:"stop_reason"`
	StopSequence *string             `json:"stop_sequence"`
	Usage        AUsage              `json:"usage"`
}

// AResponseBlock is a content block in an Anthropic response.
type AResponseBlock struct {
	Type  string          `json:"type"`
	Text  string          `json:"text,omitempty"`
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`
}

// AUsage is token usage in Anthropic format.
type AUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// AErrorResponse is an Anthropic error response.
type AErrorResponse struct {
	Type  string `json:"type"`
	Error AError `json:"error"`
}

// AError is the error detail.
type AError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

// toolIDClean removes characters not allowed in Anthropic tool IDs.
var toolIDClean = regexp.MustCompile(`[^a-zA-Z0-9_-]`)

func sanitizeToolID(id string) string {
	return toolIDClean.ReplaceAllString(id, "_")
}

// ResponseToAnthropic translates an OpenAI Chat Completion response to Anthropic Messages format.
// modelLabel is the user-facing label (not the backend model name).
func ResponseToAnthropic(body []byte, modelLabel string) ([]byte, error) {
	var oResp OResponse
	if err := json.Unmarshal(body, &oResp); err != nil {
		return nil, fmt.Errorf("parse openai response: %w", err)
	}

	if len(oResp.Choices) == 0 {
		return nil, fmt.Errorf("openai response has no choices")
	}

	choice := oResp.Choices[0]
	msg := choice.Message

	aResp := AResponse{
		ID:    "msg_" + oResp.ID,
		Type:  "message",
		Role:  "assistant",
		Model: modelLabel,
	}

	// Build content blocks
	if msg.Content != "" {
		aResp.Content = append(aResp.Content, AResponseBlock{
			Type: "text",
			Text: msg.Content,
		})
	}

	for _, tc := range msg.ToolCalls {
		var input json.RawMessage
		if tc.Function.Arguments != "" {
			// Parse the JSON string into an object
			if json.Valid([]byte(tc.Function.Arguments)) {
				input = json.RawMessage(tc.Function.Arguments)
			} else {
				input = json.RawMessage("{}")
			}
		} else {
			input = json.RawMessage("{}")
		}

		aResp.Content = append(aResp.Content, AResponseBlock{
			Type:  "tool_use",
			ID:    sanitizeToolID(tc.ID),
			Name:  tc.Function.Name,
			Input: input,
		})
	}

	// Stop reason
	stopReason := mapFinishReason(choice.FinishReason)
	aResp.StopReason = &stopReason

	// Usage
	if oResp.Usage != nil {
		aResp.Usage = AUsage{
			InputTokens:  oResp.Usage.PromptTokens,
			OutputTokens: oResp.Usage.CompletionTokens,
		}
	}

	return json.Marshal(aResp)
}

func mapFinishReason(fr string) string {
	switch fr {
	case "stop":
		return "end_turn"
	case "tool_calls":
		return "tool_use"
	case "length":
		return "max_tokens"
	default:
		return "end_turn"
	}
}

// ClassifyError categorizes an error for logging and user-facing messages.
func ClassifyError(err error) string {
	if err == nil {
		return "INTERNAL"
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "connection refused") ||
		strings.Contains(msg, "no such host") ||
		strings.Contains(msg, "dial tcp"):
		return "CONNECTION"
	case strings.Contains(msg, "deadline exceeded") ||
		strings.Contains(msg, "Client.Timeout") ||
		strings.Contains(msg, "context canceled"):
		return "TIMEOUT"
	default:
		return "INTERNAL"
	}
}

// FormatStreamError creates SSE events for a mid-stream error: an error event followed by message_stop.
func FormatStreamError(errType, message string) []byte {
	errData, _ := json.Marshal(AErrorResponse{
		Type:  "error",
		Error: AError{Type: errType, Message: message},
	})
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "event: error\ndata: %s\n\n", errData)
	stopData, _ := json.Marshal(map[string]string{"type": "message_stop"})
	fmt.Fprintf(&buf, "event: message_stop\ndata: %s\n\n", stopData)
	return buf.Bytes()
}

// FormatError creates an Anthropic-format error response body.
func FormatError(errType, message string) []byte {
	resp := AErrorResponse{
		Type: "error",
		Error: AError{
			Type:    errType,
			Message: message,
		},
	}
	out, _ := json.Marshal(resp)
	return out
}
