package translate

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCleanCache_StripsCacheControl(t *testing.T) {
	tr := &cleanCacheTransform{}
	ctx := NewTransformContext("model", "provider")

	req := map[string]interface{}{
		"messages": []interface{}{
			map[string]interface{}{
				"role":          "user",
				"cache_control": map[string]interface{}{"type": "ephemeral"},
				"content": []interface{}{
					map[string]interface{}{
						"type":          "text",
						"text":          "hello",
						"cache_control": map[string]interface{}{"type": "ephemeral"},
					},
				},
			},
			map[string]interface{}{
				"role":    "assistant",
				"content": "hi there",
			},
		},
	}

	if err := tr.TransformRequest(req, ctx); err != nil {
		t.Fatalf("TransformRequest error: %v", err)
	}

	b, _ := json.Marshal(req)
	raw := string(b)

	if strings.Contains(raw, "cache_control") {
		t.Errorf("cache_control should be stripped, got: %s", raw)
	}
	if !strings.Contains(raw, "hello") {
		t.Error("message content should be preserved")
	}
}

func TestCleanCache_NoOpWithoutCacheControl(t *testing.T) {
	tr := &cleanCacheTransform{}
	ctx := NewTransformContext("model", "provider")

	req := map[string]interface{}{
		"messages": []interface{}{
			map[string]interface{}{
				"role":    "user",
				"content": "hello",
			},
		},
	}

	before, _ := json.Marshal(req)
	if err := tr.TransformRequest(req, ctx); err != nil {
		t.Fatalf("TransformRequest error: %v", err)
	}
	after, _ := json.Marshal(req)

	if string(before) != string(after) {
		t.Errorf("expected unchanged request:\nbefore: %s\nafter:  %s", before, after)
	}
}
