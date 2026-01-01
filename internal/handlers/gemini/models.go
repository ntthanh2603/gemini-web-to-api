package gemini

import (
	"time"

	"ai-bridges/internal/providers/gemini"
)

// --- Cookie Info ---

type CookieResponse struct {
	Cookies   *gemini.CookieStore `json:"cookies"`
	Message   string              `json:"message"`
	UpdatedAt time.Time           `json:"updated_at"`
}

type ErrorResponse struct {
	Error string `json:"error"`
}

// --- Official Gemini API Models (v1beta) ---

type GeminiGenerateRequest struct {
	Contents         []Content         `json:"contents"`
	GenerationConfig *GenerationConfig `json:"generationConfig,omitempty"`
}

type Content struct {
	Role  string `json:"role,omitempty"`
	Parts []Part `json:"parts"`
}

type Part struct {
	Text       string      `json:"text,omitempty"`
	InlineData *InlineData `json:"inlineData,omitempty"`
}

type InlineData struct {
	MimeType string `json:"mimeType"`
	Data     string `json:"data"` // Base64
}

type GenerationConfig struct {
	Temperature     float32  `json:"temperature,omitempty"`
	MaxOutputTokens int      `json:"maxOutputTokens,omitempty"`
	StopSequences   []string `json:"stopSequences,omitempty"`
	TopP            float32  `json:"topP,omitempty"`
	TopK            int      `json:"topK,omitempty"`
}

type GeminiGenerateResponse struct {
	Candidates     []Candidate     `json:"candidates"`
	UsageMetadata  *UsageMetadata  `json:"usageMetadata,omitempty"`
}

type Candidate struct {
	Content       Content `json:"content"`
	FinishReason  string  `json:"finishReason,omitempty"`
	Index         int     `json:"index"`
}

type UsageMetadata struct {
	PromptTokenCount     int `json:"promptTokenCount"`
	CandidatesTokenCount int `json:"candidatesTokenCount"`
	TotalTokenCount      int `json:"totalTokenCount"`
}

type GeminiModelsResponse struct {
	Models []GeminiModel `json:"models"`
}

type GeminiModel struct {
	Name                       string   `json:"name"`
	Version                    string   `json:"version"`
	DisplayName               string   `json:"displayName"`
	Description               string   `json:"description"`
	InputTokenLimit           int      `json:"inputTokenLimit"`
	OutputTokenLimit          int      `json:"outputTokenLimit"`
	SupportedGenerationMethods []string `json:"supportedGenerationMethods"`
}

type GeminiEmbedRequest struct {
	Content Content `json:"content"`
}

type GeminiEmbedResponse struct {
	Embedding EmbeddingValues `json:"embedding"`
}

type EmbeddingValues struct {
	Values []float32 `json:"values"`
}
