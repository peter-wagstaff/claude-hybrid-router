package translate

import "testing"

func TestDeepseekRenamesMaxTokens(t *testing.T) {
	tr := newDeepseekTransform()
	ctx := NewTransformContext("model", "deepseek")

	tests := []struct {
		name    string
		req     map[string]interface{}
		wantKey string
		wantVal interface{} // expected value under wantKey, or nil if key should be absent
	}{
		{
			name:    "renames max_completion_tokens to max_tokens",
			req:     map[string]interface{}{"max_completion_tokens": float64(65536)},
			wantKey: "max_tokens",
			wantVal: float64(65536),
		},
		{
			name:    "small value also renamed",
			req:     map[string]interface{}{"max_completion_tokens": float64(4096)},
			wantKey: "max_tokens",
			wantVal: float64(4096),
		},
		{
			name:    "absent max_completion_tokens is not added",
			req:     map[string]interface{}{"model": "test"},
			wantKey: "max_tokens",
			wantVal: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tr.TransformRequest(tt.req, ctx)

			if tt.wantVal == nil {
				if _, ok := tt.req[tt.wantKey]; ok {
					t.Errorf("should not add %s if max_completion_tokens not present", tt.wantKey)
				}
				return
			}

			val, ok := tt.req[tt.wantKey]
			if !ok || val != tt.wantVal {
				t.Errorf("expected %s=%v, got %v", tt.wantKey, tt.wantVal, val)
			}
			if _, ok := tt.req["max_completion_tokens"]; ok {
				t.Error("max_completion_tokens should be removed after rename")
			}
		})
	}
}
