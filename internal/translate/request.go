// Package translate converts between Anthropic Messages API and OpenAI Chat Completions API formats.
package translate

import (
	"encoding/json"
	"fmt"
	"strings"
)

// AnthropicRequest represents the relevant fields of an Anthropic Messages API request.
type AnthropicRequest struct {
	Model         string          `json:"model"`
	System        json.RawMessage `json:"system,omitempty"` // string or []ContentBlock
	Messages      []AMessage      `json:"messages"`
	MaxTokens     int             `json:"max_tokens,omitempty"`
	Temperature   *float64        `json:"temperature,omitempty"`
	TopP          *float64        `json:"top_p,omitempty"`
	StopSequences []string        `json:"stop_sequences,omitempty"`
	Stream        bool            `json:"stream,omitempty"`
	Tools         []ATool         `json:"tools,omitempty"`
	ToolChoice    json.RawMessage `json:"tool_choice,omitempty"`
}

// AMessage is an Anthropic message.
type AMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"` // string or []ContentBlock
}

// ContentBlock is an Anthropic content block (text, tool_use, tool_result).
type ContentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`         // tool_use
	Name      string          `json:"name,omitempty"`       // tool_use
	Input     json.RawMessage `json:"input,omitempty"`      // tool_use
	ToolUseID string          `json:"tool_use_id,omitempty"` // tool_result
	Content   json.RawMessage `json:"content,omitempty"`    // tool_result (string or []ContentBlock)
	IsError   bool            `json:"is_error,omitempty"`   // tool_result
}

// ATool is an Anthropic tool definition.
type ATool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema"`
}

// OpenAI types

// ORequest is an OpenAI Chat Completions request.
type ORequest struct {
	Model       string      `json:"model"`
	Messages    []OMessage  `json:"messages"`
	MaxTokens   int         `json:"max_tokens,omitempty"`
	Temperature *float64    `json:"temperature,omitempty"`
	TopP        *float64    `json:"top_p,omitempty"`
	Stop        []string    `json:"stop,omitempty"`
	Stream      bool        `json:"stream,omitempty"`
	Tools       []OTool     `json:"tools,omitempty"`
	ToolChoice  interface{} `json:"tool_choice,omitempty"`
}

// OMessage is an OpenAI message.
type OMessage struct {
	Role       string      `json:"role"`
	Content    string      `json:"content,omitempty"`
	ToolCalls  []OToolCall `json:"tool_calls,omitempty"`  // assistant
	ToolCallID string      `json:"tool_call_id,omitempty"` // tool
}

// OToolCall is an OpenAI tool call in an assistant message.
type OToolCall struct {
	ID       string        `json:"id"`
	Type     string        `json:"type"`
	Function OFunctionCall `json:"function"`
}

// OFunctionCall holds function name and arguments (JSON string).
type OFunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// OTool is an OpenAI tool definition.
type OTool struct {
	Type     string    `json:"type"`
	Function OFunction `json:"function"`
}

// OFunction is an OpenAI function definition.
type OFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
}

// RequestToOpenAI translates an Anthropic Messages request body to OpenAI Chat Completions format.
// Schema cleaning is handled separately by the transform chain.
func RequestToOpenAI(body []byte, backendModel string, maxTokensCap int) ([]byte, error) {
	var req AnthropicRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, fmt.Errorf("parse anthropic request: %w", err)
	}

	maxTokens := req.MaxTokens
	if maxTokensCap > 0 && maxTokens > maxTokensCap {
		maxTokens = maxTokensCap
	}

	oReq := ORequest{
		Model:       backendModel,
		MaxTokens:   maxTokens,
		Temperature: req.Temperature,
		TopP:        req.TopP,
		Stop:        req.StopSequences,
		Stream:      req.Stream,
	}

	// System prompt
	systemText := extractSystemText(req.System)
	if systemText != "" {
		oReq.Messages = append(oReq.Messages, OMessage{Role: "system", Content: systemText})
	}

	// Messages
	for _, msg := range req.Messages {
		oMsgs, err := translateMessage(msg)
		if err != nil {
			return nil, err
		}
		oReq.Messages = append(oReq.Messages, oMsgs...)
	}

	// Tools
	for _, tool := range req.Tools {
		oReq.Tools = append(oReq.Tools, OTool{
			Type: "function",
			Function: OFunction{
				Name:        tool.Name,
				Description: tool.Description,
				Parameters:  tool.InputSchema,
			},
		})
	}

	// Tool choice
	if len(req.ToolChoice) > 0 {
		oReq.ToolChoice = translateToolChoice(req.ToolChoice)
	}

	if oReq.Stream {
		// Request stream options to get usage in streaming
		return marshalWithStreamOptions(oReq)
	}

	return json.Marshal(oReq)
}

