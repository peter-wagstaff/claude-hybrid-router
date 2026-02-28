package translate

// customParamsTransform injects custom parameters from config into the request body.
// Only adds new keys — existing request fields are never overwritten.
type customParamsTransform struct{}

func (c *customParamsTransform) Name() string { return "customparams" }

func (c *customParamsTransform) TransformRequest(req map[string]interface{}, ctx *TransformContext) error {
	for k, v := range ctx.Params {
		if _, exists := req[k]; !exists {
			req[k] = v
		}
	}
	return nil
}

func (c *customParamsTransform) TransformResponse(body []byte, _ *TransformContext) ([]byte, error) {
	return body, nil
}

func (c *customParamsTransform) TransformStreamChunk(data []byte, _ *TransformContext) ([][]byte, error) {
	return [][]byte{data}, nil
}

func init() {
	RegisterTransform("customparams", func() Transformer {
		return &customParamsTransform{}
	})
}
