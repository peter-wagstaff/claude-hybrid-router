package translate

// deepseekTransform caps max_tokens to 8192 for DeepSeek API compatibility.
type deepseekTransform struct{}

func newDeepseekTransform() *deepseekTransform {
	return &deepseekTransform{}
}

func (d *deepseekTransform) Name() string { return "deepseek" }

func (d *deepseekTransform) TransformRequest(req map[string]interface{}, ctx *TransformContext) error {
	if v, ok := req["max_tokens"].(float64); ok && v > 8192 {
		req["max_tokens"] = float64(8192)
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
