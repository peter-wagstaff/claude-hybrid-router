package translate

import "testing"

func TestDeepseekMaxTokensCap(t *testing.T) {
	tr := newDeepseekTransform()
	ctx := NewTransformContext("model", "deepseek")
	req := map[string]interface{}{"max_tokens": float64(65536)}
	tr.TransformRequest(req, ctx)
	if req["max_tokens"].(float64) != 8192 {
		t.Errorf("expected 8192, got %v", req["max_tokens"])
	}
}

func TestDeepseekBelowCapUnchanged(t *testing.T) {
	tr := newDeepseekTransform()
	ctx := NewTransformContext("model", "deepseek")
	req := map[string]interface{}{"max_tokens": float64(4096)}
	tr.TransformRequest(req, ctx)
	if req["max_tokens"].(float64) != 4096 {
		t.Errorf("expected 4096, got %v", req["max_tokens"])
	}
}

func TestDeepseekNoMaxTokens(t *testing.T) {
	tr := newDeepseekTransform()
	ctx := NewTransformContext("model", "deepseek")
	req := map[string]interface{}{"model": "test"}
	tr.TransformRequest(req, ctx)
	if _, ok := req["max_tokens"]; ok {
		t.Error("should not add max_tokens if not present")
	}
}
