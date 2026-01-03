package claude

// MessageRequest represents the specialized Claude request body
type MessageRequest struct {
	Model     string    `json:"model"`
	MaxTokens int       `json:"max_tokens"`
	Messages  []Message `json:"messages"`
	System    string    `json:"system,omitempty"`
	Stream    bool      `json:"stream,omitempty"`
}

// Message represents a single message in the conversation
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// MessageResponse represents the non-streaming response body
type MessageResponse struct {
	ID         string          `json:"id"`
	Type       string          `json:"type"` // "message"
	Role       string          `json:"role"` // "assistant"
	Model      string          `json:"model"`
	Content    []ConfigContent `json:"content"`
	StopReason string          `json:"stop_reason"`
	Usage      Usage           `json:"usage"`
}

// ConfigContent represents the content block in a response
type ConfigContent struct {
	Type string `json:"type"` // "text"
	Text string `json:"text"`
}

// Usage represents token usage stats
type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// Streaming Structures

type StreamEvent struct {
	Type         string      `json:"type"`           // e.g. message_start, content_block_delta
	Message      *MessageResponse `json:"message,omitempty"`     // present in message_start
	Index        int         `json:"index,omitempty"`        // present in content_block_start/delta
	ContentBlock *ConfigContent   `json:"content_block,omitempty"` // present in content_block_start
	Delta        *Delta      `json:"delta,omitempty"`        // present in content_block_delta
	StopReason   string      `json:"stop_reason,omitempty"`  // present in message_stop
	Usage        *Usage      `json:"usage,omitempty"`        // present in message_delta (optional?) but essential in message_stop sometimes
}

type Delta struct {
	Type string `json:"type"` // text_delta
	Text string `json:"text"`
}

// Models Response (Optional)
type ModelListResponse struct {
	Data []ModelData `json:"data"`
}

type ModelData struct {
	ID          string `json:"id"`
	Type        string `json:"type"`
	CreatedAt   int64  `json:"created_at"`
	DisplayName string `json:"display_name"`
}
