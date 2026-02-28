package translate

import "testing"

func TestDeepseekMaxTokensCap(t *testing.T) {
	tr := newDeepseekTransform()
	ctx := NewTransformContext("model", "deepseek")

	tests := []struct {
		name     string
		req      map[string]interface{}
		wantVal  interface{} // expected max_tokens value, or nil if key should be absent
	}{
		{
			name:    "above cap is capped to 8192",
			req:     map[string]interface{}{"max_tokens": float64(65536)},
			wantVal: float64(8192),
		},
		{
			name:    "below cap is unchanged",
			req:     map[string]interface{}{"max_tokens": float64(4096)},
			wantVal: float64(4096),
		},
		{
			name:    "absent max_tokens is not added",
			req:     map[string]interface{}{"model": "test"},
			wantVal: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tr.TransformRequest(tt.req, ctx)
			val, ok := tt.req["max_tokens"]
			if tt.wantVal == nil {
				if ok {
					t.Errorf("should not add max_tokens if not present, got %v", val)
				}
			} else {
				if !ok || val != tt.wantVal {
					t.Errorf("expected %v, got %v", tt.wantVal, val)
				}
			}
		})
	}
}
