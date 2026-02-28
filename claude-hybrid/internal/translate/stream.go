package translate

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"strings"
)

// OStreamChunk is an OpenAI streaming chunk.
type OStreamChunk struct {
	ID      string          `json:"id"`
	Choices []OStreamChoice `json:"choices"`
	Usage   *OUsage         `json:"usage,omitempty"`
}

// OStreamChoice is a choice in a streaming chunk.
type OStreamChoice struct {
	Delta        OStreamDelta `json:"delta"`
	FinishReason *string      `json:"finish_reason"`
}

// OStreamDelta is the delta content in a streaming chunk.
type OStreamDelta struct {
	Role      string            `json:"role,omitempty"`
	Content   *string           `json:"content,omitempty"`
	ToolCalls []OStreamToolCall `json:"tool_calls,omitempty"`
}

// OStreamToolCall is a tool call delta in streaming.
type OStreamToolCall struct {
	Index    int               `json:"index"`
	ID       string            `json:"id,omitempty"`
	Type     string            `json:"type,omitempty"`
	Function OStreamFuncDelta  `json:"function,omitempty"`
}

// OStreamFuncDelta is the function delta in a streaming tool call.
type OStreamFuncDelta struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

// StreamTranslator converts an OpenAI SSE stream to Anthropic SSE events.
type StreamTranslator struct {
	modelLabel   string
	msgID        string
	blockIndex   int
	inTextBlock  bool
	inToolBlock  bool
	started      bool
	finishReason string
	usage        *OUsage
	// Track tool calls by index to handle multi-chunk tool call streaming
	toolCalls map[int]*activeToolCall
	// Transform chain for stream chunk processing
	chain *TransformChain
	ctx   *TransformContext
	// Verbose logging and consecutive drop tracking
	verbose          bool
	consecutiveDrops int
}

type activeToolCall struct {
	id   string
	name string
}

// NewStreamTranslator creates a new streaming translator.
func NewStreamTranslator(modelLabel string) *StreamTranslator {
	return &StreamTranslator{
		modelLabel: modelLabel,
		msgID:      "msg_stream",
		toolCalls:  make(map[int]*activeToolCall),
	}
}

// SetVerbose enables verbose logging of dropped SSE chunks.
func (st *StreamTranslator) SetVerbose(v bool) {
	st.verbose = v
}

// SetTransformChain sets the transform chain and context for stream chunk processing.
func (st *StreamTranslator) SetTransformChain(chain *TransformChain, ctx *TransformContext) {
	st.chain = chain
	st.ctx = ctx
}

// TranslateStream reads an OpenAI SSE stream from r and writes Anthropic SSE events to w.
func (st *StreamTranslator) TranslateStream(r io.Reader, w io.Writer) error {
	scanner := bufio.NewScanner(r)
	// Increase buffer for large SSE lines
	scanner.Buffer(make([]byte, 0, 256*1024), 256*1024)

	for scanner.Scan() {
		line := scanner.Text()

		if !strings.HasPrefix(line, "data: ") {
			continue
		}

		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			break
		}

		var chunk OStreamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			st.consecutiveDrops++
			if st.verbose {
				log.Printf("[LOCAL_ERR:PARSE] dropped unparseable SSE chunk: %.200s", data)
			}
			if st.consecutiveDrops >= 3 {
				return fmt.Errorf("too many consecutive unparseable chunks (%d)", st.consecutiveDrops)
			}
			continue
		}
		st.consecutiveDrops = 0

		// Run stream transforms if chain is set
		if st.chain != nil && st.ctx != nil {
			transformedChunks, err := st.chain.RunStreamChunk([]byte(data), st.ctx)
			if err != nil {
				st.consecutiveDrops++
				if st.verbose {
					log.Printf("[LOCAL_ERR:TRANSLATE] stream transform error: %v", err)
				}
				if st.consecutiveDrops >= 3 {
					return fmt.Errorf("too many consecutive stream transform errors (%d)", st.consecutiveDrops)
				}
				continue
			}
			st.consecutiveDrops = 0
			// Process each transformed chunk through the state machine
			for _, tc := range transformedChunks {
				var transformedChunk OStreamChunk
				if json.Unmarshal(tc, &transformedChunk) != nil {
					st.consecutiveDrops++
					if st.consecutiveDrops >= 3 {
						return fmt.Errorf("too many consecutive unparseable transformed chunks (%d)", st.consecutiveDrops)
					}
					continue
				}
				st.consecutiveDrops = 0
				st.processChunk(w, transformedChunk)
			}
			continue
		}
		// Original processing (when no chain is set)
		st.processChunk(w, chunk)
	}

	// Close any open block
	st.closeCurrentBlock(w)

	// Emit message_delta with stop_reason
	st.emitMessageDelta(w)

	// Emit message_stop
	st.emitEvent(w, "message_stop", map[string]string{"type": "message_stop"})

	return scanner.Err()
}

