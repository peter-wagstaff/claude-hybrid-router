package translate

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
)

const reasoningPrompt = "\n\nAlways think step by step before answering. Output your thinking process inside <reasoning_content>...</reasoning_content> tags, then provide your final answer after the closing tag."

var reasoningContentRe = regexp.MustCompile(`(?s)<reasoning_content>(.*?)</reasoning_content>`)

// forceReasoningTransform injects a reasoning prompt into the last user message
// and extracts <reasoning_content>...</reasoning_content> tags from responses
// into Anthropic-style thinking blocks.
type forceReasoningTransform struct {
	state     int
	tagBuffer string
}

func newForceReasoningTransform() *forceReasoningTransform {
	return &forceReasoningTransform{state: stateSearching}
}

func (t *forceReasoningTransform) Name() string { return "forcereasoning" }

// TransformRequest appends the reasoning prompt to the last user message.
func (t *forceReasoningTransform) TransformRequest(req map[string]interface{}, _ *TransformContext) error {
	msgs, ok := req["messages"].([]interface{})
	if !ok || len(msgs) == 0 {
		return nil
	}

	// Find last user message (iterate backwards)
	for i := len(msgs) - 1; i >= 0; i-- {
		msg, ok := msgs[i].(map[string]interface{})
		if !ok {
			continue
		}
		if msg["role"] != "user" {
			continue
		}

		// Append reasoning prompt to content
		switch content := msg["content"].(type) {
		case string:
			msg["content"] = content + reasoningPrompt
		}
		return nil
	}

	return nil
}

// TransformResponse extracts <reasoning_content>...</reasoning_content> from non-streaming responses.
func (t *forceReasoningTransform) TransformResponse(body []byte, _ *TransformContext) ([]byte, error) {
	var parsed map[string]interface{}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return body, nil
	}

	choices, ok := parsed["choices"].([]interface{})
	if !ok || len(choices) == 0 {
		return body, nil
	}
	choice, ok := choices[0].(map[string]interface{})
	if !ok {
		return body, nil
	}
	msg, ok := choice["message"].(map[string]interface{})
	if !ok {
		return body, nil
	}
	content, ok := msg["content"].(string)
	if !ok {
		return body, nil
	}

	loc := reasoningContentRe.FindStringSubmatchIndex(content)
	if loc == nil {
		return body, nil
	}

	thinking := content[loc[2]:loc[3]]
	after := strings.TrimSpace(content[loc[1]:])

	msg["thinking"] = map[string]interface{}{
		"content": thinking,
	}
	msg["content"] = after

	out, err := json.Marshal(parsed)
	if err != nil {
		return body, nil
	}
	return out, nil
}

// TransformStreamChunk implements a state machine to extract <reasoning_content> tags from streamed chunks.
func (t *forceReasoningTransform) TransformStreamChunk(data []byte, ctx *TransformContext) ([][]byte, error) {
	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return [][]byte{data}, nil
	}

	choices, ok := parsed["choices"].([]interface{})
	if !ok || len(choices) == 0 {
		return [][]byte{data}, nil
	}
	choice, ok := choices[0].(map[string]interface{})
	if !ok {
		return [][]byte{data}, nil
	}
	delta, ok := choice["delta"].(map[string]interface{})
	if !ok {
		return [][]byte{data}, nil
	}

	content, ok := delta["content"].(string)
	if !ok {
		return [][]byte{data}, nil
	}

	// Prepend any buffered partial tag
	if t.tagBuffer != "" {
		content = t.tagBuffer + content
		t.tagBuffer = ""
	}

	switch t.state {
	case stateSearching:
		return t.handleSearching(content, parsed, choice, delta, ctx)
	case stateThinking:
		return t.handleThinking(content, parsed, choice, delta, ctx)
	case stateFinal:
		return t.handleFinal(content, parsed, delta, ctx)
	}

	return [][]byte{data}, nil
}

const openReasoningTag = "<reasoning_content>"
const closeReasoningTag = "</reasoning_content>"

