package dto

import (
	"encoding/json"
	"strings"
)

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
		SystemInstructionCamel *Content    `json:"systemInstruction"`
		ToolConfigCamel        *ToolConfig `json:"toolConfig"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*r = GeminiGenerateRequest(raw.alias)
	if r.SystemInstruction == nil && raw.SystemInstructionCamel != nil {
		r.SystemInstruction = raw.SystemInstructionCamel
	}
	if r.ToolConfig == nil && raw.ToolConfigCamel != nil {
		r.ToolConfig = raw.ToolConfigCamel
	}
	return nil
}

// FunctionCallingMode returns tool_config's mode upper-cased: "AUTO", "ANY",
// "NONE", or "" when it is not set.
func (r GeminiGenerateRequest) FunctionCallingMode() string {
	if r.ToolConfig == nil || r.ToolConfig.FunctionCallingConfig == nil {
		return ""
	}
	return strings.ToUpper(strings.TrimSpace(r.ToolConfig.FunctionCallingConfig.Mode))
}

// HasToolsEnabled reports whether the model may call functions: some are
// declared and tool_config mode is not NONE.
func (r GeminiGenerateRequest) HasToolsEnabled() bool {
	if r.FunctionCallingMode() == "NONE" {
		return false
	}
	for _, tool := range r.Tools {
		if len(tool.FunctionDeclarations) > 0 {
			return true
		}
	}
	return false
}

// DeclaresFunction reports whether name is one of the declared functions.
func (r GeminiGenerateRequest) DeclaresFunction(name string) bool {
	for _, tool := range r.Tools {
		for _, fn := range tool.FunctionDeclarations {
			if fn.Name == name {
				return true
			}
		}
	}
	return false
}

// AllowsFunction reports whether tool_config's allowed_function_names permits
// name; an empty list allows every declared function.
func (r GeminiGenerateRequest) AllowsFunction(name string) bool {
	if r.ToolConfig == nil || r.ToolConfig.FunctionCallingConfig == nil ||
		len(r.ToolConfig.FunctionCallingConfig.AllowedFunctionNames) == 0 {
		return true
	}
	for _, allowed := range r.ToolConfig.FunctionCallingConfig.AllowedFunctionNames {
		if allowed == name {
			return true
		}
	}
	return false
}

// Content represents a content block in Gemini API
type Content struct {
	Role  string `json:"role,omitempty"`
	Parts []Part `json:"parts"`
}

// Part represents a part of content. It is written in the REST (camelCase)
// spellings official SDKs parse; UnmarshalJSON also accepts snake_case.
type Part struct {
	Text             string            `json:"text,omitempty"`
	Thought          bool              `json:"thought,omitempty"`
	InlineData       *InlineData       `json:"inlineData,omitempty"`
	FileData         *FileData         `json:"fileData,omitempty"`
	FunctionCall     *FunctionCall     `json:"functionCall,omitempty"`
	FunctionResponse *FunctionResponse `json:"functionResponse,omitempty"`
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

// UnmarshalJSON reads the declarations in either spelling, since REST clients send camelCase
func (t *Tool) UnmarshalJSON(data []byte) error {
	var raw struct {
		Snake []FunctionDeclaration `json:"function_declarations"`
		Camel []FunctionDeclaration `json:"functionDeclarations"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	t.FunctionDeclarations = raw.Snake
	if len(t.FunctionDeclarations) == 0 {
		t.FunctionDeclarations = raw.Camel
	}
	return nil
}

// FunctionDeclaration represents a function schema
type FunctionDeclaration struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty" swagignore:"true"` // @SchemaType object
}

