package translate

// cleanCacheTransform strips cache_control from all messages and content blocks.
// Anthropic's cache_control is not understood by most OpenAI-compatible providers.
type cleanCacheTransform struct{}

func (c *cleanCacheTransform) Name() string { return "cleancache" }

func (c *cleanCacheTransform) TransformRequest(req map[string]interface{}, _ *TransformContext) error {
	msgs, ok := req["messages"].([]interface{})
	if !ok {
		return nil
	}
	for _, m := range msgs {
		msg, ok := m.(map[string]interface{})
		if !ok {
			continue
		}
		delete(msg, "cache_control")
		if parts, ok := msg["content"].([]interface{}); ok {
			for _, p := range parts {
				if part, ok := p.(map[string]interface{}); ok {
					delete(part, "cache_control")
				}
			}
		}
	}
	return nil
}

func (c *cleanCacheTransform) TransformResponse(body []byte, _ *TransformContext) ([]byte, error) {
	return body, nil
}

func (c *cleanCacheTransform) TransformStreamChunk(data []byte, _ *TransformContext) ([][]byte, error) {
	return [][]byte{data}, nil
}

func init() {
	RegisterTransform("cleancache", func() Transformer {
		return &cleanCacheTransform{}
	})
}
