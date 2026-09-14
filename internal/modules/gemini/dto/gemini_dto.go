package dto

import "encoding/json"

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
	Contents          []Content           `json:"contents"`
	SystemInstruction *Content            `json:"system_instruction,omitempty"`
	Tools             []Tool              `json:"tools,omitempty"`
	ToolConfig        *ToolConfig         `json:"tool_config,omitempty"`
	GenerationConfig  *GenerationConfig   `json:"generationConfig,omitempty"`
	Safety            []map[string]string `json:"safety_settings,omitempty"`
}

func (r *GeminiGenerateRequest) UnmarshalJSON(data []byte) error {
	type alias GeminiGenerateRequest
	var raw struct {
		alias
		SystemInstructionCamel *Content `json:"systemInstruction"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*r = GeminiGenerateRequest(raw.alias)
	if r.SystemInstruction == nil && raw.SystemInstructionCamel != nil {
		r.SystemInstruction = raw.SystemInstructionCamel
	}
	return nil
}

// Content represents a content block in Gemini API
type Content struct {
	Role  string `json:"role,omitempty"`
	Parts []Part `json:"parts"`
}

// Part represents a part of content
type Part struct {
	Text             string            `json:"text,omitempty"`
	Thought          bool              `json:"thought,omitempty"`
	InlineData       *InlineData       `json:"inline_data,omitempty"`
	FileData         *FileData         `json:"file_data,omitempty"`
	FunctionCall     *FunctionCall     `json:"function_call,omitempty"`
	FunctionResponse *FunctionResponse `json:"function_response,omitempty"`
}

func (p *Part) UnmarshalJSON(data []byte) error {
	var raw struct {
		Text                  string            `json:"text"`
		Thought               bool              `json:"thought"`
		InlineDataSnake       *InlineData       `json:"inline_data"`
		InlineDataCamel       *InlineData       `json:"inlineData"`
		FileDataSnake         *FileData         `json:"file_data"`
		FileDataCamel         *FileData         `json:"fileData"`
		FunctionCallSnake     *FunctionCall     `json:"function_call"`
		FunctionCallCamel     *FunctionCall     `json:"functionCall"`
		FunctionResponseSnake *FunctionResponse `json:"function_response"`
		FunctionResponseCamel *FunctionResponse `json:"functionResponse"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	p.Text = raw.Text
	p.Thought = raw.Thought
	p.InlineData = raw.InlineDataSnake
	if p.InlineData == nil {
		p.InlineData = raw.InlineDataCamel
	}
	p.FileData = raw.FileDataSnake
	if p.FileData == nil {
		p.FileData = raw.FileDataCamel
	}
	p.FunctionCall = raw.FunctionCallSnake
	if p.FunctionCall == nil {
		p.FunctionCall = raw.FunctionCallCamel
	}
	p.FunctionResponse = raw.FunctionResponseSnake
	if p.FunctionResponse == nil {
		p.FunctionResponse = raw.FunctionResponseCamel
	}
	return nil
}

// FunctionCall represents a model's request to call a tool
type FunctionCall struct {
	Name string          `json:"name"`
	Args json.RawMessage `json:"args" swagignore:"true"` // @SchemaType object
}

// FunctionResponse represents the result of a tool call
type FunctionResponse struct {
	Name     string          `json:"name"`
	Response json.RawMessage `json:"response" swagignore:"true"` // @SchemaType object
}

// Tool represents a tool available to the model
type Tool struct {
	FunctionDeclarations []FunctionDeclaration `json:"function_declarations,omitempty"`
}

// FunctionDeclaration represents a function schema
type FunctionDeclaration struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty" swagignore:"true"` // @SchemaType object
}

// ToolConfig represents configuration for tool use
type ToolConfig struct {
	FunctionCallingConfig *FunctionCallingConfig `json:"function_calling_config,omitempty"`
}

// FunctionCallingConfig represents configuration for function calling
type FunctionCallingConfig struct {
	Mode                 string   `json:"mode,omitempty"` // "AUTO", "ANY", "NONE"
	AllowedFunctionNames []string `json:"allowed_function_names,omitempty"`
}

// InlineData represents inline data (e.g., images)
type InlineData struct {
	MimeType string `json:"mimeType"`
	Data     string `json:"data"`
}

// FileData represents file data referenced by URI.
type FileData struct {
	MimeType string `json:"mimeType,omitempty"`
	FileURI  string `json:"fileUri"`
}

func (d *InlineData) UnmarshalJSON(data []byte) error {
	var raw struct {
		MimeTypeCamel string `json:"mimeType"`
		MimeTypeSnake string `json:"mime_type"`
		Data          string `json:"data"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	d.MimeType = raw.MimeTypeCamel
	if d.MimeType == "" {
		d.MimeType = raw.MimeTypeSnake
	}
	d.Data = raw.Data
	return nil
}

// ThinkingConfig represents the thinking configuration for Gemini models
type ThinkingConfig struct {
	ThinkingBudget int32  `json:"thinkingBudget,omitempty"`
	ThinkingLevel  string `json:"thinkingLevel,omitempty"`
}

// GenerationConfig represents generation configuration
type GenerationConfig struct {
	Temperature     float32         `json:"temperature,omitempty"`
	TopP            float32         `json:"topP,omitempty"`
	TopK            int32           `json:"topK,omitempty"`
	MaxOutputTokens int32           `json:"maxOutputTokens,omitempty"`
	ThinkingConfig  *ThinkingConfig `json:"thinkingConfig,omitempty"`
}

// GeminiGenerateResponse represents a Gemini generate response
type GeminiGenerateResponse struct {
	Candidates    []Candidate    `json:"candidates"`
	UsageMetadata *UsageMetadata `json:"usageMetadata,omitempty"`
}

// Candidate represents a candidate response
type Candidate struct {
	Index         int     `json:"index"`
	Content       Content `json:"content"`
	FinishReason  string  `json:"finishReason,omitempty"`
	FinishMessage string  `json:"finishMessage,omitempty"`
}

// UsageMetadata represents usage metadata
type UsageMetadata struct {
	PromptTokenCount     int32 `json:"promptTokenCount"`
	CandidatesTokenCount int32 `json:"candidatesTokenCount"`
	TotalTokenCount      int32 `json:"totalTokenCount"`
}