// UnmarshalJSON falls back to parametersJsonSchema when parameters is absent
func (d *FunctionDeclaration) UnmarshalJSON(data []byte) error {
	var raw struct {
		Name                string          `json:"name"`
		Description         string          `json:"description"`
		Parameters          json.RawMessage `json:"parameters"`
		ParametersJSONCamel json.RawMessage `json:"parametersJsonSchema"`
		ParametersJSONSnake json.RawMessage `json:"parameters_json_schema"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	d.Name = raw.Name
	d.Description = raw.Description
	d.Parameters = raw.Parameters
	if len(d.Parameters) == 0 {
		d.Parameters = raw.ParametersJSONCamel
	}
	if len(d.Parameters) == 0 {
		d.Parameters = raw.ParametersJSONSnake
	}
	return nil
}

// ToolConfig represents configuration for tool use
type ToolConfig struct {
	FunctionCallingConfig *FunctionCallingConfig `json:"function_calling_config,omitempty"`
}

// UnmarshalJSON reads the calling config in either spelling
func (c *ToolConfig) UnmarshalJSON(data []byte) error {
	var raw struct {
		Snake *FunctionCallingConfig `json:"function_calling_config"`
		Camel *FunctionCallingConfig `json:"functionCallingConfig"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	c.FunctionCallingConfig = raw.Snake
	if c.FunctionCallingConfig == nil {
		c.FunctionCallingConfig = raw.Camel
	}
	return nil
}

// FunctionCallingConfig represents configuration for function calling
type FunctionCallingConfig struct {
	Mode                 string   `json:"mode,omitempty"` // "AUTO", "ANY", "NONE"
	AllowedFunctionNames []string `json:"allowed_function_names,omitempty"`
}

// UnmarshalJSON reads the allowed names in either spelling
func (c *FunctionCallingConfig) UnmarshalJSON(data []byte) error {
	var raw struct {
		Mode         string   `json:"mode"`
		AllowedSnake []string `json:"allowed_function_names"`
		AllowedCamel []string `json:"allowedFunctionNames"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	c.Mode = raw.Mode
	c.AllowedFunctionNames = raw.AllowedSnake
	if len(c.AllowedFunctionNames) == 0 {
		c.AllowedFunctionNames = raw.AllowedCamel
	}
	return nil
}

// InlineData represents inline data (e.g., images)
type InlineData struct {
	MimeType    string `json:"mimeType"`
	Data        string `json:"data"`
	DisplayName string `json:"displayName,omitempty"`
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
		DisplayCamel  string `json:"displayName"`
		DisplaySnake  string `json:"display_name"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	d.MimeType = raw.MimeTypeCamel
	if d.MimeType == "" {
		d.MimeType = raw.MimeTypeSnake
	}
	d.Data = raw.Data
	d.DisplayName = raw.DisplayCamel
	if d.DisplayName == "" {
		d.DisplayName = raw.DisplaySnake
	}
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

// PredictLongRunningRequest is the body of models/{model}:predictLongRunning,
// which google-genai sends for client.models.generate_videos.
type PredictLongRunningRequest struct {
	Instances  []VideoInstance  `json:"instances"`
	Parameters *VideoParameters `json:"parameters,omitempty"`
}

// VideoInstance is one video request; only the text prompt is supported.
type VideoInstance struct {
	Prompt string          `json:"prompt"`
	Image  json.RawMessage `json:"image,omitempty"`
	Video  json.RawMessage `json:"video,omitempty"`
}

// VideoParameters holds generation options. Only aspectRatio and sampleCount
// are honored; Gemini Web decides duration and resolution.
type VideoParameters struct {
	AspectRatio      string `json:"aspectRatio,omitempty"`
	SampleCount      int    `json:"sampleCount,omitempty"`
	DurationSeconds  int    `json:"durationSeconds,omitempty"`
	NegativePrompt   string `json:"negativePrompt,omitempty"`
	PersonGeneration string `json:"personGeneration,omitempty"`
	Resolution       string `json:"resolution,omitempty"`
}

// VideoOperation is a google.longrunning.Operation for a video job.
type VideoOperation struct {
	Name     string                  `json:"name"`
	Done     bool                    `json:"done"`
	Metadata map[string]any          `json:"metadata,omitempty"`
	Response *VideoOperationResponse `json:"response,omitempty"`
	Error    *OperationError         `json:"error,omitempty"`
}

// VideoOperationResponse wraps the generated videos of a finished operation.
type VideoOperationResponse struct {
	Type                  string                `json:"@type"`
	GenerateVideoResponse GenerateVideoResponse `json:"generateVideoResponse"`
}

// GenerateVideoResponse lists the generated samples.
type GenerateVideoResponse struct {
	GeneratedSamples []GeneratedSample `json:"generatedSamples"`
}

// GeneratedSample is one generated video.
type GeneratedSample struct {
	Video GeneratedVideoFile `json:"video"`
}

// GeneratedVideoFile points at the downloadable MP4.
type GeneratedVideoFile struct {
	URI      string `json:"uri"`
	Encoding string `json:"encoding,omitempty"`
}

// OperationError is a google.rpc.Status.
type OperationError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Status  string `json:"status,omitempty"`
}
