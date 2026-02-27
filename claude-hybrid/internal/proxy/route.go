package proxy

import (
	"encoding/json"
	"fmt"
	"io"
	"regexp"
)

var routeMarkerRE = regexp.MustCompile(`<!-- @proxy-local-route:af83e9 model=(\S+) -->`)

// detectLocalRoute checks the system field of a JSON body for a routing marker.
// Returns the model name and the body with the marker stripped, or "" and the original body.
func detectLocalRoute(body []byte) (model string, stripped []byte) {
	if len(body) == 0 {
		return "", body
	}

	var data map[string]interface{}
	if err := json.Unmarshal(body, &data); err != nil {
		return "", body
	}

	system, ok := data["system"]
	if !ok || system == nil {
		return "", body
	}

	switch s := system.(type) {
	case string:
		m := routeMarkerRE.FindStringSubmatch(s)
		if m != nil {
			cleaned := routeMarkerRE.ReplaceAllString(s, "")
			// Trim leading/trailing whitespace left by marker removal
			data["system"] = trimSpace(cleaned)
			out, _ := json.Marshal(data)
			return m[1], out
		}
	case []interface{}:
		for _, block := range s {
			bm, ok := block.(map[string]interface{})
			if !ok {
				continue
			}
			text, ok := bm["text"].(string)
			if !ok {
				continue
			}
			m := routeMarkerRE.FindStringSubmatch(text)
			if m != nil {
				bm["text"] = trimSpace(routeMarkerRE.ReplaceAllString(text, ""))
				out, _ := json.Marshal(data)
				return m[1], out
			}
		}
	}

	return "", body
}

// trimSpace trims whitespace but preserves non-empty content.
func trimSpace(s string) string {
	// Match Python's str.strip()
	result := s
	for len(result) > 0 && (result[0] == ' ' || result[0] == '\t' || result[0] == '\n' || result[0] == '\r') {
		result = result[1:]
	}
	for len(result) > 0 && (result[len(result)-1] == ' ' || result[len(result)-1] == '\t' || result[len(result)-1] == '\n' || result[len(result)-1] == '\r') {
		result = result[:len(result)-1]
	}
	return result
}

// sendLocalStub writes an Anthropic Messages API stub response.
func sendLocalStub(w io.Writer, model string, streaming bool) {
	stubText := fmt.Sprintf("[Local model '%s' request intercepted by proxy — no local provider configured yet]", model)
	msgID := "msg_stub_local_route"

	if streaming {
		writeSSEStub(w, msgID, model, stubText)
	} else {
		writeJSONStub(w, msgID, model, stubText)
	}
}

func writeJSONStub(w io.Writer, msgID, model, stubText string) {
	resp := map[string]interface{}{
		"id":            msgID,
		"type":          "message",
		"role":          "assistant",
		"content":       []map[string]string{{"type": "text", "text": stubText}},
		"model":         model,
		"stop_reason":   "end_turn",
		"stop_sequence": nil,
		"usage":         map[string]int{"input_tokens": 0, "output_tokens": 1},
	}
	respBody, _ := json.Marshal(resp)
	fmt.Fprintf(w, "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n", len(respBody))
	w.Write(respBody)
}

func writeSSEStub(w io.Writer, msgID, model, stubText string) {
	events := []struct {
		event string
		data  interface{}
	}{
		{"message_start", map[string]interface{}{
			"type": "message_start",
			"message": map[string]interface{}{
				"id": msgID, "type": "message", "role": "assistant",
				"content": []interface{}{}, "model": model,
				"stop_reason": nil, "stop_sequence": nil,
				"usage": map[string]int{"input_tokens": 0, "output_tokens": 0},
			},
		}},
		{"content_block_start", map[string]interface{}{
			"type": "content_block_start", "index": 0,
			"content_block": map[string]string{"type": "text", "text": ""},
		}},
		{"content_block_delta", map[string]interface{}{
			"type": "content_block_delta", "index": 0,
			"delta": map[string]string{"type": "text_delta", "text": stubText},
		}},
		{"content_block_stop", map[string]interface{}{
			"type": "content_block_stop", "index": 0,
		}},
		{"message_delta", map[string]interface{}{
			"type":  "message_delta",
			"delta": map[string]interface{}{"stop_reason": "end_turn", "stop_sequence": nil},
			"usage": map[string]int{"output_tokens": 1},
		}},
		{"message_stop", map[string]interface{}{
			"type": "message_stop",
		}},
	}

	var sseBody []byte
	for _, ev := range events {
		data, _ := json.Marshal(ev.data)
		sseBody = append(sseBody, []byte(fmt.Sprintf("event: %s\ndata: %s\n\n", ev.event, data))...)
	}

	fmt.Fprintf(w, "HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nContent-Length: %d\r\n\r\n", len(sseBody))
	w.Write(sseBody)
}
