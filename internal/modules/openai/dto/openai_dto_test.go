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
