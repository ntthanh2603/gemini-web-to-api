package dto

import (
	"encoding/json"
	"fmt"
	"strings"

	models "gemini-web-to-api/internal/commons/models"
)

// ChatCompletionMessageContentPart represents OpenAI multimodal content parts.
type ChatCompletionMessageContentPart struct {
	Type     string        `json:"type"`
	Text     string        `json:"text,omitempty"`
	ImageURL *ImageURLPart `json:"image_url,omitempty"`
}

// ImageURLPart represents OpenAI-compatible image_url content.
type ImageURLPart struct {
	URL string `json:"url"`
}

// ChatCompletionMessage supports both content string and content array.
type ChatCompletionMessage struct {
	Role        string              `json:"role"`
	Content     string              `json:"content"`
	Attachments []models.Attachment `json:"attachments,omitempty"`
}

func (m *ChatCompletionMessage) UnmarshalJSON(data []byte) error {
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

	var parts []ChatCompletionMessageContentPart
	if err := json.Unmarshal(raw.Content, &parts); err != nil {
		return fmt.Errorf("unsupported messages.content format: %w", err)
	}

	textParts := make([]string, 0, len(parts))
	attachments := make([]models.Attachment, 0)
	for _, p := range parts {
		switch strings.ToLower(strings.TrimSpace(p.Type)) {
		case "text":
			if p.Text != "" {
				textParts = append(textParts, p.Text)
			}
		case "image_url", "input_image":
			if p.ImageURL == nil {
				continue
			}
			if attachment, ok := attachmentFromDataURL(p.ImageURL.URL, len(attachments)+1); ok {
				attachments = append(attachments, attachment)
			}
		}
	}
	m.Content = strings.Join(textParts, "\n")
	m.Attachments = attachments
	return nil
}

func (m ChatCompletionMessage) ToModelMessage() models.Message {
	return models.Message{
		Role:        m.Role,
		Content:     m.Content,
		Attachments: m.Attachments,
	}
}

func attachmentFromDataURL(value string, index int) (models.Attachment, bool) {
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(value, "data:") {
		return models.Attachment{}, false
	}

	metaAndData := strings.SplitN(strings.TrimPrefix(value, "data:"), ",", 2)
	if len(metaAndData) != 2 {
		return models.Attachment{}, false
	}

	meta := metaAndData[0]
	if !strings.Contains(strings.ToLower(meta), ";base64") {
		return models.Attachment{}, false
	}
	mimeType := strings.TrimSuffix(meta, ";base64")
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	return models.Attachment{
		Name:     fmt.Sprintf("image_%d%s", index, extensionFromMimeType(mimeType)),
		MimeType: mimeType,
		Data:     metaAndData[1],
	}, true
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

// ToolFunctionDefinition represents OpenAI tool function schema
type ToolFunctionDefinition struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty" swagignore:"true"` // @SchemaType object
}

// ToolDefinition represents OpenAI tool definition
type ToolDefinition struct {
	Type     string                 `json:"type"`
	Function ToolFunctionDefinition `json:"function"`
}

// ToolChoiceFunction represents forced tool function choice
type ToolChoiceFunction struct {
	Name string `json:"name"`
}

// ToolChoiceObject represents object-form tool_choice
type ToolChoiceObject struct {
	Type     string             `json:"type,omitempty"`
	Function ToolChoiceFunction `json:"function,omitempty"`
}

// ChatCompletionRequest represents OpenAI chat completion request
type ChatCompletionRequest struct {
	Model         string                  `json:"model"`
	Messages      []ChatCompletionMessage `json:"messages"`
	Tools         []ToolDefinition        `json:"tools,omitempty"`
	ToolChoiceRaw json.RawMessage         `json:"tool_choice,omitempty" swagignore:"true"` // @SchemaType object
	Stream        bool                    `json:"stream,omitempty"`
	Temperature   float32                 `json:"temperature,omitempty"`
	MaxTokens     int                     `json:"max_tokens,omitempty"`
}

func (r ChatCompletionRequest) ToModelMessages() []models.Message {
	result := make([]models.Message, 0, len(r.Messages))
	for _, msg := range r.Messages {
		result = append(result, msg.ToModelMessage())
	}
	return result
}

func (r ChatCompletionRequest) ToolChoiceMode() string {
	if len(r.ToolChoiceRaw) == 0 || string(r.ToolChoiceRaw) == "null" {
		return "auto"
	}

	var mode string
	if err := json.Unmarshal(r.ToolChoiceRaw, &mode); err == nil {
		mode = strings.ToLower(strings.TrimSpace(mode))
		switch mode {
		case "none", "auto", "required":
			return mode
		default:
			return "auto"
		}
	}

	var obj ToolChoiceObject
	if err := json.Unmarshal(r.ToolChoiceRaw, &obj); err == nil {
		if strings.EqualFold(obj.Type, "function") && strings.TrimSpace(obj.Function.Name) != "" {
			return "function"
		}
		objType := strings.ToLower(strings.TrimSpace(obj.Type))
		switch objType {
		case "none", "auto", "required":
			return objType
		}
	}

	return "auto"
}