func (t *forceReasoningTransform) handleSearching(content string, parsed map[string]interface{}, choice, delta map[string]interface{}, ctx *TransformContext) ([][]byte, error) {
	openIdx := strings.Index(content, openReasoningTag)
	if openIdx >= 0 {
		before := content[:openIdx]
		after := content[openIdx+len(openReasoningTag):]
		t.state = stateThinking

		var chunks [][]byte

		// Emit content before tag as normal content
		if before != "" {
			ctx.HasTextContent = true
			delta["content"] = before
			b, err := json.Marshal(parsed)
			if err != nil {
				return nil, fmt.Errorf("marshal pre-reasoning content: %w", err)
			}
			chunks = append(chunks, b)
		}

		// Process the remainder in thinking state
		if after != "" {
			return t.appendThinkingChunks(chunks, after, parsed, choice, delta, ctx)
		}

		if len(chunks) == 0 {
			return nil, nil
		}
		return chunks, nil
	}

	// Check for partial tag at end of content
	if partial := partialTag(content, openReasoningTag); partial != "" {
		t.tagBuffer = partial
		rest := content[:len(content)-len(partial)]
		if rest == "" {
			return nil, nil
		}
		ctx.HasTextContent = true
		delta["content"] = rest
		b, err := json.Marshal(parsed)
		if err != nil {
			return nil, fmt.Errorf("marshal partial content: %w", err)
		}
		return [][]byte{b}, nil
	}

	// No tag -- pass through
	ctx.HasTextContent = true
	delta["content"] = content
	b, err := json.Marshal(parsed)
	if err != nil {
		return nil, fmt.Errorf("marshal passthrough: %w", err)
	}
	return [][]byte{b}, nil
}

func (t *forceReasoningTransform) appendThinkingChunks(chunks [][]byte, content string, parsed map[string]interface{}, choice, delta map[string]interface{}, ctx *TransformContext) ([][]byte, error) {
	closeIdx := strings.Index(content, closeReasoningTag)
	if closeIdx >= 0 {
		thinking := content[:closeIdx]
		after := content[closeIdx+len(closeReasoningTag):]
		t.state = stateFinal

		// Emit thinking content if non-empty
		if thinking != "" {
			delta["thinking"] = map[string]interface{}{
				"content": thinking,
			}
			delete(delta, "content")
			b, err := json.Marshal(parsed)
			if err != nil {
				return nil, fmt.Errorf("marshal thinking content: %w", err)
			}
			chunks = append(chunks, b)
		}

		// Emit thinking-close with signature
		closeChunk, err := json.Marshal(map[string]interface{}{
			"choices": []interface{}{
				map[string]interface{}{
					"delta": map[string]interface{}{
						"thinking": map[string]interface{}{
							"signature": fmt.Sprintf("<%d>", time.Now().UnixMilli()),
						},
					},
				},
			},
		})
		if err != nil {
			return nil, fmt.Errorf("marshal thinking-close: %w", err)
		}
		chunks = append(chunks, closeChunk)

		// Emit content after closing tag if any
		if after := strings.TrimSpace(after); after != "" {
			ctx.HasTextContent = true
			if idx, ok := choice["index"].(float64); ok {
				choice["index"] = idx + 1
			}
			delta["content"] = after
			delete(delta, "thinking")
			b, err := json.Marshal(parsed)
			if err != nil {
				return nil, fmt.Errorf("marshal post-reasoning content: %w", err)
			}
			chunks = append(chunks, b)
		}

		return chunks, nil
	}

	// No close tag -- emit all as thinking
	delta["thinking"] = map[string]interface{}{
		"content": content,
	}
	delete(delta, "content")
	b, err := json.Marshal(parsed)
	if err != nil {
		return nil, fmt.Errorf("marshal thinking: %w", err)
	}
	chunks = append(chunks, b)
	return chunks, nil
}

func (t *forceReasoningTransform) handleThinking(content string, parsed map[string]interface{}, choice, delta map[string]interface{}, ctx *TransformContext) ([][]byte, error) {
	return t.appendThinkingChunks(nil, content, parsed, choice, delta, ctx)
}

func (t *forceReasoningTransform) handleFinal(content string, parsed map[string]interface{}, delta map[string]interface{}, ctx *TransformContext) ([][]byte, error) {
	ctx.HasTextContent = true
	delta["content"] = content
	b, err := json.Marshal(parsed)
	if err != nil {
		return nil, fmt.Errorf("marshal final content: %w", err)
	}
	return [][]byte{b}, nil
}

// partialTag checks if the end of s looks like a partial opening of the given tag.
func partialTag(s, tag string) string {
	for i := 1; i < len(tag); i++ {
		suffix := tag[:i]
		if strings.HasSuffix(s, suffix) {
			return suffix
		}
	}
	return ""
}

func init() {
	RegisterTransform("forcereasoning", func() Transformer {
		return newForceReasoningTransform()
	})
}
