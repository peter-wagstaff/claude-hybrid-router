package translate

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestGenericTransformer(t *testing.T) {
	schema := map[string]interface{}{
		"type":                 "object",
		"additionalProperties": false,
		"strict":               true,
		"$schema":              "http://json-schema.org/draft-07/schema#",
		"properties": map[string]interface{}{
			"name": map[string]interface{}{
				"type":                 "string",
				"additionalProperties": false,
			},
		},
		"required": []string{"name"},
	}

	tr := &fieldStripper{fields: []string{"additionalProperties", "$schema", "strict"}}
	tr.CleanSchema(schema)

	if _, ok := schema["additionalProperties"]; ok {
		t.Error("additionalProperties not stripped")
	}
	if _, ok := schema["strict"]; ok {
		t.Error("strict not stripped")
	}
	if _, ok := schema["$schema"]; ok {
		t.Error("$schema not stripped")
	}
	if _, ok := schema["required"]; !ok {
		t.Error("required incorrectly stripped")
	}
	// Check nested
	props := schema["properties"].(map[string]interface{})
	name := props["name"].(map[string]interface{})
	if _, ok := name["additionalProperties"]; ok {
		t.Error("nested additionalProperties not stripped")
	}
}

func TestOpenAITransformer(t *testing.T) {
	schema := map[string]interface{}{
		"type":                 "object",
		"additionalProperties": false,
		"strict":               true,
		"$schema":              "http://json-schema.org/draft-07/schema#",
	}

	tr := &fieldStripper{fields: []string{"strict"}}
	tr.CleanSchema(schema)

	// OpenAI only strips strict
	if _, ok := schema["strict"]; ok {
		t.Error("strict not stripped")
	}
	// OpenAI handles these natively — should keep them
	if _, ok := schema["additionalProperties"]; !ok {
		t.Error("additionalProperties should be kept for OpenAI")
	}
	if _, ok := schema["$schema"]; !ok {
		t.Error("$schema should be kept for OpenAI")
	}
}

func TestGeminiTransformer(t *testing.T) {
	schema := map[string]interface{}{
		"type":                 "object",
		"additionalProperties": false,
		"$schema":              "http://json-schema.org/draft-07/schema#",
		"exclusiveMaximum":     100,
		"exclusiveMinimum":     0,
		"properties": map[string]interface{}{
			"count": map[string]interface{}{
				"type":             "integer",
				"format":           "uint32",
				"exclusiveMaximum": 50,
			},
			"date": map[string]interface{}{
				"type":   "string",
				"format": "date-time",
			},
		},
	}

	tr := &geminiTransformer{}
	tr.CleanSchema(schema)

	for _, field := range []string{"additionalProperties", "$schema", "exclusiveMaximum", "exclusiveMinimum"} {
		if _, ok := schema[field]; ok {
			t.Errorf("%s not stripped at top level", field)
		}
	}

	props := schema["properties"].(map[string]interface{})

	// Nested exclusiveMaximum should be stripped
	count := props["count"].(map[string]interface{})
	if _, ok := count["exclusiveMaximum"]; ok {
		t.Error("nested exclusiveMaximum not stripped")
	}
	// Unsupported format should be stripped
	if _, ok := count["format"]; ok {
		t.Error("unsupported format 'uint32' should be stripped for Gemini")
	}

	// Supported format should be kept
	date := props["date"].(map[string]interface{})
	if date["format"] != "date-time" {
		t.Error("supported format 'date-time' should be kept for Gemini")
	}
}

func TestGeminiArrayItems(t *testing.T) {
	schema := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"tags": map[string]interface{}{
				"type": "array",
				"items": map[string]interface{}{
					"type":                 "string",
					"additionalProperties": false,
				},
			},
		},
	}

	tr := &geminiTransformer{}
	tr.CleanSchema(schema)

	props := schema["properties"].(map[string]interface{})
	tags := props["tags"].(map[string]interface{})
	items := tags["items"].(map[string]interface{})
	if _, ok := items["additionalProperties"]; ok {
		t.Error("additionalProperties not stripped from array items")
	}
}

func TestPerProviderInRequestTranslation(t *testing.T) {
	input := `{
		"model": "x",
		"messages": [{"role": "user", "content": "hi"}],
		"tools": [{
			"name": "test",
			"description": "test",
			"input_schema": {
				"type": "object",
				"additionalProperties": false,
				"strict": true,
				"properties": {"x": {"type": "string"}}
			}
		}]
	}`

	// Translate once (no schema cleaning in RequestToOpenAI anymore)
	out, _ := RequestToOpenAI([]byte(input), "m", 0)

	// OpenAI chain: should keep additionalProperties, strip strict
	chainOpenAI, _ := BuildChain([]string{"schema:openai"})
	ctxOpenAI := NewTransformContext("m", "openai")
	var reqOpenAI map[string]interface{}
	json.Unmarshal(out, &reqOpenAI)
	chainOpenAI.RunRequest(reqOpenAI, ctxOpenAI)
	outOpenAI, _ := json.Marshal(reqOpenAI)

	if !strings.Contains(string(outOpenAI), "additionalProperties") {
		t.Error("OpenAI transform should keep additionalProperties")
	}
	if strings.Contains(string(outOpenAI), "strict") {
		t.Error("OpenAI transform should strip strict")
	}

	// Generic chain: should strip both
	chainGeneric, _ := BuildChain([]string{"schema:generic"})
	ctxGeneric := NewTransformContext("m", "generic")
	var reqGeneric map[string]interface{}
	json.Unmarshal(out, &reqGeneric)
	chainGeneric.RunRequest(reqGeneric, ctxGeneric)
	outGeneric, _ := json.Marshal(reqGeneric)

	if strings.Contains(string(outGeneric), "additionalProperties") {
		t.Error("generic transform should strip additionalProperties")
	}
	if strings.Contains(string(outGeneric), "strict") {
		t.Error("generic transform should strip strict")
	}
}
