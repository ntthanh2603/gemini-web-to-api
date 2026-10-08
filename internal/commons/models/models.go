package models

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Message represents a chat message (shared across OpenAI, Claude, etc)
type Message struct {
	Role        string       `json:"role"`
	Content     string       `json:"content"`
	Attachments []Attachment `json:"attachments,omitempty"`
}

// Attachment represents inline binary input extracted from multimodal API payloads.
type Attachment struct {
	Name     string `json:"name,omitempty"`
	MimeType string `json:"mime_type,omitempty"`
	Data     string `json:"data,omitempty"`
}

func (m *Message) UnmarshalJSON(data []byte) error {
	type rawMessage struct {
		Role       string            `json:"role"`
		Content    json.RawMessage   `json:"content"`
		ToolCalls  []json.RawMessage `json:"tool_calls,omitempty"`
		ToolCallID string            `json:"tool_call_id,omitempty"`
	}

	var raw rawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	m.Role = raw.Role
	m.Content = ""
	m.Attachments = nil

	// Check if this is an OpenAI tool result message (role = tool)
	if raw.ToolCallID != "" {
		var contentStr string
		if len(raw.Content) > 0 && string(raw.Content) != "null" {
			if err := json.Unmarshal(raw.Content, &contentStr); err == nil {
				m.Content = fmt.Sprintf("[Tool Result for ID %s]: %s", raw.ToolCallID, contentStr)
			} else {
				m.Content = fmt.Sprintf("[Tool Result for ID %s]: %s", raw.ToolCallID, string(raw.Content))
			}
		} else {
			m.Content = fmt.Sprintf("[Tool Result for ID %s]: ", raw.ToolCallID)
		}
		return nil
	}

	// Check if this is an OpenAI assistant message containing tool calls
	if len(raw.ToolCalls) > 0 {
		var textParts []string
		var contentStr string
		if len(raw.Content) > 0 && string(raw.Content) != "null" {
			if err := json.Unmarshal(raw.Content, &contentStr); err == nil && contentStr != "" {
				textParts = append(textParts, contentStr)
			}
		}
		for _, tcRaw := range raw.ToolCalls {
			var tc struct {
				ID       string `json:"id"`
				Type     string `json:"type"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			}
			if err := json.Unmarshal(tcRaw, &tc); err == nil {
				textParts = append(textParts, fmt.Sprintf("[Call Tool: %s with ID %s and Input: %s]", tc.Function.Name, tc.ID, tc.Function.Arguments))
			}
		}
		m.Content = strings.Join(textParts, "\n")
		return nil
	}

	if len(raw.Content) == 0 || string(raw.Content) == "null" {
		return nil
	}

	var contentStr string
	if err := json.Unmarshal(raw.Content, &contentStr); err == nil {
		m.Content = contentStr
		return nil
	}

	var parts []struct {
		Type      string          `json:"type"`
		Text      string          `json:"text,omitempty"`
		ID        string          `json:"id,omitempty"`
		Name      string          `json:"name,omitempty"`
		Input     json.RawMessage `json:"input,omitempty"`
		ToolUseID string          `json:"tool_use_id,omitempty"`
		Content   json.RawMessage `json:"content,omitempty"`
		Source    *struct {
			Type      string `json:"type"`
			MediaType string `json:"media_type"`
			Data      string `json:"data"`
		} `json:"source,omitempty"`
	}
	if err := json.Unmarshal(raw.Content, &parts); err != nil {
		return fmt.Errorf("unsupported messages.content format: %w", err)
	}

	textParts := make([]string, 0, len(parts))
	for i, part := range parts {
		switch strings.ToLower(strings.TrimSpace(part.Type)) {
		case "text":
			if strings.TrimSpace(part.Text) != "" {
				textParts = append(textParts, part.Text)
			}
		case "image":
			if part.Source == nil || strings.ToLower(part.Source.Type) != "base64" || part.Source.Data == "" {
				continue
			}
			name := fmt.Sprintf("image_%d%s", i+1, extensionFromMimeType(part.Source.MediaType))
			m.Attachments = append(m.Attachments, Attachment{
				Name:     name,
				MimeType: part.Source.MediaType,
				Data:     part.Source.Data,
			})
		case "tool_use":
			inputStr := string(part.Input)
			textParts = append(textParts, fmt.Sprintf("[Call Tool: %s with ID %s and Input: %s]", part.Name, part.ID, inputStr))
		case "tool_result":
			var rContent string
			if len(part.Content) > 0 && string(part.Content) != "null" {
				if err := json.Unmarshal(part.Content, &rContent); err == nil {
					textParts = append(textParts, fmt.Sprintf("[Tool Result for ID %s]: %s", part.ToolUseID, rContent))
				} else {
					var innerParts []struct {
						Type string `json:"type"`
						Text string `json:"text,omitempty"`
					}
					if err := json.Unmarshal(part.Content, &innerParts); err == nil {
						var innerText []string
						for _, ip := range innerParts {
							if ip.Type == "text" {
								innerText = append(innerText, ip.Text)
							}
						}
						textParts = append(textParts, fmt.Sprintf("[Tool Result for ID %s]: %s", part.ToolUseID, strings.Join(innerText, "\n")))
					} else {
						textParts = append(textParts, fmt.Sprintf("[Tool Result for ID %s]: %s", part.ToolUseID, string(part.Content)))
					}
				}
			} else {
				textParts = append(textParts, fmt.Sprintf("[Tool Result for ID %s]: ", part.ToolUseID))
			}
		}
	}
	m.Content = strings.Join(textParts, "\n")
	return nil
}

func extensionFromMimeType(mimeType string) string {
	switch strings.ToLower(strings.TrimSpace(mimeType)) {
	case "image/jpeg", "image/jpg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/webp":
		return ".webp"
	case "image/gif":
		return ".gif"
	default:
		return ".bin"
	}
}

// CanvasFile is a code file or document Gemini wrote with its Canvas tool.
// Content is the raw file content (code is not wrapped in a Markdown fence).
type CanvasFile struct {
	ID       string `json:"id"`
	Title    string `json:"title,omitempty"`
	FileName string `json:"file_name"`
	Type     string `json:"type"` // "code" or "document"
	Language string `json:"language,omitempty"`
	MimeType string `json:"mime_type"`
	Content  string `json:"content"`
}

// ModelListResponse represents the list of models
type ModelListResponse struct {
	Object string      `json:"object,omitempty"`
	Data   []ModelData `json:"data"`
}

// ModelData represents a single model in the list
type ModelData struct {
	ID          string `json:"id"`
	Object      string `json:"object,omitempty"`
	Type        string `json:"type,omitempty"`
	Created     int64  `json:"created,omitempty"`
	CreatedAt   int64  `json:"created_at,omitempty"`
	OwnedBy     string `json:"owned_by,omitempty"`
	DisplayName string `json:"display_name,omitempty"`
}

// Delta represents the delta content in a chunk
type Delta struct {
	Type        string `json:"type,omitempty"`         // "text_delta", "input_json_delta"
	Content     string `json:"content,omitempty"`      // for OpenAI
	Text        string `json:"text,omitempty"`         // for Claude
	Thinking    string `json:"thinking,omitempty"`     // for Claude
	PartialJSON string `json:"partial_json,omitempty"` // for Claude tool use
	StopReason  string `json:"stop_reason,omitempty"`  // for Claude
	Role        string `json:"role,omitempty"`
}

// Usage represents token usage (compatible format)
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
	InputTokens      int `json:"input_tokens,omitempty"`
	OutputTokens     int `json:"output_tokens,omitempty"`
}

// ErrorResponse represents a standard error response
type ErrorResponse struct {
	Error interface{} `json:"error,omitempty"` // Can be string or map[string]interface{}
	Code  string      `json:"code,omitempty"`
	Type  string      `json:"type,omitempty"`
}

// Error represents error details (legacy struct format)
type Error struct {
	Message string `json:"message"`
	Type    string `json:"type,omitempty"`
	Code    string `json:"code,omitempty"`
}

// EmbeddingsRequest represents a request for embeddings
type EmbeddingsRequest struct {
	Input interface{} `json:"input"`
	Model string      `json:"model"`
}

// EmbeddingsResponse represents embeddings response
type EmbeddingsResponse struct {
	Object string      `json:"object"`
	Data   []Embedding `json:"data"`
	Model  string      `json:"model"`
	Usage  Usage       `json:"usage"`
}

// Embedding represents a single embedding
type Embedding struct {
	Object    string    `json:"object"`
	Index     int       `json:"index"`
	Embedding []float32 `json:"embedding"`
}
