package translate

import "testing"

func TestCustomParams_InjectsNewKeys(t *testing.T) {
	tr := &customParamsTransform{}
	ctx := NewTransformContext("model", "provider")
	ctx.Params = map[string]interface{}{
		"top_k":            float64(40),
		"presence_penalty": 0.5,
	}

	req := map[string]interface{}{
		"model":    "test",
		"messages": []interface{}{},
	}

	if err := tr.TransformRequest(req, ctx); err != nil {
		t.Fatalf("TransformRequest error: %v", err)
	}

	if req["top_k"] != float64(40) {
		t.Errorf("top_k = %v, want 40", req["top_k"])
	}
	if req["presence_penalty"] != 0.5 {
		t.Errorf("presence_penalty = %v, want 0.5", req["presence_penalty"])
	}
}

func TestCustomParams_DoesNotOverwriteExisting(t *testing.T) {
	tr := &customParamsTransform{}
	ctx := NewTransformContext("model", "provider")
	ctx.Params = map[string]interface{}{
		"model":       "should-not-overwrite",
		"temperature": 0.9,
	}

	req := map[string]interface{}{
		"model":       "original-model",
		"temperature": 0.7,
		"messages":    []interface{}{},
	}

	if err := tr.TransformRequest(req, ctx); err != nil {
		t.Fatalf("TransformRequest error: %v", err)
	}

	if req["model"] != "original-model" {
		t.Errorf("model should not be overwritten, got %v", req["model"])
	}
	if req["temperature"] != 0.7 {
		t.Errorf("temperature should not be overwritten, got %v", req["temperature"])
	}
}

func TestCustomParams_NilParamsIsNoOp(t *testing.T) {
	tr := &customParamsTransform{}
	ctx := NewTransformContext("model", "provider")
	// ctx.Params is nil by default

	req := map[string]interface{}{
		"model": "test",
	}

	if err := tr.TransformRequest(req, ctx); err != nil {
		t.Fatalf("TransformRequest error: %v", err)
	}

	if len(req) != 1 {
		t.Errorf("expected request unchanged, got %d keys", len(req))
	}
}
