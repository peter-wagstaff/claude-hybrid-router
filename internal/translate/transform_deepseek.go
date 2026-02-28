package translate

// deepseekTransform renames max_completion_tokens → max_tokens for DeepSeek
// API compatibility (DeepSeek uses the OpenAI-legacy parameter name).
type deepseekTransform struct{}

func newDeepseekTransform() *deepseekTransform {
	return &deepseekTransform{}
}

func (d *deepseekTransform) Name() string { return "deepseek" }

func (d *deepseekTransform) TransformRequest(req map[string]interface{}, ctx *TransformContext) error {
	if v, ok := req["max_completion_tokens"]; ok {
		req["max_tokens"] = v
		delete(req, "max_completion_tokens")
	}
	return nil
}

func (d *deepseekTransform) TransformResponse(body []byte, ctx *TransformContext) ([]byte, error) {
	return body, nil
}

func (d *deepseekTransform) TransformStreamChunk(data []byte, ctx *TransformContext) ([][]byte, error) {
	return [][]byte{data}, nil
}

func init() {
	RegisterTransform("deepseek", func() Transformer {
		return newDeepseekTransform()
	})
}
