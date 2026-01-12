package models

// Message represents a chat message (shared across OpenAI, Claude, etc)
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
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
	Type    string `json:"type,omitempty"`    // "text_delta"
	Content string `json:"content,omitempty"` // for OpenAI
	Text    string `json:"text,omitempty"`    // for Claude
	Role    string `json:"role,omitempty"`
}

// Usage represents token usage (compatible format)
type Usage struct {
	PromptTokens     int `json:"prompt_tokens,omitempty"`
	CompletionTokens int `json:"completion_tokens,omitempty"`
	TotalTokens      int `json:"total_tokens,omitempty"`
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

// ============= OpenAI Models =============

// ChatCompletionRequest represents OpenAI chat completion request
type ChatCompletionRequest struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	Stream      bool      `json:"stream,omitempty"`
	Temperature float32   `json:"temperature,omitempty"`
	MaxTokens   int       `json:"max_tokens,omitempty"`
}

// ChatCompletionResponse represents OpenAI chat completion response
type ChatCompletionResponse struct {
	ID      string   `json:"id"`
	Object  string   `json:"object"`
	Created int64    `json:"created"`
	Model   string   `json:"model"`
	Choices []Choice `json:"choices"`
	Usage   Usage    `json:"usage"`
}

// Choice represents a response choice
type Choice struct {
	Index        int     `json:"index"`
	Message      Message `json:"message"`
	FinishReason string  `json:"finish_reason"`
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
	Index        int    `json:"index"`
	Delta        Delta  `json:"delta"`
	FinishReason string `json:"finish_reason,omitempty"`
}

// ============= Claude Models =============

// MessageRequest represents the specialized Claude request body
type MessageRequest struct {
	Model     string    `json:"model"`
	MaxTokens int       `json:"max_tokens"`
	Messages  []Message `json:"messages"`
	System    string    `json:"system,omitempty"`
	Stream    bool      `json:"stream,omitempty"`
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

// StreamEvent represents a streaming event
type StreamEvent struct {
	Type         string           `json:"type"`                    // e.g. message_start, content_block_delta
	Message      *MessageResponse `json:"message,omitempty"`       // present in message_start
	Index        int              `json:"index,omitempty"`         // present in content_block_start/delta
	ContentBlock *ConfigContent   `json:"content_block,omitempty"` // present in content_block_start
	DeltaField   *Delta           `json:"delta,omitempty"`         // present in content_block_delta
	StopReason   string           `json:"stop_reason,omitempty"`   // present in message_stop
	UsageField   *Usage           `json:"usage,omitempty"`         // present in message_delta (optional?) but essential in message_stop sometimes
}

// ============= Gemini Models =============

// GeminiModelsResponse represents the response from /v1beta/models
type GeminiModelsResponse struct {
	Models []GeminiModel `json:"models"`
}

// GeminiModel represents a single Gemini model
type GeminiModel struct {
	Name                       string   `json:"name"`
	DisplayName                string   `json:"displayName"`
	Description                string   `json:"description,omitempty"`
	Version                    string   `json:"version,omitempty"`
	SupportedGenerationMethods []string `json:"supportedGenerationMethods"`
}

// GeminiGenerateRequest represents a Gemini generate request
type GeminiGenerateRequest struct {
	Contents        []Content             `json:"contents"`
	GenerationConfig *GenerationConfig    `json:"generationConfig,omitempty"`
	Safety           []map[string]string  `json:"safety_settings,omitempty"`
}

// Content represents a content block in Gemini API
type Content struct {
	Role  string `json:"role,omitempty"`
	Parts []Part `json:"parts"`
}

// Part represents a part of content
type Part struct {
	Text       string      `json:"text,omitempty"`
	InlineData *InlineData `json:"inlineData,omitempty"`
}

// InlineData represents inline data (e.g., images)
type InlineData struct {
	MimeType string `json:"mimeType"`
	Data     string `json:"data"`
}

// GenerationConfig represents generation configuration
type GenerationConfig struct {
	Temperature     float32 `json:"temperature,omitempty"`
	TopP            float32 `json:"topP,omitempty"`
	TopK            int32   `json:"topK,omitempty"`
	MaxOutputTokens int32   `json:"maxOutputTokens,omitempty"`
}

// GeminiGenerateResponse represents a Gemini generate response
type GeminiGenerateResponse struct {
	Candidates   []Candidate    `json:"candidates"`
	UsageMetadata *UsageMetadata `json:"usageMetadata,omitempty"`
}

// Candidate represents a candidate response
type Candidate struct {
	Index        int    `json:"index"`
	Content      Content `json:"content"`
	FinishReason string `json:"finishReason,omitempty"`
	FinishMessage string `json:"finishMessage,omitempty"`
}

// UsageMetadata represents usage metadata
type UsageMetadata struct {
	PromptTokenCount     int32 `json:"promptTokenCount"`
	CandidatesTokenCount int32 `json:"candidatesTokenCount"`
	TotalTokenCount      int32 `json:"totalTokenCount"`
}

// ============= Request/Response Common Types =============

// EmbeddingsRequest represents a request for embeddings
type EmbeddingsRequest struct {
	Input interface{} `json:"input"`
	Model string      `json:"model"`
}

// EmbeddingsResponse represents embeddings response
type EmbeddingsResponse struct {
	Object string        `json:"object"`
	Data   []Embedding   `json:"data"`
	Model  string        `json:"model"`
	Usage  Usage         `json:"usage"`
}

// Embedding represents a single embedding
type Embedding struct {
	Object    string    `json:"object"`
	Index     int       `json:"index"`
	Embedding []float32 `json:"embedding"`
}
