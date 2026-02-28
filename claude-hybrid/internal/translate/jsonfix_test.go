package translate

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestFixJSON_ValidPassthrough(t *testing.T) {
	input := `{"city": "SF", "count": 42}`
	result := FixJSON(input)
	if result != input {
		t.Errorf("valid JSON should pass through unchanged, got %s", result)
	}
}

func TestFixJSON_TrailingCommaObject(t *testing.T) {
	result := FixJSON(`{"city": "SF",}`)
	assertValidJSON(t, result)
	assertContains(t, result, `"city"`)
}

func TestFixJSON_TrailingCommaArray(t *testing.T) {
	result := FixJSON(`{"items": [1, 2, 3,]}`)
	assertValidJSON(t, result)
}

func TestFixJSON_SingleQuotes(t *testing.T) {
	result := FixJSON(`{'city': 'SF'}`)
	assertValidJSON(t, result)
	assertContains(t, result, `"city"`)
	assertContains(t, result, `"SF"`)
}

func TestFixJSON_TruncatedObject(t *testing.T) {
	result := FixJSON(`{"city": "SF"`)
	assertValidJSON(t, result)
}

func TestFixJSON_TruncatedArray(t *testing.T) {
	result := FixJSON(`{"items": ["a", "b"`)
	assertValidJSON(t, result)
}

func TestFixJSON_TruncatedNested(t *testing.T) {
	result := FixJSON(`{"a": {"b": [1, 2`)
	assertValidJSON(t, result)
}

func TestFixJSON_EmptyFallback(t *testing.T) {
	result := FixJSON(`not json at all!!!`)
	if result != "{}" {
		t.Errorf("expected {} fallback, got %s", result)
	}
}

func TestFixJSON_EmptyString(t *testing.T) {
	result := FixJSON("")
	if result != "{}" {
		t.Errorf("expected {} for empty, got %s", result)
	}
}

func TestFixJSON_AlreadyValid(t *testing.T) {
	inputs := []string{`{}`, `[]`, `{"a":1}`, `[1,2,3]`}
	for _, input := range inputs {
		if FixJSON(input) != input {
			t.Errorf("valid input %s should pass through", input)
		}
	}
}

func assertValidJSON(t *testing.T, s string) {
	t.Helper()
	if !json.Valid([]byte(s)) {
		t.Errorf("expected valid JSON, got: %s", s)
	}
}

func assertContains(t *testing.T, s, substr string) {
	t.Helper()
	if !strings.Contains(s, substr) {
		t.Errorf("expected %q to contain %q", s, substr)
	}
}