func (st *StreamTranslator) processChunk(w io.Writer, chunk OStreamChunk) {
	// Capture message ID from first chunk
	if !st.started && chunk.ID != "" {
		st.msgID = "msg_" + chunk.ID
	}

	// Capture usage if present (from stream_options)
	if chunk.Usage != nil {
		st.usage = chunk.Usage
	}

	if len(chunk.Choices) == 0 {
		return
	}

	choice := chunk.Choices[0]

	// Emit message_start on first chunk
	if !st.started {
		st.started = true
		st.emitMessageStart(w)
	}

	// Handle finish_reason
	if choice.FinishReason != nil {
		st.finishReason = *choice.FinishReason
	}

	// Handle text content
	if choice.Delta.Content != nil && *choice.Delta.Content != "" {
		if !st.inTextBlock {
			st.closeCurrentBlock(w)
			st.emitContentBlockStart(w, "text", "", "")
			st.inTextBlock = true
		}
		st.emitTextDelta(w, *choice.Delta.Content)
	}

	// Handle tool calls
	for _, tc := range choice.Delta.ToolCalls {
		// New tool call (has id and name)
		if tc.ID != "" {
			st.toolCalls[tc.Index] = &activeToolCall{id: tc.ID, name: tc.Function.Name}
			st.closeCurrentBlock(w)
			st.emitContentBlockStart(w, "tool_use", sanitizeToolID(tc.ID), tc.Function.Name)
			st.inToolBlock = true
		}

		// Argument fragment
		if tc.Function.Arguments != "" {
			st.emitInputJSONDelta(w, tc.Function.Arguments)
		}
	}
}

func (st *StreamTranslator) closeCurrentBlock(w io.Writer) {
	if st.inTextBlock || st.inToolBlock {
		st.emitEvent(w, "content_block_stop", map[string]interface{}{
			"type":  "content_block_stop",
			"index": st.blockIndex,
		})
		st.blockIndex++
		st.inTextBlock = false
		st.inToolBlock = false
	}
}

func (st *StreamTranslator) emitMessageStart(w io.Writer) {
	inputTokens := 0
	if st.usage != nil {
		inputTokens = st.usage.PromptTokens
	}
	st.emitEvent(w, "message_start", map[string]interface{}{
		"type": "message_start",
		"message": map[string]interface{}{
			"id": st.msgID, "type": "message", "role": "assistant",
			"content": []interface{}{}, "model": st.modelLabel,
			"stop_reason": nil, "stop_sequence": nil,
			"usage": map[string]int{"input_tokens": inputTokens, "output_tokens": 0},
		},
	})
}

func (st *StreamTranslator) emitContentBlockStart(w io.Writer, blockType, id, name string) {
	block := map[string]interface{}{"type": blockType}
	if blockType == "text" {
		block["text"] = ""
	} else if blockType == "tool_use" {
		block["id"] = id
		block["name"] = name
		block["input"] = map[string]interface{}{}
	}
	st.emitEvent(w, "content_block_start", map[string]interface{}{
		"type":          "content_block_start",
		"index":         st.blockIndex,
		"content_block": block,
	})
}

func (st *StreamTranslator) emitTextDelta(w io.Writer, text string) {
	st.emitEvent(w, "content_block_delta", map[string]interface{}{
		"type":  "content_block_delta",
		"index": st.blockIndex,
		"delta": map[string]string{"type": "text_delta", "text": text},
	})
}

func (st *StreamTranslator) emitInputJSONDelta(w io.Writer, partial string) {
	st.emitEvent(w, "content_block_delta", map[string]interface{}{
		"type":  "content_block_delta",
		"index": st.blockIndex,
		"delta": map[string]string{"type": "input_json_delta", "partial_json": partial},
	})
}

func (st *StreamTranslator) emitMessageDelta(w io.Writer) {
	outputTokens := 0
	if st.usage != nil {
		outputTokens = st.usage.CompletionTokens
	}
	stopReason := mapFinishReason(st.finishReason)
	st.emitEvent(w, "message_delta", map[string]interface{}{
		"type":  "message_delta",
		"delta": map[string]interface{}{"stop_reason": stopReason, "stop_sequence": nil},
		"usage": map[string]int{"output_tokens": outputTokens},
	})
}

func (st *StreamTranslator) emitEvent(w io.Writer, event string, data interface{}) {
	jsonData, _ := json.Marshal(data)
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, jsonData)
}
