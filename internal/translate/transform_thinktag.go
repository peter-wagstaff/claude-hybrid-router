package translate

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
)

const (
	stateSearching = iota
	stateThinking
	stateFinal
)

var thinkTagRe = regexp.MustCompile(`(?s)<think>(.*?)</think>`)

// thinkTagTransform extracts <think>...</think> tags from content into
// Anthropic-style thinking blocks. Used for models like Qwen3 and DeepSeek-R1
// that inline thinking in <think> tags at certain temperatures.
type thinkTagTransform struct {
	state     int
	tagBuffer string
}

func newThinkTagTransform() *thinkTagTransform {
	return &thinkTagTransform{state: stateSearching}
}

func (t *thinkTagTransform) Name() string { return "extrathinktag" }

// TransformRequest is a no-op.
func (t *thinkTagTransform) TransformRequest(_ map[string]interface{}, _ *TransformContext) error {
	return nil
}

// TransformResponse extracts <think>...</think> from the response content in non-streaming mode.
func (t *thinkTagTransform) TransformResponse(body []byte, _ *TransformContext) ([]byte, error) {
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

	loc := thinkTagRe.FindStringSubmatchIndex(content)
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

// TransformStreamChunk implements a state machine to extract <think> tags from streamed chunks.
func (t *thinkTagTransform) TransformStreamChunk(data []byte, ctx *TransformContext) ([][]byte, error) {
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

func (t *thinkTagTransform) handleSearching(content string, parsed map[string]interface{}, choice, delta map[string]interface{}, ctx *TransformContext) ([][]byte, error) {
	openIdx := strings.Index(content, "<think>")
	if openIdx >= 0 {
		before := content[:openIdx]
		after := content[openIdx+len("<think>"):]
		t.state = stateThinking

		var chunks [][]byte

		// Emit content before <think> as normal content
		if before != "" {
			ctx.HasTextContent = true
			delta["content"] = before
			b, err := json.Marshal(parsed)
			if err != nil {
				return nil, fmt.Errorf("marshal pre-think content: %w", err)
			}
			chunks = append(chunks, b)
		}

		// Process the remainder in thinking state
		if after != "" {
			return t.appendThinkingChunks(chunks, after, parsed, choice, delta, ctx)
		}

		// If nothing after <think>, just return what we have (possibly empty)
		if len(chunks) == 0 {
			return nil, nil
		}
		return chunks, nil
	}

	// Check for partial tag at end of content
	if partial := partialOpenTag(content); partial != "" {
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

	// No tag — pass through
	ctx.HasTextContent = true
	delta["content"] = content
	b, err := json.Marshal(parsed)
	if err != nil {
		return nil, fmt.Errorf("marshal passthrough: %w", err)
	}
	return [][]byte{b}, nil
}

func (t *thinkTagTransform) appendThinkingChunks(chunks [][]byte, content string, parsed map[string]interface{}, choice, delta map[string]interface{}, ctx *TransformContext) ([][]byte, error) {
	closeIdx := strings.Index(content, "</think>")
	if closeIdx >= 0 {
		thinking := content[:closeIdx]
		after := content[closeIdx+len("</think>"):]
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

		// Emit content after </think> if any
		if after := strings.TrimSpace(after); after != "" {
			ctx.HasTextContent = true
			if idx, ok := choice["index"].(float64); ok {
				choice["index"] = idx + 1
			}
			delta["content"] = after
			delete(delta, "thinking")
			b, err := json.Marshal(parsed)
			if err != nil {
				return nil, fmt.Errorf("marshal post-think content: %w", err)
			}
			chunks = append(chunks, b)
		}

		return chunks, nil
	}

	// No close tag — emit all as thinking
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

func (t *thinkTagTransform) handleThinking(content string, parsed map[string]interface{}, choice, delta map[string]interface{}, ctx *TransformContext) ([][]byte, error) {
	return t.appendThinkingChunks(nil, content, parsed, choice, delta, ctx)
}

func (t *thinkTagTransform) handleFinal(content string, parsed map[string]interface{}, delta map[string]interface{}, ctx *TransformContext) ([][]byte, error) {
	ctx.HasTextContent = true
	delta["content"] = content
	b, err := json.Marshal(parsed)
	if err != nil {
		return nil, fmt.Errorf("marshal final content: %w", err)
	}
	return [][]byte{b}, nil
}

// partialOpenTag checks if the end of s looks like a partial "<think>" tag.
// Returns the partial match (e.g., "<", "<t", "<th", etc.) or "".
func partialOpenTag(s string) string {
	tag := "<think>"
	for i := 1; i < len(tag); i++ {
		suffix := tag[:i]
		if strings.HasSuffix(s, suffix) {
			return suffix
		}
	}
	return ""
}

func init() {
	RegisterTransform("extrathinktag", func() Transformer {
		return newThinkTagTransform()
	})
}
