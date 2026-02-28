package translate

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// mockTransformer records calls via ctx.CallLog for ordering tests.
type mockTransformer struct {
	name string
	// If non-nil, returned instead of default passthrough behavior.
	requestFn     func(req map[string]interface{}, ctx *TransformContext) error
	responseFn    func(body []byte, ctx *TransformContext) ([]byte, error)
	streamChunkFn func(data []byte, ctx *TransformContext) ([][]byte, error)
}

func (m *mockTransformer) Name() string { return m.name }

func (m *mockTransformer) TransformRequest(req map[string]interface{}, ctx *TransformContext) error {
	if ctx.CallLog != nil {
		*ctx.CallLog = append(*ctx.CallLog, m.name+":req")
	}
	if m.requestFn != nil {
		return m.requestFn(req, ctx)
	}
	return nil
}

func (m *mockTransformer) TransformResponse(body []byte, ctx *TransformContext) ([]byte, error) {
	if ctx.CallLog != nil {
		*ctx.CallLog = append(*ctx.CallLog, m.name+":resp")
	}
	if m.responseFn != nil {
		return m.responseFn(body, ctx)
	}
	return body, nil
}

func (m *mockTransformer) TransformStreamChunk(data []byte, ctx *TransformContext) ([][]byte, error) {
	if ctx.CallLog != nil {
		*ctx.CallLog = append(*ctx.CallLog, m.name+":stream")
	}
	if m.streamChunkFn != nil {
		return m.streamChunkFn(data, ctx)
	}
	return [][]byte{data}, nil
}

func TestTransformContextDefaults(t *testing.T) {
	ctx := NewTransformContext("test-model", "test-provider")

	if ctx.ModelName != "test-model" {
		t.Errorf("ModelName = %q, want %q", ctx.ModelName, "test-model")
	}
	if ctx.ProviderName != "test-provider" {
		t.Errorf("ProviderName = %q, want %q", ctx.ProviderName, "test-provider")
	}
	if ctx.ExitToolIndex != -1 {
		t.Errorf("ExitToolIndex = %d, want -1", ctx.ExitToolIndex)
	}
	if ctx.ToolCallBuffers == nil {
		t.Error("ToolCallBuffers should be initialized, got nil")
	}
	if ctx.ReasoningComplete {
		t.Error("ReasoningComplete should be false")
	}
	if ctx.HasTextContent {
		t.Error("HasTextContent should be false")
	}
	if ctx.ReasoningContent.Len() != 0 {
		t.Error("ReasoningContent should be empty")
	}
}

func TestTransformChainRequestForwardOrder(t *testing.T) {
	log := []string{}
	ctx := NewTransformContext("m", "p")
	ctx.CallLog = &log

	a := &mockTransformer{name: "a"}
	b := &mockTransformer{name: "b"}
	c := &mockTransformer{name: "c"}

	chain := NewTransformChain(a, b, c)
	req := map[string]interface{}{"model": "test"}

	if err := chain.RunRequest(req, ctx); err != nil {
		t.Fatalf("RunRequest error: %v", err)
	}

	want := "a:req,b:req,c:req"
	got := strings.Join(log, ",")
	if got != want {
		t.Errorf("request order = %q, want %q", got, want)
	}
}

func TestTransformChainResponseReverseOrder(t *testing.T) {
	log := []string{}
	ctx := NewTransformContext("m", "p")
	ctx.CallLog = &log

	a := &mockTransformer{name: "a"}
	b := &mockTransformer{name: "b"}
	c := &mockTransformer{name: "c"}

	chain := NewTransformChain(a, b, c)

	result, err := chain.RunResponse([]byte("body"), ctx)
	if err != nil {
		t.Fatalf("RunResponse error: %v", err)
	}

	want := "c:resp,b:resp,a:resp"
	got := strings.Join(log, ",")
	if got != want {
		t.Errorf("response order = %q, want %q", got, want)
	}

	if string(result) != "body" {
		t.Errorf("response body = %q, want %q", string(result), "body")
	}
}

func TestTransformChainStreamChunkReverseOrder(t *testing.T) {
	log := []string{}
	ctx := NewTransformContext("m", "p")
	ctx.CallLog = &log

	a := &mockTransformer{name: "a"}
	b := &mockTransformer{name: "b"}
	c := &mockTransformer{name: "c"}

	chain := NewTransformChain(a, b, c)

	chunks, err := chain.RunStreamChunk([]byte("chunk"), ctx)
	if err != nil {
		t.Fatalf("RunStreamChunk error: %v", err)
	}

	want := "c:stream,b:stream,a:stream"
	got := strings.Join(log, ",")
	if got != want {
		t.Errorf("stream order = %q, want %q", got, want)
	}

	if len(chunks) != 1 || string(chunks[0]) != "chunk" {
		t.Errorf("stream chunks = %v, want [[chunk]]", chunks)
	}
}

