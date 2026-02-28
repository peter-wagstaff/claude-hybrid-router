package translate

import "strings"

// TransformContext holds per-request state shared across transforms in a chain.
type TransformContext struct {
	// Accumulated reasoning content for thinking-close block.
	ReasoningContent strings.Builder
	ReasoningComplete bool
	HasTextContent    bool

	// ToolCallBuffers accumulates streaming tool call arguments, keyed by tool call index.
	ToolCallBuffers map[int]*ToolCallBuffer

	// ExitToolIndex tracks the exit tool for tooluse transform (-1 = inactive).
	ExitToolIndex int
	ExitToolArgs  strings.Builder

	ModelName    string
	ProviderName string

	// Params holds custom parameters from config to inject into the request body.
	Params map[string]interface{}

	// CallLog is optional; used in tests to record transform ordering.
	CallLog *[]string
}

// ToolCallBuffer accumulates streaming tool call arguments.
type ToolCallBuffer struct {
	ID        string
	Name      string
	Arguments strings.Builder
}

// NewTransformContext creates a TransformContext with sensible defaults.
func NewTransformContext(model, provider string) *TransformContext {
	return &TransformContext{
		ToolCallBuffers: make(map[int]*ToolCallBuffer),
		ExitToolIndex:   -1,
		ModelName:       model,
		ProviderName:    provider,
	}
}

// Transformer is a composable unit that can transform requests, responses, and stream chunks.
type Transformer interface {
	Name() string
	TransformRequest(req map[string]interface{}, ctx *TransformContext) error
	TransformResponse(body []byte, ctx *TransformContext) ([]byte, error)
	TransformStreamChunk(data []byte, ctx *TransformContext) ([][]byte, error)
}

// TransformChain applies a sequence of Transformers.
// Requests are processed in forward order; responses and stream chunks in reverse order.
type TransformChain struct {
	transforms []Transformer
}

// NewTransformChain creates a chain from the given transformers.
func NewTransformChain(transforms ...Transformer) *TransformChain {
	return &TransformChain{transforms: transforms}
}

// RunRequest applies each transformer's TransformRequest in forward order.
// Stops on first error.
func (c *TransformChain) RunRequest(req map[string]interface{}, ctx *TransformContext) error {
	for _, t := range c.transforms {
		if err := t.TransformRequest(req, ctx); err != nil {
			return err
		}
	}
	return nil
}

// RunResponse applies each transformer's TransformResponse in reverse order.
// Stops on first error.
func (c *TransformChain) RunResponse(body []byte, ctx *TransformContext) ([]byte, error) {
	var err error
	for i := len(c.transforms) - 1; i >= 0; i-- {
		body, err = c.transforms[i].TransformResponse(body, ctx)
		if err != nil {
			return nil, err
		}
	}
	return body, nil
}

// RunStreamChunk applies each transformer's TransformStreamChunk in reverse order.
// Each transformer processes all chunks produced by the previous layer.
// Returns 0 chunks for suppression, 1 for normal, 2+ for expansion.
func (c *TransformChain) RunStreamChunk(data []byte, ctx *TransformContext) ([][]byte, error) {
	chunks := [][]byte{data}

	for i := len(c.transforms) - 1; i >= 0; i-- {
		var next [][]byte
		for _, chunk := range chunks {
			result, err := c.transforms[i].TransformStreamChunk(chunk, ctx)
			if err != nil {
				return nil, err
			}
			next = append(next, result...)
		}
		chunks = next
	}

	return chunks, nil
}