func (r ChatCompletionRequest) ForcedToolName() string {
	if len(r.ToolChoiceRaw) == 0 || string(r.ToolChoiceRaw) == "null" {
		return ""
	}
	var obj ToolChoiceObject
	if err := json.Unmarshal(r.ToolChoiceRaw, &obj); err != nil {
		return ""
	}
	if !strings.EqualFold(obj.Type, "function") {
		return ""
	}
	return strings.TrimSpace(obj.Function.Name)
}

func (r ChatCompletionRequest) HasToolsEnabled() bool {
	return len(r.Tools) > 0 && r.ToolChoiceMode() != "none"
}

// ChatCompletionToolCallFunction represents OpenAI tool call function payload
type ChatCompletionToolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// ChatCompletionToolCall represents OpenAI tool call payload
type ChatCompletionToolCall struct {
	ID       string                         `json:"id"`
	Type     string                         `json:"type"`
	Function ChatCompletionToolCallFunction `json:"function"`
}

// ChatCompletionResponseMessage represents assistant message payload
type ChatCompletionResponseMessage struct {
	Role             string                   `json:"role"`
	Content          string                   `json:"content,omitempty"`
	ReasoningContent string                   `json:"reasoning_content,omitempty"`
	ToolCalls        []ChatCompletionToolCall `json:"tool_calls,omitempty"`
}

// ChatCompletionResponse represents OpenAI chat completion response
type ChatCompletionResponse struct {
	ID             string       `json:"id"`
	Object         string       `json:"object"`
	Created        int64        `json:"created"`
	Model          string       `json:"model"`
	RequestedModel string       `json:"requested_model,omitempty"`
	Choices        []Choice     `json:"choices"`
	Usage          models.Usage `json:"usage"`
}

// Choice represents a response choice
type Choice struct {
	Index        int                           `json:"index"`
	Message      ChatCompletionResponseMessage `json:"message"`
	FinishReason string                        `json:"finish_reason"`
}

// ChatCompletionChunkDeltaToolFunction represents streamed tool function payload
type ChatCompletionChunkDeltaToolFunction struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

// ChatCompletionChunkDeltaToolCall represents streamed tool call payload
type ChatCompletionChunkDeltaToolCall struct {
	Index    int                                  `json:"index"`
	ID       string                               `json:"id,omitempty"`
	Type     string                               `json:"type,omitempty"`
	Function ChatCompletionChunkDeltaToolFunction `json:"function,omitempty"`
}

// ChatCompletionChunkDelta represents OpenAI streaming delta
type ChatCompletionChunkDelta struct {
	Role             string                             `json:"role,omitempty"`
	Content          string                             `json:"content,omitempty"`
	ReasoningContent string                             `json:"reasoning_content,omitempty"`
	ToolCalls        []ChatCompletionChunkDeltaToolCall `json:"tool_calls,omitempty"`
}

// ChatCompletionChunk represents a streaming chunk
type ChatCompletionChunk struct {
	ID      string        `json:"id"`
	Object  string        `json:"object"`
	Created int64         `json:"created"`
	Model   string        `json:"model"`
	Choices []ChunkChoice `json:"choices"`
}

// ChunkChoice represents a choice in a chunk
type ChunkChoice struct {
	Index        int                      `json:"index"`
	Delta        ChatCompletionChunkDelta `json:"delta"`
	FinishReason string                   `json:"finish_reason,omitempty"`
}

// ImageGenerationRequest represents an OpenAI-compatible image generation request.
type ImageGenerationRequest struct {
	Model          string `json:"model,omitempty"`
	Prompt         string `json:"prompt"`
	N              int    `json:"n,omitempty"`
	Size           string `json:"size,omitempty"`
	ResponseFormat string `json:"response_format,omitempty"`
}

// ImageGenerationResponse represents an OpenAI-compatible image generation response.
type ImageGenerationResponse struct {
	Created int64                 `json:"created"`
	Data    []ImageGenerationData `json:"data"`
}

// ImageGenerationData represents one generated image.
type ImageGenerationData struct {
	URL           string `json:"url,omitempty"`
	B64JSON       string `json:"b64_json,omitempty"`
	RevisedPrompt string `json:"revised_prompt,omitempty"`
}
