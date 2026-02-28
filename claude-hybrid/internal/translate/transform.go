package translate

// SchemaTransformer cleans tool parameter schemas for provider compatibility.
type SchemaTransformer interface {
	// CleanSchema removes unsupported fields from a JSON Schema object, recursively.
	CleanSchema(schema map[string]interface{})
}

// fieldStripper removes a fixed set of fields recursively.
type fieldStripper struct {
	fields []string
}

func (s *fieldStripper) CleanSchema(m map[string]interface{}) {
	for _, f := range s.fields {
		delete(m, f)
	}
	recurseSchema(m, s)
}

// geminiTransformer strips Gemini-incompatible fields and sanitizes format values.
type geminiTransformer struct{}

var geminiAllowedFormats = map[string]bool{
	"date":      true,
	"date-time": true,
	"int32":     true,
	"int64":     true,
	"float":     true,
	"double":    true,
}

func (g *geminiTransformer) CleanSchema(m map[string]interface{}) {
	delete(m, "additionalProperties")
	delete(m, "$schema")
	delete(m, "exclusiveMaximum")
	delete(m, "exclusiveMinimum")

	// Gemini only supports specific format values
	if format, ok := m["format"].(string); ok {
		if !geminiAllowedFormats[format] {
			delete(m, "format")
		}
	}

	recurseSchema(m, g)
}

// recurseSchema applies a transformer to nested schema structures.
func recurseSchema(m map[string]interface{}, t SchemaTransformer) {
	if props, ok := m["properties"].(map[string]interface{}); ok {
		for _, v := range props {
			if prop, ok := v.(map[string]interface{}); ok {
				t.CleanSchema(prop)
			}
		}
	}
	if items, ok := m["items"].(map[string]interface{}); ok {
		t.CleanSchema(items)
	}
	for _, key := range []string{"anyOf", "oneOf", "allOf"} {
		if arr, ok := m[key].([]interface{}); ok {
			for _, v := range arr {
				if sub, ok := v.(map[string]interface{}); ok {
					t.CleanSchema(sub)
				}
			}
		}
	}
}
