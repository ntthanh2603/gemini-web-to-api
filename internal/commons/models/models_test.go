package models

import (
	"encoding/json"
	"testing"
)

func TestExtensionForMimeType(t *testing.T) {
	for mimeType, want := range map[string]string{
		"image/jpeg":                ".jpg",
		"Application/PDF":           ".pdf",
		"text/plain; charset=utf-8": ".txt",
		"text/csv;charset=UTF-8":    ".csv",
		"application/vnd.openxmlformats-officedocument.wordprocessingml.document": ".docx",
		"application/octet-stream": ".bin",
		"":                         ".bin",
		"not a mime type":          ".bin",
	} {
		if got := ExtensionForMimeType(mimeType); got != want {
			t.Errorf("ExtensionForMimeType(%q) = %q, want %q", mimeType, got, want)
		}
	}
}

func TestMessage_UnmarshalJSON(t *testing.T) {
	tests := []struct {
		name        string
		jsonData    string
		wantRole    string
		wantContent string
	}{
		{
			name:        "Simple text message",
			jsonData:    `{"role": "user", "content": "Hello"}`,
			wantRole:    "user",
			wantContent: "Hello",
		},
		{
			name: "Claude tool_use message",
			jsonData: `{
				"role": "assistant",
				"content": [
					{"type": "text", "text": "Let me run this"},
					{"type": "tool_use", "id": "toolu_123", "name": "run_command", "input": {"cmd": "ls"}}
				]
			}`,
			wantRole:    "assistant",
			wantContent: "Let me run this\n[Call Tool: run_command with ID toolu_123 and Input: {\"cmd\": \"ls\"}]",
		},
		{
			name: "Claude tool_result message with string content",
			jsonData: `{
				"role": "user",
				"content": [
					{"type": "tool_result", "tool_use_id": "toolu_123", "content": "file1.txt\nfile2.txt"}
				]
			}`,
			wantRole:    "user",
			wantContent: "[Tool Result for ID toolu_123]: file1.txt\nfile2.txt",
		},
		{
			name: "Claude tool_result message with structured content blocks",
			jsonData: `{
				"role": "user",
				"content": [
					{
						"type": "tool_result",
						"tool_use_id": "toolu_123",
						"content": [{"type": "text", "text": "result content"}]
					}
				]
			}`,
			wantRole:    "user",
			wantContent: "[Tool Result for ID toolu_123]: result content",
		},
		{
			name: "OpenAI tool result message",
			jsonData: `{
				"role": "tool",
				"tool_call_id": "call_123",
				"content": "command output"
			}`,
			wantRole:    "tool",
			wantContent: "[Tool Result for ID call_123]: command output",
		},
		{
			name: "OpenAI tool_calls message",
			jsonData: `{
				"role": "assistant",
				"content": "Preparing to execute",
				"tool_calls": [
					{
						"id": "call_123",
						"type": "function",
						"function": {
							"name": "read_file",
							"arguments": "{\"path\":\"main.go\"}"
						}
					}
				]
			}`,
			wantRole:    "assistant",
			wantContent: "Preparing to execute\n[Call Tool: read_file with ID call_123 and Input: {\"path\":\"main.go\"}]",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var msg Message
			if err := json.Unmarshal([]byte(tt.jsonData), &msg); err != nil {
				t.Fatalf("Failed to unmarshal JSON: %v", err)
			}
			if msg.Role != tt.wantRole {
				t.Errorf("Role got = %q, want %q", msg.Role, tt.wantRole)
			}
			if msg.Content != tt.wantContent {
				t.Errorf("Content got = %q, want %q", msg.Content, tt.wantContent)
			}
		})
	}
}