// marshalWithStreamOptions adds stream_options to get usage data in streaming responses.
func marshalWithStreamOptions(req ORequest) ([]byte, error) {
	// Marshal normally then add stream_options
	data, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	var m map[string]interface{}
	json.Unmarshal(data, &m)
	m["stream_options"] = map[string]bool{"include_usage": true}
	return json.Marshal(m)
}

func extractSystemText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}

	// Try string first
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}

	// Try array of content blocks
	var blocks []ContentBlock
	if json.Unmarshal(raw, &blocks) == nil {
		var parts []string
		for _, b := range blocks {
			if b.Type == "text" && b.Text != "" {
				parts = append(parts, b.Text)
			}
		}
		return strings.Join(parts, "\n")
	}

	return ""
}

func translateMessage(msg AMessage) ([]OMessage, error) {
	// Content can be a string or array of content blocks
	var contentStr string
	if json.Unmarshal(msg.Content, &contentStr) == nil {
		return []OMessage{{Role: msg.Role, Content: contentStr}}, nil
	}

	var blocks []ContentBlock
	if err := json.Unmarshal(msg.Content, &blocks); err != nil {
		return nil, fmt.Errorf("parse message content: %w", err)
	}

	if msg.Role == "assistant" {
		return translateAssistantBlocks(blocks)
	}

	// User message: may contain text + tool_result blocks
	return translateUserBlocks(blocks)
}

func translateAssistantBlocks(blocks []ContentBlock) ([]OMessage, error) {
	msg := OMessage{Role: "assistant"}
	var textParts []string

	for _, b := range blocks {
		switch b.Type {
		case "text":
			if b.Text != "" {
				textParts = append(textParts, b.Text)
			}
		case "tool_use":
			args := string(b.Input)
			if args == "" || args == "null" {
				args = "{}"
			}
			msg.ToolCalls = append(msg.ToolCalls, OToolCall{
				ID:   b.ID,
				Type: "function",
				Function: OFunctionCall{
					Name:      b.Name,
					Arguments: args,
				},
			})
		}
	}

	msg.Content = strings.Join(textParts, "\n")
	return []OMessage{msg}, nil
}

func translateUserBlocks(blocks []ContentBlock) ([]OMessage, error) {
	var msgs []OMessage
	var textParts []string

	for _, b := range blocks {
		switch b.Type {
		case "text":
			textParts = append(textParts, b.Text)
		case "tool_result":
			// Flush accumulated text first
			if len(textParts) > 0 {
				msgs = append(msgs, OMessage{Role: "user", Content: strings.Join(textParts, "\n")})
				textParts = nil
			}
			content := extractToolResultContent(b)
			msgs = append(msgs, OMessage{
				Role:       "tool",
				ToolCallID: b.ToolUseID,
				Content:    content,
			})
		}
	}

	// Flush remaining text
	if len(textParts) > 0 {
		msgs = append(msgs, OMessage{Role: "user", Content: strings.Join(textParts, "\n")})
	}

	return msgs, nil
}

func extractToolResultContent(b ContentBlock) string {
	if len(b.Content) == 0 {
		return ""
	}

	// Try string
	var s string
	if json.Unmarshal(b.Content, &s) == nil {
		return s
	}

	// Try array of content blocks
	var blocks []ContentBlock
	if json.Unmarshal(b.Content, &blocks) == nil {
		var parts []string
		for _, cb := range blocks {
			if cb.Type == "text" {
				parts = append(parts, cb.Text)
			}
		}
		return strings.Join(parts, "\n")
	}

	return string(b.Content)
}

func translateToolChoice(raw json.RawMessage) interface{} {
	var tc struct {
		Type string `json:"type"`
		Name string `json:"name"`
	}
	if json.Unmarshal(raw, &tc) != nil {
		return nil
	}

	switch tc.Type {
	case "auto":
		return "auto"
	case "any":
		return "required"
	case "tool":
		return map[string]interface{}{
			"type":     "function",
			"function": map[string]string{"name": tc.Name},
		}
	default:
		return "auto"
	}
}
