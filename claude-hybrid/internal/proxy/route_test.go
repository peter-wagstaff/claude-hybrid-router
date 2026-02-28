package proxy

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestDetectLocalRoute_StringSystem(t *testing.T) {
	body, _ := json.Marshal(map[string]interface{}{
		"system":   "<!-- @proxy-local-route:af83e9 model=my_model --> You are helpful",
		"messages": []map[string]string{{"role": "user", "content": "hi"}},
	})

	model, stripped := detectLocalRoute(body)
	if model != "my_model" {
		t.Fatalf("expected my_model, got %q", model)
	}

	var data map[string]interface{}
	json.Unmarshal(stripped, &data)
	sys := data["system"].(string)
	if sys != "You are helpful" {
		t.Errorf("expected stripped system, got %q", sys)
	}
}

func TestDetectLocalRoute_ListSystem(t *testing.T) {
	body, _ := json.Marshal(map[string]interface{}{
		"system": []map[string]string{
			{"type": "text", "text": "<!-- @proxy-local-route:af83e9 model=list_model --> Instructions"},
		},
		"messages": []map[string]string{{"role": "user", "content": "hi"}},
	})

	model, stripped := detectLocalRoute(body)
	if model != "list_model" {
		t.Fatalf("expected list_model, got %q", model)
	}

	var data map[string]interface{}
	json.Unmarshal(stripped, &data)
	blocks := data["system"].([]interface{})
	text := blocks[0].(map[string]interface{})["text"].(string)
	if text != "Instructions" {
		t.Errorf("expected stripped text, got %q", text)
	}
}

func TestDetectLocalRoute_NoMarker(t *testing.T) {
	body, _ := json.Marshal(map[string]interface{}{
		"system":   "You are helpful",
		"messages": []map[string]string{{"role": "user", "content": "hi"}},
	})

	model, stripped := detectLocalRoute(body)
	if model != "" {
		t.Fatalf("expected no model, got %q", model)
	}
	if !bytes.Equal(stripped, body) {
		t.Error("body should be unchanged")
	}
}

func TestDetectLocalRoute_MarkerInMessages(t *testing.T) {
	body, _ := json.Marshal(map[string]interface{}{
		"messages": []map[string]string{{
			"role":    "user",
			"content": "<!-- @proxy-local-route:af83e9 model=my_model --> hello",
		}},
	})

	model, stripped := detectLocalRoute(body)
	if model != "" {
		t.Fatalf("should not detect marker in messages, got %q", model)
	}
	if !bytes.Equal(stripped, body) {
		t.Error("body should be unchanged")
	}
}

func TestDetectLocalRoute_NonJSON(t *testing.T) {
	body := []byte("not json at all")
	model, stripped := detectLocalRoute(body)
	if model != "" {
		t.Fatalf("expected no model, got %q", model)
	}
	if !bytes.Equal(stripped, body) {
		t.Error("body should be unchanged")
	}
}

func TestDetectLocalRoute_EmptyBody(t *testing.T) {
	model, stripped := detectLocalRoute(nil)
	if model != "" || stripped != nil {
		t.Error("expected nil passthrough")
	}
}

func TestSendLocalStub_NonStreaming(t *testing.T) {
	var buf bytes.Buffer
	sendLocalStub(&buf, "test_model", false)

	output := buf.String()
	if !strings.Contains(output, "HTTP/1.1 200 OK") {
		t.Error("missing status line")
	}
	if !strings.Contains(output, "application/json") {
		t.Error("missing content type")
	}

	// Extract body after headers
	parts := strings.SplitN(output, "\r\n\r\n", 2)
	if len(parts) != 2 {
		t.Fatal("could not split headers/body")
	}
	var resp map[string]interface{}
	if err := json.Unmarshal([]byte(parts[1]), &resp); err != nil {
		t.Fatalf("invalid JSON response: %v", err)
	}
	if resp["type"] != "message" {
		t.Error("unexpected type")
	}
	if resp["model"] != "test_model" {
		t.Error("unexpected model")
	}
	content := resp["content"].([]interface{})[0].(map[string]interface{})
	if !strings.Contains(content["text"].(string), "test_model") {
		t.Error("stub text missing model name")
	}
	if !strings.Contains(content["text"].(string), "no local provider configured") {
		t.Error("stub text missing expected message")
	}
}

func TestSendLocalStub_Streaming(t *testing.T) {
	var buf bytes.Buffer
	sendLocalStub(&buf, "test_model", true)

	output := buf.String()
	if !strings.Contains(output, "text/event-stream") {
		t.Error("missing SSE content type")
	}

	assertSSELifecycle(t, output)
	if !strings.Contains(output, "test_model") {
		t.Error("missing model in SSE output")
	}
	if !strings.Contains(output, "no local provider configured") {
		t.Error("missing stub text in SSE output")
	}
}