func TestTransformChainStreamSuppression(t *testing.T) {
	// Middle transformer suppresses the chunk (returns nil/empty).
	suppressor := &mockTransformer{
		name: "suppressor",
		streamChunkFn: func(data []byte, ctx *TransformContext) ([][]byte, error) {
			return nil, nil // suppress
		},
	}
	passthrough := &mockTransformer{name: "pass"}

	// Chain: passthrough applied after suppressor (reverse order: pass first, then suppressor).
	// But suppressor is later in the chain so it runs first in reverse.
	// Order: [passthrough, suppressor] -> reverse: suppressor runs first, then passthrough.
	// If suppressor returns nil, passthrough should not see any chunks.
	chain := NewTransformChain(passthrough, suppressor)
	ctx := NewTransformContext("m", "p")

	chunks, err := chain.RunStreamChunk([]byte("data"), ctx)
	if err != nil {
		t.Fatalf("RunStreamChunk error: %v", err)
	}

	if len(chunks) != 0 {
		t.Errorf("expected 0 chunks after suppression, got %d: %v", len(chunks), chunks)
	}
}

func TestTransformChainStreamExpansion(t *testing.T) {
	// A transformer that expands one chunk into two.
	expander := &mockTransformer{
		name: "expander",
		streamChunkFn: func(data []byte, ctx *TransformContext) ([][]byte, error) {
			return [][]byte{
				[]byte("thinking-close"),
				[]byte("content"),
			}, nil
		},
	}

	// A recorder that passes through and records what it sees.
	var seen []string
	recorder := &mockTransformer{
		name: "recorder",
		streamChunkFn: func(data []byte, ctx *TransformContext) ([][]byte, error) {
			seen = append(seen, string(data))
			return [][]byte{data}, nil
		},
	}

	// Chain: [recorder, expander] -> reverse order: expander runs first, then recorder.
	// Expander produces 2 chunks, recorder should see both.
	chain := NewTransformChain(recorder, expander)
	ctx := NewTransformContext("m", "p")

	chunks, err := chain.RunStreamChunk([]byte("input"), ctx)
	if err != nil {
		t.Fatalf("RunStreamChunk error: %v", err)
	}

	if len(chunks) != 2 {
		t.Fatalf("expected 2 chunks, got %d: %v", len(chunks), chunks)
	}
	if string(chunks[0]) != "thinking-close" || string(chunks[1]) != "content" {
		t.Errorf("chunks = [%q, %q], want [thinking-close, content]",
			string(chunks[0]), string(chunks[1]))
	}

	if len(seen) != 2 || seen[0] != "thinking-close" || seen[1] != "content" {
		t.Errorf("recorder saw %v, want [thinking-close content]", seen)
	}
}

func TestEmptyChainPassthrough(t *testing.T) {
	chain := NewTransformChain()
	ctx := NewTransformContext("m", "p")

	// Request passthrough
	req := map[string]interface{}{"model": "test"}
	if err := chain.RunRequest(req, ctx); err != nil {
		t.Fatalf("empty chain RunRequest error: %v", err)
	}
	if req["model"] != "test" {
		t.Error("empty chain modified request")
	}

	// Response passthrough
	resp, err := chain.RunResponse([]byte("body"), ctx)
	if err != nil {
		t.Fatalf("empty chain RunResponse error: %v", err)
	}
	if string(resp) != "body" {
		t.Errorf("empty chain response = %q, want %q", string(resp), "body")
	}

	// Stream passthrough
	chunks, err := chain.RunStreamChunk([]byte("chunk"), ctx)
	if err != nil {
		t.Fatalf("empty chain RunStreamChunk error: %v", err)
	}
	if len(chunks) != 1 || string(chunks[0]) != "chunk" {
		t.Errorf("empty chain stream = %v, want [[chunk]]", chunks)
	}
}

func TestTransformChainRequestError(t *testing.T) {
	failing := &mockTransformer{
		name: "fail",
		requestFn: func(req map[string]interface{}, ctx *TransformContext) error {
			return fmt.Errorf("request transform failed")
		},
	}
	after := &mockTransformer{name: "after"}

	log := []string{}
	ctx := NewTransformContext("m", "p")
	ctx.CallLog = &log

	chain := NewTransformChain(failing, after)
	err := chain.RunRequest(map[string]interface{}{}, ctx)

	if err == nil {
		t.Fatal("expected error from RunRequest")
	}
	// "after" should not have been called
	if strings.Contains(strings.Join(log, ","), "after:req") {
		t.Error("transforms after the failing one should not run")
	}
}

