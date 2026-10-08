package dto

import (
	"encoding/json"
	models "gemini-web-to-api/internal/commons/models"
)

// MessageRequest represents the specialized Claude request body
type MessageRequest struct {
	Model     string           			`json:"model"`
	MaxTokens int              			`json:"max_tokens"`
	Messages  []models.Message 			`json:"messages"`
	System    AnthropicSystemContent    `json:"system,omitempty"`
	Stream    bool             			`json:"stream,omitempty"`
	Tools     []Tool           			`json:"tools,omitempty"`
	ToolChoice *ToolChoice     			`json:"tool_choice,omitempty"`
}

// Tool represents a tool available to the model
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema" swagignore:"true"` // @SchemaType object
}

// ToolChoice represents how the model should use tools
type ToolChoice struct {
	Type string `json:"type"` // "auto", "any", "tool"
	Name string `json:"name,omitempty"`
}

// MessageResponse represents the non-streaming response body
type MessageResponse struct {
	ID         string          `json:"id"`
	Type       string          `json:"type"` // "message"
	Role       string          `json:"role"` // "assistant"
	Model      string          `json:"model"`
	Content    []ConfigContent `json:"content"`
	StopReason string          `json:"stop_reason"`
	Usage      models.Usage    `json:"usage"`
	// Canvases lists files written by Gemini's Canvas tool ("-canvas" models).
	Canvases []models.CanvasFile `json:"canvases,omitempty"`
}

// ConfigContent represents the content block in a response
type ConfigContent struct {
	Type     string                 `json:"type"` // "text", "tool_use", "thinking"
	Text     string                 `json:"text,omitempty"`
	Thinking string                 `json:"thinking,omitempty"`
	ID       string                 `json:"id,omitempty"`    // for tool_use
	Name     string                 `json:"name,omitempty"`  // for tool_use
	Input    map[string]interface{} `json:"input,omitempty"` // for tool_use
}

// StreamEvent represents a streaming event
type StreamEvent struct {
	Type         string           `json:"type"`                    // e.g. message_start, content_block_delta
	Message      *MessageResponse `json:"message,omitempty"`       // present in message_start
	Index        int              `json:"index,omitempty"`         // present in content_block_start/delta
	ContentBlock *ConfigContent   `json:"content_block,omitempty"` // present in content_block_start
	DeltaField   *models.Delta    `json:"delta,omitempty"`         // present in content_block_delta
	StopReason   string           `json:"stop_reason,omitempty"`   // present in message_stop
	UsageField   *models.Usage    `json:"usage,omitempty"`         // present in message_delta (optional?) but essential in message_stop sometimes
	Error        *Error           `json:"error,omitempty"`         // present in error event
}

type Error struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}
