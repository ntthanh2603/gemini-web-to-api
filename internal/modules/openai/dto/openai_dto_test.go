package dto

import (
	"encoding/json"
	"testing"
)

func TestChatCompletionMessageUnmarshal(t *testing.T) {
	t.Run("simple string content", func(t *testing.T) {
		raw := `{"role": "user", "content": "hello world"}`
		var msg ChatCompletionMessage
		if err := json.Unmarshal([]byte(raw), &msg); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if msg.Role != "user" || msg.Content != "hello world" {
			t.Errorf("got role=%q content=%q", msg.Role, msg.Content)
		}
	})

	t.Run("tool call in assistant message", func(t *testing.T) {
		raw := `{
			"role": "assistant",
			"content": null,
			"tool_calls": [
				{
					"id": "call_123",
					"type": "function",
					"function": {
						"name": "get_weather",
						"arguments": "{\"city\": \"Tokyo\"}"
					}
				}
			]
		}`
		var msg ChatCompletionMessage
		if err := json.Unmarshal([]byte(raw), &msg); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if msg.Role != "assistant" {
			t.Errorf("got role=%q, want assistant", msg.Role)
		}
		wantSnippet := "[Call Tool: get_weather with ID call_123 and Input: {\"city\": \"Tokyo\"}]"
		if msg.Content != wantSnippet {
			t.Errorf("got content=%q, want=%q", msg.Content, wantSnippet)
		}
	})

	t.Run("tool result message", func(t *testing.T) {
		raw := `{
			"role": "tool",
			"tool_call_id": "call_123",
			"content": "22°C, sunny"
		}`
		var msg ChatCompletionMessage
		if err := json.Unmarshal([]byte(raw), &msg); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if msg.Role != "tool" {
			t.Errorf("got role=%q, want tool", msg.Role)
		}
		wantSnippet := "[Tool Result for ID call_123]: 22°C, sunny"
		if msg.Content != wantSnippet {
			t.Errorf("got content=%q, want=%q", msg.Content, wantSnippet)
		}
	})

	t.Run("multimodal image_url", func(t *testing.T) {
		raw := `{
			"role": "user",
			"content": [
				{"type": "text", "text": "What is in this image?"},
				{"type": "image_url", "image_url": {"url": "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg=="}}
			]
		}`
		var msg ChatCompletionMessage
		if err := json.Unmarshal([]byte(raw), &msg); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if msg.Content != "What is in this image?" {
			t.Errorf("got content=%q", msg.Content)
		}
		if len(msg.Attachments) != 1 {
			t.Fatalf("expected 1 attachment, got %d", len(msg.Attachments))
		}
		if msg.Attachments[0].MimeType != "image/png" {
			t.Errorf("expected mime image/png, got %s", msg.Attachments[0].MimeType)
		}
	})
}

func TestChatCompletionRequestJSONResponseRequested(t *testing.T) {
	const schema = `{"type":"object","required":["name"]}`

	tests := []struct {
		name       string
		body       string
		wantActive bool
		wantSchema string
	}{
		{name: "absent", body: `{}`},
		{name: "text", body: `{"response_format":{"type":"text"}}`},
		{name: "unknown type", body: `{"response_format":{"type":"xml"}}`},
		{name: "json_object", body: `{"response_format":{"type":"json_object"}}`, wantActive: true},
		{
			name:       "json_schema with the OpenAI wrapper",
			body:       `{"response_format":{"type":"json_schema","json_schema":{"name":"person","strict":true,"schema":` + schema + `}}}`,
			wantActive: true,
			wantSchema: schema,
		},
		{
			name:       "json_schema sent inline",
			body:       `{"response_format":{"type":"json_schema","json_schema":` + schema + `}}`,
			wantActive: true,
			wantSchema: schema,
		},
		{
			name:       "uppercase type",
			body:       `{"response_format":{"type":"JSON_OBJECT"}}`,
			wantActive: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var req ChatCompletionRequest
			if err := json.Unmarshal([]byte(tt.body), &req); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			gotSchema, active := req.JSONResponseRequested()
			if active != tt.wantActive {
				t.Fatalf("active = %v, want %v", active, tt.wantActive)
			}
			if string(gotSchema) != tt.wantSchema {
				t.Errorf("schema = %s, want %q", gotSchema, tt.wantSchema)
			}
		})
	}
}