func TestTransformChainResponseError(t *testing.T) {
	failing := &mockTransformer{
		name: "fail",
		responseFn: func(body []byte, ctx *TransformContext) ([]byte, error) {
			return nil, fmt.Errorf("response transform failed")
		},
	}
	before := &mockTransformer{name: "before"}

	log := []string{}
	ctx := NewTransformContext("m", "p")
	ctx.CallLog = &log

	// [before, fail] -> reverse: fail runs first
	chain := NewTransformChain(before, failing)
	_, err := chain.RunResponse([]byte("body"), ctx)

	if err == nil {
		t.Fatal("expected error from RunResponse")
	}
	// "before" should not have been called (fail runs first in reverse)
	if strings.Contains(strings.Join(log, ","), "before:resp") {
		t.Error("transforms after the failing one should not run")
	}
}

func TestTransformChainStreamError(t *testing.T) {
	failing := &mockTransformer{
		name: "fail",
		streamChunkFn: func(data []byte, ctx *TransformContext) ([][]byte, error) {
			return nil, fmt.Errorf("stream transform failed")
		},
	}

	chain := NewTransformChain(&mockTransformer{name: "before"}, failing)
	ctx := NewTransformContext("m", "p")

	_, err := chain.RunStreamChunk([]byte("data"), ctx)
	if err == nil {
		t.Fatal("expected error from RunStreamChunk")
	}
}

// Verify TransformContext fields are usable.
func TestTransformContextFields(t *testing.T) {
	ctx := NewTransformContext("deepseek-r1", "ollama")

	ctx.ReasoningContent.WriteString("thinking...")
	ctx.ReasoningComplete = true
	ctx.HasTextContent = true

	ctx.ToolCallBuffers[0] = &ToolCallBuffer{
		ID:   "call_123",
		Name: "bash",
	}
	ctx.ToolCallBuffers[0].Arguments.WriteString(`{"cmd":"ls"}`)

	ctx.ExitToolIndex = 2
	ctx.ExitToolArgs.WriteString(`{"result":"ok"}`)

	if ctx.ReasoningContent.String() != "thinking..." {
		t.Error("ReasoningContent mismatch")
	}
	if ctx.ToolCallBuffers[0].Arguments.String() != `{"cmd":"ls"}` {
		t.Error("ToolCallBuffer arguments mismatch")
	}
	if ctx.ExitToolIndex != 2 {
		t.Error("ExitToolIndex mismatch")
	}
	if ctx.ExitToolArgs.String() != `{"result":"ok"}` {
		t.Error("ExitToolArgs mismatch")
	}
}

func TestRegistryLookup(t *testing.T) {
	chain, err := BuildChain([]string{"schema:generic"})
	if err != nil {
		t.Fatal(err)
	}
	if chain == nil {
		t.Fatal("expected non-nil chain")
	}
}

func TestRegistryUnknown(t *testing.T) {
	_, err := BuildChain([]string{"nonexistent_transform"})
	if err == nil {
		t.Error("expected error for unknown transform name")
	}
}

func TestSchemaTransformViaChain(t *testing.T) {
	chain, _ := BuildChain([]string{"schema:generic"})
	ctx := NewTransformContext("model", "provider")

	req := map[string]interface{}{
		"tools": []interface{}{
			map[string]interface{}{
				"type": "function",
				"function": map[string]interface{}{
					"name": "test",
					"parameters": map[string]interface{}{
						"type":                 "object",
						"additionalProperties": false,
						"strict":               true,
						"$schema":              "http://json-schema.org/draft-07/schema#",
					},
				},
			},
		},
	}

	if err := chain.RunRequest(req, ctx); err != nil {
		t.Fatal(err)
	}

	tools := req["tools"].([]interface{})
	fn := tools[0].(map[string]interface{})["function"].(map[string]interface{})
	params := fn["parameters"].(map[string]interface{})

	if _, ok := params["additionalProperties"]; ok {
		t.Error("additionalProperties not stripped")
	}
	if _, ok := params["strict"]; ok {
		t.Error("strict not stripped")
	}
	if _, ok := params["$schema"]; ok {
		t.Error("$schema not stripped")
	}
}

// Suppress unused import warning — json is used in transform_test.go (same package)
// but we include it here to ensure compilation.
var _ = json.Marshal
